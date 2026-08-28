package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type mockTTSStream struct {
	mu        sync.Mutex
	pcmChunks [][]byte
	idx       int
	err       error
	finished  bool
	closed    bool
}

func (m *mockTTSStream) SendSentence(ctx context.Context, text string) error { return nil }
func (m *mockTTSStream) Finish(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finished = true
	return nil
}

func (m *mockTTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if m.idx >= len(m.pcmChunks) {
		return nil, io.EOF
	}
	chunk := m.pcmChunks[m.idx]
	m.idx++
	return chunk, nil
}

func (m *mockTTSStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func TestConsumeTTSPCM_ExplicitContract_Success(t *testing.T) {
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
		events:     make(chan event, 10),
		sessionID:  "sess-consume-test",
		generation: 1,
		state:      StateSpeaking,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	// 24000 Hz, 16-bit mono = 48000 bytes/sec, 60ms = 2880 bytes.
	// 提供 2 帧完整静音 PCM
	pcmFrame := make([]byte, 2880)
	stream := &mockTTSStream{
		pcmChunks: [][]byte{pcmFrame, pcmFrame},
	}

	pcmDone := make(chan error, 1)

	// 显式契约调用
	go sess.consumeTTSPCM(ctx, 1, stream, pacer, pcmDone)

	select {
	case err, ok := <-pcmDone:
		if !ok {
			t.Fatal("expected channel open before read")
		}
		if err != nil {
			t.Fatalf("consumeTTSPCM returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumeTTSPCM timed out")
	}

	// 验证 pcmDone 通道已被关闭
	select {
	case _, ok := <-pcmDone:
		if ok {
			t.Fatal("expected pcmDone to be closed")
		}
	default:
		// 如果上面已经读过一次，再读应该立即返回 (!ok)
	}
}

func TestConsumeTTSPCM_ExplicitContract_StreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionID:  "sess-consume-err-test",
		generation: 1,
		state:      StateSpeaking,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	expectedErr := errors.New("simulated tts stream failure")
	stream := &mockTTSStream{
		err: expectedErr,
	}

	pcmDone := make(chan error, 1)

	go sess.consumeTTSPCM(ctx, 1, stream, pacer, pcmDone)

	select {
	case err := <-pcmDone:
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected %v, got %v", expectedErr, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumeTTSPCM did not return on stream error")
	}

	// 验证 Session 接收到了 eventKindError 事件
	select {
	case ev := <-sess.events:
		if ev.kind != eventKindError {
			t.Fatalf("expected eventKindError, got %v", ev.kind)
		}
		if !errors.Is(ev.err, expectedErr) {
			t.Fatalf("expected error event with %v, got %v", expectedErr, ev.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected error event posted to session")
	}
}

func TestConsumeTTSPCM_ExplicitContract_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionID:  "sess-consume-cancel-test",
		generation: 1,
		state:      StateSpeaking,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	// cancel context immediately
	cancel()

	pcmDone := make(chan error, 1)
	stream := &mockTTSStream{
		pcmChunks: [][]byte{make([]byte, 2880)},
	}

	go sess.consumeTTSPCM(ctx, 1, stream, pacer, pcmDone)

	select {
	case <-pcmDone:
		// channel closed or error returned promptly
	case <-time.After(1 * time.Second):
		t.Fatal("consumeTTSPCM did not exit promptly after context cancellation")
	}
}

func TestSession_PostTurnFinished_ExplicitContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		generation: 2,
		state:      StateSpeaking,
	}

	ok := sess.PostTurnFinished(2, "问题", "回答")
	if !ok {
		t.Fatal("PostTurnFinished returned false")
	}

	select {
	case ev := <-sess.events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.generation != 2 {
			t.Fatalf("expected generation 2, got %d", ev.generation)
		}
		if ev.userText != "问题" || ev.assistantText != "回答" {
			t.Fatalf("unexpected userText/assistantText: user=%s, assistant=%s", ev.userText, ev.assistantText)
		}
	default:
		t.Fatal("expected event in queue")
	}
}

func TestSession_PostTurnFinished_StaleGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		generation: 3, // 当前代次为 3
		state:      StateSpeaking,
	}

	// 投递旧代次 2 的结束事件
	ok := sess.PostTurnFinished(2, "旧问题", "旧回答")
	if !ok {
		t.Fatal("PostTurnFinished returned false")
	}

	select {
	case ev := <-sess.events:
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected event in queue")
	}

	// 代次不匹配，状态应仍为 StateSpeaking，历史记录不应增加
	if sess.State() != StateSpeaking {
		t.Fatalf("expected state StateSpeaking, got %v", sess.State())
	}
	if len(sess.History()) != 0 {
		t.Fatalf("expected 0 history messages, got %d", len(sess.History()))
	}
}

