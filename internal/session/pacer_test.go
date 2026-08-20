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
	"xiaozhi-esp32-golang-server/internal/audio"
	"xiaozhi-esp32-golang-server/internal/config"
)

// manualTicker 供单元测试使用的确定性可控 Ticker。
type manualTicker struct {
	ch chan time.Time
}

func newManualTicker() *manualTicker {
	return &manualTicker{
		ch: make(chan time.Time, 100),
	}
}

func (m *manualTicker) C() <-chan time.Time {
	return m.ch
}

func (m *manualTicker) Stop() {}

func (m *manualTicker) Tick() {
	m.ch <- time.Now()
}

// channelLLMStream 支持按需推送 chunk 的受控 LLM 流。
type channelLLMStream struct {
	ch     chan string
	closed bool
	mu     sync.Mutex
}

func newChannelLLMStream() *channelLLMStream {
	return &channelLLMStream{
		ch: make(chan string, 10),
	}
}

func (s *channelLLMStream) Recv() (string, error) {
	chunk, ok := <-s.ch
	if !ok {
		return "", io.EOF
	}
	return chunk, nil
}

func (s *channelLLMStream) Push(chunk string) {
	s.ch <- chunk
}

func (s *channelLLMStream) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

func (s *channelLLMStream) ToolCalls() []ai.ToolCall {
	return nil
}

func (s *channelLLMStream) Close() error {
	s.Finish()
	return nil
}

// dynamicTTSStream 支持按需推送 PCM 数据块的受控 TTS 流。
type dynamicTTSStream struct {
	pcmCh  chan []byte
	closed bool
	mu     sync.Mutex
}

func newDynamicTTSStream() *dynamicTTSStream {
	return &dynamicTTSStream{
		pcmCh: make(chan []byte, 10),
	}
}

func (s *dynamicTTSStream) SendSentence(ctx context.Context, text string) error {
	return nil
}

func (s *dynamicTTSStream) Finish(ctx context.Context) error {
	return nil
}

func (s *dynamicTTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case chunk, ok := <-s.pcmCh:
		if !ok {
			return nil, io.EOF
		}
		return chunk, nil
	}
}

func (s *dynamicTTSStream) PushPCM(pcm []byte) {
	s.pcmCh <- pcm
}

func (s *dynamicTTSStream) ClosePCM() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.pcmCh)
	}
}

func (s *dynamicTTSStream) Close() error {
	s.ClosePCM()
	return nil
}

// recordingWSConn 记录写入的所有消息（区分文本与二进制）。
type recordingWSConn struct {
	mu       sync.Mutex
	messages []fakeWSMessage
	writeErr error
	errOnNth int
	count    int
}

func newRecordingWSConn() *recordingWSConn {
	return &recordingWSConn{}
}

func (c *recordingWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.count++
	if c.writeErr != nil && (c.errOnNth == 0 || c.count >= c.errOnNth) {
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

func (c *recordingWSConn) Messages() []fakeWSMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make([]fakeWSMessage, len(c.messages))
	copy(copied, c.messages)
	return copied
}

func (c *recordingWSConn) MessageCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

// createTestSessionWithManualTicker 创建绑定了可控 Ticker 并已完成 Hello 握手的测试 Session。
func createTestSessionWithManualTicker(ctx context.Context, mt *manualTicker, wsConn WSConn, llmClient ai.LLMClient, ttsClient ai.TTSClient) (*Session, *Writer) {
	writer := NewWriter(ctx, wsConn, 100, slog.Default())
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
		DeviceID:     "test-pacer-device",
		ClientID:     "test-pacer-client",
		SerialNumber: "test-pacer-sn",
	}

	sess := NewSessionWithWriter(ctx, nil, writer, info, cfg, nil, llmClient, ttsClient, slog.Default())
	if mt != nil {
		sess.SetTickerFactory(func(d time.Duration) Ticker {
			return mt
		})
	}

	return sess, writer
}

// initSessionToListening 辅助函数：通过合法 Hello 与 listen.start 将会话驱动至 LISTENING 状态（generation = 1）。
func initSessionToListening(t *testing.T, sess *Session) {
	t.Helper()
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
}

// TestPacer_FullDownlinkTimingAndMessageOrder 验证完整下行时序与总消息顺序：
// 1. stt 消息 -> 2. tts.sentence_start(句1) -> 3. tts.start -> 4. 音频包1 (随 start 立即发送)
// 5. tts.sentence_start(句2) -> 6. 60 ms tick -> 音频包2 -> 7. 60 ms tick -> 音频包3
// 8. 60 ms tick -> 全部完成发送 tts.stop -> 状态机回到 READY
func TestPacer_FullDownlinkTimingAndMessageOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mt := newManualTicker()
	wsConn := newRecordingWSConn()

	frame1 := generate24kSinePCMForSession(440.0, 15000.0) // 句1 音频帧 1
	frame2 := generate24kSinePCMForSession(440.0, 15000.0) // 句1 音频帧 2
	frame3 := generate24kSinePCMForSession(880.0, 15000.0) // 句2 音频帧 1

	llmStream := newChannelLLMStream()
	llmClient := &mockLLMClient{streamToReturn: (*mockLLMStream)(nil)}
	// 使用自定义 CreateStream
	dynamicLLM := &pacerDynamicLLMClient{stream: llmStream}

	ttsStream := newDynamicTTSStream()
	dynamicTTS := &pacerDynamicTTSClient{stream: ttsStream}

	sess, writer := createTestSessionWithManualTicker(ctx, mt, wsConn, dynamicLLM, dynamicTTS)
	defer writer.Close()

	go func() { _ = sess.Run() }()

	// 1. 初始化进入 LISTENING (generation = 1)
	initSessionToListening(t, sess)

	// 2. 投递 ASRFinal 事件触发 stt 消息与 LLM/TTS 编排
	sess.PostASRFinal(1, "查询天气")

	// 3. 向 LLM 输入第 1 句
	llmStream.Push("今天天气晴朗。")

	// 向 TTS 输入第 1 句的音频（2 帧）
	ttsStream.PushPCM(frame1)
	ttsStream.PushPCM(frame2)

	// 等待首包音频就绪并发送：应包含 helloResp, stt, sentence_start(句1), tts.start, 二进制帧 1
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msgs := wsConn.Messages()
		if len(msgs) >= 5 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	msgs := wsConn.Messages()
	if len(msgs) < 5 {
		t.Fatalf("expected at least 5 messages initially, got %d", len(msgs))
	}

	// 验证消息 0：server hello
	var helloResp ServerHelloMessage
	if err := json.Unmarshal(msgs[0].payload, &helloResp); err != nil || helloResp.Type != MessageTypeHello {
		t.Errorf("msg[0] expected hello response, got: %s", string(msgs[0].payload))
	}

	// 验证消息 1：stt
	var sttMsg ServerSTTMessage
	if err := json.Unmarshal(msgs[1].payload, &sttMsg); err != nil || sttMsg.Type != MessageTypeSTT || sttMsg.Text != "查询天气" {
		t.Errorf("msg[1] expected stt, got: %s", string(msgs[1].payload))
	}

	// 验证消息 2：sentence_start (句1)
	var sStart1 ServerTTSSentenceStartMessage
	if err := json.Unmarshal(msgs[2].payload, &sStart1); err != nil || sStart1.State != TTSStateSentenceStart || sStart1.Text != "今天天气晴朗。" {
		t.Errorf("msg[2] expected sentence_start 1, got: %s", string(msgs[2].payload))
	}

	// 验证消息 3：tts.start (必须在首个二进制音频包前)
	var tStart ServerTTSStartMessage
	if err := json.Unmarshal(msgs[3].payload, &tStart); err != nil || tStart.Type != MessageTypeTTS || tStart.State != TTSStateStart {
		t.Errorf("msg[3] expected tts.start, got: %s", string(msgs[3].payload))
	}

	// 验证消息 4：二进制音频帧 1
	if msgs[4].typ != websocket.MessageBinary || len(msgs[4].payload) == 0 {
		t.Errorf("msg[4] expected binary opus frame 1, got typ %v, len %d", msgs[4].typ, len(msgs[4].payload))
	}

	// 状态机此时应处于 SPEAKING
	if sess.State() != StateSpeaking {
		t.Errorf("expected state SPEAKING, got %v", sess.State())
	}

	// 4. 向 LLM 输入第 2 句并结束 LLM，向 TTS 输入第 2 句音频并结束 TTS PCM
	llmStream.Push("适合出门运动。")
	llmStream.Finish()
	ttsStream.PushPCM(frame3)
	ttsStream.ClosePCM()

	// 等待 sentence_start 2 进入写队列
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msgs = wsConn.Messages()
		if len(msgs) >= 6 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 5. 触发第 1 个 60 ms tick：下发第 2 帧音频
	mt.Tick()
	time.Sleep(20 * time.Millisecond)

	// 6. 触发第 2 个 60 ms tick：下发第 3 帧音频
	mt.Tick()
	time.Sleep(20 * time.Millisecond)

	// 7. 触发第 3 个 60 ms tick：排空队列并下发 tts.stop
	mt.Tick()

	waitState(t, sess, StateReady, 2*time.Second)

	finalMsgs := wsConn.Messages()
	if len(finalMsgs) != 9 {
		t.Fatalf("expected exactly 9 messages in total (1 hello + 8 turn messages), got %d: %+v", len(finalMsgs), finalMsgs)
	}

	// 逐条严格验证消息总顺序：
	// [0] hello
	// [1] stt
	// [2] sentence_start(句1)
	// [3] tts.start
	// [4] Binary(帧1)
	// [5] sentence_start(句2)
	// [6] Binary(帧2)
	// [7] Binary(帧3)
	// [8] tts.stop
	if finalMsgs[0].typ != websocket.MessageText {
		t.Errorf("expected msg[0] text, got %v", finalMsgs[0].typ)
	}
	if finalMsgs[1].typ != websocket.MessageText {
		t.Errorf("expected msg[1] text, got %v", finalMsgs[1].typ)
	}
	if finalMsgs[2].typ != websocket.MessageText {
		t.Errorf("expected msg[2] text, got %v", finalMsgs[2].typ)
	}
	if finalMsgs[3].typ != websocket.MessageText {
		t.Errorf("expected msg[3] text, got %v", finalMsgs[3].typ)
	}
	if finalMsgs[4].typ != websocket.MessageBinary {
		t.Errorf("expected msg[4] binary frame 1, got %v", finalMsgs[4].typ)
	}
	if finalMsgs[5].typ != websocket.MessageText {
		t.Errorf("expected msg[5] sentence_start 2, got %v", finalMsgs[5].typ)
	}
	if finalMsgs[6].typ != websocket.MessageBinary {
		t.Errorf("expected msg[6] binary frame 2, got %v", finalMsgs[6].typ)
	}
	if finalMsgs[7].typ != websocket.MessageBinary {
		t.Errorf("expected msg[7] binary frame 3, got %v", finalMsgs[7].typ)
	}
	if finalMsgs[8].typ != websocket.MessageText {
		t.Errorf("expected msg[8] text tts.stop, got %v", finalMsgs[8].typ)
	}

	var stopMsg ServerTTSStopMessage
	if err := json.Unmarshal(finalMsgs[8].payload, &stopMsg); err != nil || stopMsg.Type != MessageTypeTTS || stopMsg.State != TTSStateStop {
		t.Errorf("expected msg[8] to be tts.stop, got: %s", string(finalMsgs[8].payload))
	}

	// 校验每个二进制包都是合法的 24 kHz Opus 帧
	for _, idx := range []int{4, 6, 7} {
		decoded := decode24kOpusForSession(t, finalMsgs[idx].payload)
		if len(decoded) != audio.DownlinkSamplesPerFrame {
			t.Errorf("frame at index %d sample count mismatch: %d", idx, len(decoded))
		}
	}
	_ = llmClient
}

// pacerDynamicLLMClient 测试用自定义 LLMClient。
type pacerDynamicLLMClient struct {
	stream ai.LLMStream
}

func (c *pacerDynamicLLMClient) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	return c.stream, nil
}

// pacerDynamicTTSClient 测试用自定义 TTSClient。
type pacerDynamicTTSClient struct {
	stream ai.TTSStream
}

func (c *pacerDynamicTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	return c.stream, nil
}

// TestPacer_EmptyResponse_NoStartNoStop 验证空回答分支：
// 当回答无任何音频输出时，不发送 tts.start 也不发送 tts.stop，安全回到 READY 状态。
func TestPacer_EmptyResponse_NoStartNoStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mt := newManualTicker()
	wsConn := newRecordingWSConn()

	mockStream := newMockLLMStream([]string{"", "   "}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := &mockTTSStream{
		pcmDataToReturn: nil, // 无任何音频数据
	}
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, writer := createTestSessionWithManualTicker(ctx, mt, wsConn, llmClient, ttsClient)
	defer writer.Close()

	go func() { _ = sess.Run() }()

	initSessionToListening(t, sess)

	// 投递 ASRFinal
	sess.PostASRFinal(1, "空回答测试")

	// 等待会话安全回到 READY
	waitState(t, sess, StateReady, 2*time.Second)

	msgs := wsConn.Messages()
	for _, m := range msgs {
		if m.typ == websocket.MessageBinary {
			t.Errorf("unexpected binary message for empty response: %v", m)
		}
		var ttsMsg ServerTTSStartMessage
		if err := json.Unmarshal(m.payload, &ttsMsg); err == nil && ttsMsg.Type == MessageTypeTTS {
			if ttsMsg.State == TTSStateStart {
				t.Errorf("unexpected tts.start for empty response: %s", string(m.payload))
			}
			if ttsMsg.State == TTSStateStop {
				t.Errorf("unexpected tts.stop for empty response: %s", string(m.payload))
			}
		}
	}
}

// TestPacer_AbortWhileSpeaking_EmitsStopAndClearsQueue 验证播放中发生中断（Abort）：
// 1. 若已发送 tts.start，补发一次且仅一次 tts.stop；
// 2. 清空未发送的残留音频包，绝不继续下发；
// 3. 安全重置状态为 READY，代次递增。
func TestPacer_AbortWhileSpeaking_EmitsStopAndClearsQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mt := newManualTicker()
	wsConn := newRecordingWSConn()

	frame1 := generate24kSinePCMForSession(440.0, 15000.0)
	frame2 := generate24kSinePCMForSession(440.0, 15000.0)
	frame3 := generate24kSinePCMForSession(440.0, 15000.0)
	frame4 := generate24kSinePCMForSession(440.0, 15000.0)

	mockStream := newMockLLMStream([]string{"长回答测试文本。"}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := &mockTTSStream{
		pcmDataToReturn: [][]byte{frame1, frame2, frame3, frame4},
	}
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, writer := createTestSessionWithManualTicker(ctx, mt, wsConn, llmClient, ttsClient)
	defer writer.Close()

	go func() { _ = sess.Run() }()

	initSessionToListening(t, sess)

	sess.PostASRFinal(1, "长问题")

	// 等待首包发出（tts.start + 帧1），状态进入 SPEAKING
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.State() == StateSpeaking {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if sess.State() != StateSpeaking {
		t.Fatalf("expected session in SPEAKING, got %v", sess.State())
	}

	// 此时触发显式打断
	sess.PostAbort("用户主动打断")

	// 等待会话回到 READY
	waitState(t, sess, StateReady, 2*time.Second)

	// 继续触发多次 tick，验证不会再有旧音频或多余消息发出
	mt.Tick()
	mt.Tick()
	mt.Tick()
	time.Sleep(30 * time.Millisecond)

	msgs := wsConn.Messages()
	var startCount, stopCount, binaryCount int
	for _, m := range msgs {
		if m.typ == websocket.MessageBinary {
			binaryCount++
		}
		var ttsMsg ServerTTSStartMessage
		if err := json.Unmarshal(m.payload, &ttsMsg); err == nil && ttsMsg.Type == MessageTypeTTS {
			if ttsMsg.State == TTSStateStart {
				startCount++
			}
			if ttsMsg.State == TTSStateStop {
				stopCount++
			}
		}
	}

	if startCount != 1 {
		t.Errorf("expected exactly 1 tts.start, got %d", startCount)
	}
	if stopCount != 1 {
		t.Errorf("expected exactly 1 tts.stop on abort, got %d", stopCount)
	}
	if binaryCount >= 4 {
		t.Errorf("expected remaining frames cleared on abort, but received %d frames", binaryCount)
	}
	if sess.Generation() != 2 {
		t.Errorf("expected generation incremented to 2, got %d", sess.Generation())
	}
}

// TestPacer_AbortWhileProcessing_NoStopEmitted 验证未进入 SPEAKING 前（PROCESSING 阶段）发生中断：
// 不得发送 tts.stop，也不得发送 tts.start。
func TestPacer_AbortWhileProcessing_NoStopEmitted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mt := newManualTicker()
	wsConn := newRecordingWSConn()

	sess, writer := createTestSessionWithManualTicker(ctx, mt, wsConn, nil, nil)
	defer writer.Close()

	go func() { _ = sess.Run() }()

	initSessionToListening(t, sess)

	// 投递 ASRFinal 进入 PROCESSING (此时无 LLM/TTS 生成音频)
	sess.PostASRFinal(1, "打断测试")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 在 PROCESSING 阶段触发 abort
	sess.PostAbort("ASR 后立即打断")

	waitState(t, sess, StateReady, 2*time.Second)

	msgs := wsConn.Messages()
	for _, m := range msgs {
		var ttsMsg ServerTTSStartMessage
		if err := json.Unmarshal(m.payload, &ttsMsg); err == nil && ttsMsg.Type == MessageTypeTTS {
			if ttsMsg.State == TTSStateStart || ttsMsg.State == TTSStateStop {
				t.Errorf("unexpected TTS message (%s) for processing abort: %s", ttsMsg.State, string(m.payload))
			}
		}
	}
}

// TestPacer_Backpressure_DownlinkQueueFull 验证下行队列积压满时触发背压错误并安全关闭连接。
func TestPacer_Backpressure_DownlinkQueueFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pacer := NewDownlinkPacer(ctx, nil, 1, 2, nil) // 容量仅为 2
	defer pacer.Stop()

	pkt := []byte{0xF8, 0xFF, 0xFE}

	// 写入前 2 包成功
	if err := pacer.Enqueue(pkt); err != nil {
		t.Fatalf("enqueue 1 failed: %v", err)
	}
	if err := pacer.Enqueue(pkt); err != nil {
		t.Fatalf("enqueue 2 failed: %v", err)
	}

	// 第 3 包应触发满载背压拒绝
	err := pacer.Enqueue(pkt)
	if !errors.Is(err, ErrDownlinkQueueFull) {
		t.Fatalf("expected ErrDownlinkQueueFull on full queue, got: %v", err)
	}
}

// TestPacer_WriteFailureDuringPlayback 验证播放过程中底层 WebSocket 写入失败时的错误处理与关闭保护。
func TestPacer_WriteFailureDuringPlayback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mt := newManualTicker()
	wsConn := newRecordingWSConn()
	wsConn.writeErr = errors.New("simulated write pipe broken")
	wsConn.errOnNth = 3 // 在写入第 3 条消息时报错

	frame1 := generate24kSinePCMForSession(440.0, 15000.0)
	frame2 := generate24kSinePCMForSession(440.0, 15000.0)

	mockStream := newMockLLMStream([]string{"测试写失败。"}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	mockTTS := &mockTTSStream{
		pcmDataToReturn: [][]byte{frame1, frame2},
	}
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, writer := createTestSessionWithManualTicker(ctx, mt, wsConn, llmClient, ttsClient)
	defer writer.Close()

	go func() { _ = sess.Run() }()

	initSessionToListening(t, sess)

	sess.PostASRFinal(1, "触发写失败")

	// 等待会话检测到写失败并关闭
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.State() == StateClosed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if sess.State() != StateClosed {
		t.Errorf("expected session closed on fatal write error, got %v", sess.State())
	}
}

// TestPacer_RealTickerTiming 验证默认实时 Ticker（60 ms 间隔）的逐包节奏与耗时范围。
func TestPacer_RealTickerTiming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsConn := newRecordingWSConn()
	writer := NewWriter(ctx, wsConn, 100, slog.Default())
	defer writer.Close()

	pacer := NewDownlinkPacer(ctx, nil, 1, 50, nil) // 使用默认实时 Ticker (60ms)
	pacer.writer = writer

	go pacer.Run()

	pkt1 := []byte{1, 2, 3}
	pkt2 := []byte{4, 5, 6}
	pkt3 := []byte{7, 8, 9}

	start := time.Now()

	_ = pacer.Enqueue(pkt1)
	_ = pacer.Enqueue(pkt2)
	_ = pacer.Enqueue(pkt3)
	pacer.FinishInput()

	select {
	case <-pacer.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pacer to finish")
	}

	elapsed := time.Since(start)

	// 3 帧音频按 60 ms 节奏下发：首帧立即发，等待 tick1 (60ms) -> 发帧2，等待 tick2 (60ms) -> 发帧3，等待 tick3 (60ms) -> 完成
	// 总耗时大约在 180 ms 左右 (>= 120 ms 且 <= 500 ms)
	if elapsed < 120*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Errorf("expected 3 frames downlink duration ~180ms, got %v", elapsed)
	}

	msgs := wsConn.Messages()
	var binaryCount int
	for _, m := range msgs {
		if m.typ == websocket.MessageBinary {
			binaryCount++
		}
	}
	if binaryCount != 3 {
		t.Fatalf("expected exactly 3 binary frames delivered, got %d", binaryCount)
	}
}

// TestPacer_ConcurrentRaceSafety 验证多协程高并发操作下的竞态安全性与幂等性。
func TestPacer_ConcurrentRaceSafety(t *testing.T) {
	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mt := newManualTicker()
			pacer := NewDownlinkPacer(ctx, nil, uint64(idx+1), 50, func(d time.Duration) Ticker {
				return mt
			})

			go pacer.Run()

			// 并发入队、Tick 与中断
			for j := 0; j < 10; j++ {
				_ = pacer.Enqueue([]byte{byte(j), 1, 2})
				mt.Tick()
			}

			if idx%2 == 0 {
				pacer.FinishInput()
			} else {
				pacer.Stop()
			}

			select {
			case <-pacer.Done():
			case <-time.After(1 * time.Second):
			}
		}(i)
	}

	wg.Wait()
}
