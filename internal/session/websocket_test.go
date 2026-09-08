package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/protocol/ws"
)

func newSessionTestConnection(t *testing.T, ctx context.Context, version ws.Version, cfg SessionConfig) (*ws.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		accepted <- raw
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	peer, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.CloseNow() })
	var raw *websocket.Conn
	select {
	case raw = <-accepted:
	case <-dialCtx.Done():
		t.Fatal(dialCtx.Err())
	}
	t.Cleanup(func() { _ = raw.CloseNow() })
	cfg = NormalizeConfig(cfg)
	conn, err := ws.NewConn(raw, ws.Options{
		Version:             version,
		MaxTextMessageBytes: cfg.MaxWSTextMessageBytes,
		MaxOpusPacketBytes:  cfg.MaxOpusPacketBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return conn, peer
}

func newTestSession(t *testing.T, ctx context.Context, output WSConn, opts Options) *Session {
	t.Helper()
	conn, peer := newSessionTestConnection(t, ctx, ws.Version1, opts.Config)
	opts.Conn = conn
	sess := NewSession(ctx, opts)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			typ, data, err := peer.Read(ctx)
			if err != nil {
				return
			}
			if err := output.Write(ctx, typ, data); err != nil {
				t.Errorf("record websocket output: %v", err)
				return
			}
		}
	}()
	t.Cleanup(func() {
		sess.Close()
		_ = peer.CloseNow()
		select {
		case <-sess.Done():
		case <-time.After(5 * time.Second):
			t.Error("session did not stop")
		}
		<-readDone
	})
	return sess
}

func testSessionBySerial(reg *Registry, serialNumber string) *Session {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.byDevice[serialNumber]
}

func TestSessionWireHandshakeFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  websocket.MessageType
		data []byte
		code websocket.StatusCode
	}{
		{"binary_first", websocket.MessageBinary, []byte{0x11}, websocket.StatusPolicyViolation},
		{"empty_audio", websocket.MessageBinary, nil, websocket.StatusPolicyViolation},
		{"bad_hello", websocket.MessageText, []byte(`{"type":"hello","version":4}`), websocket.StatusPolicyViolation},
		{"long_number", websocket.MessageText, []byte(`{"type":"hello","version":` + strings.Repeat("9", 200) + `}`), websocket.StatusPolicyViolation},
		{"long_hello", websocket.MessageText, []byte(promptToneTestHello + strings.Repeat(" ", 512)), websocket.StatusMessageTooBig},
		{"library_limit", websocket.MessageText, []byte(strings.Repeat(" ", 2048)), websocket.StatusMessageTooBig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cfg := SessionConfig{MaxWSTextMessageBytes: 512}
			conn, peer := newSessionTestConnection(t, ctx, ws.Version1, cfg)
			sess := NewSession(ctx, Options{Conn: conn, Config: cfg})
			go func() { _ = sess.Run() }()
			defer func() { sess.Close(); <-sess.Done() }()
			if err := peer.Write(ctx, tc.typ, tc.data); err != nil {
				t.Fatal(err)
			}
			_, _, err := peer.Read(ctx)
			if websocket.CloseStatus(err) != tc.code {
				t.Fatalf("close = %v, want %v", err, tc.code)
			}
		})
	}
}

func TestSessionWireHelloTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := SessionConfig{HelloTimeout: 30 * time.Millisecond}
	conn, peer := newSessionTestConnection(t, ctx, ws.Version1, cfg)
	sess := NewSession(ctx, Options{Conn: conn, Config: cfg})
	go func() { _ = sess.Run() }()
	defer func() { sess.Close(); <-sess.Done() }()
	if _, _, err := peer.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("hello timeout = %v", err)
	}
}

func TestSessionWireListeningTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := SessionConfig{MaxListeningDuration: 30 * time.Millisecond}
	conn, peer := newSessionTestConnection(t, ctx, ws.Version1, cfg)
	sess := NewSession(ctx, Options{Conn: conn, Config: cfg, ASRClient: &waitingASRClient{}})
	go func() { _ = sess.Run() }()
	defer func() { sess.Close(); <-sess.Done() }()
	if err := peer.Write(ctx, websocket.MessageText, []byte(promptToneTestHello)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := peer.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err := peer.Write(ctx, websocket.MessageText, []byte(`{"type":"listen","state":"start","mode":"auto"}`)); err != nil {
		t.Fatal(err)
	}
	_, _, err := peer.Read(ctx)
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.StatusPolicyViolation || closeErr.Reason != "listening timeout" {
		t.Fatalf("listening timeout = %v", err)
	}
}

func TestSessionIgnoresStaleListeningTimeout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       State
		turnId      uint64
		inputClosed bool
	}{
		{"previous_turn", StateTurnActive, 1, false},
		{"input_closed", StateTurnActive, 2, true},
		{"turn_finished", StateReady, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, peer := newSessionTestConnection(t, ctx, ws.Version1, SessionConfig{})
			sess := NewSession(ctx, Options{Conn: conn})
			defer func() { _ = peer.CloseNow(); sess.cleanup() }()
			sess.runtime.state = tc.state
			sess.runtime.currentTurnId = 2
			sess.runtime.turnInputClosed = tc.inputClosed
			if sess.handleEvent(sessionEvent{kind: eventKindListeningTimeout, turnId: tc.turnId}) || sess.ctx.Err() != nil {
				t.Fatal("stale listening timeout closed the session")
			}
		})
	}
}

func TestSessionWireIgnoresLateHelloTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, peer := newSessionTestConnection(t, ctx, ws.Version1, SessionConfig{})
	sess := NewSession(ctx, Options{Conn: conn})
	go func() { _ = sess.Run() }()
	defer func() { sess.Close(); <-sess.Done() }()
	if err := peer.Write(ctx, websocket.MessageText, []byte(promptToneTestHello)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := peer.Read(ctx); err != nil {
		t.Fatal(err)
	}
	sess.postEvent(sessionEvent{kind: eventKindHelloTimeout})
	if err := peer.Write(ctx, websocket.MessageText, []byte(promptToneTestHello)); err != nil {
		t.Fatal(err)
	}
	_, _, err := peer.Read(ctx)
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.StatusPolicyViolation || closeErr.Reason != "duplicate hello" {
		t.Fatalf("late timeout changed handshake state: %v", err)
	}
}

func TestSessionCloseCancelsTurnBeforeHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, peer := newSessionTestConnection(t, ctx, ws.Version1, SessionConfig{})
	sess := NewSession(ctx, Options{Conn: conn})
	defer sess.cleanup()
	turnCtx, turnCancel := context.WithCancel(sess.ctx)
	sess.runtime.turnCancel = turnCancel
	closed := make(chan struct{})
	go func() {
		sess.closeWithReason(websocket.StatusPolicyViolation, "invalid audio frame")
		close(closed)
	}()
	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("turn remained active while waiting for close handshake")
	}
	if _, _, err := peer.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("close frame was not delivered: %v", err)
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestSessionAudioBackpressure(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "active"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cfg := SessionConfig{ASRPCMQueueCapacity: 1}
			conn, peer := newSessionTestConnection(t, ctx, ws.Version1, cfg)
			sess := NewSession(ctx, Options{Conn: conn, Config: cfg})
			defer sess.cleanup()
			sess.runtime.state = StateTurnActive
			turnCtx, turnCancel := context.WithCancel(sess.ctx)
			sess.runtime.turnCancel = turnCancel
			if pending {
				sess.runtime.turnInputClosed = true
				sess.runtime.pendingTurn = &PendingTurn{audioBuffers: [][]byte{{0x11}}}
			} else {
				sess.runtime.turnInputCh = make(chan []byte, 1)
				sess.runtime.turnInputCh <- []byte{0x11}
			}
			closed := make(chan struct{})
			go func() {
				sess.handleAudioFrame([]byte{0x22})
				close(closed)
			}()
			if _, _, err := peer.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("backpressure close = %v", err)
			}
			select {
			case <-closed:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if turnCtx.Err() == nil || sess.ctx.Err() == nil {
				t.Fatal("backpressure did not cancel the turn and session")
			}
		})
	}
}
