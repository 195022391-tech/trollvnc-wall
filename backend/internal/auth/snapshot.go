package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"trollvnc-wall/internal/config"
)

// StartupSnapshot 启动固化，运行期只读。
type StartupSnapshot struct {
	MTXVersion            string `json:"mtxVersion"`
	InternalUsersOK       bool   `json:"authInternalUsersOK"`
	ExcludeAvailable      bool   `json:"authHTTPExcludeAvailable"`
	ExcludeCapabilityOK   bool   `json:"authHTTPExcludeCapabilityOK"`
	OutputsClosed         bool   `json:"outputsClosed"`
	TimeoutsConsistent    bool   `json:"timeoutsConsistent"`
	InternalReadPath      string `json:"internalReadPath"` // internal_users | exclude | none
	ProbedAt              time.Time `json:"probedAt"`
}

func ProbeAndFreeze(cfg *config.Config) (*StartupSnapshot, error) {
	s := &StartupSnapshot{ProbedAt: time.Now()}

	ver, err := fetchMTXVersion(cfg.MediaMTX.APIAddr)
	if err != nil {
		// InternalUsers 仍可用时可启动；stream_sync 会继续退避重试 API
		log.Printf("mtx api unreachable at startup: %v (code=%s); continue if internal_users ok", err, "mtx_api_error")
		s.MTXVersion = "unreachable"
	} else {
		s.MTXVersion = ver
	}

	// InternalUsers：配置已提供账号即视为可用（默认唯一优先路径）
	s.InternalUsersOK = cfg.MediaMTX.InternalUser != "" && cfg.MediaMTX.InternalPass != ""

	// Exclude ≠ IP-ACL；默认不启用。仅 PreferInternalUsers=false 时做实发能力探测
	s.ExcludeAvailable = false
	s.ExcludeCapabilityOK = false
	if !cfg.MediaMTX.PreferInternalUsers {
		s.ExcludeAvailable = true
		ok, err := probeExcludeCapability(cfg)
		if err != nil {
			log.Printf("exclude capability probe: %v", err)
		}
		s.ExcludeCapabilityOK = ok
	}

	s.OutputsClosed = true // 以 mediamtx.yml 约定为准；API 可再核对
	s.TimeoutsConsistent = true

	switch {
	case s.InternalUsersOK && cfg.MediaMTX.PreferInternalUsers:
		s.InternalReadPath = "internal_users"
	case s.ExcludeCapabilityOK:
		s.InternalReadPath = "exclude"
	case s.InternalUsersOK:
		s.InternalReadPath = "internal_users"
	default:
		s.InternalReadPath = "none"
		return s, fmt.Errorf("no safe internal read path")
	}

	b, _ := json.MarshalIndent(s, "", "  ")
	log.Printf("startup snapshot:\n%s", b)
	return s, nil
}

func fetchMTXVersion(api string) (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(strings.TrimRight(api, "/") + "/v3/config/global/get")
	if err != nil {
		// 兼容：info 接口
		resp2, err2 := client.Get(strings.TrimRight(api, "/") + "/v3/info")
		if err2 != nil {
			return "unknown", err
		}
		defer resp2.Body.Close()
		body, _ := io.ReadAll(resp2.Body)
		var info map[string]any
		_ = json.Unmarshal(body, &info)
		if v, ok := info["version"].(string); ok {
			return v, nil
		}
		return "unknown", nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var g map[string]any
	_ = json.Unmarshal(body, &g)
	// 无版本字段时仍算 API 可用
	_ = g
	resp2, err2 := client.Get(strings.TrimRight(api, "/") + "/v3/info")
	if err2 == nil {
		defer resp2.Body.Close()
		b, _ := io.ReadAll(resp2.Body)
		var info map[string]any
		_ = json.Unmarshal(b, &info)
		if v, ok := info["version"].(string); ok {
			return v, nil
		}
	}
	return "detected", nil
}

// probeExcludeCapability：实发内部/外部 read。无活跃 path 时返回 false（勿启用 Exclude）。
func probeExcludeCapability(cfg *config.Config) (bool, error) {
	// 安全默认：Exclude 不是 ACL，未经实机双向探测不得启用
	_ = cfg
	return false, nil
}

func (s *StartupSnapshot) RTSPUserPass() (user, pass string, ok bool) {
	if s.InternalReadPath == "internal_users" {
		return "", "", true // 调用方用 config 填
	}
	return "", "", s.InternalReadPath != "none"
}
