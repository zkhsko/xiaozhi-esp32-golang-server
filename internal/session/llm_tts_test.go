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

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
)

// traceEvent 记录编排流程中关键动作的时序事件。
type traceEvent struct {
	Kind string // "sentence_start", "tts_send", "tts_finish"
	Text string
}

// traceRecorder 线程安全地记录编排事件发生顺序。
type traceRecorder struct {
	mu     sync.Mutex
	events []traceEvent
}

func (r *traceRecorder) record(kind, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, traceEvent{Kind: kind, Text: text})
}

func (r *traceRecorder) getEvents() []traceEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]traceEvent, len(r.events))
	copy(copied, r.events)
	return copied
}

// tracingWSConn 实现带事件记录的 fake WebSocket 连接。
type tracingWSConn struct {
	mu       sync.Mutex
	recorder *traceRecorder
	messages []fakeWSMessage
}

func newTracingWSConn(recorder *traceRecorder) *tracingWSConn {
	return &tracingWSConn{
		recorder: recorder,
	}
}

func (f *tracingWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	copied := make([]byte, len(p))
	copy(copied, p)
	f.messages = append(f.messages, fakeWSMessage{
		typ:     typ,
		payload: copied,
	})

	if typ == websocket.MessageText && f.recorder != nil {
		var msg ServerTTSSentenceStartMessage
		if err := json.Unmarshal(p, &msg); err == nil && msg.Type == MessageTypeTTS && msg.State == TTSStateSentenceStart {
			f.recorder.record("sentence_start", msg.Text)
		}
	}
	return nil
}

func (f *tracingWSConn) Messages() []fakeWSMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]fakeWSMessage, len(f.messages))
	copy(result, f.messages)
	return result
}

func (f *tracingWSConn) TextMessages() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res [][]byte
	for _, m := range f.messages {
		if m.typ == websocket.MessageText {
			res = append(res, m.payload)
		}
	}
	return res
}

// mockLLMStream 实现测试用 LLMStream。
type mockLLMStream struct {
	mu     sync.Mutex
	chunks []string
	index  int
	err    error
	closed bool
}

func newMockLLMStream(chunks []string, err error) *mockLLMStream {
	return &mockLLMStream{
		chunks: chunks,
		err:    err,
	}
}

func (s *mockLLMStream) Recv() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", errors.New("llm stream is closed")
	}
	if s.index < len(s.chunks) {
		chunk := s.chunks[s.index]
		s.index++
		return chunk, nil
	}
	if s.err != nil {
		return "", s.err
	}
	return "", io.EOF
}

func (s *mockLLMStream) ToolCalls() []ai.ToolCall {
	return nil
}

func (s *mockLLMStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// mockLLMClient 实现测试用 LLMClient。
type mockLLMClient struct {
	mu             sync.Mutex
	createCalls    int
	createErr      error
	streamToReturn *mockLLMStream
	lastMessages   []ai.Message
}

func newMockLLMClient(stream *mockLLMStream, createErr error) *mockLLMClient {
	return &mockLLMClient{
		streamToReturn: stream,
		createErr:      createErr,
	}
}

func (c *mockLLMClient) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.createCalls++
	c.lastMessages = make([]ai.Message, len(messages))
	copy(c.lastMessages, messages)

	if c.createErr != nil {
		return nil, c.createErr
	}
	return c.streamToReturn, nil
}

func (c *mockLLMClient) CreateCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.createCalls
}

func (c *mockLLMClient) LastMessages() []ai.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make([]ai.Message, len(c.lastMessages))
	copy(copied, c.lastMessages)
	return copied
}

// mockTTSStream 实现测试用 TTSStream。
type mockTTSStream struct {
	mu              sync.Mutex
	recorder        *traceRecorder
	writer          *Writer
	sentSentences   []string
	finishCalls     int
	closeCalls      int
	sendErrOnIndex  int // 在第几个句子时报错（1-based，0 表示不报错）
	finishErr       error
	nextPCMErr      error
	closed          bool
	pcmDataToReturn [][]byte
	pcmIndex        int
}

func newMockTTSStream(recorder *traceRecorder) *mockTTSStream {
	return &mockTTSStream{
		recorder:       recorder,
		sendErrOnIndex: 0,
	}
}

func (s *mockTTSStream) SendSentence(ctx context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("tts stream is closed")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	s.sentSentences = append(s.sentSentences, text)
	if s.recorder != nil {
		s.recorder.record("tts_send", text)
	}

	if s.sendErrOnIndex > 0 && len(s.sentSentences) == s.sendErrOnIndex {
		return errors.New("mock tts send error")
	}
	return nil
}

func (s *mockTTSStream) Finish(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("tts stream is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.finishCalls++
	if s.recorder != nil {
		s.recorder.record("tts_finish", "")
	}

	return s.finishErr
}

func (s *mockTTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("tts stream is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.nextPCMErr != nil {
		return nil, s.nextPCMErr
	}
	if s.pcmIndex >= len(s.pcmDataToReturn) {
		return nil, io.EOF
	}
	chunk := s.pcmDataToReturn[s.pcmIndex]
	s.pcmIndex++
	return chunk, nil
}

func (s *mockTTSStream) FinishCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishCalls
}

func (s *mockTTSStream) SentSentences() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]string, len(s.sentSentences))
	copy(copied, s.sentSentences)
	return copied
}

func (s *mockTTSStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.closeCalls++
	return nil
}

// mockTTSClient 实现测试用 TTSClient。
type mockTTSClient struct {
	mu             sync.Mutex
	createCalls    int
	createErr      error
	streamToReturn *mockTTSStream
}

func newMockTTSClient(stream *mockTTSStream, createErr error) *mockTTSClient {
	return &mockTTSClient{
		streamToReturn: stream,
		createErr:      createErr,
	}
}

func (c *mockTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.createCalls++
	if c.createErr != nil {
		return nil, c.createErr
	}
	return c.streamToReturn, nil
}

func (c *mockTTSClient) CreateCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.createCalls
}

// createTestSessionForOrchestration 创建带有录制能力的测试 Session。
func createTestSessionForOrchestration(ctx context.Context, llmClient ai.LLMClient, ttsClient ai.TTSClient, recorder *traceRecorder) (*Session, *tracingWSConn, *Writer) {
	conn := newTracingWSConn(recorder)
	writer := NewWriter(ctx, conn, 100, slog.Default())
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:              5 * time.Second,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      30 * time.Second,
			MaxHistoryTurns:           6,
			SystemPrompt:              "你是小智，一个智能语音助手。",
			ASRPCMQueueCapacity:       50,
			TTSPCMQueueCapacity:       50,
			DownlinkOpusQueueCapacity: 50,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "test-device",
		ClientID:     "test-client",
		SerialNumber: "test-sn",
	}

	sess := NewSessionWithWriter(ctx, nil, writer, info, cfg, nil, llmClient, ttsClient, slog.Default())

	// 设置 sessionID 以便编码消息
	sess.mu.Lock()
	sess.sessionID = "test-session-id"
	sess.state = StateProcessing
	sess.mu.Unlock()

	return sess, conn, writer
}

// TestOrchestration_SentenceOrderAndTTSFlow 验证正常问答流程下：
// 1. 单个回答只创建一条回答级 TTSStream；
// 2. 消费 LLM 流文本增量并通过 SentenceSplitter 进行分句；
// 3. 对切分出的每一个完整句子：先下发 tts.sentence_start，再调用 ttsStream.SendSentence，严格保证文本先于音频；
// 4. LLM 正常结束时调用 Flush 处理残句，并调用且只调用一次 ttsStream.Finish；
// 5. 句子顺序与 LLM 产出完全一致。
func TestOrchestration_SentenceOrderAndTTSFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &traceRecorder{}

	// LLM 增量包含跨 chunk 句子、标点切分以及末尾无标点残句
	llmChunks := []string{
		"你好呀", "！今天", "天气真好，",
		"我们一起", "去公园散步吧。",
		"祝你今天开心",
	}
	expectedSentences := []string{
		"你好呀！",
		"今天天气真好，",
		"我们一起去公园散步吧。",
		"祝你今天开心",
	}

	mockStream := newMockLLMStream(llmChunks, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := newMockTTSStream(recorder)
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, conn, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, recorder)

	// 运行编排
	gen := uint64(1)
	sess.orchestrateLLMAndTTS(ctx, gen, "今天天气怎么样？")

	// 1. 断言 LLMClient 与 TTSClient 的创建调用次数恰好为 1
	if llmClient.CreateCalls() != 1 {
		t.Fatalf("expected LLMClient.CreateStream called 1 time, got %d", llmClient.CreateCalls())
	}
	if ttsClient.CreateCalls() != 1 {
		t.Fatalf("expected TTSClient.CreateStream called 1 time, got %d", ttsClient.CreateCalls())
	}

	// 2. 断言 TTSStream.SendSentence 收到的句子完全一致
	sentSentences := mockTTS.SentSentences()
	if len(sentSentences) != len(expectedSentences) {
		t.Fatalf("expected %d sentences sent to TTS, got %d: %v", len(expectedSentences), len(sentSentences), sentSentences)
	}
	for i, expected := range expectedSentences {
		if sentSentences[i] != expected {
			t.Errorf("sentence [%d] mismatch: expected %q, got %q", i, expected, sentSentences[i])
		}
	}

	// 4. 等待 Writer 队列排空并验证下发的 sentence_start 消息顺序
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(conn.TextMessages()) >= len(expectedSentences) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	textMsgs := conn.TextMessages()
	var sentenceStartTexts []string
	for _, raw := range textMsgs {
		var msg ServerTTSSentenceStartMessage
		if err := json.Unmarshal(raw, &msg); err == nil && msg.Type == MessageTypeTTS && msg.State == TTSStateSentenceStart {
			sentenceStartTexts = append(sentenceStartTexts, msg.Text)
			if msg.SessionID != "test-session-id" {
				t.Errorf("expected session_id 'test-session-id', got %q", msg.SessionID)
			}
		}
	}

	if len(sentenceStartTexts) != len(expectedSentences) {
		t.Fatalf("expected %d sentence_start messages, got %d: %v", len(expectedSentences), len(sentenceStartTexts), sentenceStartTexts)
	}
	for i, expected := range expectedSentences {
		if sentenceStartTexts[i] != expected {
			t.Errorf("sentence_start [%d] mismatch: expected %q, got %q", i, expected, sentenceStartTexts[i])
		}
	}

	// 5. 验证 TTS Finish 恰好调用 1 次
	if mockTTS.FinishCalls() != 1 {
		t.Fatalf("expected TTSStream.Finish called 1 time, got %d", mockTTS.FinishCalls())
	}

	// 6. 验证 LLM 流已关闭
	if !mockStream.closed {
		t.Errorf("expected LLMStream to be closed after completion")
	}
}

// TestOrchestration_LongSentenceAutoSplit 验证当 LLM 输出长文本且无标点时，分句器按最大字符限制自动切分并投递。
func TestOrchestration_LongSentenceAutoSplit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &traceRecorder{}

	// 构造 65 字符的无标点连续文本
	longText := "这是一段非常非常非常非常非常长的没有标点的文本用来测试流式分句器在长时间没有标点符号时自动按照最大字符数强制切分保护首字延迟"
	mockStream := newMockLLMStream([]string{longText}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := newMockTTSStream(recorder)
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, _, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, recorder)

	sess.orchestrateLLMAndTTS(ctx, 1, "测试长文本")

	// 65 字符应切分为 30 + 30 + 5
	runes := []rune(longText)
	expectedSentences := []string{
		string(runes[:30]),
		string(runes[30:60]),
		string(runes[60:]),
	}

	sentSentences := mockTTS.SentSentences()
	if len(sentSentences) != len(expectedSentences) {
		t.Fatalf("expected %d split sentences, got %d: %v", len(expectedSentences), len(sentSentences), sentSentences)
	}
	for i, expected := range expectedSentences {
		if sentSentences[i] != expected {
			t.Errorf("sentence [%d] mismatch: expected %q, got %q", i, expected, sentSentences[i])
		}
	}

	if mockTTS.FinishCalls() != 1 {
		t.Fatalf("expected TTSStream.Finish called 1 time, got %d", mockTTS.FinishCalls())
	}
}

// TestOrchestration_LLMErrorAbortsTTS 验证 LLM 在流式输出过程中报错时：
// 1. 立即停止编排流程；
// 2. 绝不再向 TTSStream 投递新句子；
// 3. 绝不调用 TTSStream.Finish；
// 4. 会话接收到错误事件。
func TestOrchestration_LLMErrorAbortsTTS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &traceRecorder{}

	// LLM 在返回第 1 句后抛出网络错误
	llmErr := errors.New("upstream llm connection reset")
	mockStream := newMockLLMStream([]string{"第一句话完成。"}, llmErr)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := newMockTTSStream(recorder)
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, _, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, recorder)

	sess.orchestrateLLMAndTTS(ctx, 1, "测试 LLM 错误")

	// 验证第 1 句成功送入
	sentSentences := mockTTS.SentSentences()
	if len(sentSentences) != 1 || sentSentences[0] != "第一句话完成。" {
		t.Fatalf("expected 1 sentence sent before error, got: %v", sentSentences)
	}

	// 核心断言：LLM 错误后绝不调用 Finish
	if mockTTS.FinishCalls() != 0 {
		t.Fatalf("expected TTSStream.Finish NOT called on LLM error, but called %d times", mockTTS.FinishCalls())
	}

	// 验证会话收到错误事件
	select {
	case ev := <-sess.events:
		if ev.kind != eventKindError {
			t.Errorf("expected eventKindError, got %v", ev.kind)
		}
		if !errors.Is(ev.err, llmErr) {
			t.Errorf("expected error %v, got %v", llmErr, ev.err)
		}
	default:
		t.Fatal("expected error event posted to session, but event channel was empty")
	}
}

// TestOrchestration_TTSSendSentenceErrorAbortsPipeline 验证当 TTSStream.SendSentence 报错时：
// 1. 编排流程立即退出；
// 2. 绝不继续向 TTS 投递后续句子；
// 3. 绝不调用 TTSStream.Finish；
// 4. 会话接收到错误事件。
func TestOrchestration_TTSSendSentenceErrorAbortsPipeline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &traceRecorder{}

	// LLM 返回多句
	chunks := []string{"第一句。", "第二句。", "第三句。"}
	mockStream := newMockLLMStream(chunks, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := newMockTTSStream(recorder)
	// 在第 2 句时模拟 TTS 写入报错
	mockTTS.sendErrOnIndex = 2
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, _, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, recorder)

	sess.orchestrateLLMAndTTS(ctx, 1, "测试 TTS 错误")

	// 验证在第 2 句报错后，第 3 句不再送入
	sentSentences := mockTTS.SentSentences()
	if len(sentSentences) != 2 {
		t.Fatalf("expected exactly 2 sentences attempted before abort, got %d: %v", len(sentSentences), sentSentences)
	}

	// 核心断言：TTS 写入报错后绝不调用 Finish
	if mockTTS.FinishCalls() != 0 {
		t.Fatalf("expected TTSStream.Finish NOT called after SendSentence error, got %d", mockTTS.FinishCalls())
	}

	// 验证会话收到错误事件
	select {
	case ev := <-sess.events:
		if ev.kind != eventKindError {
			t.Errorf("expected eventKindError, got %v", ev.kind)
		}
	default:
		t.Fatal("expected error event posted to session, but event channel was empty")
	}
}

// TestOrchestration_TTSCreateStreamError 验证 TTSStream 创建失败时流程立即退出并不调用 LLM。
func TestOrchestration_TTSCreateStreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	createErr := errors.New("tts quota exceeded")
	ttsClient := newMockTTSClient(nil, createErr)

	mockStream := newMockLLMStream([]string{"测试文本。"}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	sess, _, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, nil)

	sess.orchestrateLLMAndTTS(ctx, 1, "测试 TTS 创建错误")

	// 断言 LLM 未被调用
	if llmClient.CreateCalls() != 0 {
		t.Fatalf("expected LLMClient.CreateStream NOT called when TTS create fails, got %d", llmClient.CreateCalls())
	}

	// 验证错误事件投递
	select {
	case ev := <-sess.events:
		if ev.kind != eventKindError || !errors.Is(ev.err, createErr) {
			t.Errorf("expected error event with %v, got %v", createErr, ev.err)
		}
	default:
		t.Fatal("expected error event in session events")
	}
}

// TestOrchestration_LLMCreateStreamError 验证 LLMStream 创建失败时流程立即退出并不向 TTS 投递句子。
func TestOrchestration_LLMCreateStreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmCreateErr := errors.New("llm auth failure")
	llmClient := newMockLLMClient(nil, llmCreateErr)

	mockTTS := newMockTTSStream(nil)
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, _, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, nil)

	sess.orchestrateLLMAndTTS(ctx, 1, "测试 LLM 创建错误")

	if len(mockTTS.SentSentences()) != 0 {
		t.Fatalf("expected 0 sentences sent to TTS, got %d", len(mockTTS.SentSentences()))
	}
	if mockTTS.FinishCalls() != 0 {
		t.Fatalf("expected Finish NOT called, got %d", mockTTS.FinishCalls())
	}

	select {
	case ev := <-sess.events:
		if ev.kind != eventKindError || !errors.Is(ev.err, llmCreateErr) {
			t.Errorf("expected error event with %v, got %v", llmCreateErr, ev.err)
		}
	default:
		t.Fatal("expected error event in session events")
	}
}

// TestOrchestration_ContextCanceledExitsPromptly 验证 context 被外部取消时编排流程快速退出且不发生死锁或向 TTS 投递。
func TestOrchestration_ContextCanceledExitsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockTTS := newMockTTSStream(nil)
	ttsClient := newMockTTSClient(mockTTS, nil)

	// 构造一个在 Recv 时阻塞等待 context 取消的 stream
	blockingStream := &blockingLLMStream{ctx: ctx}
	llmClient := newMockLLMClient((*mockLLMStream)(nil), nil)
	llmClient.streamToReturn = (*mockLLMStream)(nil) // 自定义覆盖

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, ttsClient, nil)
	sess.llmClient = &customLLMClient{stream: blockingStream}

	done := make(chan struct{})
	go func() {
		sess.orchestrateLLMAndTTS(ctx, 1, "测试 Context 取消")
		close(done)
	}()

	// 触发取消
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// 成功快速退出
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for orchestrateLLMAndTTS to exit after context cancellation")
	}

	if mockTTS.FinishCalls() != 0 {
		t.Fatalf("expected Finish NOT called on canceled context, got %d", mockTTS.FinishCalls())
	}
}

type blockingLLMStream struct {
	ctx context.Context
}

func (s *blockingLLMStream) Recv() (string, error) {
	<-s.ctx.Done()
	return "", s.ctx.Err()
}

func (s *blockingLLMStream) ToolCalls() []ai.ToolCall {
	return nil
}

func (s *blockingLLMStream) Close() error {
	return nil
}

type customLLMClient struct {
	stream ai.LLMStream
}

func (c *customLLMClient) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	return c.stream, nil
}

// TestOrchestration_HistoryContextManagement 验证多轮对话历史上下文构造与 FIFO 淘汰机制。
func TestOrchestration_HistoryContextManagement(t *testing.T) {
	ctx := context.Background()

	mockStream := newMockLLMStream([]string{"回答。"}, nil)
	llmClient := newMockLLMClient(mockStream, nil)
	mockTTS := newMockTTSStream(nil)
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, _, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, nil)
	sess.cfg.Session.MaxHistoryTurns = 2 // 最多保留 2 轮历史

	// 轮次 1
	sess.AppendHistory("用户问 1", "助手答 1")
	// 轮次 2
	sess.AppendHistory("用户问 2", "助手答 2")

	// 验证当前历史
	history := sess.History()
	if len(history) != 4 {
		t.Fatalf("expected 4 history messages, got %d", len(history))
	}

	// 触发第 3 轮编排，验证构建的 messages 列表
	sess.orchestrateLLMAndTTS(ctx, 1, "用户问 3")

	msgs := llmClient.LastMessages()
	// 预期包含：1 条 System Prompt + 4 条历史（轮次 1 和 2）+ 1 条当前 User 消息 = 6 条
	if len(msgs) != 6 {
		t.Fatalf("expected 6 messages in LLM request, got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Role != ai.RoleSystem || msgs[0].Content != "你是小智，一个智能语音助手。" {
		t.Errorf("system prompt mismatch: %v", msgs[0])
	}
	if msgs[1].Content != "用户问 1" || msgs[2].Content != "助手答 1" {
		t.Errorf("history turn 1 mismatch: %v, %v", msgs[1], msgs[2])
	}
	if msgs[3].Content != "用户问 2" || msgs[4].Content != "助手答 2" {
		t.Errorf("history turn 2 mismatch: %v, %v", msgs[3], msgs[4])
	}
	if msgs[5].Role != ai.RoleUser || msgs[5].Content != "用户问 3" {
		t.Errorf("current user message mismatch: %v", msgs[5])
	}

	// 写入第 3 轮历史后，历史轮数超过 2 轮，应淘汰最旧的一整轮（用户问 1 / 助手答 1）
	sess.AppendHistory("用户问 3", "助手答 3")
	newHistory := sess.History()
	if len(newHistory) != 4 {
		t.Fatalf("expected 4 history messages after FIFO trim, got %d", len(newHistory))
	}
	if newHistory[0].Content != "用户问 2" || newHistory[1].Content != "助手答 2" {
		t.Errorf("expected oldest turn eliminated, got %v, %v", newHistory[0], newHistory[1])
	}
	if newHistory[2].Content != "用户问 3" || newHistory[3].Content != "助手答 3" {
		t.Errorf("expected newest turn preserved, got %v, %v", newHistory[2], newHistory[3])
	}
}

// TestOrchestration_EmptyLLMOutput 验证 LLM 仅输出空字符串或纯空白文本时正常结束 TTS 而不下发空句子。
func TestOrchestration_EmptyLLMOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockLLMStream([]string{"", "   ", "\n\t"}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := newMockTTSStream(nil)
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, conn, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, nil)

	sess.orchestrateLLMAndTTS(ctx, 1, "测试空输出")

	// 不应发送任何句子
	if len(mockTTS.SentSentences()) != 0 {
		t.Fatalf("expected 0 sentences sent to TTS, got %d: %v", len(mockTTS.SentSentences()), mockTTS.SentSentences())
	}

	// 依然调用一次 Finish
	if mockTTS.FinishCalls() != 1 {
		t.Fatalf("expected Finish called 1 time, got %d", mockTTS.FinishCalls())
	}

	// 下发消息中不包含 sentence_start
	for _, raw := range conn.TextMessages() {
		var msg ServerTTSSentenceStartMessage
		if err := json.Unmarshal(raw, &msg); err == nil && msg.State == TTSStateSentenceStart {
			t.Errorf("unexpected sentence_start message for empty LLM output: %s", string(raw))
		}
	}
}

// TestOrchestration_EndToEndIntegration 验证 Session 状态机与 LLM/TTS 编排的完整端到端协同流程。
func TestOrchestration_EndToEndIntegration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := &traceRecorder{}

	mockStream := newMockLLMStream([]string{"北京今天晴朗，", "气温25度。"}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := newMockTTSStream(recorder)
	ttsClient := newMockTTSClient(mockTTS, nil)

	asrClient := newMockSessionASRClient()

	conn := newTracingWSConn(recorder)
	writer := NewWriter(ctx, conn, 100, slog.Default())
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:              5 * time.Second,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      30 * time.Second,
			ASRPCMQueueCapacity:       50,
			TTSPCMQueueCapacity:       50,
			DownlinkOpusQueueCapacity: 50,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "test-device",
		ClientID:     "test-client",
		SerialNumber: "test-sn",
	}

	sess := NewSessionWithWriter(ctx, nil, writer, info, cfg, asrClient, llmClient, ttsClient, slog.Default())
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 1. 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 2. listen.start -> LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	// 3. ASR 产生最终识别结果 "今天天气" -> 状态机进入 PROCESSING 并触发 LLM/TTS 编排
	sess.postEvent(event{
		kind:       eventKindASRFinal,
		generation: 1,
		text:       "今天天气",
	})

	// 等待编排完成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mockTTS.FinishCalls() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if mockTTS.FinishCalls() != 1 {
		t.Fatalf("expected TTS finish to be called, got %d", mockTTS.FinishCalls())
	}

	waitState(t, sess, StateReady, 2*time.Second)

	// 验证下发了 STT 消息和 sentence_start 消息
	textMsgs := conn.TextMessages()
	var gotSTT bool
	var gotSentenceStarts []string
	for _, raw := range textMsgs {
		var stt ServerSTTMessage
		if err := json.Unmarshal(raw, &stt); err == nil && stt.Type == MessageTypeSTT {
			if stt.Text == "今天天气" {
				gotSTT = true
			}
		}
		var sStart ServerTTSSentenceStartMessage
		if err := json.Unmarshal(raw, &sStart); err == nil && sStart.Type == MessageTypeTTS && sStart.State == TTSStateSentenceStart {
			gotSentenceStarts = append(gotSentenceStarts, sStart.Text)
		}
	}

	if !gotSTT {
		t.Errorf("expected STT message for '今天天气' was not sent")
	}
	if len(gotSentenceStarts) != 2 || gotSentenceStarts[0] != "北京今天晴朗，" || gotSentenceStarts[1] != "气温25度。" {
		t.Errorf("unexpected sentence_start messages: %v", gotSentenceStarts)
	}
}

// TestOrchestration_ConcurrentRace 验证并发编排与并发中断场景下的竞态安全性。
func TestOrchestration_ConcurrentRace(t *testing.T) {
	const concurrency = 15
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mockStream := newMockLLMStream([]string{"第一句。", "第二句。", "第三句。"}, nil)
			llmClient := newMockLLMClient(mockStream, nil)
			mockTTS := newMockTTSStream(nil)
			ttsClient := newMockTTSClient(mockTTS, nil)

			sess, _, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, nil)

			// 随机测试：正常编排、中途取消、或并发 abort
			switch idx % 3 {
			case 0:
				sess.orchestrateLLMAndTTS(ctx, uint64(idx+1), "正常测试")
			case 1:
				go func() {
					time.Sleep(2 * time.Millisecond)
					cancel()
				}()
				sess.orchestrateLLMAndTTS(ctx, uint64(idx+1), "取消测试")
			case 2:
				go func() {
					time.Sleep(2 * time.Millisecond)
					sess.PostAbort("并发打断")
				}()
				sess.orchestrateLLMAndTTS(ctx, uint64(idx+1), "中断测试")
			}
		}(i)
	}

	wg.Wait()
}
