package gop

import (
	"math"
	"sync"
)

// Tracker: effectiveGop = clamp(EWMA, min, cfg)；只降不升超配置；抖动不立即下调。
type Tracker struct {
	mu      sync.Mutex
	cfgGOP  float64
	minGOP  float64
	ewma    float64
	have    bool
	alpha   float64
	lastObs float64
}

func New(cfgGOP, minGOP float64) *Tracker {
	return &Tracker{cfgGOP: cfgGOP, minGOP: minGOP, alpha: 0.25}
}

func (t *Tracker) ObserveIDRInterval(seconds float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seconds <= 0 || math.IsNaN(seconds) {
		return
	}
	t.lastObs = seconds
	if !t.have {
		t.ewma = seconds
		t.have = true
		return
	}
	// 抖动过大：相对 EWMA 偏差 >50% 则忽略本次下调意图
	if seconds < t.ewma*0.5 {
		return
	}
	t.ewma = t.alpha*seconds + (1-t.alpha)*t.ewma
}

func (t *Tracker) Effective() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.have {
		return t.cfgGOP
	}
	v := t.ewma
	if v > t.cfgGOP {
		v = t.cfgGOP // 不上调超配置
	}
	if v < t.minGOP {
		v = t.minGOP
	}
	return v
}

func (t *Tracker) LastObserved() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastObs
}
