package tsgateway

import (
	"encoding/binary"
	"io"
	"time"
)

const (
	pidPAT = 0x0000
	pidPMT = 0x1000
	pidPCR = 0x0100
	pidVID = 0x0100
)

// Muxer：H264 Annex-B AU → MPEG-TS；维护单调 90kHz PTS/PCR；周期 PAT/PMT。
type Muxer struct {
	w            io.Writer
	ccPAT, ccPMT, ccVID byte
	pts90k       int64
	pcr90k       int64
	lastPAT      time.Time
	lastPCR      time.Time
	patEvery     time.Duration
	pcrEvery     time.Duration
	started      bool
	discont      bool
}

func NewMuxer(w io.Writer, patMs, pcrMs int) *Muxer {
	if patMs <= 0 {
		patMs = 500
	}
	if pcrMs <= 0 {
		pcrMs = 100
	}
	return &Muxer{
		w:        w,
		patEvery: time.Duration(patMs) * time.Millisecond,
		pcrEvery: time.Duration(pcrMs) * time.Millisecond,
		discont:  true,
	}
}

func (m *Muxer) ResetTimeline() {
	m.pts90k = 0
	m.pcr90k = 0
	m.discont = true
	m.started = false
	m.lastPAT = time.Time{}
	m.lastPCR = time.Time{}
}

// WriteAU 写入一个访问单元；keyframe 时允许作为流起点。
func (m *Muxer) WriteAU(annexB []byte, keyframe bool, duration90k int64) error {
	now := time.Now()
	if !m.started {
		if !keyframe {
			return nil // 等 IDR
		}
		m.started = true
		m.discont = true
		if err := m.writePATPMT(true); err != nil {
			return err
		}
		m.lastPAT = now
	}
	if now.Sub(m.lastPAT) >= m.patEvery {
		if err := m.writePATPMT(false); err != nil {
			return err
		}
		m.lastPAT = now
	}
	if duration90k <= 0 {
		duration90k = 3000 // ~33ms @90k
	}
	m.pts90k += duration90k
	m.pcr90k = m.pts90k
	withPCR := now.Sub(m.lastPCR) >= m.pcrEvery || m.discont
	if withPCR {
		m.lastPCR = now
	}
	err := m.writePES(annexB, m.pts90k, withPCR)
	m.discont = false
	return err
}

func (m *Muxer) writePATPMT(force bool) error {
	_ = force
	pat := buildPAT(m.ccPAT)
	m.ccPAT = (m.ccPAT + 1) & 0x0f
	if err := writeTS(m.w, pat); err != nil {
		return err
	}
	pmt := buildPMT(m.ccPMT)
	m.ccPMT = (m.ccPMT + 1) & 0x0f
	return writeTS(m.w, pmt)
}

func (m *Muxer) writePES(payload []byte, pts int64, withPCR bool) error {
	pes := buildPES(payload, pts)
	return m.writePayload(pidVID, &m.ccVID, pes, withPCR, pts)
}

func (m *Muxer) writePayload(pid uint16, cc *byte, payload []byte, withPCR bool, pcrPTS int64) error {
	off := 0
	first := true
	for off < len(payload) {
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		pkt[1] = byte(pid >> 8) & 0x1f
		if first {
			pkt[1] |= 0x40 // payload unit start
		}
		pkt[2] = byte(pid)
		adapt := withPCR && first
		afLen := 0
		if adapt {
			afLen = 1 + 6 // flags + PCR
			if m.discont {
				afLen = 1 + 6
			}
		}
		avail := 184
		if adapt {
			pkt[3] = 0x30 | (*cc & 0x0f) // adapt + payload
			pkt[4] = byte(afLen)
			flags := byte(0x10) // PCR flag
			if m.discont {
				flags |= 0x80 // discontinuity
			}
			pkt[5] = flags
			writePCR(pkt[6:12], pcrPTS)
			avail = 188 - 6 - afLen
			if avail < 0 {
				avail = 0
			}
			start := 6 + afLen
			n := min(avail, len(payload)-off)
			copy(pkt[start:start+n], payload[off:off+n])
			for i := start + n; i < 188; i++ {
				pkt[i] = 0xff
			}
			off += n
		} else {
			pkt[3] = 0x10 | (*cc & 0x0f)
			n := min(184, len(payload)-off)
			copy(pkt[4:4+n], payload[off:off+n])
			for i := 4 + n; i < 188; i++ {
				pkt[i] = 0xff
			}
			off += n
		}
		*cc = (*cc + 1) & 0x0f
		if err := writeTS(m.w, pkt); err != nil {
			return err
		}
		first = false
		withPCR = false
	}
	return nil
}

func writeTS(w io.Writer, pkt []byte) error {
	_, err := w.Write(pkt)
	return err
}

func writePCR(b []byte, pts90k int64) {
	// PCR = pts * 300 for 27MHz base; simplified: use 90k in PCR base field
	pcr := uint64(pts90k) * 300
	base := pcr / 300
	b[0] = byte(base >> 25)
	b[1] = byte(base >> 17)
	b[2] = byte(base >> 9)
	b[3] = byte(base >> 1)
	b[4] = byte((base&1)<<7) | 0x7e
	b[5] = 0x00
}

func buildPAT(cc byte) []byte {
	pkt := make([]byte, 188)
	for i := range pkt {
		pkt[i] = 0xff
	}
	pkt[0] = 0x47
	pkt[1] = 0x40
	pkt[2] = 0x00
	pkt[3] = 0x10 | (cc & 0x0f)
	// pointer
	pkt[4] = 0x00
	// PAT section
	sec := []byte{
		0x00, 0xb0, 0x0d, // table_id + section length
		0x00, 0x01, 0xc1, 0x00, 0x00,
		0x00, 0x01, 0xe0, 0x00, // program 1 -> PMT pid 0x1000 later
	}
	// program_map_PID = 0x1000
	sec[10] = 0xe0 | byte((pidPMT>>8)&0x1f)
	sec[11] = byte(pidPMT & 0xff)
	crc := mpegCRC(sec)
	sec = append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	copy(pkt[5:], sec)
	return pkt
}

func buildPMT(cc byte) []byte {
	pkt := make([]byte, 188)
	for i := range pkt {
		pkt[i] = 0xff
	}
	pkt[0] = 0x47
	pkt[1] = 0x40 | byte((pidPMT>>8)&0x1f)
	pkt[2] = byte(pidPMT & 0xff)
	pkt[3] = 0x10 | (cc & 0x0f)
	pkt[4] = 0x00
	sec := []byte{
		0x02, 0xb0, 0x12,
		0x00, 0x01, 0xc1, 0x00, 0x00,
		0xe0 | byte((pidPCR>>8)&0x1f), byte(pidPCR & 0xff),
		0xf0, 0x00,
		0x1b, // H264
		0xe0 | byte((pidVID>>8)&0x1f), byte(pidVID & 0xff),
		0xf0, 0x00,
	}
	crc := mpegCRC(sec)
	sec = append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	copy(pkt[5:], sec)
	return pkt
}

func buildPES(payload []byte, pts int64) []byte {
	hdr := make([]byte, 0, 19+len(payload))
	hdr = append(hdr, 0x00, 0x00, 0x01, 0xe0)
	pesLen := len(payload) + 13
	if pesLen > 0xffff {
		pesLen = 0
	}
	hdr = append(hdr, byte(pesLen>>8), byte(pesLen))
	hdr = append(hdr, 0x80, 0x80, 0x05) // pts only
	hdr = appendPTS(hdr, pts)
	hdr = append(hdr, payload...)
	return hdr
}

func appendPTS(b []byte, pts int64) []byte {
	p := uint64(pts) & 0x1ffffffff
	b = append(b, byte(0x20|((p>>29)&0x0e)|1))
	b = append(b, byte((p>>22)&0xff))
	b = append(b, byte(((p>>14)&0xfe)|1))
	b = append(b, byte((p>>7)&0xff))
	b = append(b, byte(((p<<1)&0xfe)|1))
	return b
}

func mpegCRC(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, v := range data {
		crc ^= uint32(v) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// IsIDR 粗判 Annex-B
func IsIDR(annexB []byte) bool {
	for i := 0; i+4 < len(annexB); i++ {
		if annexB[i] == 0 && annexB[i+1] == 0 {
			sc := 0
			if annexB[i+2] == 1 {
				sc = i + 3
			} else if annexB[i+2] == 0 && annexB[i+3] == 1 {
				sc = i + 4
			}
			if sc > 0 && sc < len(annexB) {
				nal := annexB[sc] & 0x1f
				if nal == 5 {
					return true
				}
			}
		}
	}
	return false
}

// RTPTimestampMapper RTP ts → 单调 90kHz
type RTPTimestampMapper struct {
	baseRTP  uint32
	basePTS  int64
	lastRTP  uint32
	have     bool
	wrapBias uint64
}

func (m *RTPTimestampMapper) Map(rtpTS uint32) int64 {
	if !m.have {
		m.baseRTP = rtpTS
		m.basePTS = 0
		m.lastRTP = rtpTS
		m.have = true
		return 0
	}
	delta := int32(rtpTS - m.lastRTP)
	m.lastRTP = rtpTS
	m.basePTS += int64(delta)
	return m.basePTS
}

func (m *RTPTimestampMapper) Reset() { *m = RTPTimestampMapper{} }

// 保证未使用 binary 时不报错
var _ = binary.BigEndian
