package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type mockWSConn struct {
	mu       sync.Mutex
	messages []mockWSMessage
}

type mockWSMessage struct {
	typ     websocket.MessageType
	payload []byte
}

func (m *mockWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	m.messages = append(m.messages, mockWSMessage{typ: typ, payload: cp})
	return nil
}

func (m *mockWSConn) getMessages() []mockWSMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]mockWSMessage, len(m.messages))
	copy(res, m.messages)
	return res
}

type manualTicker struct {
	ch chan time.Time
}

func newManualTicker() *manualTicker {
	return &manualTicker{ch: make(chan time.Time, 100)}
}

func (t *manualTicker) C() <-chan time.Time {
	return t.ch
}

func (t *manualTicker) Stop() {}

func (t *manualTicker) Tick() {
	t.ch <- time.Now()
}

func TestDownlinkPacer_SendPacketAndFinishInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	ticker := newManualTicker()
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		writer:     writer,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-pacer-test",
		generation: 1,
		state:      StateSpeaking,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 10, func(d time.Duration) Ticker {
		return ticker
	})

	go pacer.Run()

	pkt1 := []byte{0x01, 0x02, 0x03}
	pkt2 := []byte{0x04, 0x05, 0x06}

	if err := pacer.Enqueue(pkt1); err != nil {
		t.Fatalf("Enqueue pkt1 failed: %v", err)
	}
	if err := pacer.Enqueue(pkt2); err != nil {
		t.Fatalf("Enqueue pkt2 failed: %v", err)
	}

	userText := "你好"
	assistantText := "你好，我是小智"
	pacer.FinishInput(userText, assistantText)

	// 触发定时器推动音频包逐包发送
	for i := 0; i < 5; i++ {
		ticker.Tick()
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-pacer.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("pacer did not finish in time")
	}

	if !pacer.HasSentStart() {
		t.Fatal("expected tts.start to have been sent")
	}

	// 检查 session 接收到了 eventKindTurnFinished 事件
	select {
	case ev := <-sess.events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.userText != userText || ev.assistantText != assistantText {
			t.Fatalf("unexpected turn texts: user=%s assistant=%s", ev.userText, ev.assistantText)
		}
		// 处理该事件以验证历史追加与 tts.stop
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected TurnFinished event on session")
	}

	history := sess.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages, got %d", len(history))
	}
	if history[0].Content != userText || history[1].Content != assistantText {
		t.Fatalf("unexpected history content: %+v", history)
	}

	msgs := conn.getMessages()
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages (start, pkts, stop), got %d", len(msgs))
	}

	var firstMsg map[string]any
	if err := json.Unmarshal(msgs[0].payload, &firstMsg); err != nil {
		t.Fatalf("failed to unmarshal first message: %v", err)
	}
	if firstMsg["type"] != "tts" || firstMsg["state"] != "start" {
		t.Fatalf("expected first message to be tts.start, got %+v", firstMsg)
	}
}

func TestDownlinkPacer_FinishInputEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	ticker := newManualTicker()
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		writer:     writer,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-empty-test",
		generation: 1,
		state:      StateSpeaking,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 10, func(d time.Duration) Ticker {
		return ticker
	})

	go pacer.Run()

	pacer.FinishInput("", "")

	select {
	case <-pacer.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pacer did not finish in time")
	}

	select {
	case ev := <-sess.events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.userText != "" || ev.assistantText != "" {
			t.Fatalf("expected empty texts, got user=%s assistant=%s", ev.userText, ev.assistantText)
		}
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected TurnFinished event on session")
	}

	if len(sess.History()) != 0 {
		t.Fatalf("expected 0 history messages, got %d", len(sess.History()))
	}
}

func TestDownlinkPacer_Stop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		writer:     writer,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-stop-test",
		generation: 1,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 10, nil)
	go pacer.Run()

	_ = pacer.Enqueue([]byte{0x01})
	pacer.Stop()

	select {
	case <-pacer.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pacer did not exit on stop")
	}

	if err := pacer.Enqueue([]byte{0x02}); err != ErrPacerStopped {
		t.Fatalf("expected ErrPacerStopped after stop, got %v", err)
	}
}

func TestDownlinkPacer_ConcurrencyRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		writer:     writer,
		logger:     slog.Default(),
		events:     make(chan event, 100),
		sessionId:  "sess-race-test",
		generation: 1,
		state:      StateSpeaking,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()

	var wg sync.WaitGroup
	// 并发 Enqueue
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = pacer.Enqueue([]byte{0x01, 0x02})
			}
		}()
	}

	// 并发查询/调用
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		pacer.FinishInput("user", "assistant")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = pacer.HasSentStart()
		_ = pacer.HasSentStop()
	}()

	wg.Wait()
	pacer.Stop()
	<-pacer.Done()
}

