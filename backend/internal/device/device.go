package device

import (
	"sync"
	"sync/atomic"
	"time"
)

type UIState string

const (
	Offline    UIState = "offline"
	Preview    UIState = "preview"
	Switching  UIState = "switching"
	HD         UIState = "hd"
)

type Device struct {
	ID    string
	Token string
	Name  string

	alive atomic.Bool // 业务可用；Delete 后永不复用指针

	mu sync.Mutex

	CtrlOnline      bool
	MTXPublisherUp  bool
	TSPullHealthy   bool

	Width  int
	Height int
	Mode   string // preview | hd

	IsSwitching bool
	SwitchSeq   uint64
	SwitchSince time.Time

	IsFocused bool
	HDSince   time.Time

	MissHeartbeats int
	LastHeartbeat  time.Time
	LastFrameAt    time.Time

	sessionGen uint64 // TCP 会话代数；dead 后旧消息拒
}

func (d *Device) Alive() bool { return d.alive.Load() }

// SnapshotFlags 跨包只读状态（避免导出 mutex）。
func (d *Device) SnapshotFlags() (ctrl, mtxUp, tsOK, switching, focused bool, mode string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.CtrlOnline, d.MTXPublisherUp, d.TSPullHealthy, d.IsSwitching, d.IsFocused, d.Mode
}

func (d *Device) SwitchTimeoutInfo() (id string, switching bool, seq uint64, since time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ID, d.IsSwitching, d.SwitchSeq, d.SwitchSince
}

func (d *Device) StreamOnline() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.MTXPublisherUp && d.TSPullHealthy
}

func (d *Device) UIStateLocked() UIState {
	if !d.CtrlOnline || !(d.MTXPublisherUp && d.TSPullHealthy) {
		return Offline
	}
	if d.IsSwitching {
		return Switching
	}
	if d.Mode == "hd" {
		return HD
	}
	return Preview
}

type Snapshot struct {
	DeviceID       string  `json:"deviceId"`
	Name           string  `json:"name"`
	Alive          bool    `json:"alive"`
	CtrlOnline     bool    `json:"ctrlOnline"`
	MTXPublisherUp bool    `json:"mtxPublisherUp"`
	TSPullHealthy  bool    `json:"tsPullHealthy"`
	StreamOnline   bool    `json:"streamOnline"`
	UIState        UIState `json:"uiState"`
	Mode           string  `json:"mode"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	IsFocused      bool    `json:"isFocused"`
	IsSwitching    bool    `json:"isSwitching"`
	SwitchSeq      uint64  `json:"switchSeq"`
	PlayURL        string  `json:"playUrl"`
}
