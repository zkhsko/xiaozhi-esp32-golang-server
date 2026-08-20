package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
)

// historyWSConn 用于测试历史管理的 fake WebSocket 写入连接。
type historyWSConn struct {
	mu       sync.Mutex
	messages []fakeWSMessage
	writeErr error
}

func newHistoryWSConn() *historyWSConn {
	return &historyWSConn{}
}

func (c *historyWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.writeErr != nil {
		return c.writeErr
	}

	copied := make([]byte, len(p))
	copy(copied, p)
	c.messages = append(c.messages, fakeWSMessage{
		typ:     typ,
		payload: copied,
	})
	return nil
}

func (c *historyWSConn) SetWriteErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeErr = err
}

func (c *historyWSConn) TextMessages() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result [][]byte
	for _, m := range c.messages {
		if m.typ == websocket.MessageText {
			result = append(result, m.payload)
		}
	}
	return result
}

// historyMockLLMStream 支持返回预设 chunk 或错误的流式 LLM 输出流。
type historyMockLLMStream struct {
	chunks []string
	index  int
	err    error
	closed bool
	mu     sync.Mutex
}

func newHistoryMockLLMStream(chunks []string, err error) *historyMockLLMStream {
	return &historyMockLLMStream{
		chunks: chunks,
		err:    err,
	}
}

func (s *historyMockLLMStream) Recv() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", errors.New("llm stream closed")
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

func (s *historyMockLLMStream) ToolCalls() []ai.ToolCall {
	return nil
}

func (s *historyMockLLMStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// historyMockLLMClient 实现可记录入参消息列表并支持多轮动态响应的 LLM 客户端。
type historyMockLLMClient struct {
	mu           sync.Mutex
	lastMessages []ai.Message
	allRequests  [][]ai.Message
	responder    func(messages []ai.Message) (ai.LLMStream, error)
}

func newHistoryMockLLMClient(responder func(messages []ai.Message) (ai.LLMStream, error)) *historyMockLLMClient {
	return &historyMockLLMClient{
		responder: responder,
	}
}

func (c *historyMockLLMClient) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	copied := make([]ai.Message, len(messages))
	copy(copied, messages)
	c.lastMessages = copied
	c.allRequests = append(c.allRequests, copied)

	if c.responder != nil {
		return c.responder(messages)
	}
	return newHistoryMockLLMStream([]string{"默认回答。"}, nil), nil
}

func (c *historyMockLLMClient) LastMessages() []ai.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make([]ai.Message, len(c.lastMessages))
	copy(copied, c.lastMessages)
	return copied
}

func (c *historyMockLLMClient) AllRequests() [][]ai.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result [][]ai.Message
	for _, req := range c.allRequests {
		copied := make([]ai.Message, len(req))
		copy(copied, req)
		result = append(result, copied)
	}
	return result
}

// historyMockTTSStream 支持按需在收到句子后生产 PCM 数据的 TTS 输出流。
type historyMockTTSStream struct {
	mu          sync.Mutex
	pcmCh       chan []byte
	pcmErr      error
	sentTexts   []string
	finishCount int
	closed      bool
}

func newHistoryMockTTSStream(initialPackets [][]byte, pcmErr error) *historyMockTTSStream {
	ch := make(chan []byte, len(initialPackets)+10)
	for _, pkt := range initialPackets {
		ch <- pkt
	}
	return &historyMockTTSStream{
		pcmCh:  ch,
		pcmErr: pcmErr,
	}
}

func (s *historyMockTTSStream) SendSentence(ctx context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sentTexts = append(s.sentTexts, text)
	// 每收到一句文本，生产 1 帧 24 kHz 60 ms PCM 数据 (2880 字节)
	select {
	case s.pcmCh <- make([]byte, 2880):
	default:
	}
	return nil
}

func (s *historyMockTTSStream) Finish(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCount++
	if !s.closed {
		s.closed = true
		close(s.pcmCh)
	}
	return nil
}

func (s *historyMockTTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case pkt, ok := <-s.pcmCh:
		if !ok {
			s.mu.Lock()
			err := s.pcmErr
			s.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		return pkt, nil
	}
}

func (s *historyMockTTSStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.pcmCh)
	}
	return nil
}

// historyMockTTSClient 实现可生成有效 TTS 数据的客户端。
type historyMockTTSClient struct {
	mu        sync.Mutex
	createErr error
	responder func() (ai.TTSStream, error)
}

func newHistoryMockTTSClient(responder func() (ai.TTSStream, error)) *historyMockTTSClient {
	return &historyMockTTSClient{
		responder: responder,
	}
}

func (c *historyMockTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.createErr != nil {
		return nil, c.createErr
	}
	if c.responder != nil {
		return c.responder()
	}
	return newHistoryMockTTSStream(nil, nil), nil
}

// helper 创建测试用的基础 Session 配置。
func newTestHistoryConfig(maxHistoryTurns int, systemPrompt string) *config.Config {
	return &config.Config{
		Session: config.SessionConfig{
			SystemPrompt:              systemPrompt,
			MaxHistoryTurns:           maxHistoryTurns,
			HelloTimeout:              5 * time.Second,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      30 * time.Second,
			ASRPCMQueueCapacity:       50,
			TTSPCMQueueCapacity:       50,
			DownlinkOpusQueueCapacity: 50,
		},
	}
}

// TestHistory_AppendAndFIFOEviction 单元测试：验证会话历史追加与 FIFO 滚动淘汰行为。
func TestHistory_AppendAndFIFOEviction(t *testing.T) {
	cfg := newTestHistoryConfig(2, "系统提示词")
	sess := NewSession(context.Background(), nil, nil, cfg, nil, nil, nil, slog.Default())

	// 1. 空输入不追加
	sess.AppendHistory("", "有效回答")
	sess.AppendHistory("有效问题", "")
	if len(sess.History()) != 0 {
		t.Fatalf("expected empty history for blank text, got %d", len(sess.History()))
	}

	// 2. 正常追加第 1 轮
	sess.AppendHistory("问题 1", "回答 1")
	h1 := sess.History()
	if len(h1) != 2 {
		t.Fatalf("expected 2 messages after turn 1, got %d", len(h1))
	}
	if h1[0].Role != ai.RoleUser || h1[0].Content != "问题 1" {
		t.Errorf("turn 1 user message mismatch: %v", h1[0])
	}
	if h1[1].Role != ai.RoleAssistant || h1[1].Content != "回答 1" {
		t.Errorf("turn 1 assistant message mismatch: %v", h1[1])
	}

	// 3. 追加第 2 轮，历史达到上限 2 轮（4 条消息）
	sess.AppendHistory("问题 2", "回答 2")
	h2 := sess.History()
	if len(h2) != 4 {
		t.Fatalf("expected 4 messages after turn 2, got %d", len(h2))
	}

	// 4. 追加第 3 轮，应当触发 FIFO 淘汰最旧的第 1 轮，保留第 2、3 轮
	sess.AppendHistory("问题 3", "回答 3")
	h3 := sess.History()
	if len(h3) != 4 {
		t.Fatalf("expected 4 messages after turn 3 due to FIFO limit of 2 turns, got %d", len(h3))
	}
	if h3[0].Content != "问题 2" || h3[1].Content != "回答 2" {
		t.Errorf("expected turn 1 evicted and turn 2 preserved, got %v, %v", h3[0], h3[1])
	}
	if h3[2].Content != "问题 3" || h3[3].Content != "回答 3" {
		t.Errorf("expected turn 3 appended, got %v, %v", h3[2], h3[3])
	}

	// 5. 显式清空历史
	sess.ClearHistory()
	if len(sess.History()) != 0 {
		t.Fatalf("expected empty history after ClearHistory, got %d", len(sess.History()))
	}
}

// TestHistory_BuildLLMMessages 单元测试：验证大语言模型消息列表构造与内存隔离。
func TestHistory_BuildLLMMessages(t *testing.T) {
	t.Run("with system prompt", func(t *testing.T) {
		cfg := newTestHistoryConfig(6, "你是小智。")
		sess := NewSession(context.Background(), nil, nil, cfg, nil, nil, nil, slog.Default())

		// 无历史时
		msgs0 := sess.buildLLMMessages("当前问题 0")
		if len(msgs0) != 2 {
			t.Fatalf("expected 2 messages (system + current user), got %d", len(msgs0))
		}
		if msgs0[0].Role != ai.RoleSystem || msgs0[0].Content != "你是小智。" {
			t.Errorf("system prompt mismatch: %v", msgs0[0])
		}
		if msgs0[1].Role != ai.RoleUser || msgs0[1].Content != "当前问题 0" {
			t.Errorf("current user message mismatch: %v", msgs0[1])
		}

		// 追加 2 轮历史
		sess.AppendHistory("问 1", "答 1")
		sess.AppendHistory("问 2", "答 2")

		msgs := sess.buildLLMMessages("当前问题 3")
		if len(msgs) != 6 {
			t.Fatalf("expected 6 messages (1 system + 4 history + 1 current), got %d", len(msgs))
		}
		if msgs[0].Role != ai.RoleSystem || msgs[0].Content != "你是小智。" {
			t.Errorf("system prompt mismatch: %v", msgs[0])
		}
		if msgs[1].Content != "问 1" || msgs[2].Content != "答 1" {
			t.Errorf("history turn 1 mismatch: %v, %v", msgs[1], msgs[2])
		}
		if msgs[3].Content != "问 2" || msgs[4].Content != "答 2" {
			t.Errorf("history turn 2 mismatch: %v, %v", msgs[3], msgs[4])
		}
		if msgs[5].Role != ai.RoleUser || msgs[5].Content != "当前问题 3" {
			t.Errorf("current user message mismatch: %v", msgs[5])
		}

		// 验证内存隔离：修改返回的切片元素不影响 Session 内部历史
		msgs[1].Content = "篡改历史"
		internalHistory := sess.History()
		if internalHistory[0].Content != "问 1" {
			t.Errorf("internal history was mutated by external slice: got %s", internalHistory[0].Content)
		}
	})

	t.Run("without system prompt", func(t *testing.T) {
		cfg := newTestHistoryConfig(6, "")
		sess := NewSession(context.Background(), nil, nil, cfg, nil, nil, nil, slog.Default())

		sess.AppendHistory("问 1", "答 1")
		msgs := sess.buildLLMMessages("当前问题 2")
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages (2 history + 1 current), got %d", len(msgs))
		}
		if msgs[0].Content != "问 1" || msgs[1].Content != "答 1" || msgs[2].Content != "当前问题 2" {
			t.Errorf("messages mismatch: %v", msgs)
		}
	})
}

// TestHistory_SevenConsecutiveTurns_E2E 端到端集成测试：
// 完成 7 轮连续完整语音对话，验证第 7 轮 LLM 请求严格仅包含 System Prompt + 最近 6 轮历史 + 当前 User 消息（共 14 条）；
// 第 7 轮结束后淘汰第 1 轮，历史严格保留最近 6 轮；
// 第 8 轮 LLM 请求严格包含第 2~7 轮历史。
func TestHistory_SevenConsecutiveTurns_E2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const maxTurns = 6
	cfg := newTestHistoryConfig(maxTurns, "你是小智，一个智能语音助手。")

	llmClient := newHistoryMockLLMClient(func(messages []ai.Message) (ai.LLMStream, error) {
		// 根据当前 user 消息返回对应的回答
		lastMsg := messages[len(messages)-1].Content
		ans := fmt.Sprintf("助手回答对[%s]", lastMsg)
		return newHistoryMockLLMStream([]string{ans + "。"}, nil), nil
	})

	ttsClient := newHistoryMockTTSClient(func() (ai.TTSStream, error) {
		return newHistoryMockTTSStream([][]byte{make([]byte, 2880)}, nil), nil
	})

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())
	info := &ClientHeaderInfo{
		DeviceID:     "device-test-7turns",
		ClientID:     "client-test-7turns",
		SerialNumber: "sn-test-7turns",
	}

	sess := NewSessionWithWriter(ctx, nil, writer, info, cfg, nil, llmClient, ttsClient, slog.Default())

	// 设置手动可控时钟，加速下行发送
	ticker := newManualTicker()
	sess.SetTickerFactory(func(d time.Duration) Ticker {
		return ticker
	})

	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 1. 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 2. 连续执行 7 轮问答
	for turn := 1; turn <= 7; turn++ {
		gen := uint64(turn)
		userText := fmt.Sprintf("用户问题 %d", turn)

		// 触发收音开始 -> LISTENING
		sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
		waitState(t, sess, StateListening, 2*time.Second)

		// 投递 ASR 识别结果 -> 进入 PROCESSING 并启动编排
		sess.postEvent(event{
			kind:       eventKindASRFinal,
			generation: gen,
			text:       userText,
		})

		// 驱动 Ticker 下发音频包直至播放完成回到 READY
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			ticker.Tick()
			if sess.State() == StateReady && sess.Generation() == gen {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		if sess.State() != StateReady {
			t.Fatalf("turn %d: expected session to return to READY, got %v", turn, sess.State())
		}
	}

	// 3. 验证所有 7 轮 LLM 请求的上下文构造
	allReqs := llmClient.AllRequests()
	if len(allReqs) != 7 {
		t.Fatalf("expected 7 LLM requests, got %d", len(allReqs))
	}

	// 验证第 1 轮：1 System + 0 历史 + 1 User = 2 条
	req1 := allReqs[0]
	if len(req1) != 2 {
		t.Fatalf("turn 1: expected 2 messages in LLM request, got %d", len(req1))
	}
	if req1[0].Role != ai.RoleSystem || req1[1].Content != "用户问题 1" {
		t.Errorf("turn 1 request mismatch: %v", req1)
	}

	// 验证第 7 轮：严格包含 1 System + 6 轮已提交历史（12 条） + 1 当前 User = 14 条！
	req7 := allReqs[6]
	if len(req7) != 14 {
		t.Fatalf("turn 7: expected exactly 14 messages (1 system + 12 history + 1 current user), got %d: %v", len(req7), req7)
	}
	if req7[0].Role != ai.RoleSystem || req7[0].Content != "你是小智，一个智能语音助手。" {
		t.Errorf("turn 7 system prompt mismatch: %v", req7[0])
	}
	for i := 1; i <= 6; i++ {
		userIdx := 1 + (i-1)*2
		asstIdx := userIdx + 1
		expectedUser := fmt.Sprintf("用户问题 %d", i)
		expectedAsst := fmt.Sprintf("助手回答对[用户问题 %d]。", i)

		if req7[userIdx].Role != ai.RoleUser || req7[userIdx].Content != expectedUser {
			t.Errorf("turn 7 history turn %d user mismatch: expected %q, got %q", i, expectedUser, req7[userIdx].Content)
		}
		if req7[asstIdx].Role != ai.RoleAssistant || req7[asstIdx].Content != expectedAsst {
			t.Errorf("turn 7 history turn %d assistant mismatch: expected %q, got %q", i, expectedAsst, req7[asstIdx].Content)
		}
	}
	if req7[13].Role != ai.RoleUser || req7[13].Content != "用户问题 7" {
		t.Errorf("turn 7 current user message mismatch: expected '用户问题 7', got %q", req7[13].Content)
	}

	// 4. 验证第 7 轮完成后，Session 内存历史按 FIFO 淘汰第 1 轮，严格保留第 2~7 轮（共 12 条）
	finalHistory := sess.History()
	if len(finalHistory) != 12 {
		t.Fatalf("expected 12 history messages after 7 turns, got %d", len(finalHistory))
	}
	if finalHistory[0].Content != "用户问题 2" || finalHistory[1].Content != "助手回答对[用户问题 2]。" {
		t.Errorf("expected turn 1 evicted, but got oldest: %v, %v", finalHistory[0], finalHistory[1])
	}
	if finalHistory[10].Content != "用户问题 7" || finalHistory[11].Content != "助手回答对[用户问题 7]。" {
		t.Errorf("expected newest turn 7 present, got %v, %v", finalHistory[10], finalHistory[11])
	}

	// 5. 执行第 8 轮对话，断言 LLM 接收到的消息严格包含第 2~7 轮历史
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	sess.postEvent(event{
		kind:       eventKindASRFinal,
		generation: 8,
		text:       "用户问题 8",
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ticker.Tick()
		if sess.State() == StateReady && sess.Generation() == 8 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	allReqsAfter8 := llmClient.AllRequests()
	if len(allReqsAfter8) != 8 {
		t.Fatalf("expected 8 LLM requests, got %d", len(allReqsAfter8))
	}
	req8 := allReqsAfter8[7]
	if len(req8) != 14 {
		t.Fatalf("turn 8: expected 14 messages, got %d", len(req8))
	}
	if req8[1].Content != "用户问题 2" || req8[13].Content != "用户问题 8" {
		t.Errorf("turn 8 request mismatch: oldest history %q, current %q", req8[1].Content, req8[13].Content)
	}
}

// TestHistory_AbortInProcessingDoesNotPollute 验证在 PROCESSING 阶段（生成中）触发 abort 时：
// 该轮的部分回答绝不进入历史，会话历史未受任何污染，后续轮次上下文正常。
func TestHistory_AbortInProcessingDoesNotPollute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := newTestHistoryConfig(6, "系统提示词")

	// 阻塞型 LLM Stream，在 Recv 时等待 Context 取消
	blockingStream := &blockingHistoryLLMStream{}
	llmClient := newHistoryMockLLMClient(func(messages []ai.Message) (ai.LLMStream, error) {
		lastMsg := messages[len(messages)-1].Content
		if lastMsg == "打断问题 2" {
			return blockingStream, nil
		}
		return newHistoryMockLLMStream([]string{"正常回答。"}, nil), nil
	})

	ttsClient := newHistoryMockTTSClient(nil)

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())
	sess := NewSessionWithWriter(ctx, nil, writer, nil, cfg, nil, llmClient, ttsClient, slog.Default())
	ticker := newManualTicker()
	sess.SetTickerFactory(func(d time.Duration) Ticker { return ticker })

	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 1. 完成第 1 轮正常对话
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	sess.postEvent(event{kind: eventKindASRFinal, generation: 1, text: "正常问题 1"})
	for i := 0; i < 50; i++ {
		ticker.Tick()
		if sess.State() == StateReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	h1 := sess.History()
	if len(h1) != 2 {
		t.Fatalf("expected 1 completed turn in history, got %d", len(h1))
	}

	// 2. 发起第 2 轮对话，进入 PROCESSING 时触发 abort
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	sess.postEvent(event{kind: eventKindASRFinal, generation: 2, text: "打断问题 2"})
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 在 PROCESSING 阶段触发 abort
	sess.PostAbort("用户在思考中打断")
	waitState(t, sess, StateReady, 2*time.Second)

	// 3. 断言第 2 轮未被写入历史，历史依然严格只包含第 1 轮
	hAfterAbort := sess.History()
	if len(hAfterAbort) != 2 {
		t.Fatalf("expected history to remain 2 messages after abort in processing, got %d", len(hAfterAbort))
	}
	if hAfterAbort[0].Content != "正常问题 1" || hAfterAbort[1].Content != "正常回答。" {
		t.Errorf("history polluted after abort: %v", hAfterAbort)
	}

	// 4. 发起第 3 轮正常对话，验证 LLM 请求只携带第 1 轮历史
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	sess.postEvent(event{kind: eventKindASRFinal, generation: sess.Generation(), text: "正常问题 3"})
	for i := 0; i < 50; i++ {
		ticker.Tick()
		if sess.State() == StateReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	req3 := llmClient.LastMessages()
	if len(req3) != 4 {
		t.Fatalf("expected 4 messages in turn 3 LLM request (system + turn 1 history + turn 3 user), got %d: %v", len(req3), req3)
	}
	if req3[1].Content != "正常问题 1" || req3[3].Content != "正常问题 3" {
		t.Errorf("turn 3 LLM request mismatch: %v", req3)
	}

	hAfterTurn3 := sess.History()
	if len(hAfterTurn3) != 4 {
		t.Fatalf("expected 4 messages in history after turn 3, got %d", len(hAfterTurn3))
	}
	if hAfterTurn3[0].Content != "正常问题 1" || hAfterTurn3[2].Content != "正常问题 3" {
		t.Errorf("history content mismatch: %v", hAfterTurn3)
	}
}

type blockingHistoryLLMStream struct {
	mu     sync.Mutex
	closed bool
}

func (s *blockingHistoryLLMStream) Recv() (string, error) {
	time.Sleep(1 * time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("stream closed")
	}
	return "", context.Canceled
}

func (s *blockingHistoryLLMStream) ToolCalls() []ai.ToolCall {
	return nil
}

func (s *blockingHistoryLLMStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// speakingMockTTSStream 每收到一句文本生产 10 个 PCM 数据包，便于在播放中途打断。
type speakingMockTTSStream struct {
	*historyMockTTSStream
}

func (s *speakingMockTTSStream) SendSentence(ctx context.Context, text string) error {
	s.historyMockTTSStream.mu.Lock()
	defer s.historyMockTTSStream.mu.Unlock()
	s.historyMockTTSStream.sentTexts = append(s.historyMockTTSStream.sentTexts, text)
	for i := 0; i < 10; i++ {
		select {
		case s.historyMockTTSStream.pcmCh <- make([]byte, 2880):
		default:
		}
	}
	return nil
}

// TestHistory_AbortInSpeakingDoesNotPollute 验证在 SPEAKING 阶段（播放中）触发 abort 时：
// 虽然会话发送了 tts.stop 作为中断信号，但该轮部分回答绝不写入历史，会话历史保持不变。
func TestHistory_AbortInSpeakingDoesNotPollute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := newTestHistoryConfig(6, "系统提示词")

	llmClient := newHistoryMockLLMClient(func(messages []ai.Message) (ai.LLMStream, error) {
		lastMsg := messages[len(messages)-1].Content
		return newHistoryMockLLMStream([]string{fmt.Sprintf("完整回答[%s]。", lastMsg)}, nil), nil
	})

	ttsClient := newHistoryMockTTSClient(func() (ai.TTSStream, error) {
		baseStream := newHistoryMockTTSStream(nil, nil)
		return &speakingMockTTSStream{historyMockTTSStream: baseStream}, nil
	})

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())
	sess := NewSessionWithWriter(ctx, nil, writer, nil, cfg, nil, llmClient, ttsClient, slog.Default())
	ticker := newManualTicker()
	sess.SetTickerFactory(func(d time.Duration) Ticker { return ticker })

	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 1. 完成第 1 轮
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	sess.postEvent(event{kind: eventKindASRFinal, generation: 1, text: "轮次 1"})
	for i := 0; i < 50; i++ {
		ticker.Tick()
		if sess.State() == StateReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(sess.History()) != 2 {
		t.Fatalf("expected 1 turn in history, got %d", len(sess.History()))
	}

	// 2. 发起第 2 轮，进入 SPEAKING 阶段
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	sess.postEvent(event{kind: eventKindASRFinal, generation: 2, text: "轮次 2 在播放中打断"})

	// 等待进入 SPEAKING
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 在 SPEAKING 中途触发 abort
	sess.PostAbort("用户在播放中打断")
	waitState(t, sess, StateReady, 2*time.Second)

	// 3. 断言第 2 轮未被提交至历史
	hAfterAbort := sess.History()
	if len(hAfterAbort) != 2 {
		t.Fatalf("expected history to remain 2 messages after speaking abort, got %d", len(hAfterAbort))
	}
	if hAfterAbort[0].Content != "轮次 1" {
		t.Errorf("history turn 1 corrupted: %v", hAfterAbort)
	}
}

// TestHistory_FailureDoesNotPollute 验证 LLM 报错、TTS 报错及下行写失败等异常情况下，该轮绝不进入历史。
func TestHistory_FailureDoesNotPollute(t *testing.T) {
	t.Run("llm error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cfg := newTestHistoryConfig(6, "系统提示词")
		llmClient := newHistoryMockLLMClient(func(messages []ai.Message) (ai.LLMStream, error) {
			return newHistoryMockLLMStream([]string{"部分文本"}, errors.New("upstream llm network error")), nil
		})
		ttsClient := newHistoryMockTTSClient(nil)

		sess := NewSession(ctx, nil, nil, cfg, nil, llmClient, ttsClient, slog.Default())
		sess.orchestrateLLMAndTTS(ctx, 1, "测试 LLM 失败")

		if len(sess.History()) != 0 {
			t.Fatalf("expected empty history on LLM error, got %d", len(sess.History()))
		}
	})

	t.Run("tts create error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cfg := newTestHistoryConfig(6, "系统提示词")
		llmClient := newHistoryMockLLMClient(nil)
		ttsClient := newHistoryMockTTSClient(nil)
		ttsClient.createErr = errors.New("tts service unavailable")

		sess := NewSession(ctx, nil, nil, cfg, nil, llmClient, ttsClient, slog.Default())
		sess.orchestrateLLMAndTTS(ctx, 1, "测试 TTS 失败")

		if len(sess.History()) != 0 {
			t.Fatalf("expected empty history on TTS create error, got %d", len(sess.History()))
		}
	})

	t.Run("empty llm output", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cfg := newTestHistoryConfig(6, "系统提示词")
		llmClient := newHistoryMockLLMClient(func(messages []ai.Message) (ai.LLMStream, error) {
			return newHistoryMockLLMStream([]string{"", "   "}, nil), nil
		})
		ttsClient := newHistoryMockTTSClient(func() (ai.TTSStream, error) {
			return newHistoryMockTTSStream(nil, nil), nil
		})

		sess := NewSession(ctx, nil, nil, cfg, nil, llmClient, ttsClient, slog.Default())
		sess.orchestrateLLMAndTTS(ctx, 1, "测试空回答")

		if len(sess.History()) != 0 {
			t.Fatalf("expected empty history on blank LLM output, got %d", len(sess.History()))
		}
	})

	t.Run("tts stop write error does not commit history", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cfg := newTestHistoryConfig(6, "系统提示词")
		conn := newHistoryWSConn()
		writer := NewWriter(ctx, conn, 100, slog.Default())

		sess := NewSessionWithWriter(ctx, nil, writer, nil, cfg, nil, nil, nil, slog.Default())
		sess.state = StateSpeaking
		sess.sessionID = "test-session"

		// 主动关闭 writer 使得后续 sendTextMessage 必然返回 ErrWriterClosed
		_ = writer.Close()

		// 投递 TurnFinished 事件
		sess.handleTurnFinishedEvent(event{
			kind:          eventKindTurnFinished,
			generation:    sess.Generation(),
			userText:      "写入失败的用户问题",
			assistantText: "写入失败的助手回答",
		})

		// 断言该轮未被写入历史
		if len(sess.History()) != 0 {
			t.Fatalf("expected history NOT committed on tts.stop write failure, got %d", len(sess.History()))
		}
	})
}

// TestHistory_CloseClearsMemoryAndIsolation 验证 Session 关闭后历史清空释放，且不同 Session 实例间历史完全隔离。
func TestHistory_CloseClearsMemoryAndIsolation(t *testing.T) {
	cfg := newTestHistoryConfig(6, "系统提示词")

	sess1 := NewSession(context.Background(), nil, nil, cfg, nil, nil, nil, slog.Default())
	sess2 := NewSession(context.Background(), nil, nil, cfg, nil, nil, nil, slog.Default())

	// 会话 1 追加 2 轮历史
	sess1.AppendHistory("S1 问 1", "S1 答 1")
	sess1.AppendHistory("S1 问 2", "S1 答 2")

	// 验证会话隔离性：会话 2 历史仍为空
	if len(sess2.History()) != 0 {
		t.Fatalf("expected sess2 history to be empty, got %d", len(sess2.History()))
	}

	// 验证会话 1 历史正常
	if len(sess1.History()) != 4 {
		t.Fatalf("expected sess1 history to have 4 messages, got %d", len(sess1.History()))
	}

	// 关闭会话 1
	sess1.Close()

	// 断言会话 1 历史被完全清空释放
	if sess1.History() != nil {
		t.Fatalf("expected sess1 history to be nil after Close, got %v", sess1.History())
	}
}

// TestHistory_ConcurrentRace 高并发竞态测试：
// 多协程并发执行 AppendHistory、History、ClearHistory、buildLLMMessages、PostAbort 与 Close，验证并发安全与数据竞争。
func TestHistory_ConcurrentRace(t *testing.T) {
	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	cfg := newTestHistoryConfig(4, "并发测试提示词")
	sess := NewSession(context.Background(), nil, nil, cfg, nil, nil, nil, slog.Default())

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()

			switch idx % 5 {
			case 0:
				sess.AppendHistory(fmt.Sprintf("并发问 %d", idx), fmt.Sprintf("并发答 %d", idx))
			case 1:
				_ = sess.History()
			case 2:
				_ = sess.buildLLMMessages(fmt.Sprintf("并发查询 %d", idx))
			case 3:
				sess.PostAbort("并发中断")
			case 4:
				sess.ClearHistory()
			}
		}(i)
	}

	wg.Wait()
}
