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

// abortMockASRStream 用于中断测试的可控 ASR 流。
type abortMockASRStream struct {
	mu         sync.Mutex
	resultChan chan string
	errChan    chan error
	closed     bool
}

func newAbortMockASRStream() *abortMockASRStream {
	return &abortMockASRStream{
		resultChan: make(chan string, 1),
		errChan:    make(chan error, 1),
	}
}

func (s *abortMockASRStream) WritePCM(ctx context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("asr stream is closed")
	}
	return ctx.Err()
}

func (s *abortMockASRStream) Finish(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("asr stream is closed")
	}
	return ctx.Err()
}

func (s *abortMockASRStream) Result(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-s.errChan:
		return "", err
	case res := <-s.resultChan:
		return res, nil
	}
}

func (s *abortMockASRStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *abortMockASRStream) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// abortMockASRClient 用于中断测试的 ASR 客户端。
type abortMockASRClient struct {
	mu          sync.Mutex
	lastStream  *abortMockASRStream
	createCalls int
}

func newAbortMockASRClient() *abortMockASRClient {
	return &abortMockASRClient{}
}

func (c *abortMockASRClient) CreateStream(ctx context.Context) (ai.ASRStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls++
	stream := newAbortMockASRStream()
	c.lastStream = stream
	return stream, nil
}

func (c *abortMockASRClient) LastStream() *abortMockASRStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStream
}

// abortMockLLMStream 用于中断测试的可阻塞 LLM 流。
type abortMockLLMStream struct {
	mu          sync.Mutex
	chunks      []string
	chunkIndex  int
	pauseChan   chan struct{}
	closed      bool
	cancelCause error
}

func newAbortMockLLMStream(chunks []string, pauseChan chan struct{}) *abortMockLLMStream {
	return &abortMockLLMStream{
		chunks:    chunks,
		pauseChan: pauseChan,
	}
}

func (s *abortMockLLMStream) Recv() (string, error) {
	if s.pauseChan != nil {
		<-s.pauseChan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("llm stream closed")
	}
	if s.chunkIndex < len(s.chunks) {
		c := s.chunks[s.chunkIndex]
		s.chunkIndex++
		return c, nil
	}
	return "", io.EOF
}

func (s *abortMockLLMStream) ToolCalls() []ai.ToolCall {
	return nil
}

func (s *abortMockLLMStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// abortMockLLMClient 用于中断测试的 LLM 客户端。
type abortMockLLMClient struct {
	mu          sync.Mutex
	streamFunc  func() ai.LLMStream
	createCalls int
}

func newAbortMockLLMClient(streamFunc func() ai.LLMStream) *abortMockLLMClient {
	return &abortMockLLMClient{
		streamFunc: streamFunc,
	}
}

func (c *abortMockLLMClient) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls++
	if c.streamFunc != nil {
		return c.streamFunc(), nil
	}
	return newAbortMockLLMStream([]string{"你好呀！"}, nil), nil
}

// abortMockTTSStream 用于中断测试的 TTS 流。
type abortMockTTSStream struct {
	mu            sync.Mutex
	pcmData       [][]byte
	pcmIndex      int
	pausePCMChan  chan struct{}
	sentSentences []string
	closed        bool
	finishCalled  bool
}

func newAbortMockTTSStream(pcmData [][]byte, pausePCMChan chan struct{}) *abortMockTTSStream {
	return &abortMockTTSStream{
		pcmData:      pcmData,
		pausePCMChan: pausePCMChan,
	}
}

func (s *abortMockTTSStream) SendSentence(ctx context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("tts stream closed")
	}
	s.sentSentences = append(s.sentSentences, text)
	return ctx.Err()
}

func (s *abortMockTTSStream) Finish(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("tts stream closed")
	}
	s.finishCalled = true
	return ctx.Err()
}

func (s *abortMockTTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	if s.pausePCMChan != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.pausePCMChan:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("tts stream closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.pcmIndex >= len(s.pcmData) {
		return nil, io.EOF
	}
	chunk := s.pcmData[s.pcmIndex]
	s.pcmIndex++
	return chunk, nil
}

func (s *abortMockTTSStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// abortMockTTSClient 用于中断测试的 TTS 客户端。
type abortMockTTSClient struct {
	mu          sync.Mutex
	streamFunc  func() ai.TTSStream
	createCalls int
}

func newAbortMockTTSClient(streamFunc func() ai.TTSStream) *abortMockTTSClient {
	return &abortMockTTSClient{
		streamFunc: streamFunc,
	}
}

func (c *abortMockTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls++
	if c.streamFunc != nil {
		return c.streamFunc(), nil
	}
	return newAbortMockTTSStream(nil, nil), nil
}

// createAbortTestSession 创建用于中断与代次测试的会话及假连接。
func createAbortTestSession(ctx context.Context, asr ai.ASRClient, llm ai.LLMClient, tts ai.TTSClient) (*Session, *fakeWSConn) {
	fakeConn := &fakeWSConn{}
	writer := NewWriter(ctx, fakeConn, 100, slog.Default())
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:              5 * time.Second,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      30 * time.Second,
			MaxHistoryTurns:           6,
			SystemPrompt:              "你是小智助手。",
			ASRPCMQueueCapacity:       50,
			TTSPCMQueueCapacity:       50,
			DownlinkOpusQueueCapacity: 50,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "abort-device-1",
		ClientID:     "abort-client-1",
		SerialNumber: "abort-sn-1",
	}

	sess := NewSessionWithWriter(ctx, nil, writer, info, cfg, asr, llm, tts, slog.Default())
	return sess, fakeConn
}

// extractMessagesByType 从捕获的消息列表中提取特定类型与状态的消息。
func extractMessagesByType(messages []fakeWSMessage) (ttsStarts, ttsStops, sttMsgs []string, binaryCount int) {
	for _, m := range messages {
		if m.typ == websocket.MessageBinary {
			binaryCount++
			continue
		}
		var base struct {
			Type  string `json:"type"`
			State string `json:"state"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(m.payload, &base); err == nil {
			if base.Type == MessageTypeTTS && base.State == TTSStateStart {
				ttsStarts = append(ttsStarts, string(m.payload))
			} else if base.Type == MessageTypeTTS && base.State == TTSStateStop {
				ttsStops = append(ttsStops, string(m.payload))
			} else if base.Type == MessageTypeSTT {
				sttMsgs = append(sttMsgs, base.Text)
			}
		}
	}
	return
}

// TestSession_Abort_Processing_NoStop_ResetReady 验证在 PROCESSING 处理中（LLM 运行中）收到 abort 时的行为：
// 1. 代次自增，调用当前轮次 turnCancel；
// 2. 取消正在运行的 LLM/TTS 任务；
// 3. 不下发 tts.stop（因从未发送过 tts.start）；
// 4. 状态重置为 READY；
// 5. 会话历史保持为空。
func TestSession_Abort_Processing_NoStop_ResetReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pauseLLM := make(chan struct{})
	defer close(pauseLLM)

	llmStream := newAbortMockLLMStream([]string{"处理中的回答"}, pauseLLM)
	llmClient := newAbortMockLLMClient(func() ai.LLMStream { return llmStream })
	ttsClient := newAbortMockTTSClient(func() ai.TTSStream { return newAbortMockTTSStream(nil, nil) })
	asrClient := newAbortMockASRClient()

	sess, fakeConn := createAbortTestSession(ctx, asrClient, llmClient, ttsClient)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// listen.start auto -> LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	genBefore := sess.Generation()

	// 触发 ASR 最终结果 -> PROCESSING
	sess.PostASRFinal(genBefore, "用户说了一句话")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 在 PROCESSING 中收到 abort 消息
	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "用户打断"})
	waitState(t, sess, StateReady, 2*time.Second)

	// 验证代次自增
	if sess.Generation() <= genBefore {
		t.Fatalf("expected generation to increment, before: %d, after: %d", genBefore, sess.Generation())
	}

	// 验证消息列表：不得包含 tts.start 与 tts.stop
	ttsStarts, ttsStops, sttMsgs, _ := extractMessagesByType(fakeConn.Messages())
	if len(ttsStarts) != 0 {
		t.Fatalf("expected 0 tts.start message in processing abort, got %d", len(ttsStarts))
	}
	if len(ttsStops) != 0 {
		t.Fatalf("expected 0 tts.stop message in processing abort, got %d", len(ttsStops))
	}
	if len(sttMsgs) != 1 || sttMsgs[0] != "用户说了一句话" {
		t.Fatalf("expected 1 STT message before abort, got %v", sttMsgs)
	}

	// 验证历史记录未写入
	if len(sess.History()) != 0 {
		t.Fatalf("expected empty history after aborted processing, got %d messages", len(sess.History()))
	}
}

// TestSession_Abort_Speaking_SendStopOnce_ClearDownlink_ResetReady 验证在 SPEAKING 播放中（Opus 下发中）收到 abort 时的行为：
// 1. 代次自增，取消当前轮次；
// 2. 补发且仅补发一次 tts.stop；
// 3. 清空未发送音频包，停止 Pacer；
// 4. 状态重置为 READY；
// 5. 历史记录未写入。
func TestSession_Abort_Speaking_SendStopOnce_ClearDownlink_ResetReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 构造包含多帧音频的 TTS 流
	frame1 := generate24kSinePCMForSession(440.0, 15000.0)
	frame2 := generate24kSinePCMForSession(880.0, 15000.0)
	frame3 := generate24kSinePCMForSession(1200.0, 15000.0)

	pauseTTS := make(chan struct{})
	defer close(pauseTTS)

	ttsStream := newAbortMockTTSStream([][]byte{frame1, frame2, frame3}, pauseTTS)
	ttsClient := newAbortMockTTSClient(func() ai.TTSStream { return ttsStream })
	llmStream := newAbortMockLLMStream([]string{"正在播放的语音回答。"}, nil)
	llmClient := newAbortMockLLMClient(func() ai.LLMStream { return llmStream })
	asrClient := newAbortMockASRClient()

	sess, fakeConn := createAbortTestSession(ctx, asrClient, llmClient, ttsClient)

	// 使用确定性 mock 节奏器
	manualTicker := newManualTicker()
	sess.SetTickerFactory(func(d time.Duration) Ticker {
		return manualTicker
	})

	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 开启第 1 轮 -> LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	gen1 := sess.Generation()

	// ASR 结果 -> PROCESSING -> SPEAKING
	sess.PostASRFinal(gen1, "请问天气")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 允许 TTS 产出首帧 PCM 并触发 Pacer 发送首包进入 SPEAKING
	pauseTTS <- struct{}{}
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 此时会话已处于 SPEAKING 状态，发送 abort 消息
	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "用户打断播放"})
	waitState(t, sess, StateReady, 2*time.Second)

	// 确定性等待 tts.stop 异步写出
	deadline := time.Now().Add(2 * time.Second)
	var ttsStarts, ttsStops []string
	for time.Now().Before(deadline) {
		ttsStarts, ttsStops, _, _ = extractMessagesByType(fakeConn.Messages())
		if len(ttsStops) >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// 验证代次自增
	if sess.Generation() <= gen1 {
		t.Fatalf("expected generation to increment, before: %d, after: %d", gen1, sess.Generation())
	}

	// 验证消息列表：恰好有 1 次 tts.start 和 1 次 tts.stop
	if len(ttsStarts) != 1 {
		t.Fatalf("expected exactly 1 tts.start message, got %d", len(ttsStarts))
	}
	if len(ttsStops) != 1 {
		t.Fatalf("expected exactly 1 tts.stop message on abort speaking, got %d", len(ttsStops))
	}

	// 验证历史记录为空（被打断的轮次不得入库）
	if len(sess.History()) != 0 {
		t.Fatalf("expected empty history after aborted speaking, got %d messages", len(sess.History()))
	}
}

// TestSession_Abort_MultipleConsecutiveAborts_Idempotent 验证连续快速多次 abort 的幂等性：
// 1. 处于 SPEAKING 状态时连续收到 3 次 abort；
// 2. 仅在首次处于 SPEAKING 时补发一次 tts.stop，后续 abort 不会重复补发；
// 3. 状态稳定保持在 READY，无死锁或 panic。
func TestSession_Abort_MultipleConsecutiveAborts_Idempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frame1 := generate24kSinePCMForSession(440.0, 15000.0)
	pauseTTS := make(chan struct{})
	defer close(pauseTTS)

	ttsStream := newAbortMockTTSStream([][]byte{frame1}, pauseTTS)
	ttsClient := newAbortMockTTSClient(func() ai.TTSStream { return ttsStream })
	llmStream := newAbortMockLLMStream([]string{"连续打断测试。"}, nil)
	llmClient := newAbortMockLLMClient(func() ai.LLMStream { return llmStream })
	asrClient := newAbortMockASRClient()

	sess, fakeConn := createAbortTestSession(ctx, asrClient, llmClient, ttsClient)
	manualTicker := newManualTicker()
	sess.SetTickerFactory(func(d time.Duration) Ticker { return manualTicker })

	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 进入 SPEAKING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	gen := sess.Generation()
	sess.PostASRFinal(gen, "测试打断")
	waitState(t, sess, StateProcessing, 2*time.Second)
	pauseTTS <- struct{}{}
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 连续快速发送 3 次 abort
	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "打断1"})
	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "打断2"})
	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "打断3"})

	waitState(t, sess, StateReady, 2*time.Second)

	// 确定性等待 tts.stop 异步写出
	deadline := time.Now().Add(2 * time.Second)
	var ttsStops []string
	for time.Now().Before(deadline) {
		_, ttsStops, _, _ = extractMessagesByType(fakeConn.Messages())
		if len(ttsStops) >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// 验证 tts.stop 仅补发了 1 次
	if len(ttsStops) != 1 {
		t.Fatalf("expected exactly 1 tts.stop message for multiple consecutive aborts, got %d", len(ttsStops))
	}

	// 状态保持 READY
	if sess.State() != StateReady {
		t.Fatalf("expected state READY, got %v", sess.State())
	}
}

// TestSession_Abort_ImmediateNewTurn_CompleteLifecycle 验证在 abort 后立即发起新一轮 listen.start 的完整问答流程：
// 1. 第 1 轮被打断；
// 2. 立即开启第 2 轮完整问答（ASR -> LLM -> TTS -> Opus 下行 -> tts.stop）；
// 3. 第 2 轮不受第 1 轮旧代次影响正常结束；
// 4. 会话历史中仅包含第 2 轮的问答。
func TestSession_Abort_ImmediateNewTurn_CompleteLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var turn int
	var mu sync.Mutex
	pauseTurn1 := make(chan struct{})
	defer func() {
		select {
		case <-pauseTurn1:
		default:
			close(pauseTurn1)
		}
	}()

	llmClient := newAbortMockLLMClient(func() ai.LLMStream {
		mu.Lock()
		turn++
		currentTurn := turn
		mu.Unlock()
		if currentTurn == 1 {
			return newAbortMockLLMStream([]string{"第一轮回答"}, pauseTurn1)
		}
		return newAbortMockLLMStream([]string{"今天", "天气晴朗。"}, nil)
	})

	var ttsCount int
	var ttsMu sync.Mutex
	ttsClient := newAbortMockTTSClient(func() ai.TTSStream {
		ttsMu.Lock()
		ttsCount++
		currentTTS := ttsCount
		ttsMu.Unlock()
		if currentTTS == 1 {
			return newAbortMockTTSStream(nil, nil)
		}
		frame := generate24kSinePCMForSession(500.0, 15000.0)
		return newAbortMockTTSStream([][]byte{frame}, nil)
	})
	asrClient := newAbortMockASRClient()

	sess, fakeConn := createAbortTestSession(ctx, asrClient, llmClient, ttsClient)
	manualTicker := newManualTicker()
	sess.SetTickerFactory(func(d time.Duration) Ticker { return manualTicker })

	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 第 1 轮开启并在 PROCESSING 中 abort
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	sess.PostASRFinal(sess.Generation(), "第一轮问题")
	waitState(t, sess, StateProcessing, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "取消第一轮"})
	waitState(t, sess, StateReady, 2*time.Second)

	// 第 2 轮立即开启
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	gen2 := sess.Generation()

	// 第 2 轮 ASR 结果 -> 进入 SPEAKING 播放
	sess.PostASRFinal(gen2, "第二轮问题")
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 驱动 mock ticker 完成第 2 轮下行播放
	for i := 0; i < 5; i++ {
		manualTicker.Tick()
		time.Sleep(5 * time.Millisecond)
	}

	// 等待第 2 轮正常完成回到 READY
	waitState(t, sess, StateReady, 2*time.Second)

	// 验证会话历史中仅包含第 2 轮的内容（共 2 条消息：user + assistant）
	history := sess.History()
	if len(history) != 2 {
		t.Fatalf("expected exactly 2 messages in history (turn 2 only), got %d: %v", len(history), history)
	}
	if history[0].Content != "第二轮问题" {
		t.Errorf("expected user text '第二轮问题', got %q", history[0].Content)
	}
	if history[1].Content != "今天天气晴朗。" {
		t.Errorf("expected assistant text '今天天气晴朗。', got %q", history[1].Content)
	}

	// 验证消息列表：STT 包含两轮，tts.start 与 tts.stop 各有 1 次（第 2 轮）
	ttsStarts, ttsStops, sttMsgs, _ := extractMessagesByType(fakeConn.Messages())
	if len(ttsStarts) != 1 {
		t.Fatalf("expected 1 tts.start message, got %d", len(ttsStarts))
	}
	if len(ttsStops) != 1 {
		t.Fatalf("expected 1 tts.stop message, got %d", len(ttsStops))
	}
	if len(sttMsgs) != 2 {
		t.Fatalf("expected 2 STT messages, got %d", len(sttMsgs))
	}
}

// TestSession_StaleGenerationEvents_Discarded 验证迟到的旧代次各类事件被统一丢弃，不得产生输出、不改变状态、不写历史、不关闭连接。
func TestSession_StaleGenerationEvents_Discarded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, fakeConn := createAbortTestSession(ctx, nil, nil, nil)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 开启第 1 轮 -> LISTENING (gen = 1)
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	staleGen := sess.Generation() // staleGen = 1

	// abort 推进代次 (gen = 2) -> READY
	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "代次推进"})
	waitState(t, sess, StateReady, 2*time.Second)
	waitGeneration(t, sess, 2, 2*time.Second)

	msgCountBefore := len(fakeConn.Messages())

	// 1. 注入迟到的 ASR 最终结果 (gen = 1)
	sess.PostASRFinal(staleGen, "迟到的 ASR 结果")
	time.Sleep(20 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY on stale ASR, got %v", sess.State())
	}

	// 2. 注入迟到的 TTS Started (gen = 1)
	sess.PostTTSStarted(staleGen)
	time.Sleep(20 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY on stale TTSStarted, got %v", sess.State())
	}

	// 3. 注入迟到的 TurnFinished (gen = 1)
	sess.PostTurnFinished(staleGen, "迟到用户", "迟到回答")
	time.Sleep(20 * time.Millisecond)
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to remain empty on stale TurnFinished, got %d", len(sess.History()))
	}

	// 4. 注入迟到的超时 (gen = 1)
	sess.PostTimeout(staleGen, "max listening duration exceeded")
	time.Sleep(20 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY on stale Timeout, got %v", sess.State())
	}

	// 5. 注入迟到的错误 (gen = 1)
	sess.PostError(staleGen, errors.New("stale error"), true)
	time.Sleep(20 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY on stale Error, got %v", sess.State())
	}

	// 验证以上所有旧代次事件未向 fakeConn 发送任何新消息
	msgCountAfter := len(fakeConn.Messages())
	if msgCountAfter != msgCountBefore {
		t.Fatalf("expected 0 new messages from stale events, got %d new messages", msgCountAfter-msgCountBefore)
	}
}

// TestSession_Abort_ConcurrentStressRace 模拟极端并发环境下的高频抢占与快速中断，验证并发安全与竞态检测稳定通过。
func TestSession_Abort_ConcurrentStressRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	validAudioPacket := encodeSineOpusPacket(t, 440.0)

	llmClient := newAbortMockLLMClient(func() ai.LLMStream {
		return newAbortMockLLMStream([]string{"并发测试回答1", "并发测试回答2"}, nil)
	})
	ttsClient := newAbortMockTTSClient(func() ai.TTSStream {
		frame := generate24kSinePCMForSession(440.0, 15000.0)
		return newAbortMockTTSStream([][]byte{frame}, nil)
	})
	asrClient := newAbortMockASRClient()

	sess, _ := createAbortTestSession(ctx, asrClient, llmClient, ttsClient)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	var wg sync.WaitGroup
	workers := 8
	iterations := 50

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch i % 5 {
				case 0:
					sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				case 1:
					gen := sess.Generation()
					sess.PostASRFinal(gen, "并发语音文本")
				case 2:
					sess.PostClientAudio(validAudioPacket)
				case 3:
					sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "并发打断"})
				case 4:
					sess.PostClientText(&ClientMessage{Kind: KindListenStop})
				}
			}
		}(w)
	}

	wg.Wait()

	// 最后发送一次 abort，确保会话平稳收敛至 READY
	sess.PostClientText(&ClientMessage{Kind: KindAbort, AbortReason: "最终收敛"})
	waitState(t, sess, StateReady, 2*time.Second)
}
