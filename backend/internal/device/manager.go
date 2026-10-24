package device

import (
	"sync"
	"sync/atomic"
	"time"

	"trollvnc-wall/internal/errcode"
)

type EventSink interface {
	OnDeviceUpdate(snap Snapshot)
}

type Manager struct {
	devices  sync.Map // string -> *Device
	playBase string
	sink     EventSink

	onCtrlLost     func(deviceID string)
	onNeedTSCancel func(deviceID string)
}

func NewManager(playBase string) *Manager {
	return &Manager{playBase: playBase}
}

func (m *Manager) SetSink(s EventSink) { m.sink = s }

func (m *Manager) SetHooks(onCtrlLost, onTSCancel func(string)) {
	m.onCtrlLost = onCtrlLost
	m.onNeedTSCancel = onTSCancel
}

func (m *Manager) PlayURL(id string) string {
	return m.playBase + "/" + id + ".ts"
}

func (m *Manager) RegisterToken(id, token, name string) (*Device, error) {
	if id == "" || token == "" {
		return nil, errString(errcode.AuthPublishDenied)
	}
	if v, ok := m.devices.Load(id); ok {
		d := v.(*Device)
		if !d.Alive() {
			m.devices.Delete(id)
		} else {
			d.mu.Lock()
			d.Token = token
			if name != "" {
				d.Name = name
			}
			d.mu.Unlock()
			return d, nil
		}
	}
	d := &Device{ID: id, Token: token, Name: name, Mode: "preview"}
	d.alive.Store(true)
	m.devices.Store(id, d)
	return d, nil
}

func (m *Manager) Get(id string) *Device {
	v, ok := m.devices.Load(id)
	if !ok {
		return nil
	}
	d := v.(*Device)
	if !d.Alive() {
		return nil
	}
	return d
}

func (m *Manager) ValidateToken(id, token string) bool {
	d := m.Get(id)
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Token == token && token != ""
}

// BindCtrlSession：先失效旧会话语义，再升代数。
func (m *Manager) BindCtrlSession(id string) (sessionGen uint64, err error) {
	d := m.Get(id)
	if d == nil {
		return 0, errString(errcode.DeviceDestroyed)
	}
	d.mu.Lock()
	d.sessionGen++
	sessionGen = d.sessionGen
	d.CtrlOnline = true
	d.MissHeartbeats = 0
	d.LastHeartbeat = time.Now()
	d.IsSwitching = false
	d.Mode = "preview"
	d.IsFocused = false
	d.mu.Unlock()

	if m.onCtrlLost != nil {
		m.onCtrlLost(id)
	}
	m.emit(d)
	return sessionGen, nil
}

func (m *Manager) SessionAlive(id string, gen uint64) bool {
	d := m.Get(id)
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessionGen == gen && d.CtrlOnline
}

func (m *Manager) MarkSessionDead(id string, gen uint64) {
	d := m.Get(id)
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.sessionGen != gen {
		d.mu.Unlock()
		return
	}
	d.CtrlOnline = false
	d.IsSwitching = false
	d.IsFocused = false
	d.Mode = "preview"
	d.mu.Unlock()
	if m.onCtrlLost != nil {
		m.onCtrlLost(id)
	}
	m.emit(d)
}

func (m *Manager) Heartbeat(id string, gen uint64) error {
	d := m.Get(id)
	if d == nil {
		return errString(errcode.DeviceDestroyed)
	}
	d.mu.Lock()
	if d.sessionGen != gen {
		d.mu.Unlock()
		return errString(errcode.SwitchSeqStale)
	}
	d.MissHeartbeats = 0
	d.LastHeartbeat = time.Now()
	d.CtrlOnline = true
	d.mu.Unlock()
	return nil
}

func (m *Manager) TickHeartbeats(missLimit int) {
	m.devices.Range(func(_, v any) bool {
		d := v.(*Device)
		if !d.Alive() {
			return true
		}
		d.mu.Lock()
		if !d.CtrlOnline {
			d.mu.Unlock()
			return true
		}
		d.MissHeartbeats++
		miss := d.MissHeartbeats
		gen := d.sessionGen
		id := d.ID
		d.mu.Unlock()
		if miss >= missLimit {
			m.MarkSessionDead(id, gen)
		}
		return true
	})
}

func (m *Manager) SetMTXPublisher(id string, up bool) {
	d := m.Get(id)
	if d == nil {
		return
	}
	d.mu.Lock()
	d.MTXPublisherUp = up
	if !up {
		d.TSPullHealthy = false
	}
	d.mu.Unlock()
	m.emit(d)
}

func (m *Manager) SetTSHealthy(id string, ok bool) {
	d := m.Get(id)
	if d == nil {
		return
	}
	d.mu.Lock()
	d.TSPullHealthy = ok
	if ok {
		d.LastFrameAt = time.Now()
	}
	d.mu.Unlock()
	m.emit(d)
}

func (m *Manager) BeginSwitch(id string, seq uint64, mode string) error {
	d := m.Get(id)
	if d == nil {
		return errString(errcode.DeviceDestroyed)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.CtrlOnline {
		return errString(errcode.NotCtrlOnline)
	}
	if d.IsSwitching {
		return errString(errcode.TargetSwitching)
	}
	d.IsSwitching = true
	d.SwitchSeq = seq
	d.SwitchSince = time.Now()
	_ = mode
	return nil
}

func (m *Manager) CurrentSwitchSeq(id string) (uint64, bool) {
	d := m.Get(id)
	if d == nil {
		return 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.SwitchSeq, d.IsSwitching
}

func (m *Manager) CompleteSwitch(id string, seq uint64, ok bool, mode string, w, h int) error {
	d := m.Get(id)
	if d == nil {
		return errString(errcode.DeviceDestroyed)
	}
	d.mu.Lock()
	if !d.IsSwitching || d.SwitchSeq != seq {
		d.mu.Unlock()
		return errString(errcode.SwitchSeqStale)
	}
	d.IsSwitching = false
	if ok {
		d.Mode = mode
		if w > 0 {
			d.Width = w
		}
		if h > 0 {
			d.Height = h
		}
	} else {
		d.Mode = "preview"
	}
	d.mu.Unlock()
	m.emit(d)
	return nil
}

func (m *Manager) CancelSwitch(id string, seq uint64) {
	d := m.Get(id)
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.IsSwitching && d.SwitchSeq == seq {
		d.IsSwitching = false
		d.Mode = "preview"
	}
	d.mu.Unlock()
	m.emit(d)
}

func (m *Manager) SetFocused(id string) {
	m.devices.Range(func(_, v any) bool {
		od := v.(*Device)
		if !od.Alive() {
			return true
		}
		od.mu.Lock()
		od.IsFocused = od.ID == id
		od.mu.Unlock()
		m.emit(od)
		return true
	})
}

func (m *Manager) SetMode(id, mode string) {
	d := m.Get(id)
	if d == nil {
		return
	}
	d.mu.Lock()
	d.Mode = mode
	if mode == "hd" {
		d.HDSince = time.Now()
	}
	d.mu.Unlock()
	m.emit(d)
}

func (m *Manager) IsSwitching(id string) bool {
	d := m.Get(id)
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.IsSwitching
}

func (m *Manager) SnapshotOf(d *Device) Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Snapshot{
		DeviceID:       d.ID,
		Name:           d.Name,
		Alive:          d.Alive(),
		CtrlOnline:     d.CtrlOnline,
		MTXPublisherUp: d.MTXPublisherUp,
		TSPullHealthy:  d.TSPullHealthy,
		StreamOnline:   d.MTXPublisherUp && d.TSPullHealthy,
		UIState:        d.UIStateLocked(),
		Mode:           d.Mode,
		Width:          d.Width,
		Height:         d.Height,
		IsFocused:      d.IsFocused,
		IsSwitching:    d.IsSwitching,
		SwitchSeq:      d.SwitchSeq,
		PlayURL:        m.PlayURL(d.ID),
	}
}

func (m *Manager) emit(d *Device) {
	if m.sink == nil || !d.Alive() {
		return
	}
	m.sink.OnDeviceUpdate(m.SnapshotOf(d))
}

func (m *Manager) List() []Snapshot {
	var out []Snapshot
	m.devices.Range(func(_, v any) bool {
		d := v.(*Device)
		if d.Alive() {
			out = append(out, m.SnapshotOf(d))
		}
		return true
	})
	return out
}

func (m *Manager) Destroy(id string) {
	v, ok := m.devices.Load(id)
	if !ok {
		return
	}
	d := v.(*Device)
	d.alive.Store(false)
	d.mu.Lock()
	d.CtrlOnline = false
	d.IsSwitching = false
	d.mu.Unlock()
	if m.onNeedTSCancel != nil {
		m.onNeedTSCancel(id)
	}
	if m.onCtrlLost != nil {
		m.onCtrlLost(id)
	}
	m.devices.Delete(id)
}

func (m *Manager) Range(fn func(*Device) bool) {
	m.devices.Range(func(_, v any) bool {
		d := v.(*Device)
		if d.Alive() {
			return fn(d)
		}
		return true
	})
}

type errString string

func (e errString) Error() string { return string(e) }

var switchSeq atomic.Uint64

func NextSwitchSeq() uint64 { return switchSeq.Add(1) }
