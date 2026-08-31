package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockTTSStream struct {
	mu        sync.Mutex
	pcmChunks [][]byte
	idx       int
	err       error
	finished  bool
	closed    bool
	sentences []string
}

func (m *mockTTSStream) SendSentence(ctx context.Context, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentences = append(m.sentences, text)
	return nil
}

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

type mockTTSClient struct {
	mu        sync.Mutex
	err       error
	streams   []*mockTTSStream
	newStream func() *mockTTSStream
}

func (m *mockTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	var stream *mockTTSStream
	if m.newStream != nil {
		stream = m.newStream()
	} else {
		stream = &mockTTSStream{
			pcmChunks: [][]byte{make([]byte, 2880)},
		}
	}
	m.streams = append(m.streams, stream)
	return stream, nil
}

type mockLLMClient struct {
	mu          sync.Mutex
	callCount   int
	generate    func(ctx context.Context, request ai.LLMRequest, callback ai.LLMStreamCallback) (string, error)
	reqReceived []ai.LLMRequest
}

func (m *mockLLMClient) Generate(ctx context.Context, request ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
	m.mu.Lock()
	m.callCount++
	m.reqReceived = append(m.reqReceived, request)
	fn := m.generate
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, request, callback)
	}
	if callback != nil {
		_ = callback(ctx, ai.LLMChunk{Text: "默认回复", Iteration: 0})
	}
	return "默认回复", nil
}

func TestConsumeSentencesTTS_ExplicitContract_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	// 24000 Hz, 16-bit mono = 48000 bytes/sec, 60ms = 2880 bytes.
	pcmFrame := make([]byte, 2880)
	stream := &mockTTSStream{
		pcmChunks: [][]byte{pcmFrame, pcmFrame},
	}
	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return stream
		},
	}

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		writer:     writer,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-consume-test",
		generation: 1,
		state:      StateSpeaking,
		ttsClient:  mockTTS,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	sentenceCh := make(chan string, 5)
	sentenceCh <- "你好测试"
	close(sentenceCh)

	pcmDone := make(chan error, 1)

	go sess.consumeSentencesTTS(ctx, 1, sentenceCh, pacer, pcmDone)

	select {
	case err, ok := <-pcmDone:
		if !ok {
			t.Fatal("expected channel open before read")
		}
		if err != nil {
			t.Fatalf("consumeSentencesTTS returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumeSentencesTTS timed out")
	}

	select {
	case _, ok := <-pcmDone:
		if ok {
			t.Fatal("expected pcmDone to be closed")
		}
	default:
	}
}

func TestConsumeSentencesTTS_ExplicitContract_StreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("simulated tts stream failure")
	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return &mockTTSStream{err: expectedErr}
		},
	}

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-consume-err-test",
		generation: 1,
		state:      StateSpeaking,
		ttsClient:  mockTTS,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	sentenceCh := make(chan string, 5)
	sentenceCh <- "测试错误句子"
	close(sentenceCh)

	pcmDone := make(chan error, 1)

	go sess.consumeSentencesTTS(ctx, 1, sentenceCh, pacer, pcmDone)

	select {
	case err := <-pcmDone:
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected %v, got %v", expectedErr, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumeSentencesTTS did not return on stream error")
	}

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

func TestConsumeSentencesTTS_ExplicitContract_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return &mockTTSStream{pcmChunks: [][]byte{make([]byte, 2880)}}
		},
	}

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-consume-cancel-test",
		generation: 1,
		state:      StateSpeaking,
		ttsClient:  mockTTS,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	cancel()

	sentenceCh := make(chan string, 5)
	sentenceCh <- "测试取消句子"
	close(sentenceCh)

	pcmDone := make(chan error, 1)

	go sess.consumeSentencesTTS(ctx, 1, sentenceCh, pacer, pcmDone)

	select {
	case <-pcmDone:
	case <-time.After(1 * time.Second):
		t.Fatal("consumeSentencesTTS did not exit promptly after context cancellation")
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
		generation: 3,
		state:      StateSpeaking,
	}

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

	if sess.State() != StateSpeaking {
		t.Fatalf("expected state StateSpeaking, got %v", sess.State())
	}
	if len(sess.History()) != 0 {
		t.Fatalf("expected 0 history messages, got %d", len(sess.History()))
	}
}

func TestOrchestrateLLMAndTTS_MultiTurnTools_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	var toolExecuted bool
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		// 验证传入了可执行工具
		for _, tool := range req.Tools {
			if tool.Name == agentkit.ToolGetCurrentTime {
				res, err := tool.Run(ctx, map[string]any{})
				if err != nil {
					return "", err
				}
				if res != nil {
					toolExecuted = true
				}
			}
		}

		// 模拟流式生成分句
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "正在为您查询当前时间。", Iteration: 0})
			_ = callback(ctx, ai.LLMChunk{Text: "当前时间是上午十点。", Iteration: 1})
		}
		return "当前时间是上午十点。", nil
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionId:  "sess-multi-tool-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "现在几点了")

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.userText != "现在几点了" {
			t.Fatalf("expected userText '现在几点了', got %q", ev.userText)
		}
		if ev.assistantText != "当前时间是上午十点。" {
			t.Fatalf("expected assistantText '当前时间是上午十点。', got %q", ev.assistantText)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out")
	}

	if !toolExecuted {
		t.Fatal("expected server tool to be executed during generate")
	}
}

func TestOrchestrateLLMAndTTS_MaxTurnsExceeded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		return "", ai.ErrMaxTurnsExceeded
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionId:  "sess-max-turns-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "测试超出最大轮次")

	select {
	case ev := <-events:
		if ev.kind != eventKindError {
			t.Fatalf("expected eventKindError, got %v", ev.kind)
		}
		if !errors.Is(ev.err, ai.ErrMaxTurnsExceeded) {
			t.Fatalf("expected ErrMaxTurnsExceeded, got: %v", ev.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out")
	}
}

func TestOrchestrateLLMAndTTS_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		cancel()
		return "", context.Canceled
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionId:  "sess-cancel-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	done := make(chan struct{})
	go func() {
		sess.orchestrateLLMAndTTS(ctx, 1, "测试取消")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrateLLMAndTTS did not exit promptly upon context cancellation")
	}

	select {
	case ev := <-events:
		if ev.kind == eventKindTurnFinished {
			t.Fatalf("unexpected turn finished event after cancellation: %v", ev)
		}
	default:
	}
}

func TestOrchestrateLLMAndTTS_SentenceSubtitleSync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	ticker := newManualTicker()

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return &mockTTSStream{
				pcmChunks: [][]byte{make([]byte, 2880*3)},
			}
		},
	}

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "你好世界。", Iteration: 0})
				_ = callback(ctx, ai.LLMChunk{Text: "今天天气真好。", Iteration: 0})
			}
			return "你好世界。今天天气真好。", nil
		},
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:           ctx,
		cancel:        cancel,
		writer:        writer,
		logger:        slog.Default(),
		events:        events,
		sessionId:     "sess-subtitle-sync",
		generation:    1,
		state:         StateSpeaking,
		llmClient:     mockLLM,
		ttsClient:     mockTTS,
		tickerFactory: func(d time.Duration) Ticker { return ticker },
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "测试音字同步")

	// 驱动 ticker 推进所有音频包下发
	go func() {
		for i := 0; i < 20; i++ {
			ticker.Tick()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(3 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out")
	}

	msgs := conn.getMessages()
	if len(msgs) == 0 {
		t.Fatal("expected messages sent to websocket")
	}

	var sentenceStarts []string
	for _, m := range msgs {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "tts" && parsed["state"] == "sentence_start" {
					if txt, ok := parsed["text"].(string); ok {
						sentenceStarts = append(sentenceStarts, txt)
					}
				}
			}
		}
	}

	if len(sentenceStarts) != 2 {
		t.Fatalf("expected 2 sentence_start messages, got %d: %v", len(sentenceStarts), sentenceStarts)
	}

	if sentenceStarts[0] != "你好世界。" {
		t.Fatalf("expected first sentence '你好世界。', got %q", sentenceStarts[0])
	}
	if sentenceStarts[1] != "今天天气真好。" {
		t.Fatalf("expected second sentence '今天天气真好。', got %q", sentenceStarts[1])
	}
}
