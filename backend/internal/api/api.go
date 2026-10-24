package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"trollvnc-wall/internal/auth"
	"trollvnc-wall/internal/config"
	"trollvnc-wall/internal/ctrl"
	"trollvnc-wall/internal/device"
	"trollvnc-wall/internal/errcode"
	"trollvnc-wall/internal/hdlease"
)

// Hub：Wails/前端 HTTP API + 简易 SSE 状态推送。
type Hub struct {
	cfg   *config.Config
	dm    *device.Manager
	hd    *hdlease.Manager
	ctrl  *ctrl.Server
	snap  *auth.StartupSnapshot

	mu       sync.Mutex
	sessions map[string]*uiSession
}

type uiSession struct {
	id          string
	pageVisible bool
	alive       bool
	failCount   int
	ch          chan device.Snapshot
}

func NewHub(cfg *config.Config, dm *device.Manager, hd *hdlease.Manager, c *ctrl.Server, snap *auth.StartupSnapshot) *Hub {
	h := &Hub{
		cfg: cfg, dm: dm, hd: hd, ctrl: c, snap: snap,
		sessions: map[string]*uiSession{},
	}
	dm.SetSink(h)
	return h
}

func (h *Hub) OnDeviceUpdate(snap device.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if !s.alive {
			continue
		}
		select {
		case s.ch <- snap:
			s.failCount = 0
		default:
			s.failCount++
			if s.failCount > 20 {
				s.alive = false
			}
		}
	}
}

func (h *Hub) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "snapshot": h.snap, "playBase": h.cfg.PlayBaseURL(),
		})
	})
	mux.HandleFunc("/api/devices", h.listDevices)
	mux.HandleFunc("/api/devices/register", h.register) // also on auth mux
	mux.HandleFunc("/api/hd/acquire", h.hdAcquire)
	mux.HandleFunc("/api/hd/release", h.hdRelease)
	mux.HandleFunc("/api/focus", h.focus)
	mux.HandleFunc("/api/switch/idr_seen", h.idrSeen)
	mux.HandleFunc("/api/switch/cancel", h.switchCancel)
	mux.HandleFunc("/api/session/subscribe", h.subscribe)
	mux.HandleFunc("/api/session/unsubscribe", h.unsubscribe)
	mux.HandleFunc("/api/session/visibility", h.visibility)
	mux.HandleFunc("/api/events", h.eventsSSE)
	mux.HandleFunc("/api/metrics", h.metrics)
}

func (h *Hub) listDevices(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"devices": h.dm.List()})
}

func (h *Hub) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"deviceId"`
		Token    string `json:"token"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, err := h.dm.RegisterToken(body.DeviceID, body.Token, body.Name); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "playUrl": h.dm.PlayURL(body.DeviceID)})
}

func (h *Hub) hdAcquire(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID    string `json:"deviceId"`
		SessionID   string `json:"sessionId"`
		PageVisible bool   `json:"pageVisible"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	res := h.hd.Acquire(body.DeviceID, body.SessionID, body.PageVisible)
	_ = json.NewEncoder(w).Encode(res)
}

func (h *Hub) hdRelease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"deviceId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	seq := device.NextSwitchSeq()
	_ = h.dm.BeginSwitch(body.DeviceID, seq, "preview")
	_ = h.ctrl.SendSetQuality(body.DeviceID, seq, "preview")
	h.hd.Release(body.DeviceID)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "switchSeq": seq})
}

func (h *Hub) focus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"deviceId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.dm.SetFocused(body.DeviceID)
	h.hd.SyncFocusFlags()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *Hub) idrSeen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID  string `json:"deviceId"`
		SwitchSeq uint64 `json:"switchSeq"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Mode      string `json:"mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cur, sw := h.dm.CurrentSwitchSeq(body.DeviceID)
	if !sw || cur != body.SwitchSeq {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": errcode.SwitchSeqStale})
		return
	}
	mode := body.Mode
	if mode == "" {
		mode = "hd"
	}
	_ = h.dm.CompleteSwitch(body.DeviceID, body.SwitchSeq, true, mode, body.Width, body.Height)
	h.ctrl.ClearRetransmit(body.DeviceID)
	h.hd.ReevaluateOnce()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *Hub) switchCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID  string `json:"deviceId"`
		SwitchSeq uint64 `json:"switchSeq"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.dm.CancelSwitch(body.DeviceID, body.SwitchSeq)
	h.ctrl.ClearRetransmit(body.DeviceID)
	h.hd.ReevaluateOnce()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *Hub) subscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.SessionID == "" {
		body.SessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	h.mu.Lock()
	h.sessions[body.SessionID] = &uiSession{
		id: body.SessionID, alive: true, pageVisible: true,
		ch: make(chan device.Snapshot, 64),
	}
	h.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "sessionId": body.SessionID})
}

func (h *Hub) unsubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.mu.Lock()
	if s, ok := h.sessions[body.SessionID]; ok {
		s.alive = false
		delete(h.sessions, body.SessionID)
	}
	h.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *Hub) visibility(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID   string `json:"sessionId"`
		PageVisible bool   `json:"pageVisible"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.mu.Lock()
	if s, ok := h.sessions[body.SessionID]; ok {
		s.pageVisible = body.PageVisible
	}
	h.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *Hub) eventsSSE(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sessionId")
	h.mu.Lock()
	s, ok := h.sessions[sid]
	h.mu.Unlock()
	if !ok {
		http.Error(w, errcode.SessionDead, 404)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case snap := <-s.ch:
			b, _ := json.Marshal(snap)
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (h *Hub) metrics(w http.ResponseWriter, r *http.Request) {
	online, switching := 0, 0
	h.dm.Range(func(d *device.Device) bool {
		ctrl, _, _, sw, _, _ := d.SnapshotFlags()
		if ctrl {
			online++
		}
		if sw {
			switching++
		}
		return true
	})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"online": online, "hd": h.hd.Count(), "switching": switching,
	})
}

// OnIDREmitted 服务端 TS 首关键/切换期信号
func (h *Hub) OnIDREmitted(deviceID string, seq uint64) {
	cur, sw := h.dm.CurrentSwitchSeq(deviceID)
	if !sw || cur != seq {
		return
	}
	_ = h.dm.CompleteSwitch(deviceID, seq, true, "hd", 0, 0)
	h.ctrl.ClearRetransmit(deviceID)
	h.hd.ReevaluateOnce()
}

func (h *Hub) PageVisible(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	return ok && s.alive && s.pageVisible
}

// QualityProvider for ctrl
type Quality struct{ Cfg *config.Config }

func (q Quality) PreviewParams() (w, h, br, fps int) {
	c := q.Cfg.Quality
	return c.PreviewWidth, c.PreviewHeight, c.PreviewBitrateKbps, c.PreviewFPS
}
func (q Quality) HDParams() (w, h, br, fps int) {
	c := q.Cfg.Quality
	if c.AllowResolutionSwitch {
		return c.HDWidth, c.HDHeight, c.HDBitrateKbps, c.HDFPS
	}
	return c.PreviewWidth, c.PreviewHeight, c.HDBitrateKbps, c.HDFPS
}
func (q Quality) AllowResSwitch() bool { return q.Cfg.Quality.AllowResolutionSwitch }
