package ctrl

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"

	"trollvnc-wall/internal/config"
	"trollvnc-wall/internal/device"
	"trollvnc-wall/internal/errcode"
)

type QualityProvider interface {
	PreviewParams() (w, h, br, fps int)
	HDParams() (w, h, br, fps int)
	AllowResSwitch() bool
}

type Server struct {
	cfg   *config.Config
	dm    *device.Manager
	qp    QualityProvider
	ln    net.Listener

	mu       sync.Mutex
	sessions map[string]*Session // deviceID -> session
}

type Session struct {
	deviceID string
	gen      uint64
	conn     net.Conn
	dead     bool

	mu       sync.Mutex
	pending  map[uint64]*pendingCmd // seq -> reliable
	lastMove *Envelope              // 覆盖型
	writeCh  chan []byte
	cancel   context.CancelFunc
}

type pendingCmd struct {
	payload []byte
	tries   int
	expire  time.Time
}

func NewServer(cfg *config.Config, dm *device.Manager, qp QualityProvider) *Server {
	return &Server{
		cfg: cfg, dm: dm, qp: qp,
		sessions: map[string]*Session{},
	}
}

func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Server.CtrlListen)
	if err != nil {
		return err
	}
	s.ln = ln
	go s.acceptLoop(ctx)
	go s.heartbeatScanner(ctx)
	go s.retransmitLoop(ctx)
	return nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("ctrl accept from %s", remote)
	_ = conn.SetDeadline(time.Now().Add(time.Duration(s.cfg.Ctrl.ReadTimeoutMs) * time.Millisecond * 3))

	raw, err := ReadFrame(conn, s.cfg.Ctrl.MaxFrameSize)
	if err != nil {
		log.Printf("ctrl hello read fail from %s: %v", remote, err)
		return
	}
	env, err := Decode(raw)
	if err != nil || env.Type != "hello" {
		log.Printf("ctrl bad hello from %s type=%q err=%v", remote, env.Type, err)
		return
	}
	var hello HelloBody
	if err := json.Unmarshal(env.Body, &hello); err != nil {
		log.Printf("ctrl hello body from %s: %v raw=%s", remote, err, string(raw))
		return
	}
	// 必须先经 API 登记 token，禁止 hello 自注册
	if !s.dm.ValidateToken(hello.DeviceID, hello.Token) {
		log.Printf("ctrl reject unknown device/token id=%s from %s", hello.DeviceID, remote)
		return
	}
	log.Printf("ctrl hello ok id=%s from %s", hello.DeviceID, remote)

	// 先 dead 旧 session，再绑新（早于后续消息）
	s.killSession(hello.DeviceID)

	gen, err := s.dm.BindCtrlSession(hello.DeviceID)
	if err != nil {
		return
	}

	cctx, cancel := context.WithCancel(ctx)
	sess := &Session{
		deviceID: hello.DeviceID,
		gen:      gen,
		conn:     conn,
		pending:  map[uint64]*pendingCmd{},
		writeCh:  make(chan []byte, 64),
		cancel:   cancel,
	}
	s.mu.Lock()
	s.sessions[hello.DeviceID] = sess
	s.mu.Unlock()

	ack, _ := Encode(Envelope{Type: "hello_ok", Seq: 0})
	_ = WriteFrame(conn, ack)

	go sess.writeLoop(s.cfg)
	defer func() {
		cancel()
		s.mu.Lock()
		if cur, ok := s.sessions[hello.DeviceID]; ok && cur == sess {
			delete(s.sessions, hello.DeviceID)
		}
		s.mu.Unlock()
		sess.mu.Lock()
		sess.dead = true
		sess.mu.Unlock()
		s.dm.MarkSessionDead(hello.DeviceID, gen)
	}()

	rt := time.Duration(s.cfg.Ctrl.ReadTimeoutMs) * time.Millisecond
	for {
		select {
		case <-cctx.Done():
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(rt))
		frame, err := ReadFrame(conn, s.cfg.Ctrl.MaxFrameSize)
		if err != nil {
			return
		}
		s.onFrame(sess, frame)
	}
}

func (s *Server) killSession(deviceID string) {
	s.mu.Lock()
	old, ok := s.sessions[deviceID]
	if ok {
		delete(s.sessions, deviceID)
	}
	s.mu.Unlock()
	if ok {
		old.mu.Lock()
		old.dead = true
		old.pending = map[uint64]*pendingCmd{}
		old.mu.Unlock()
		if old.cancel != nil {
			old.cancel()
		}
		_ = old.conn.Close()
	}
}

func (s *Server) onFrame(sess *Session, frame []byte) {
	env, err := Decode(frame)
	if err != nil {
		return
	}
	if !s.dm.SessionAlive(sess.deviceID, sess.gen) {
		return
	}
	switch env.Type {
	case "heartbeat":
		_ = s.dm.Heartbeat(sess.deviceID, sess.gen)
		if s.cfg.Logging.HeartbeatDebug {
			log.Printf("hb %s", sess.deviceID)
		}
	case "ack":
		sess.mu.Lock()
		delete(sess.pending, env.Seq)
		sess.mu.Unlock()
	case "switch_ack":
		var body SwitchAckBody
		_ = json.Unmarshal(env.Body, &body)
		seq, switching := s.dm.CurrentSwitchSeq(sess.deviceID)
		if !switching || seq != env.Seq {
			s.replyErr(sess, env.Seq, errcode.SwitchSeqStale)
			return
		}
		// ack 不直接成功；等 idr_seen / idr_emitted
		_ = body
	case "move":
		// 覆盖型：不入可靠重传
		sess.mu.Lock()
		sess.lastMove = &env
		sess.mu.Unlock()
	default:
	}
}

func (s *Server) replyErr(sess *Session, seq uint64, code string) {
	b, _ := json.Marshal(map[string]string{"error": code})
	payload, _ := Encode(Envelope{Type: "error", Seq: seq, Body: b})
	sess.enqueueWrite(payload)
}

func (sess *Session) enqueueWrite(payload []byte) {
	sess.mu.Lock()
	dead := sess.dead
	sess.mu.Unlock()
	if dead {
		return
	}
	select {
	case sess.writeCh <- payload:
	default:
	}
}

func (sess *Session) writeLoop(cfg *config.Config) {
	wt := time.Duration(cfg.Ctrl.WriteTimeoutMs) * time.Millisecond
	for payload := range sess.writeCh {
		_ = sess.conn.SetWriteDeadline(time.Now().Add(wt))
		if err := WriteFrame(sess.conn, payload); err != nil {
			sess.cancel()
			return
		}
	}
}

// SendSetQuality 可靠下行；IsSwitching 时由上层保证单飞。
func (s *Server) SendSetQuality(deviceID string, seq uint64, mode string) error {
	s.mu.Lock()
	sess, ok := s.sessions[deviceID]
	s.mu.Unlock()
	if !ok {
		return errString(errcode.NotCtrlOnline)
	}
	sess.mu.Lock()
	if sess.dead {
		sess.mu.Unlock()
		return errString(errcode.SessionDead)
	}
	// switching 中丢弃新 set_quality 并清重传：由 BeginSwitch 保证；此处若已有 pending set_quality 则清
	for id := range sess.pending {
		delete(sess.pending, id)
	}
	sess.mu.Unlock()

	w, h, br, fps := s.qp.PreviewParams()
	if mode == "hd" {
		w, h, br, fps = s.qp.HDParams()
	}
	body, _ := json.Marshal(SetQualityBody{
		Mode: mode, ForceIDR: true, Width: w, Height: h, Bitrate: br, FPS: fps,
	})
	payload, _ := Encode(Envelope{Type: "set_quality", Seq: seq, Body: body})
	expire := time.Now().Add(time.Duration(s.cfg.Ctrl.SwitchTimeoutMs) * time.Millisecond)
	sess.mu.Lock()
	sess.pending[seq] = &pendingCmd{payload: payload, tries: 0, expire: expire}
	sess.mu.Unlock()
	sess.enqueueWrite(payload)
	return nil
}

func (s *Server) ClearRetransmit(deviceID string) {
	s.mu.Lock()
	sess := s.sessions[deviceID]
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	sess.pending = map[uint64]*pendingCmd{}
	sess.mu.Unlock()
}

func (s *Server) heartbeatScanner(ctx context.Context) {
	iv := time.Duration(s.cfg.Ctrl.HeartbeatIntervalMs) * time.Millisecond
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.dm.TickHeartbeats(s.cfg.Ctrl.HeartbeatMissLimit)
		}
	}
}

func (s *Server) retransmitLoop(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			list := make([]*Session, 0, len(s.sessions))
			for _, sess := range s.sessions {
				list = append(list, sess)
			}
			s.mu.Unlock()
			now := time.Now()
			for _, sess := range list {
				sess.mu.Lock()
				for seq, p := range sess.pending {
					if now.After(p.expire) {
						delete(sess.pending, seq)
						continue
					}
					if p.tries >= s.cfg.Ctrl.ReliableRetries {
						continue
					}
					p.tries++
					payload := p.payload
					sess.mu.Unlock()
					sess.enqueueWrite(payload)
					sess.mu.Lock()
				}
				sess.mu.Unlock()
			}
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
