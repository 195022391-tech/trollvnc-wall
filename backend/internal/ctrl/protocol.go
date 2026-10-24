package ctrl

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type Envelope struct {
	Type string          `json:"type"`
	Seq  uint64          `json:"seq,omitempty"`
	Body json.RawMessage `json:"body,omitempty"`
}

type HeartbeatBody struct{}

type SetQualityBody struct {
	Mode     string `json:"mode"` // preview | hd
	ForceIDR bool   `json:"forceIdr"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Bitrate  int    `json:"bitrateKbps,omitempty"`
	FPS      int    `json:"fps,omitempty"`
}

type SwitchAckBody struct {
	OK     bool   `json:"ok"`
	ErrMsg string `json:"errMsg,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type MoveBody struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type HelloBody struct {
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
	Name     string `json:"name,omitempty"`
}

func ReadFrame(r io.Reader, max int) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if int(n) <= 0 || int(n) > max {
		return nil, fmt.Errorf("bad frame len %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func WriteFrame(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func Encode(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}

func Decode(b []byte) (Envelope, error) {
	var e Envelope
	err := json.Unmarshal(b, &e)
	return e, err
}
