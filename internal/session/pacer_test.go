package session

import (
	"context"
	"encoding/json"
	"errors"
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
	var startedCalled, completedCalled bool
	var mu sync.Mutex

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-pacer-test",
		Sender:    writer,
		QueueCap:  10,
		TickerFactory: func(d time.Duration) Ticker {
			return ticker
		},
		Logger: slog.Default(),
		Callbacks: PacerCallbacks{
			OnStarted: func() {
				mu.Lock()
				startedCalled = true
				mu.Unlock()
			},
			OnCompleted: func() {
				mu.Lock()
				completedCalled = true
				mu.Unlock()
			},
		},
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

	pacer.FinishInput()

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

	mu.Lock()
	started := startedCalled
	completed := completedCalled
	mu.Unlock()

	if !started {
		t.Fatal("expected OnStarted to be called")
	}
	if !completed {
		t.Fatal("expected OnCompleted to be called")
	}

	if !pacer.HasSentStart() {
		t.Fatal("expected tts.start to have been sent")
	}
	if !pacer.HasSentStop() {
		t.Fatal("expected tts.stop to have been sent")
	}

	time.Sleep(50 * time.Millisecond)
	msgs := conn.getMessages()
	if len(msgs) != 4 { // start, pkt1, pkt2, stop
		t.Fatalf("expected 4 messages (start, pkt1, pkt2, stop), got %d", len(msgs))
	}

	var firstMsg map[string]any
	if err := json.Unmarshal(msgs[0].payload, &firstMsg); err != nil {
		t.Fatalf("failed to unmarshal first message: %v", err)
	}
	if firstMsg["type"] != "tts" || firstMsg["state"] != "start" {
		t.Fatalf("expected first message to be tts.start, got %+v", firstMsg)
	}

	var lastMsg map[string]any
	if err := json.Unmarshal(msgs[3].payload, &lastMsg); err != nil {
		t.Fatalf("failed to unmarshal last message: %v", err)
	}
	if lastMsg["type"] != "tts" || lastMsg["state"] != "stop" {
		t.Fatalf("expected last message to be tts.stop, got %+v", lastMsg)
	}
}

func TestDownlinkPacer_FinishInputEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	ticker := newManualTicker()
	var completedCalled bool
	var mu sync.Mutex

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-empty-test",
		Sender:    writer,
		QueueCap:  10,
		TickerFactory: func(d time.Duration) Ticker {
			return ticker
		},
		Callbacks: PacerCallbacks{
			OnCompleted: func() {
				mu.Lock()
				completedCalled = true
				mu.Unlock()
			},
		},
	})

	go pacer.Run()

	pacer.FinishInput()

	select {
	case <-pacer.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pacer did not finish in time")
	}

	mu.Lock()
	completed := completedCalled
	mu.Unlock()

	if !completed {
		t.Fatal("expected OnCompleted to be called even when empty")
	}

	time.Sleep(50 * time.Millisecond)
	msgs := conn.getMessages()
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages sent for empty input without start, got %d", len(msgs))
	}
}

func TestDownlinkPacer_Stop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-stop-test",
		Sender:    writer,
		QueueCap:  10,
	})
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

func TestDownlinkPacer_Abort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	ticker := newManualTicker()
	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-abort-test",
		Sender:    writer,
		QueueCap:  10,
		TickerFactory: func(d time.Duration) Ticker {
			return ticker
		},
	})
	go pacer.Run()

	_ = pacer.Enqueue([]byte{0x01})
	ticker.Tick()
	time.Sleep(10 * time.Millisecond)

	if !pacer.HasSentStart() {
		t.Fatal("expected tts.start to have been sent before abort")
	}

	pacer.Abort()

	select {
	case <-pacer.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pacer did not exit on abort")
	}

	if !pacer.HasSentStop() {
		t.Fatal("expected tts.stop to have been sent on abort")
	}
}

func TestDownlinkPacer_ConcurrencyRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-race-test",
		Sender:    writer,
		QueueCap:  100,
	})
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

	// 并发 FinishInput
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		pacer.FinishInput()
	}()

	// 并发查询
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

func TestDownlinkPacer_SentenceStartSyncOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	ticker := newManualTicker()
	var completedCalled bool
	var mu sync.Mutex

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-pacer-order-test",
		Sender:    writer,
		QueueCap:  50,
		TickerFactory: func(d time.Duration) Ticker {
			return ticker
		},
		Callbacks: PacerCallbacks{
			OnCompleted: func() {
				mu.Lock()
				completedCalled = true
				mu.Unlock()
			},
		},
	})

	go pacer.Run()

	pkt1 := []byte{0x10, 0x11}
	pkt2 := []byte{0x20, 0x21}
	pkt3 := []byte{0x30, 0x31}

	if err := pacer.EnqueueSentenceStart("第一句"); err != nil {
		t.Fatalf("EnqueueSentenceStart 1 failed: %v", err)
	}
	if err := pacer.Enqueue(pkt1); err != nil {
		t.Fatalf("Enqueue pkt1 failed: %v", err)
	}
	if err := pacer.Enqueue(pkt2); err != nil {
		t.Fatalf("Enqueue pkt2 failed: %v", err)
	}
	if err := pacer.EnqueueSentenceStart("第二句"); err != nil {
		t.Fatalf("EnqueueSentenceStart 2 failed: %v", err)
	}
	if err := pacer.Enqueue(pkt3); err != nil {
		t.Fatalf("Enqueue pkt3 failed: %v", err)
	}

	pacer.FinishInput()

	// 驱动 ticker 逐帧下发
	for i := 0; i < 10; i++ {
		ticker.Tick()
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-pacer.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("pacer did not finish in time")
	}

	mu.Lock()
	completed := completedCalled
	mu.Unlock()
	if !completed {
		t.Fatal("expected OnCompleted callback to be fired")
	}

	var msgs []mockWSMessage
	for i := 0; i < 50; i++ {
		msgs = conn.getMessages()
		if len(msgs) == 6 { // start, sent1, pkt1, pkt2, sent2, pkt3, stop (actually 7 items: start, sent1, pkt1, pkt2, sent2, pkt3, stop)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(msgs) != 7 {
		t.Fatalf("expected exactly 7 messages, got %d", len(msgs))
	}

	// 1. tts.start
	if msgs[0].typ != websocket.MessageText {
		t.Fatalf("msg 0 expected text, got %v", msgs[0].typ)
	}
	var m0 map[string]any
	_ = json.Unmarshal(msgs[0].payload, &m0)
	if m0["type"] != "tts" || m0["state"] != "start" {
		t.Fatalf("msg 0 expected tts.start, got %+v", m0)
	}

	// 2. tts.sentence_start("第一句")
	if msgs[1].typ != websocket.MessageText {
		t.Fatalf("msg 1 expected text, got %v", msgs[1].typ)
	}
	var m1 map[string]any
	_ = json.Unmarshal(msgs[1].payload, &m1)
	if m1["type"] != "tts" || m1["state"] != "sentence_start" || m1["text"] != "第一句" {
		t.Fatalf("msg 1 expected tts.sentence_start '第一句', got %+v", m1)
	}

	// 3. pkt1
	if msgs[2].typ != websocket.MessageBinary || len(msgs[2].payload) != 2 {
		t.Fatalf("msg 2 expected binary pkt1, got %+v", msgs[2])
	}

	// 4. pkt2
	if msgs[3].typ != websocket.MessageBinary || len(msgs[3].payload) != 2 {
		t.Fatalf("msg 3 expected binary pkt2, got %+v", msgs[3])
	}

	// 5. tts.sentence_start("第二句")
	if msgs[4].typ != websocket.MessageText {
		t.Fatalf("msg 4 expected text, got %v", msgs[4].typ)
	}
	var m4 map[string]any
	_ = json.Unmarshal(msgs[4].payload, &m4)
	if m4["type"] != "tts" || m4["state"] != "sentence_start" || m4["text"] != "第二句" {
		t.Fatalf("msg 4 expected tts.sentence_start '第二句', got %+v", m4)
	}

	// 6. pkt3
	if msgs[5].typ != websocket.MessageBinary || len(msgs[5].payload) != 2 {
		t.Fatalf("msg 5 expected binary pkt3, got %+v", msgs[5])
	}

	// 7. tts.stop
	if msgs[6].typ != websocket.MessageText {
		t.Fatalf("msg 6 expected text, got %v", msgs[6].typ)
	}
	var m6 map[string]any
	_ = json.Unmarshal(msgs[6].payload, &m6)
	if m6["type"] != "tts" || m6["state"] != "stop" {
		t.Fatalf("msg 6 expected tts.stop, got %+v", m6)
	}
}

type errSender struct {
	err error
}

func (e *errSender) SendText(ctx context.Context, payload []byte) error {
	return e.err
}

func (e *errSender) SendBinary(ctx context.Context, payload []byte) error {
	return e.err
}

func TestDownlinkPacer_ErrorCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("network write failed")
	var errReported error
	var mu sync.Mutex

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-err-test",
		Sender:    &errSender{err: expectedErr},
		QueueCap:  10,
		Callbacks: PacerCallbacks{
			OnError: func(err error) {
				mu.Lock()
				errReported = err
				mu.Unlock()
			},
		},
	})
	go pacer.Run()

	_ = pacer.Enqueue([]byte{0x01, 0x02})

	select {
	case <-pacer.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pacer did not exit on error")
	}

	mu.Lock()
	gotErr := errReported
	mu.Unlock()

	if !errors.Is(gotErr, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, gotErr)
	}
}
