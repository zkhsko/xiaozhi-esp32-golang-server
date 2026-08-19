package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
)

// logRecord 捕获单条日志记录。
type logRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// logCaptureHandler 自定义 slog.Handler 用于捕获并校验日志数量与内容。
type logCaptureHandler struct {
	mu      sync.Mutex
	records []logRecord
}

func newLogCaptureHandler() *logCaptureHandler {
	return &logCaptureHandler{}
}

func (h *logCaptureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *logCaptureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.records = append(h.records, logRecord{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   attrs,
	})
	return nil
}

func (h *logCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *logCaptureHandler) WithGroup(name string) slog.Handler {
	return h
}

func (h *logCaptureHandler) Records() []logRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := make([]logRecord, len(h.records))
	copy(copied, h.records)
	return copied
}

func (h *logCaptureHandler) CountLevel(level slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, r := range h.records {
		if r.Level >= level {
			count++
		}
	}
	return count
}

func (h *logCaptureHandler) Messages(level slog.Level) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var msgs []string
	for _, r := range h.records {
		if r.Level >= level {
			msgs = append(msgs, r.Message)
		}
	}
	return msgs
}

// faultWSConn 模拟用于故障注入与状态断言的底层 WebSocket 连接。
type faultWSConn struct {
	mu          sync.Mutex
	messages    []faultWSMessage
	writeErr    error
	closed      bool
	closeCode   websocket.StatusCode
	closeReason string
}

type faultWSMessage struct {
	typ     websocket.MessageType
	payload []byte
}

func (f *faultWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("connection closed")
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	copied := make([]byte, len(p))
	copy(copied, p)
	f.messages = append(f.messages, faultWSMessage{
		typ:     typ,
		payload: copied,
	})
	return nil
}

func (f *faultWSConn) Close(code websocket.StatusCode, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.closeCode = code
	f.closeReason = reason
	return nil
}

func (f *faultWSConn) Messages() []faultWSMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]faultWSMessage, len(f.messages))
	copy(res, f.messages)
	return res
}

func (f *faultWSConn) IsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *faultWSConn) CloseCode() websocket.StatusCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCode
}

func (f *faultWSConn) HasTextMessageWithType(t string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.messages {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == t {
					return true
				}
			}
		}
	}
	return false
}

func (f *faultWSConn) HasTTSStop() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.messages {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "tts" && parsed["state"] == "stop" {
					return true
				}
			}
		}
	}
	return false
}

func (f *faultWSConn) HasTTSStart() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.messages {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "tts" && parsed["state"] == "start" {
					return true
				}
			}
		}
	}
	return false
}

func (f *faultWSConn) TTSStopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, m := range f.messages {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "tts" && parsed["state"] == "stop" {
					count++
				}
			}
		}
	}
	return count
}

// faultASRStream 模拟用于故障注入的 ASR 流。
type faultASRStream struct {
	mu         sync.Mutex
	resultChan chan string
	errChan    chan error
	closed     atomic.Bool
}

func newFaultASRStream() *faultASRStream {
	return &faultASRStream{
		resultChan: make(chan string, 1),
		errChan:    make(chan error, 1),
	}
}

func (s *faultASRStream) WritePCM(ctx context.Context, data []byte) error {
	if s.closed.Load() {
		return errors.New("stream closed")
	}
	return ctx.Err()
}

func (s *faultASRStream) Finish(ctx context.Context) error {
	if s.closed.Load() {
		return errors.New("stream closed")
	}
	return ctx.Err()
}

func (s *faultASRStream) Result(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-s.errChan:
		return "", err
	case res := <-s.resultChan:
		return res, nil
	}
}

func (s *faultASRStream) Close() error {
	s.closed.Store(true)
	return nil
}

// faultASRClient 模拟 ASR 客户端。
type faultASRClient struct {
	mu         sync.Mutex
	createErr  error
	lastStream *faultASRStream
}

func newFaultASRClient() *faultASRClient {
	return &faultASRClient{}
}

func (c *faultASRClient) CreateStream(ctx context.Context) (ai.ASRStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.createErr != nil {
		return nil, c.createErr
	}
	stream := newFaultASRStream()
	c.lastStream = stream
	return stream, nil
}

func (c *faultASRClient) LastStream() *faultASRStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStream
}

// waitASRStream 辅助函数：等待 ASR 流创建完成并安全返回。
func waitASRStream(t *testing.T, client *faultASRClient, timeout time.Duration) *faultASRStream {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := client.LastStream(); s != nil {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ASR stream to be created")
	return nil
}

// faultLLMStream 模拟 LLM 流。
type faultLLMStream struct {
	mu         sync.Mutex
	chunks     []string
	chunkIndex int
	recvErr    error
	closed     atomic.Bool
	pauseChan  chan struct{}
}

func (s *faultLLMStream) Recv() (string, error) {
	if s.closed.Load() {
		return "", io.EOF
	}
	if s.pauseChan != nil {
		select {
		case <-s.pauseChan:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recvErr != nil {
		return "", s.recvErr
	}
	if s.chunkIndex >= len(s.chunks) {
		return "", io.EOF
	}
	c := s.chunks[s.chunkIndex]
	s.chunkIndex++
	return c, nil
}

func (s *faultLLMStream) Close() error {
	s.closed.Store(true)
	return nil
}

// faultLLMClient 模拟 LLM 客户端。
type faultLLMClient struct {
	mu         sync.Mutex
	createErr  error
	chunks     []string
	recvErr    error
	pauseChan  chan struct{}
	lastStream *faultLLMStream
}

func newFaultLLMClient() *faultLLMClient {
	return &faultLLMClient{}
}

func (c *faultLLMClient) CreateStream(ctx context.Context, messages []ai.Message) (ai.LLMStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.createErr != nil {
		return nil, c.createErr
	}
	stream := &faultLLMStream{
		chunks:    c.chunks,
		recvErr:   c.recvErr,
		pauseChan: c.pauseChan,
	}
	c.lastStream = stream
	return stream, nil
}

func (c *faultLLMClient) LastStream() *faultLLMStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStream
}

// faultTTSStream 模拟 TTS 流。
type faultTTSStream struct {
	mu             sync.Mutex
	sendErr        error
	pcmChunks      [][]byte
	chunkIndex     int
	nextPCMErr     error
	closed         atomic.Bool
	sentSentences  []string
	finishReceived atomic.Bool
	pauseChan      chan struct{}
}

func (s *faultTTSStream) SendSentence(ctx context.Context, text string) error {
	if s.closed.Load() {
		return errors.New("stream closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sentSentences = append(s.sentSentences, text)
	return nil
}

func (s *faultTTSStream) Finish(ctx context.Context) error {
	if s.closed.Load() {
		return errors.New("stream closed")
	}
	s.finishReceived.Store(true)
	return nil
}

func (s *faultTTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	if s.closed.Load() {
		return nil, io.EOF
	}

	s.mu.Lock()
	if s.chunkIndex < len(s.pcmChunks) {
		chunk := s.pcmChunks[s.chunkIndex]
		s.chunkIndex++
		s.mu.Unlock()
		return chunk, nil
	}
	s.mu.Unlock()

	if s.pauseChan != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.pauseChan:
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextPCMErr != nil {
		return nil, s.nextPCMErr
	}
	return nil, io.EOF
}

func (s *faultTTSStream) Close() error {
	s.closed.Store(true)
	return nil
}

// faultTTSClient 模拟 TTS 客户端。
type faultTTSClient struct {
	mu             sync.Mutex
	createErr      error
	sendErr        error
	pcmChunks      [][]byte
	nextPCMErr     error
	pauseChan      chan struct{}
	lastStream     *faultTTSStream
	finishReceived atomic.Bool
}

func newFaultTTSClient() *faultTTSClient {
	return &faultTTSClient{}
}

func (c *faultTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.createErr != nil {
		return nil, c.createErr
	}
	stream := &faultTTSStream{
		sendErr:    c.sendErr,
		pcmChunks:  c.pcmChunks,
		nextPCMErr: c.nextPCMErr,
		pauseChan:  c.pauseChan,
	}
	c.lastStream = stream
	return stream, nil
}

func (c *faultTTSClient) LastStream() *faultTTSStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStream
}

// createFaultTestSession 创建测试使用的会话环境。
func createFaultTestSession(t *testing.T, asr ai.ASRClient, llm ai.LLMClient, tts ai.TTSClient, logHandler slog.Handler) (*Session, *faultWSConn, *logCaptureHandler) {
	t.Helper()
	var capHandler *logCaptureHandler
	if logHandler == nil {
		capHandler = newLogCaptureHandler()
		logHandler = capHandler
	}
	logger := slog.New(logHandler)

	conn := &faultWSConn{}
	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxConcurrentSessions: 10,
		},
		Session: config.SessionConfig{
			HelloTimeout:              10 * time.Second,
			MaxListeningDuration:      30 * time.Second,
			MaxHistoryTurns:           6,
			MaxOpusPacketBytes:        1024,
			ASRPCMQueueCapacity:       100,
			TTSPCMQueueCapacity:       100,
			DownlinkOpusQueueCapacity: 100,
		},
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMFirstTokenTimeout: 15 * time.Second,
				LLMOverallTimeout:    60 * time.Second,
				ASRConnectTimeout:    10 * time.Second,
				TTSConnectTimeout:    10 * time.Second,
			},
		},
	}

	info := &ClientHeaderInfo{
		DeviceID:     "test-dev-01",
		ClientID:     "test-client-01",
		SerialNumber: "SN123456",
	}

	w := NewWriter(context.Background(), conn, 100, logger)
	sess := NewSessionWithWriter(context.Background(), nil, w, info, cfg, asr, llm, tts, logger)
	sess.SetTickerFactory(func(d time.Duration) Ticker {
		return &testImmediateTicker{c: make(chan time.Time, 100)}
	})

	go func() {
		_ = sess.Run()
	}()

	return sess, conn, capHandler
}

// testImmediateTicker 瞬时响应的测试定时器。
type testImmediateTicker struct {
	c chan time.Time
}

func (t *testImmediateTicker) C() <-chan time.Time {
	return t.c
}

func (t *testImmediateTicker) Stop() {}

// completeHello 辅助函数：完成 hello 握手并将会话置为 READY 状态。
func completeHello(t *testing.T, sess *Session, conn *faultWSConn) {
	t.Helper()
	helloJSON := `{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`
	sess.postEvent(event{
		kind:     eventKindClientHello,
		rawBytes: []byte(helloJSON),
		isBinary: false,
	})
	waitState(t, sess, StateReady, 2*time.Second)
}

// -----------------------------------------------------------------------------------------
// 测试 1：ASR 建连失败 -> 未发 tts.stop，会话进入 CLOSED，资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ASRConnectFailure(t *testing.T) {
	asrClient := newFaultASRClient()
	asrClient.createErr = errors.New("asr connect timeout or connection refused")

	sess, conn, logCap := createFaultTestSession(t, asrClient, nil, nil, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	// 触发收音开始
	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})

	// 状态应在建连失败后迅速转入 CLOSED
	waitState(t, sess, StateClosed, 2*time.Second)

	// 断言：由于在 tts.start 发送前失败，严禁发送 tts.stop
	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop sent on ASR connect failure, but found tts.stop")
	}
	if conn.HasTTSStart() {
		t.Fatalf("expected no tts.start sent on ASR connect failure")
	}

	// 断言：会话进入 CLOSED
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	// 断言：日志仅记录 1 次 ERROR/WARN 级别
	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 2：ASR 识别流失败 -> 未发 tts.stop，会话进入 CLOSED，资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ASRRecognitionFailure(t *testing.T) {
	asrClient := newFaultASRClient()
	sess, conn, logCap := createFaultTestSession(t, asrClient, nil, nil, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})

	waitState(t, sess, StateListening, 2*time.Second)

	// 模拟 ASR 识别流报错
	asrStream := waitASRStream(t, asrClient, 2*time.Second)
	asrStream.errChan <- errors.New("asr stream disconnected by server")

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop sent on ASR recognition failure")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	// 验证 ASR 流被清理
	if !asrStream.closed.Load() {
		t.Fatalf("expected ASR stream to be closed")
	}

	// 验证单错误单日志
	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 3：LLM 建连失败 -> 未发 tts.stop，会话进入 CLOSED，资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_LLMConnectFailure(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.createErr = errors.New("llm connect error: 500 maas internal error")
	ttsClient := newFaultTTSClient()

	sess, conn, logCap := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	// 模拟 ASR 成功返回文本，进入 PROCESSING
	asrStream.resultChan <- "你好小智"

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop sent on LLM connect failure")
	}
	if conn.HasTTSStart() {
		t.Fatalf("expected no tts.start sent on LLM connect failure")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	// 验证历史未被污染
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to be empty, got %d", len(sess.History()))
	}

	// 验证单错误单日志
	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 4：LLM 首 token 超时 (15s) -> 未发 tts.stop，会话进入 CLOSED，资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_LLMFirstTokenTimeout(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.recvErr = errors.New("llm first token timeout (15s)")
	ttsClient := newFaultTTSClient()

	sess, conn, logCap := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	// ASR 识别成功
	asrStream.resultChan <- "今天天气怎么样"

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop sent on LLM first token timeout")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to be empty")
	}

	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 5：LLM 整体超时 (60s) / 流中报错 -> 未发 tts.stop，会话进入 CLOSED，资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_LLMOverallTimeout(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.chunks = []string{"这是前半句"}
	llmClient.recvErr = errors.New("llm overall timeout (60s)")
	ttsClient := newFaultTTSClient()

	sess, conn, logCap := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	asrStream.resultChan <- "讲一个长故事"

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop sent on LLM overall timeout before tts.start")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to be empty")
	}

	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 6：TTS 在 tts.start 发送前建连失败 -> 未发 tts.stop，直接关闭连接
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_TTSCreateFailureBeforeStart(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.chunks = []string{"你好！"}
	ttsClient := newFaultTTSClient()
	ttsClient.createErr = errors.New("tts websocket handshake timeout (10s)")

	sess, conn, logCap := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	asrStream.resultChan <- "你好"

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop sent when TTS fails before tts.start")
	}
	if conn.HasTTSStart() {
		t.Fatalf("expected no tts.start sent")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to be empty")
	}

	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 7：TTS 在已发送 tts.start 后合成报错 -> 尽力补发一次且仅一次 tts.stop 并关闭连接
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_TTSSynthesisErrorAfterStart(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.chunks = []string{"第一句完成。", "第二句继续。"}
	ttsClient := newFaultTTSClient()

	// 提供第 1 句的有效 24 kHz PCM 帧（2880 字节），触发首包 Opus 编码与 tts.start
	pcmFrame := make([]byte, 2880)
	ttsClient.pcmChunks = [][]byte{pcmFrame}
	ttsClient.nextPCMErr = errors.New("tts synthesis failed midway")
	pauseChan := make(chan struct{})
	ttsClient.pauseChan = pauseChan

	sess, conn, logCap := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	asrStream.resultChan <- "播放一段长语音"

	// 等待会话先确定性地进入 SPEAKING 状态（已发送 tts.start）
	waitState(t, sess, StateSpeaking, 2*time.Second)

	if !conn.HasTTSStart() {
		t.Fatalf("expected tts.start to have been sent before injecting next PCM error")
	}

	// 释放暂停，让 TTS 触发合成错误
	close(pauseChan)

	// 等待会话因 TTS 错误转入 CLOSED
	waitState(t, sess, StateClosed, 2*time.Second)

	// 断言：由于此前已发送 tts.start，必须且只能补发一次 tts.stop
	if !conn.HasTTSStop() {
		t.Fatalf("expected tts.stop to be sent as best-effort cleanup")
	}
	if conn.TTSStopCount() != 1 {
		t.Fatalf("expected exactly 1 tts.stop, got %d", conn.TTSStopCount())
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	// 断言：异常轮次未进入会话历史
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to be empty for failed turn, got %d", len(sess.History()))
	}

	// 断言：单错误单日志
	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 8：下行队列满载背压保护（已发 tts.start 后） -> 补发一次 tts.stop 并关闭连接
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_DownlinkBackpressureAfterStart(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	ttsClient := newFaultTTSClient()

	sess, conn, logCap := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	// 直接推进到 SPEAKING 状态并建立容量为 1 的 Pacer
	pacer := NewDownlinkPacer(context.Background(), sess, 1, 1, nil)
	sess.mu.Lock()
	sess.state = StateSpeaking
	sess.generation = 1
	sess.pacer = pacer
	sess.mu.Unlock()

	// 不启动 pacer.Run() 使得消费挂起，第 1 包占满队列，第 2 包触发背压拒绝并投递 PostError
	_ = pacer.Enqueue([]byte{1, 2, 3})
	err := pacer.Enqueue([]byte{4, 5, 6})
	if !errors.Is(err, ErrDownlinkQueueFull) {
		t.Fatalf("expected ErrDownlinkQueueFull, got %v", err)
	}

	waitState(t, sess, StateClosed, 2*time.Second)

	if !conn.HasTTSStop() {
		t.Fatalf("expected tts.stop to be sent on backpressure in speaking state")
	}
	if conn.TTSStopCount() != 1 {
		t.Fatalf("expected exactly 1 tts.stop, got %d", conn.TTSStopCount())
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn/error log, got %d: %v", errCount, logCap.Messages(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 9：设备在 CONNECTED 阶段主动断开连接 -> 立即 CLOSED，无写操作，资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ClientDisconnectInConnectedState(t *testing.T) {
	sess, conn, _ := createFaultTestSession(t, nil, nil, nil, nil)
	defer sess.Close()

	if sess.State() != StateConnected {
		t.Fatalf("expected initial state CONNECTED, got %s", sess.State())
	}

	// 注入客户端断开事件
	sess.PostClose(websocket.StatusNormalClosure, "client disconnected")

	waitState(t, sess, StateClosed, 2*time.Second)

	// 断言：未向连接写入任何数据
	if len(conn.Messages()) != 0 {
		t.Fatalf("expected no messages written to disconnected client, got %d", len(conn.Messages()))
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}
}

// -----------------------------------------------------------------------------------------
// 测试 10：设备在 LISTENING 阶段主动断开连接 -> 立即 CLOSED，无写操作，ASR 资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ClientDisconnectInListeningState(t *testing.T) {
	asrClient := newFaultASRClient()
	sess, conn, _ := createFaultTestSession(t, asrClient, nil, nil, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)

	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	// 记录已发消息数量
	msgCountBeforeDisconnect := len(conn.Messages())

	// 客户端主动断开
	sess.PostClose(websocket.StatusNormalClosure, "client disconnected")

	waitState(t, sess, StateClosed, 2*time.Second)

	// 断言：断开后绝无新的 WebSocket 写入（包括无 tts.stop）
	if len(conn.Messages()) != msgCountBeforeDisconnect {
		t.Fatalf("expected no new messages written after disconnect, before: %d, after: %d",
			msgCountBeforeDisconnect, len(conn.Messages()))
	}

	// 断言：ASR 流被安全销毁
	if !asrStream.closed.Load() {
		t.Fatalf("expected ASR stream to be closed")
	}
}

// -----------------------------------------------------------------------------------------
// 测试 11：设备在 PROCESSING 阶段主动断开连接 -> 立即 CLOSED，无写操作，LLM/TTS 资源释放
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ClientDisconnectInProcessingState(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.pauseChan = make(chan struct{}) // 模拟 LLM 正在生成
	ttsClient := newFaultTTSClient()

	sess, conn, _ := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	asrStream.resultChan <- "正在处理中的问题"
	waitState(t, sess, StateProcessing, 2*time.Second)

	msgCountBeforeDisconnect := len(conn.Messages())

	// 客户端主动断开
	sess.PostClose(websocket.StatusNormalClosure, "client disconnected")

	waitState(t, sess, StateClosed, 2*time.Second)

	// 断言：断开后绝无任何 WebSocket 写入（不发 tts.stop）
	if len(conn.Messages()) != msgCountBeforeDisconnect {
		t.Fatalf("expected no new messages written after disconnect, before: %d, after: %d",
			msgCountBeforeDisconnect, len(conn.Messages()))
	}
	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop sent when client disconnected in PROCESSING")
	}

	// 验证历史未被污染
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to be empty")
	}
}

// -----------------------------------------------------------------------------------------
// 测试 12：设备在 SPEAKING 阶段主动断开连接 -> 立即 CLOSED，严禁再写已断开 WebSocket
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ClientDisconnectInSpeakingState(t *testing.T) {
	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.chunks = []string{"第一句回答。"}
	ttsClient := newFaultTTSClient()
	pcmFrame := make([]byte, 2880)
	ttsClient.pcmChunks = [][]byte{pcmFrame}
	ttsClient.pauseChan = make(chan struct{}) // 保持播报状态

	sess, conn, _ := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	asrStream := waitASRStream(t, asrClient, 2*time.Second)

	asrStream.resultChan <- "你好"
	waitState(t, sess, StateSpeaking, 2*time.Second)

	if !conn.HasTTSStart() {
		t.Fatalf("expected tts.start to have been sent")
	}

	msgCountBeforeDisconnect := len(conn.Messages())

	// 客户端主动断开连接
	sess.PostClose(websocket.StatusNormalClosure, "client disconnected")

	waitState(t, sess, StateClosed, 2*time.Second)

	// 断言：断开后严禁再向 WebSocket 写入任何消息（严禁补发 tts.stop 或任何二进制音频）
	if len(conn.Messages()) != msgCountBeforeDisconnect {
		t.Fatalf("expected no messages written after disconnect in SPEAKING, before: %d, after: %d",
			msgCountBeforeDisconnect, len(conn.Messages()))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 13：Hello 握手超时 (10s) -> 1008 关闭连接，未发 tts.stop，单日志记录
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_HelloTimeout(t *testing.T) {
	sess, conn, logCap := createFaultTestSession(t, nil, nil, nil, nil)
	defer sess.Close()

	// 注入 hello 超时事件
	sess.PostTimeout(0, "hello handshake timeout")

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop on hello timeout")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn log for hello timeout, got %d", errCount)
	}
}

// -----------------------------------------------------------------------------------------
// 测试 14：收音时长上限 (30s) 超时 -> 1008 关闭连接，未发 tts.stop，单日志记录
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ListeningDurationTimeout(t *testing.T) {
	asrClient := newFaultASRClient()
	sess, conn, logCap := createFaultTestSession(t, asrClient, nil, nil, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)

	// 注入单次收音超时事件
	sess.PostTimeout(sess.Generation(), "max listening duration exceeded")

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop on listening timeout")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn log for listening duration timeout, got %d", errCount)
	}
}

// -----------------------------------------------------------------------------------------
// 测试 15：通用超时兜底 -> 1008 关闭连接，未发 tts.stop，单日志记录
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_GenericTimeout(t *testing.T) {
	sess, conn, logCap := createFaultTestSession(t, nil, nil, nil, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	// 注入通用操作超时
	sess.PostTimeout(sess.Generation(), "custom operation timeout")

	waitState(t, sess, StateClosed, 2*time.Second)

	if conn.HasTTSStop() {
		t.Fatalf("expected no tts.stop on generic timeout")
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected session state to be CLOSED, got %s", sess.State())
	}

	errCount := logCap.CountLevel(slog.LevelWarn)
	if errCount != 1 {
		t.Fatalf("expected exactly 1 warn log for generic timeout, got %d", errCount)
	}
}

// -----------------------------------------------------------------------------------------
// 测试 16：迟到错误事件被安全丢弃 -> 不改变状态，不重复关闭
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_StaleErrorEventDiscarded(t *testing.T) {
	sess, conn, logCap := createFaultTestSession(t, nil, nil, nil, nil)
	defer sess.Close()

	completeHello(t, sess, conn)

	// 当前 generation 为 0，注入 generation 为 99 的迟到错误事件
	sess.PostError(99, errors.New("stale error from past generation"), true)

	// 会话状态应保持 READY，不被迟到错误关闭
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY on stale error, got %s", sess.State())
	}
	if sess.State() == StateClosed {
		t.Fatalf("expected connection not to be closed by stale error")
	}

	// 迟到错误仅产生 debug 日志，不产生 warn/error
	if logCap.CountLevel(slog.LevelWarn) != 0 {
		t.Fatalf("expected 0 warn logs for stale error, got %d", logCap.CountLevel(slog.LevelWarn))
	}
}

// -----------------------------------------------------------------------------------------
// 测试 17：并发故障、超时与断线竞态安全与 Goroutine 零泄漏
// -----------------------------------------------------------------------------------------
func TestFaultCleanup_ConcurrentFaultsAndDisconnectRace(t *testing.T) {
	initialGoroutines := runtime.NumGoroutine()

	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()

			asrClient := newFaultASRClient()
			llmClient := newFaultLLMClient()
			ttsClient := newFaultTTSClient()

			sess, conn, _ := createFaultTestSession(t, asrClient, llmClient, ttsClient, nil)
			completeHello(t, sess, conn)

			// 并发执行各类操作与故障注入
			var opWg sync.WaitGroup
			opWg.Add(4)

			go func() {
				defer opWg.Done()
				sess.PostClientText(&ClientMessage{
					Kind: KindListenStart,
					Mode: ListenModeAuto,
				})
			}()

			go func() {
				defer opWg.Done()
				sess.PostError(sess.Generation(), errors.New("concurrent injected error"), true)
			}()

			go func() {
				defer opWg.Done()
				sess.PostTimeout(sess.Generation(), "concurrent timeout")
			}()

			go func() {
				defer opWg.Done()
				sess.PostClose(websocket.StatusNormalClosure, "concurrent disconnect")
			}()

			opWg.Wait()
			sess.Close()

			// 等待退出
			select {
			case <-sess.Done():
			case <-time.After(3 * time.Second):
				t.Errorf("session %d failed to close in time", idx)
			}
		}(i)
	}

	wg.Wait()

	// 验证最终无 Goroutine 泄漏（允许少量测试 runtime 协程浮动）
	deadline := time.Now().Add(3 * time.Second)
	var finalGoroutines int
	for time.Now().Before(deadline) {
		runtime.GC()
		finalGoroutines = runtime.NumGoroutine()
		if finalGoroutines <= initialGoroutines+5 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if finalGoroutines > initialGoroutines+10 {
		t.Fatalf("potential goroutine leak: initial %d, final %d", initialGoroutines, finalGoroutines)
	}
}
