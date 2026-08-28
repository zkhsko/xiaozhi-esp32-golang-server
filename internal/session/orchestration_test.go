package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

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

type mockLLMStream struct {
	chunks    []string
	chunkIdx  int
	toolCalls []ai.ToolCall
	err       error
	closed    bool
}

func (m *mockLLMStream) Recv() (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if m.chunkIdx >= len(m.chunks) {
		return "", io.EOF
	}
	chunk := m.chunks[m.chunkIdx]
	m.chunkIdx++
	return chunk, nil
}

func (m *mockLLMStream) ToolCalls() []ai.ToolCall {
	return m.toolCalls
}

func (m *mockLLMStream) Close() error {
	m.closed = true
	return nil
}

type mockLLMClient struct {
	mu               sync.Mutex
	createStream     func(ctx context.Context, callCount int, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error)
	callCount        int
	receivedTools    [][]ai.Tool
	receivedMessages [][]ai.Message
}

func (m *mockLLMClient) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	m.mu.Lock()
	m.callCount++
	currentCount := m.callCount
	m.receivedTools = append(m.receivedTools, tools)
	m.receivedMessages = append(m.receivedMessages, messages)
	fn := m.createStream
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, currentCount, messages, tools)
	}
	return &mockLLMStream{chunks: []string{"默认回复"}}, nil
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

func TestOrchestrateLLMAndTTS_MultiTurnTools_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	mockLLM.createStream = func(ctx context.Context, callCount int, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
		if callCount == 1 {
			// 第一轮：LLM 返回工具调用 server.get_current_time
			return &mockLLMStream{
				chunks: []string{"正在为您查询当前时间。"},
				toolCalls: []ai.ToolCall{
					{
						ID:   "call_1",
						Name: ServerToolGetCurrentTime,
					},
				},
			}, nil
		}

		// 第二轮：接收工具执行结果，返回最终文本回复
		return &mockLLMStream{
			chunks: []string{"当前时间是上午十点。"},
		}, nil
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionID:  "sess-multi-tool-test",
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

	mockLLM.mu.Lock()
	defer mockLLM.mu.Unlock()
	if mockLLM.callCount != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", mockLLM.callCount)
	}
	if len(mockLLM.receivedTools[0]) == 0 {
		t.Fatal("expected non-empty tools on first LLM call")
	}
	if len(mockLLM.receivedTools[1]) == 0 {
		t.Fatal("expected non-empty tools on second LLM call (under limit)")
	}
}

func TestOrchestrateLLMAndTTS_MaxToolIterations_BoundedTermination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	mockLLM.createStream = func(ctx context.Context, callCount int, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
		// 前 maxToolIterations 次调用返回工具调用
		if callCount <= maxToolIterations {
			return &mockLLMStream{
				chunks: []string{"正在执行工具调用。"},
				toolCalls: []ai.ToolCall{
					{
						ID:   "call_id",
						Name: ServerToolGetCurrentTime,
					},
				},
			}, nil
		}

		// 第 maxToolIterations + 1 次调用（此时 tools 应已被置为 nil），返回最终回复
		return &mockLLMStream{
			chunks: []string{"已达到单轮工具调用上限，这是最终回复。"},
		}, nil
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionID:  "sess-max-iterations-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "重复调用工具")

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.assistantText != "已达到单轮工具调用上限，这是最终回复。" {
			t.Fatalf("unexpected assistantText: %q", ev.assistantText)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out (possible infinite loop)")
	}

	mockLLM.mu.Lock()
	defer mockLLM.mu.Unlock()

	expectedCalls := maxToolIterations + 1
	if mockLLM.callCount != expectedCalls {
		t.Fatalf("expected exactly %d LLM calls, got %d", expectedCalls, mockLLM.callCount)
	}

	// 验证前 maxToolIterations 次调用传入了 tools
	for i := 0; i < maxToolIterations; i++ {
		if len(mockLLM.receivedTools[i]) == 0 {
			t.Fatalf("expected non-empty tools on iteration %d", i)
		}
	}

	// 验证达到上限后（第 maxToolIterations + 1 次）未传入 tools (nil)
	if len(mockLLM.receivedTools[maxToolIterations]) != 0 {
		t.Fatalf("expected empty/nil tools on iteration %d, got %v", maxToolIterations, mockLLM.receivedTools[maxToolIterations])
	}
}

func TestOrchestrateLLMAndTTS_MaxToolIterations_ForceBreakIfModelStillReturnsToolCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	// 模拟异常模型：即使没给 tools 也无条件返回 toolCalls
	mockLLM.createStream = func(ctx context.Context, callCount int, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
		return &mockLLMStream{
			chunks: []string{"兜底回复文本。"},
			toolCalls: []ai.ToolCall{
				{
					ID:   "rogue_call",
					Name: ServerToolGetCurrentTime,
				},
			},
		}, nil
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionID:  "sess-force-break-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "异常模型死循环测试")

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.assistantText != "兜底回复文本。" {
			t.Fatalf("expected '兜底回复文本。', got %q", ev.assistantText)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out (infinite loop not broken)")
	}

	mockLLM.mu.Lock()
	defer mockLLM.mu.Unlock()

	expectedCalls := maxToolIterations + 1
	if mockLLM.callCount != expectedCalls {
		t.Fatalf("expected bounded %d LLM calls, got %d", expectedCalls, mockLLM.callCount)
	}
}

func TestOrchestrateLLMAndTTS_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	mockLLM.createStream = func(ctx context.Context, callCount int, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
		// 在第一次创建流时立即取消 context
		cancel()
		return nil, context.Canceled
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionID:  "sess-cancel-test",
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
		// 正常退出
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrateLLMAndTTS did not exit promptly upon context cancellation")
	}

	// 确认没有发出错误的 TurnFinished 事件
	select {
	case ev := <-events:
		if ev.kind == eventKindTurnFinished {
			t.Fatalf("unexpected turn finished event after cancellation: %v", ev)
		}
	default:
	}
}

