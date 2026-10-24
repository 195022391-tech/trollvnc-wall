package hdlease

import (
	"sync"
	"time"

	"trollvnc-wall/internal/device"
	"trollvnc-wall/internal/errcode"
)

type Lease struct {
	DeviceID   string
	IsFocused  bool
	ActivateAt time.Time
	SwitchSeq  uint64
}

type QueueItem struct {
	DeviceID  string
	SwitchSeq uint64
	SessionID string
	Deadline  time.Time
}

type Sender interface {
	SendSetQuality(deviceID string, seq uint64, mode string) error
}

type Manager struct {
	mu      sync.Mutex
	maxHD   int
	leases  map[string]*Lease
	queue   *QueueItem // 仅 1 条
	devices *device.Manager
	sender  Sender
	qTO     time.Duration
	onEvict func(deviceID string)
}

func New(maxHD int, qTimeout time.Duration, dm *device.Manager, sender Sender) *Manager {
	return &Manager{
		maxHD:   maxHD,
		leases:  map[string]*Lease{},
		devices: dm,
		sender:  sender,
		qTO:     qTimeout,
	}
}

func (m *Manager) SetEvictHook(fn func(string)) { m.onEvict = fn }

type AcquireResult struct {
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Evicted []string `json:"evicted,omitempty"`
	Seq     uint64   `json:"switchSeq,omitempty"`
}

func (m *Manager) Release(deviceID string) {
	m.mu.Lock()
	delete(m.leases, deviceID)
	m.mu.Unlock()
	m.devices.SetMode(deviceID, "preview")
}

func (m *Manager) OnCtrlLost(deviceID string) {
	m.Release(deviceID)
	m.mu.Lock()
	if m.queue != nil && m.queue.DeviceID == deviceID {
		m.queue = nil
	}
	m.mu.Unlock()
}

func (m *Manager) Acquire(deviceID, sessionID string, pageVisible bool) AcquireResult {
	if !pageVisible {
		return AcquireResult{OK: false, Error: errcode.PageNotVisible}
	}
	d := m.devices.Get(deviceID)
	if d == nil {
		return AcquireResult{OK: false, Error: errcode.DeviceDestroyed}
	}
	ctrl, mtxUp, tsOK, switching, _, _ := d.SnapshotFlags()
	if !ctrl {
		return AcquireResult{OK: false, Error: errcode.NotCtrlOnline}
	}
	if !(mtxUp && tsOK) {
		return AcquireResult{OK: false, Error: errcode.TSPullFailed}
	}
	if switching {
		return m.enqueue(deviceID, sessionID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if l, ok := m.leases[deviceID]; ok {
		return AcquireResult{OK: true, Seq: l.SwitchSeq}
	}

	var evicted []string
	if len(m.leases) >= m.maxHD {
		victim := m.oldestNonFocused()
		if victim == "" {
			return AcquireResult{OK: false, Error: errcode.MaxHD}
		}
		if m.devices.IsSwitching(victim) {
			return m.enqueueLocked(deviceID, sessionID)
		}
		delete(m.leases, victim)
		evicted = append(evicted, victim)
		go m.kickPreview(victim)
	}

	seq := device.NextSwitchSeq()
	m.leases[deviceID] = &Lease{
		DeviceID: deviceID, ActivateAt: time.Now(), SwitchSeq: seq,
	}
	m.devices.SetFocused(deviceID)
	m.leases[deviceID].IsFocused = true

	if err := m.devices.BeginSwitch(deviceID, seq, "hd"); err != nil {
		delete(m.leases, deviceID)
		return AcquireResult{OK: false, Error: err.Error()}
	}
	go func() {
		_ = m.sender.SendSetQuality(deviceID, seq, "hd")
	}()
	return AcquireResult{OK: true, Seq: seq, Evicted: evicted}
}

func (m *Manager) enqueue(deviceID, sessionID string) AcquireResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enqueueLocked(deviceID, sessionID)
}

func (m *Manager) enqueueLocked(deviceID, sessionID string) AcquireResult {
	seq := device.NextSwitchSeq()
	m.queue = &QueueItem{
		DeviceID: deviceID, SwitchSeq: seq, SessionID: sessionID,
		Deadline: time.Now().Add(m.qTO),
	}
	return AcquireResult{OK: false, Error: errcode.TargetSwitching, Seq: seq}
}

func (m *Manager) oldestNonFocused() string {
	var best string
	var bestT time.Time
	for id, l := range m.leases {
		if l.IsFocused {
			continue
		}
		if best == "" || l.ActivateAt.Before(bestT) {
			best = id
			bestT = l.ActivateAt
		}
	}
	return best
}

func (m *Manager) kickPreview(deviceID string) {
	seq := device.NextSwitchSeq()
	_ = m.devices.BeginSwitch(deviceID, seq, "preview")
	_ = m.sender.SendSetQuality(deviceID, seq, "preview")
	if m.onEvict != nil {
		m.onEvict(deviceID)
	}
}

// ReevaluateOnce：目标退出 switching 后单次重评估排队。
func (m *Manager) ReevaluateOnce() {
	m.mu.Lock()
	q := m.queue
	if q == nil {
		m.mu.Unlock()
		return
	}
	if time.Now().After(q.Deadline) {
		m.queue = nil
		m.mu.Unlock()
		return
	}
	if m.devices.IsSwitching(q.DeviceID) {
		m.mu.Unlock()
		return
	}
	m.queue = nil
	id, sid := q.DeviceID, q.SessionID
	m.mu.Unlock()
	_ = m.Acquire(id, sid, true)
}

func (m *Manager) SyncFocusFlags() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices.Range(func(d *device.Device) bool {
		_, _, _, _, focused, _ := d.SnapshotFlags()
		id := d.ID
		if l, ok := m.leases[id]; ok {
			l.IsFocused = focused
		}
		return true
	})
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.leases)
}
