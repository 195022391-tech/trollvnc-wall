package streamsync

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"trollvnc-wall/internal/config"
	"trollvnc-wall/internal/device"
	"trollvnc-wall/internal/errcode"
)

type Syncer struct {
	cfg *config.Config
	dm  *device.Manager
}

func New(cfg *config.Config, dm *device.Manager) *Syncer {
	return &Syncer{cfg: cfg, dm: dm}
}

func (s *Syncer) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Syncer) loop(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := s.pollOnce()
		if err != nil {
			log.Printf("%s: %v", errcode.MTXAPIError, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			max := time.Duration(s.cfg.StreamSync.APIBackoffMaxMs) * time.Millisecond
			if backoff > max {
				backoff = max
			}
			continue
		}
		backoff = time.Second
		n := 0
		s.dm.Range(func(_ *device.Device) bool { n++; return true })
		var wait time.Duration
		if n > s.cfg.StreamSync.SlowThresholdDevices {
			wait = time.Duration(s.cfg.StreamSync.SlowPollMs) * time.Millisecond
		} else {
			lo := s.cfg.StreamSync.PollJitterMinMs
			hi := s.cfg.StreamSync.PollJitterMaxMs
			if hi <= lo {
				hi = lo + 1
			}
			wait = time.Duration(lo+rand.Intn(hi-lo)) * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (s *Syncer) pollOnce() error {
	client := &http.Client{Timeout: 3 * time.Second}
	url := strings.TrimRight(s.cfg.MediaMTX.APIAddr, "/") + "/v3/paths/list"
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return apiErr{code: resp.StatusCode, msg: string(b)}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var parsed struct {
		Items []struct {
			Name   string `json:"name"`
			Ready  bool   `json:"ready"`
			Source *struct {
				Type string `json:"type"`
			} `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 兼容旧结构
		var alt map[string]json.RawMessage
		if err2 := json.Unmarshal(body, &alt); err2 != nil {
			return err
		}
		return nil
	}
	seen := map[string]bool{}
	for _, it := range parsed.Items {
		up := it.Ready || it.Source != nil
		seen[it.Name] = up
		s.dm.SetMTXPublisher(it.Name, up)
	}
	s.dm.Range(func(d *device.Device) bool {
		if !seen[d.ID] {
			s.dm.SetMTXPublisher(d.ID, false)
		}
		return true
	})
	return nil
}

type apiErr struct {
	code int
	msg  string
}

func (e apiErr) Error() string { return e.msg }
