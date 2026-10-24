package tsgateway

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"trollvnc-wall/internal/config"
	"trollvnc-wall/internal/device"
	"trollvnc-wall/internal/errcode"
	"trollvnc-wall/internal/gop"
)

// Gateway：每设备独占 HTTP-TS writer；RTSP 经 ffmpeg -c copy 零转码出 TS（生命周期/踢连由 Go 管）。
type Gateway struct {
	cfg  *config.Config
	dm   *device.Manager
	gop  *gop.Tracker

	mu      sync.Mutex
	writers map[string]*writerSlot

	onIDR func(deviceID string, switchSeq uint64)
}

type writerSlot struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func New(cfg *config.Config, dm *device.Manager, g *gop.Tracker) *Gateway {
	return &Gateway{
		cfg: cfg, dm: dm, gop: g,
		writers: map[string]*writerSlot{},
	}
}

func (g *Gateway) SetIDRHook(fn func(deviceID string, switchSeq uint64)) {
	g.onIDR = fn
}

func (g *Gateway) Cancel(deviceID string) {
	g.mu.Lock()
	slot := g.writers[deviceID]
	if slot != nil {
		delete(g.writers, deviceID)
	}
	g.mu.Unlock()
	if slot != nil {
		slot.cancel()
		select {
		case <-slot.done:
		case <-time.After(time.Duration(g.cfg.TS.WriterHandoffTimeoutMs) * time.Millisecond):
		}
	}
}

func (g *Gateway) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/", g.serve)
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if !strings.HasSuffix(path, ".ts") {
		http.NotFound(w, r)
		return
	}
	deviceID := strings.TrimSuffix(path, ".ts")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}
	d := g.dm.Get(deviceID)
	if d == nil {
		http.Error(w, errcode.DeviceDestroyed, 404)
		return
	}
	_, mtxUp, _, _, _, _ := d.SnapshotFlags()
	if !mtxUp {
		http.Error(w, errcode.MTXPublisherLost, 503)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 新踢旧：cancel → 等退出 → 再开
	g.mu.Lock()
	if old := g.writers[deviceID]; old != nil {
		old.cancel()
		g.mu.Unlock()
		select {
		case <-old.done:
		case <-time.After(time.Duration(g.cfg.TS.WriterHandoffTimeoutMs) * time.Millisecond):
			log.Printf("ts handoff timeout %s", deviceID)
		}
		g.mu.Lock()
	}
	done := make(chan struct{})
	g.writers[deviceID] = &writerSlot{cancel: cancel, done: done}
	g.mu.Unlock()
	defer func() {
		close(done)
		g.mu.Lock()
		if cur, ok := g.writers[deviceID]; ok && cur.done == done {
			delete(g.writers, deviceID)
		}
		g.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", 500)
		return
	}

	rtspURL := g.rtspURL(deviceID)
	wait := g.cfg.FirstIDRWait(g.gop.Effective())
	cmdCtx, cmdCancel := context.WithCancel(ctx)
	defer cmdCancel()

	// ffmpeg 零转码 remux；Go 管独占/取消/健康打标
	// 凭据在 URL；MTX HTTP auth 常收不到 userinfo → auth 侧对本机 read 放行
	args := []string{
		"-nostdin", "-loglevel", "error",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-an", "-c", "copy", "-f", "mpegts",
		"pipe:1",
	}
	cmd := exec.CommandContext(cmdCtx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		g.dm.SetTSHealthy(deviceID, false)
		http.Error(w, errcode.TSPullFailed, 502)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("ffmpeg start: %v", err)
		g.dm.SetTSHealthy(deviceID, false)
		http.Error(w, errcode.TSPullFailed, 502)
		return
	}
	defer func() {
		cmdCancel()
		_ = cmd.Wait()
	}()

	buf := make([]byte, 32*1024)
	first := true
	deadline := time.Now().Add(wait)
	for {
		select {
		case <-ctx.Done():
			// 客户端正常断开不打掉 tsPullHealthy（避免墙「有推流却假离线」）
			return
		default:
		}
		if first {
			_ = setReadDeadline(stdout, time.Until(deadline))
		} else {
			_ = setReadDeadline(stdout, time.Duration(g.cfg.TS.WriteTimeoutMs)*time.Millisecond)
		}
		n, err := stdout.Read(buf)
		if n > 0 {
			if first {
				first = false
				g.dm.SetTSHealthy(deviceID, true)
				if seq, sw := g.dm.CurrentSwitchSeq(deviceID); sw && g.onIDR != nil {
					g.onIDR(deviceID, seq)
				}
			}
			_ = setWriteDeadline(w, time.Duration(g.cfg.TS.WriteTimeoutMs)*time.Millisecond)
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
			g.dm.SetTSHealthy(deviceID, true)
		}
		if err != nil {
			g.dm.SetTSHealthy(deviceID, false)
			if first {
				http.Error(w, errcode.IDRTimeout, 504)
			}
			return
		}
	}
}

func (g *Gateway) rtspURL(deviceID string) string {
	u := g.cfg.MediaMTX.InternalUser
	p := g.cfg.MediaMTX.InternalPass
	addr := g.cfg.MediaMTX.RTSPAddr
	return fmt.Sprintf("rtsp://%s:%s@%s/%s", u, p, addr, deviceID)
}

type deadlineReader interface {
	SetReadDeadline(time.Time) error
}

func setReadDeadline(r io.Reader, d time.Duration) error {
	if dr, ok := r.(deadlineReader); ok {
		if d < 0 {
			d = 0
		}
		return dr.SetReadDeadline(time.Now().Add(d))
	}
	return nil
}

type deadlineWriter interface {
	SetWriteDeadline(time.Time) error
}

func setWriteDeadline(w http.ResponseWriter, d time.Duration) error {
	if dw, ok := w.(deadlineWriter); ok {
		return dw.SetWriteDeadline(time.Now().Add(d))
	}
	_ = d
	return nil
}
