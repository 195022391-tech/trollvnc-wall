package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     Server     `yaml:"server"`
	MediaMTX   MediaMTX   `yaml:"mediamtx"`
	Encode     Encode     `yaml:"encode"`
	Quality    Quality    `yaml:"quality"`
	HD         HD         `yaml:"hd"`
	Ctrl       Ctrl       `yaml:"ctrl"`
	StreamSync StreamSync `yaml:"stream_sync"`
	TS         TS         `yaml:"ts"`
	Auth       Auth       `yaml:"auth"`
	UI         UI         `yaml:"ui"`
	Logging    Logging    `yaml:"logging"`
}

type Server struct {
	CtrlListen string `yaml:"ctrl_listen"`
	AuthListen string `yaml:"auth_listen"`
	TSListen   string `yaml:"ts_listen"`
	APIListen  string `yaml:"api_listen"`
	PublicHost string `yaml:"public_host"`
}

type MediaMTX struct {
	APIAddr              string `yaml:"api_addr"`
	RTSPAddr             string `yaml:"rtsp_addr"`
	InternalUser         string `yaml:"internal_user"`
	InternalPass         string `yaml:"internal_pass"`
	PreferInternalUsers  bool   `yaml:"prefer_internal_users"`
}

type Encode struct {
	GOPSeconds         float64 `yaml:"gop_seconds"`
	MinGOP             float64 `yaml:"min_gop"`
	TimeoutCapSeconds  float64 `yaml:"timeout_cap_seconds"`
}

type Quality struct {
	PreviewWidth           int  `yaml:"preview_width"`
	PreviewHeight          int  `yaml:"preview_height"`
	PreviewBitrateKbps     int  `yaml:"preview_bitrate_kbps"`
	PreviewFPS             int  `yaml:"preview_fps"`
	HDBitrateKbps          int  `yaml:"hd_bitrate_kbps"`
	HDFPS                  int  `yaml:"hd_fps"`
	AllowResolutionSwitch  bool `yaml:"allow_resolution_switch"`
	HDWidth                int  `yaml:"hd_width"`
	HDHeight               int  `yaml:"hd_height"`
}

type HD struct {
	MaxHD                   int `yaml:"max_hd"`
	QueueTimeoutMs          int `yaml:"queue_timeout_ms"`
	IdleDowngradeSeconds    int `yaml:"idle_downgrade_seconds"`
	NoFrameReleaseSeconds   int `yaml:"no_frame_release_seconds"`
}

type Ctrl struct {
	MaxFrameSize         int `yaml:"max_frame_size"`
	HeartbeatIntervalMs  int `yaml:"heartbeat_interval_ms"`
	HeartbeatMissLimit   int `yaml:"heartbeat_miss_limit"`
	ReadTimeoutMs        int `yaml:"read_timeout_ms"`
	WriteTimeoutMs       int `yaml:"write_timeout_ms"`
	ReliableRetries      int `yaml:"reliable_retries"`
	SwitchTimeoutMs      int `yaml:"switch_timeout_ms"`
}

type StreamSync struct {
	PollJitterMinMs       int `yaml:"poll_jitter_min_ms"`
	PollJitterMaxMs       int `yaml:"poll_jitter_max_ms"`
	SlowThresholdDevices  int `yaml:"slow_threshold_devices"`
	SlowPollMs            int `yaml:"slow_poll_ms"`
	APIBackoffMaxMs       int `yaml:"api_backoff_max_ms"`
}

type TS struct {
	FirstIDRWaitCapMs       int `yaml:"first_idr_wait_cap_ms"`
	WriterHandoffTimeoutMs  int `yaml:"writer_handoff_timeout_ms"`
	WriteTimeoutMs          int `yaml:"write_timeout_ms"`
	MaxRedirects            int `yaml:"max_redirects"`
	PCRIntervalMs           int `yaml:"pcr_interval_ms"`
	PATIntervalMs           int `yaml:"pat_interval_ms"`
}

type Auth struct {
	RateLimitFailPerSec int `yaml:"rate_limit_fail_per_sec"`
	RateLimitOKPerSec   int `yaml:"rate_limit_ok_per_sec"`
}

type UI struct {
	PageSize  int `yaml:"page_size"`
	MaxDevice int `yaml:"max_device"`
}

type Logging struct {
	HeartbeatDebug bool `yaml:"heartbeat_debug"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Encode.GOPSeconds <= 0 {
		c.Encode.GOPSeconds = 2
	}
	if c.Encode.MinGOP <= 0 {
		c.Encode.MinGOP = 1
	}
	if c.Encode.TimeoutCapSeconds <= 0 {
		c.Encode.TimeoutCapSeconds = 8
	}
	if c.HD.MaxHD <= 0 {
		c.HD.MaxHD = 4
	}
	if c.Ctrl.MaxFrameSize <= 0 {
		c.Ctrl.MaxFrameSize = 65536
	}
	if c.Ctrl.HeartbeatMissLimit <= 0 {
		c.Ctrl.HeartbeatMissLimit = 5
	}
	if c.UI.PageSize <= 0 {
		c.UI.PageSize = 32
	}
}

func (c *Config) validate() error {
	if c.Server.CtrlListen == "" || c.Server.TSListen == "" {
		return fmt.Errorf("server listens required")
	}
	if c.MediaMTX.InternalUser == "" || c.MediaMTX.InternalPass == "" {
		return fmt.Errorf("mediamtx internal credentials required")
	}
	if c.Encode.MinGOP > c.Encode.GOPSeconds {
		return fmt.Errorf("min_gop must be <= gop_seconds")
	}
	return nil
}

// PublisherTimeout from effective GOP: min(max(5s, 2*g+1s), cap)
func (c *Config) PublisherTimeout(effectiveGOP float64) time.Duration {
	g := effectiveGOP
	sec := 2*g + 1
	if sec < 5 {
		sec = 5
	}
	if sec > c.Encode.TimeoutCapSeconds {
		sec = c.Encode.TimeoutCapSeconds
	}
	return time.Duration(sec * float64(time.Second))
}

// FirstIDRWait: min(max(2s, 2*g), cap)
func (c *Config) FirstIDRWait(effectiveGOP float64) time.Duration {
	g := effectiveGOP
	sec := 2 * g
	if sec < 2 {
		sec = 2
	}
	cap := c.Encode.TimeoutCapSeconds
	if float64(c.TS.FirstIDRWaitCapMs)/1000 < cap {
		cap = float64(c.TS.FirstIDRWaitCapMs) / 1000
	}
	if sec > cap {
		sec = cap
	}
	return time.Duration(sec * float64(time.Second))
}

func (c *Config) PlayBaseURL() string {
	host := c.Server.PublicHost
	if host == "" {
		host = "127.0.0.1"
	}
	// ts_listen like 0.0.0.0:18081 → use port
	port := "18081"
	if _, p, err := splitHostPort(c.Server.TSListen); err == nil {
		port = p
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

func splitHostPort(addr string) (string, string, error) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("no port")
}
