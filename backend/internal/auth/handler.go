package auth

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"trollvnc-wall/internal/config"
	"trollvnc-wall/internal/device"
	"trollvnc-wall/internal/errcode"
)

// MTX auth HTTP payload
type mtxAuthReq struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Token    string `json:"token"`
	IP       string `json:"ip"`
	Action   string `json:"action"`
	Path     string `json:"path"`
	Protocol string `json:"protocol"`
	Query    string `json:"query"`
}

type Handler struct {
	cfg *config.Config
	dm  *device.Manager
	snap *StartupSnapshot

	mu   sync.Mutex
	fail map[string]*limiter
	ok   map[string]*limiter
}

type limiter struct {
	window time.Time
	count  int
}

func NewHandler(cfg *config.Config, dm *device.Manager, snap *StartupSnapshot) *Handler {
	return &Handler{
		cfg: cfg, dm: dm, snap: snap,
		fail: map[string]*limiter{},
		ok:   map[string]*limiter{},
	}
}

func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/rtsp", h.handleMTXAuth)
	mux.HandleFunc("/api/devices/register", h.handleRegister)
}

func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
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
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *Handler) handleMTXAuth(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	var req mtxAuthReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, errcode.AuthPublishDenied, 403)
		return
	}

	// authMethod=http 时 MTX 所有 action 都打到这里。
	// publish：设备 token；read：仅内部账号（不下发前端）；其它拒绝。
	switch req.Action {
	case "publish":
		token := req.Token
		if token == "" {
			token = queryToken(req.Query)
		}
		if token == "" {
			token = req.Password
		}
		path := strings.Trim(req.Path, "/")
		if path == "" {
			path = req.User
		}
		if !h.dm.ValidateToken(path, token) {
			h.hitFail(ip)
			if h.limitedFail(ip) {
				http.Error(w, errcode.AuthRateLimited, 429)
				return
			}
			log.Printf("auth deny publish path=%s ip=%s", path, ip)
			http.Error(w, errcode.AuthPublishDenied, 403)
			return
		}
	case "read":
		// ffmpeg 常不把 userinfo 传给 MTX HTTP auth；本机 remux 放行。
		// 非本机必须 wall_internal。
		local := req.IP == "127.0.0.1" || req.IP == "::1" || ip == "127.0.0.1" || ip == "::1"
		credOK := req.User == h.cfg.MediaMTX.InternalUser && req.Password == h.cfg.MediaMTX.InternalPass
		if !local && !credOK {
			h.hitFail(ip)
			if h.limitedFail(ip) {
				http.Error(w, errcode.AuthRateLimited, 429)
				return
			}
			log.Printf("auth deny read user=%s ip=%s path=%s", req.User, ip, req.Path)
			http.Error(w, errcode.AuthPublishOnly, 403)
			return
		}
	default:
		h.hitFail(ip)
		if h.limitedFail(ip) {
			http.Error(w, errcode.AuthRateLimited, 429)
			return
		}
		log.Printf("auth deny action=%s ip=%s path=%s", req.Action, ip, req.Path)
		http.Error(w, errcode.AuthPublishOnly, 403)
		return
	}

	h.hitOK(ip)
	if h.limitedOK(ip) {
		http.Error(w, errcode.AuthRateLimited, 429)
		return
	}
	w.WriteHeader(200)
}

func queryToken(q string) string {
	for _, part := range strings.Split(q, "&") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 && (kv[0] == "token" || kv[0] == "mtx_token") {
			return kv[1]
		}
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *Handler) hitFail(ip string) { h.bump(h.fail, ip) }
func (h *Handler) hitOK(ip string)   { h.bump(h.ok, ip) }

func (h *Handler) bump(m map[string]*limiter, ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := m[ip]
	now := time.Now()
	if l == nil || now.Sub(l.window) > time.Second {
		m[ip] = &limiter{window: now, count: 1}
		return
	}
	l.count++
}

func (h *Handler) limitedFail(ip string) bool {
	return h.limited(h.fail, ip, h.cfg.Auth.RateLimitFailPerSec)
}
func (h *Handler) limitedOK(ip string) bool {
	return h.limited(h.ok, ip, h.cfg.Auth.RateLimitOKPerSec)
}

func (h *Handler) limited(m map[string]*limiter, ip string, max int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := m[ip]
	if l == nil {
		return false
	}
	return l.count > max
}
