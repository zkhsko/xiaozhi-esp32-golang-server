package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// 会话管理相关的默认常量。
const (
	// DefaultMaxOpusPacketBytes 默认单 Opus 包最大字节数（1024 字节）。
	DefaultMaxOpusPacketBytes = 1024

	// DefaultMaxListeningDuration 默认单次最大收音时长（30 秒）。
	DefaultMaxListeningDuration = 30 * time.Second

	// DefaultASRResultTimeout 默认手动模式收音结束后等待 ASR 识别结果的最大超时（5 秒）。
	DefaultASRResultTimeout = 5 * time.Second

	// DefaultMaxHistoryTurns 默认最多保留历史轮数（6 轮）。
	DefaultMaxHistoryTurns = 6

	// DefaultEventChannelCapacity 默认会话监督事件通道容量。
	DefaultEventChannelCapacity = 100
)

// eventKind 定义进入监督流程的事件类型。
type eventKind int

const (
	eventKindClientHello eventKind = iota
	eventKindClientText
	eventKindClientAudio
	eventKindASRFinal
	eventKindTTSStarted
	eventKindTurnFinished
	eventKindAbort
	eventKindTimeout
	eventKindError
	eventKindClose
)

// event 封装投递给单一监督主循环的统一事件对象。
type event struct {
	kind          eventKind
	generation    uint64
	text          string
	userText      string
	assistantText string
	clientMsg     *ClientMessage
	audioData     []byte
	rawBytes      []byte
	isBinary      bool
	err           error
	fatal         bool
	closeCode     websocket.StatusCode
}

// Session 负责管理单个 WebSocket 连接的状态机、事件所有权、监听模式与回答代次。
// 所有状态变更由单一主监督循环串行处理，保证线程安全与代次隔离。
type Session struct {
	conn        *websocket.Conn
	clientInfo  *ClientHeaderInfo
	cfg         *config.Config
	asrClient   ai.ASRClient
	llmClient   ai.LLMClient
	ttsClient   ai.TTSClient
	logger      *slog.Logger
	diagLimiter *logger.RateLimiter

	writer *Writer
	events chan event

	decoder       *audio.Decoder
	onEncodedOpus func(gen uint64, packet []byte)
	asrStream     ai.ASRStream
	asrQueue      *audio.ASRAudioQueue
	ttsStream     ai.TTSStream
	pacer         *DownlinkPacer
	tickerFactory func(time.Duration) Ticker

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}

	mu                 sync.RWMutex
	state              State
	mode               string
	generation         uint64
	sessionID          string
	manualStopReceived bool
	history            []ai.Message

	turnCtx    context.Context
	turnCancel context.CancelFunc

	helloTimer     *time.Timer
	listeningTimer *time.Timer
	asrResultTimer *time.Timer

	mcpMu      sync.Mutex
	mcpSeq     atomic.Int64
	pendingMCP map[int64]chan *mcpResponse
	mcpTools   []ai.Tool
}

// Options 聚合构造单个 WebSocket 会话的依赖与上下文。
type Options struct {
	Conn          *websocket.Conn
	Writer        *Writer
	ClientInfo    *ClientHeaderInfo
	Config        *config.Config
	ASRClient     ai.ASRClient
	LLMClient     ai.LLMClient
	TTSClient     ai.TTSClient
	Logger        *slog.Logger
	TickerFactory func(time.Duration) Ticker
}

// NewSession 使用具名选项创建配置就绪的 WebSocket 会话对象。
func NewSession(ctx context.Context, opts Options) *Session {
	if ctx == nil {
		ctx = context.Background()
	}
	l := opts.Logger
	if l == nil {
		l = slog.Default()
	}

	w := opts.Writer
	if w == nil && opts.Conn != nil {
		queueCap := DefaultWriteQueueCapacity
		if opts.Config != nil && opts.Config.Session.DownlinkOpusQueueCapacity > 0 {
			queueCap = opts.Config.Session.DownlinkOpusQueueCapacity
		}
		w = NewWriter(ctx, opts.Conn, queueCap, l)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	eventCap := DefaultEventChannelCapacity
	if opts.Config != nil && opts.Config.Session.ASRPCMQueueCapacity > 0 {
		eventCap = opts.Config.Session.ASRPCMQueueCapacity
	}

	maxOpusBytes := DefaultMaxOpusPacketBytes
	if opts.Config != nil && opts.Config.Session.MaxOpusPacketBytes > 0 {
		maxOpusBytes = opts.Config.Session.MaxOpusPacketBytes
	}
	dec, err := audio.NewDecoder(maxOpusBytes)
	if err != nil {
		l.Error("failed to initialize session opus decoder", "error", err)
	}

	return &Session{
		conn:          opts.Conn,
		clientInfo:    opts.ClientInfo,
		cfg:           opts.Config,
		asrClient:     opts.ASRClient,
		llmClient:     opts.LLMClient,
		ttsClient:     opts.TTSClient,
		logger:        l,
		diagLimiter:   logger.NewDiagRateLimiter(),
		writer:        w,
		events:        make(chan event, eventCap),
		tickerFactory: opts.TickerFactory,
		ctx:           sessionCtx,
		cancel:        cancel,
		done:          make(chan struct{}),
		pendingMCP:    make(map[int64]chan *mcpResponse),
		state:         StateConnected,
		mode:          ListenModeAuto,
		decoder:       dec,
	}
}

// ASRClient 返回当前关联的 ASR 客户端。
func (s *Session) ASRClient() ai.ASRClient {
	return s.asrClient
}

// LLMClient 返回当前关联的 LLM 客户端。
func (s *Session) LLMClient() ai.LLMClient {
	return s.llmClient
}

// TTSClient 返回当前关联的 TTS 客户端。
func (s *Session) TTSClient() ai.TTSClient {
	return s.ttsClient
}

// TTSStream 返回当前轮次的 TTS 流。
func (s *Session) TTSStream() ai.TTSStream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ttsStream
}

// Pacer 返回当前轮次的下行节奏调度器。
func (s *Session) Pacer() *DownlinkPacer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pacer
}

// SetTickerFactory 设置下行节奏器使用的定时器工厂，供可控时钟单元测试使用。
func (s *Session) SetTickerFactory(factory func(time.Duration) Ticker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickerFactory = factory
}

// stopPacer 停止并清空当前轮次的下行节奏调度器。
func (s *Session) stopPacer() {
	s.mu.Lock()
	p := s.pacer
	s.pacer = nil
	s.mu.Unlock()

	if p != nil {
		p.Stop()
	}
}

// transitionToSpeaking 将指定代次的会话状态由 Processing 转换为 Speaking。
func (s *Session) transitionToSpeaking(gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.generation == gen && s.state == StateProcessing {
		s.state = StateSpeaking
		s.logger.Info("session entered speaking state",
			"session_id", s.sessionID,
			"generation", gen,
		)
	}
}

// State 返回当前会话的状态。
func (s *Session) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Generation 返回当前问答代次。
func (s *Session) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// Mode 返回当前收音监听模式（auto 或 manual）。
func (s *Session) Mode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// SessionID 返回协商成功的会话标识。
func (s *Session) SessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

// DeviceKey 返回用于全局唯一标识单设备连接的键（优先 SerialNumber，次选 DeviceID，再次选 ClientID）。
func (s *Session) DeviceKey() string {
	if s == nil || s.clientInfo == nil {
		return ""
	}
	if s.clientInfo.SerialNumber != "" {
		return s.clientInfo.SerialNumber
	}
	if s.clientInfo.DeviceID != "" {
		return s.clientInfo.DeviceID
	}
	return s.clientInfo.ClientID
}

// ProtocolVersion 返回握手协商的协议版本。
func (s *Session) ProtocolVersion() string {
	if s == nil || s.clientInfo == nil {
		return ""
	}
	return s.clientInfo.ProtocolVersion
}

// SerialNumber 返回会话绑定的设备序列号。
func (s *Session) SerialNumber() string {
	if s == nil || s.clientInfo == nil {
		return ""
	}
	return s.clientInfo.SerialNumber
}

// DeviceID 返回会话关联的设备辅助标识。
func (s *Session) DeviceID() string {
	if s == nil || s.clientInfo == nil {
		return ""
	}
	return s.clientInfo.DeviceID
}

// ClientInfo 返回会话关联的客户端头信息快照。
func (s *Session) ClientInfo() *ClientHeaderInfo {
	if s == nil {
		return nil
	}
	return s.clientInfo
}

// Writer 返回当前关联的串行写流程对象。
func (s *Session) Writer() *Writer {
	return s.writer
}

// Decoder 返回当前会话的 Opus 解码器。
func (s *Session) Decoder() *audio.Decoder {
	return s.decoder
}

// SetOnEncodedOpus 设置单个 Opus 包编码完成后的回调。
func (s *Session) SetOnEncodedOpus(cb func(gen uint64, packet []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEncodedOpus = cb
}

// ASRQueue 返回当前轮次的 ASR 音频队列。
func (s *Session) ASRQueue() *audio.ASRAudioQueue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.asrQueue
}

// TurnContext 返回当前轮次上下文（随 abort 或轮次结束取消）。
func (s *Session) TurnContext() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.turnCtx != nil {
		return s.turnCtx
	}
	return s.ctx
}

// Done 返回会话完全关闭后的信号通道。
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// PostClientText 投递客户端文本消息事件。
func (s *Session) PostClientText(msg *ClientMessage) bool {
	if msg == nil {
		return false
	}
	return s.postEvent(event{
		kind:      eventKindClientText,
		clientMsg: msg,
	})
}

// PostClientAudio 投递客户端 Opus 音频包事件。
func (s *Session) PostClientAudio(data []byte) bool {
	return s.postEvent(event{
		kind:      eventKindClientAudio,
		audioData: data,
		isBinary:  true,
	})
}

// PostASRFinal 投递指定代次的 ASR 最终识别文本事件。
func (s *Session) PostASRFinal(generation uint64, text string) bool {
	return s.postEvent(event{
		kind:       eventKindASRFinal,
		generation: generation,
		text:       text,
	})
}

// PostTTSStarted 投递指定代次的 TTS 首音频就绪/播报开始事件。
func (s *Session) PostTTSStarted(generation uint64) bool {
	return s.postEvent(event{
		kind:       eventKindTTSStarted,
		generation: generation,
	})
}

// PostTurnFinished 投递指定代次的问答轮次结束事件。
// 可选传入本轮正常完成的用户输入文本与完整助手回复。
func (s *Session) PostTurnFinished(generation uint64, turnTexts ...string) bool {
	var userText, assistantText string
	if len(turnTexts) > 0 {
		userText = turnTexts[0]
	}
	if len(turnTexts) > 1 {
		assistantText = turnTexts[1]
	}
	return s.postEvent(event{
		kind:          eventKindTurnFinished,
		generation:    generation,
		userText:      userText,
		assistantText: assistantText,
	})
}

// PostAbort 投递显式中断请求事件。
func (s *Session) PostAbort(reason string) bool {
	return s.postEvent(event{
		kind: eventKindAbort,
		text: reason,
	})
}

// PostError 投递指定代次的错误事件。
func (s *Session) PostError(generation uint64, err error, fatal bool) bool {
	return s.postEvent(event{
		kind:       eventKindError,
		generation: generation,
		err:        err,
		fatal:      fatal,
	})
}

// PostTimeout 投递指定代次的超时事件。
func (s *Session) PostTimeout(generation uint64, reason string) bool {
	return s.postEvent(event{
		kind:       eventKindTimeout,
		generation: generation,
		text:       reason,
	})
}

// PostClose 投递连接关闭事件。
func (s *Session) PostClose(code websocket.StatusCode, reason string) bool {
	return s.postEvent(event{
		kind:      eventKindClose,
		closeCode: code,
		text:      reason,
	})
}

// Close 主动关闭会话。
func (s *Session) Close() {
	s.closeWithReason(websocket.StatusNormalClosure, "session closed")
}

// postEvent 向事件通道投递事件，若会话已取消则返回 false。
func (s *Session) postEvent(ev event) bool {
	select {
	case <-s.ctx.Done():
		return false
	case s.events <- ev:
		return true
	}
}

// Run 启动会话监督流程与消息读取循环，阻塞直至连接断开或会话终止。
func (s *Session) Run() error {
	defer func() {
		s.closeWithReason(websocket.StatusNormalClosure, "session finished")
		close(s.done)
	}()

	// 启动 hello 超时定时器
	s.startHelloTimer()

	// 若存在底层连接，启动读取循环 goroutine
	if s.conn != nil {
		maxWSTextBytes := int64(DefaultMaxWSTextMessageBytes)
		if s.cfg != nil && s.cfg.Session.MaxWSTextMessageBytes > 0 {
			maxWSTextBytes = s.cfg.Session.MaxWSTextMessageBytes
		}
		s.conn.SetReadLimit(maxWSTextBytes)
		go s.readLoop()
	}

	// 主事件监督循环
	return s.supervisorLoop()
}

// supervisorLoop 单独主事件循环，按序处理所有事件并驱动状态转换。
func (s *Session) supervisorLoop() error {
	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case ev, ok := <-s.events:
			if !ok {
				return nil
			}
			s.handleEvent(ev)
			if s.State() == StateClosed {
				return nil
			}
		}
	}
}

// handleEvent 分发处理各类事件。
func (s *Session) handleEvent(ev event) {
	if s.State() == StateClosed {
		return
	}

	switch ev.kind {
	case eventKindClientHello:
		s.handleHelloEvent(ev)
	case eventKindClientText:
		s.handleClientTextEvent(ev)
	case eventKindClientAudio:
		s.handleClientAudioEvent(ev)
	case eventKindASRFinal:
		s.handleASRFinalEvent(ev)
	case eventKindTTSStarted:
		s.handleTTSStartedEvent(ev)
	case eventKindTurnFinished:
		s.handleTurnFinishedEvent(ev)
	case eventKindAbort:
		s.handleAbortEvent(ev.text)
	case eventKindTimeout:
		s.handleTimeoutEvent(ev)
	case eventKindError:
		s.handleErrorEvent(ev)
	case eventKindClose:
		s.handleCloseEvent(ev)
	}
}

// handleHelloEvent 处理握手阶段首包 hello 逻辑。
func (s *Session) handleHelloEvent(ev event) {
	if s.State() != StateConnected {
		s.logger.Warn("duplicate hello received after handshake",
			"session_id", s.SessionID(),
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusPolicyViolation, ErrDuplicateHello.Error())
		return
	}

	if ev.isBinary {
		s.logger.Warn("first message is not text hello",
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusUnsupportedData, "first message must be text hello")
		return
	}

	var clientHello ClientHelloMessage
	if err := json.Unmarshal(ev.rawBytes, &clientHello); err != nil {
		s.logger.Warn("invalid json in hello message",
			"error", err,
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusPolicyViolation, "invalid json in hello message")
		return
	}

	if err := ValidateClientHello(&clientHello); err != nil {
		s.logger.Warn("invalid hello message fields",
			"error", err,
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusPolicyViolation, err.Error())
		return
	}

	s.stopHelloTimer()

	sessionID, err := GenerateSessionID()
	if err != nil {
		s.logger.Error("failed to generate session id",
			"error", err,
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusInternalError, "internal server error")
		return
	}

	respBytes, err := EncodeServerHelloMessage(sessionID)
	if err != nil {
		s.logger.Error("failed to encode server hello",
			"error", err,
			"session_id", sessionID,
		)
		s.closeWithReason(websocket.StatusInternalError, "internal server error")
		return
	}

	if err := s.sendTextMessage(respBytes); err != nil {
		s.logger.Warn("failed to write server hello",
			"error", err,
			"session_id", sessionID,
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusInternalError, "failed to write server hello")
		return
	}

	s.mu.Lock()
	s.sessionID = sessionID
	s.state = StateReady
	s.mu.Unlock()

	if clientHello.Features != nil && clientHello.Features.MCP {
		go s.discoverMCPTools(s.ctx)
	}

	s.logger.Info("websocket hello handshake succeeded",
		"session_id", sessionID,
		"device_id", s.truncatedDeviceID(),
		"client_id", s.truncatedClientID(),
		"serial_number", s.truncatedSerialNumber(),
	)
}

// handleClientTextEvent 处理客户端文本消息事件。
func (s *Session) handleClientTextEvent(ev event) {
	if ev.clientMsg == nil {
		return
	}

	currentState := s.State()

	switch ev.clientMsg.Kind {
	case KindHello:
		s.logger.Warn("duplicate hello received after handshake",
			"session_id", s.SessionID(),
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusPolicyViolation, ErrDuplicateHello.Error())

	case KindListenStart:
		switch currentState {
		case StateReady:
			mode := ev.clientMsg.Mode
			if mode == "" {
				mode = ListenModeAuto
			}
			s.mu.Lock()
			s.state = StateListening
			s.mode = mode
			s.manualStopReceived = false
			s.generation++
			gen := s.generation
			s.turnCtx, s.turnCancel = context.WithCancel(s.ctx)
			s.mu.Unlock()

			s.startListeningTimer(gen)
			s.startASRStream(gen, mode)
			s.logger.Info("session entered listening state",
				"session_id", s.SessionID(),
				"generation", gen,
				"mode", mode,
			)

		case StateListening:
			s.logDiag("duplicate listen.start ignored in listening state",
				"generation", s.Generation(),
			)

		default:
			s.logDiag("listen.start ignored in active state",
				"state", currentState.String(),
				"generation", s.Generation(),
			)
		}

	case KindListenStop:
		switch currentState {
		case StateListening:
			if s.Mode() == ListenModeAuto {
				s.logDiag("listen.stop ignored in auto mode",
					"generation", s.Generation(),
				)
			} else {
				s.mu.Lock()
				if s.manualStopReceived {
					s.mu.Unlock()
					s.logDiag("duplicate listen.stop ignored in manual mode",
						"generation", s.Generation(),
					)
					return
				}
				s.manualStopReceived = true
				gen := s.generation
				turnCtx := s.turnCtx
				stream := s.asrStream
				queue := s.asrQueue
				s.mu.Unlock()

				s.logger.Info("manual listen.stop received",
					"session_id", s.SessionID(),
					"generation", gen,
				)
				s.stopListeningTimer()
				s.startASRResultTimer(gen)
				s.finishASR()
				if stream != nil {
					go s.readASRResultManual(turnCtx, stream, queue, gen)
				}
			}
		default:
			s.logDiag("listen.stop ignored in non-listening state",
				"state", currentState.String(),
			)
		}

	case KindListenDetect:
		s.logDiag("listen.detect received",
			"text", logger.TruncateString(ev.clientMsg.DetectText),
		)

	case KindAbort:
		s.handleAbortEvent(ev.clientMsg.AbortReason)

	case KindMCP:
		s.handleIncomingMCP(ev.clientMsg)

	case KindUnknownExtension:
		s.logDiag("unknown extension message received",
			"raw_type", logger.TruncateString(ev.clientMsg.RawType),
		)
	}
}

// handleClientAudioEvent 处理客户端 Opus 音频包事件。
func (s *Session) handleClientAudioEvent(ev event) {
	currentState := s.State()

	if currentState == StateConnected {
		s.logger.Warn("binary audio message received before hello",
			"device_id", s.truncatedDeviceID(),
		)
		s.closeWithReason(websocket.StatusUnsupportedData, "binary message not allowed before hello")
		return
	}

	maxOpusBytes := DefaultMaxOpusPacketBytes
	if s.cfg != nil && s.cfg.Session.MaxOpusPacketBytes > 0 {
		maxOpusBytes = s.cfg.Session.MaxOpusPacketBytes
	}

	packetLen := len(ev.audioData)
	if packetLen == 0 || packetLen > maxOpusBytes {
		s.logger.Warn("invalid opus packet size",
			"size", packetLen,
			"max", maxOpusBytes,
			"session_id", s.SessionID(),
		)
		s.closeWithReason(websocket.StatusPolicyViolation, "invalid opus packet size")
		return
	}

	switch currentState {
	case StateReady:
		// READY 状态收到客户端 Opus 音频直接丢弃（如唤醒词残留音频），不触发 ASR，不缓存
		return
	case StateListening:
		s.mu.RLock()
		manualStopped := (s.mode == ListenModeManual && s.manualStopReceived)
		s.mu.RUnlock()

		if manualStopped {
			// manual 模式已收到 listen.stop，后续到达的残留音频直接丢弃
			return
		}

		if s.decoder == nil {
			s.logger.Error("opus decoder not initialized",
				"session_id", s.SessionID(),
			)
			s.closeWithReason(websocket.StatusInternalError, "decoder not initialized")
			return
		}

		pcmBytes, err := s.decoder.Decode(ev.audioData)
		if err != nil {
			s.logger.Warn("failed to decode uplink opus packet",
				"error", err,
				"session_id", s.SessionID(),
				"generation", s.Generation(),
			)
			s.closeWithReason(websocket.StatusPolicyViolation, "opus decode error")
			return
		}

		s.mu.RLock()
		queue := s.asrQueue
		s.mu.RUnlock()

		if queue != nil {
			if err := queue.Push(pcmBytes); err != nil {
				s.logger.Warn("asr audio queue push failed",
					"error", err,
					"session_id", s.SessionID(),
					"generation", s.Generation(),
				)
				s.closeWithReason(websocket.StatusPolicyViolation, "asr queue full or unavailable")
				return
			}
		}

	case StateProcessing:
		// PROCESSING 状态下首个 tts.start 前残留音频直接丢弃
		return
	case StateSpeaking:
		// SPEAKING 状态下播报音频时直接丢弃上行音频（非全双工）
		return
	}
}

// handleASRFinalEvent 处理 ASR 最终识别文本事件。
func (s *Session) handleASRFinalEvent(ev event) {
	currentGen := s.Generation()
	if ev.generation != currentGen {
		s.logger.Debug("stale asr final event discarded",
			"event_gen", ev.generation,
			"current_gen", currentGen,
		)
		return
	}

	if s.State() != StateListening {
		s.logger.Debug("asr final event ignored in non-listening state",
			"state", s.State().String(),
			"generation", ev.generation,
		)
		return
	}

	s.stopListeningTimer()
	s.stopASRResultTimer()
	s.stopASR()

	if ev.text == "" {
		s.mu.Lock()
		if s.turnCancel != nil {
			s.turnCancel()
			s.turnCancel = nil
		}
		s.state = StateReady
		s.mu.Unlock()

		s.logger.Info("empty asr result, session returned to ready state",
			"session_id", s.SessionID(),
			"generation", ev.generation,
		)
		return
	}

	sttBytes, err := EncodeSTTMessage(s.SessionID(), ev.text)
	if err != nil {
		s.logger.Error("failed to encode stt message",
			"error", err,
			"session_id", s.SessionID(),
			"generation", ev.generation,
		)
	} else {
		if err := s.sendTextMessage(sttBytes); err != nil {
			s.logger.Warn("failed to send stt message",
				"error", err,
				"session_id", s.SessionID(),
				"generation", ev.generation,
			)
		}
	}

	s.mu.Lock()
	s.state = StateProcessing
	turnCtx := s.turnCtx
	s.mu.Unlock()

	s.logger.Info("session entered processing state",
		"session_id", s.SessionID(),
		"generation", ev.generation,
	)

	if turnCtx == nil {
		turnCtx = s.ctx
	}
	go s.orchestrateLLMAndTTS(turnCtx, ev.generation, ev.text)
}

// handleTTSStartedEvent 处理 TTS 首音频就绪下发 tts.start 事件。
func (s *Session) handleTTSStartedEvent(ev event) {
	currentGen := s.Generation()
	if ev.generation != currentGen {
		s.logger.Debug("stale tts started event discarded",
			"event_gen", ev.generation,
			"current_gen", currentGen,
		)
		return
	}

	if s.State() != StateProcessing {
		s.logger.Debug("tts started event ignored in non-processing state",
			"state", s.State().String(),
			"generation", ev.generation,
		)
		return
	}

	startBytes, err := EncodeTTSStartMessage(s.SessionID())
	if err != nil {
		s.logger.Error("failed to encode tts start message",
			"error", err,
			"session_id", s.SessionID(),
		)
	} else {
		_ = s.sendTextMessage(startBytes)
	}

	s.mu.Lock()
	s.state = StateSpeaking
	s.mu.Unlock()

	s.logger.Info("session entered speaking state",
		"session_id", s.SessionID(),
		"generation", ev.generation,
	)
}

// handleTurnFinishedEvent 处理问答轮次结束事件。
func (s *Session) handleTurnFinishedEvent(ev event) {
	currentGen := s.Generation()
	if ev.generation != currentGen {
		s.logger.Debug("stale turn finished event discarded",
			"event_gen", ev.generation,
			"current_gen", currentGen,
		)
		return
	}

	currentState := s.State()
	if currentState != StateSpeaking && currentState != StateProcessing {
		return
	}

	var stopSucceeded bool
	if currentState == StateSpeaking {
		stopBytes, err := EncodeTTSStopMessage(s.SessionID())
		if err != nil {
			s.logger.Error("failed to encode tts stop message",
				"error", err,
				"session_id", s.SessionID(),
			)
		} else {
			if sendErr := s.sendTextMessage(stopBytes); sendErr != nil {
				s.logger.Warn("failed to send tts stop message",
					"error", sendErr,
					"session_id", s.SessionID(),
					"generation", ev.generation,
				)
				s.closeWithReason(websocket.StatusInternalError, sendErr.Error())
				return
			}
			stopSucceeded = true
		}
	}

	// 只有在 tts.stop 成功写出且本轮问答文本完整时，才正式提交至会话历史
	if stopSucceeded && ev.userText != "" && ev.assistantText != "" {
		s.AppendHistory(ev.userText, ev.assistantText)
	}

	s.stopASR()
	s.stopTTS()
	s.stopPacer()

	s.mu.Lock()
	if s.turnCancel != nil {
		s.turnCancel()
		s.turnCancel = nil
	}
	s.state = StateReady
	s.mu.Unlock()

	s.logger.Info("session returned to ready state",
		"session_id", s.SessionID(),
		"generation", ev.generation,
	)
}

// handleAbortEvent 处理显式中断事件。
func (s *Session) handleAbortEvent(reason string) {
	currentState := s.State()
	if currentState == StateConnected || currentState == StateClosed {
		return
	}

	s.mu.Lock()
	s.generation++
	newGen := s.generation
	if s.turnCancel != nil {
		s.turnCancel()
		s.turnCancel = nil
	}
	wasSpeaking := (s.state == StateSpeaking)
	s.state = StateReady
	s.mu.Unlock()

	s.stopListeningTimer()
	s.stopASRResultTimer()
	s.stopASR()
	s.stopTTS()
	s.stopPacer()

	if s.writer != nil {
		s.writer.DrainPending()
	}

	if wasSpeaking {
		stopBytes, err := EncodeTTSStopMessage(s.SessionID())
		if err != nil {
			s.logger.Error("failed to encode tts stop message on abort",
				"error", err,
				"session_id", s.SessionID(),
			)
		} else {
			_ = s.sendTextMessage(stopBytes)
		}
	}

	s.logger.Info("session aborted and reset to ready",
		"session_id", s.SessionID(),
		"new_generation", newGen,
		"reason", logger.TruncateString(reason),
		"was_speaking", wasSpeaking,
	)
}

// handleTimeoutEvent 处理超时事件。
func (s *Session) handleTimeoutEvent(ev event) {
	if ev.text == "hello handshake timeout" {
		if s.State() == StateConnected {
			s.logger.Warn("hello handshake timeout",
				"device_id", s.truncatedDeviceID(),
				"client_id", s.truncatedClientID(),
				"serial_number", s.truncatedSerialNumber(),
			)
			s.closeWithReason(websocket.StatusPolicyViolation, "hello handshake timeout")
		}
		return
	}

	if ev.text == "max listening duration exceeded" {
		if ev.generation == s.Generation() && s.State() == StateListening {
			s.logger.Warn("max listening duration exceeded",
				"session_id", s.SessionID(),
				"generation", ev.generation,
			)
			s.closeWithReason(websocket.StatusPolicyViolation, "max listening duration exceeded")
		}
		return
	}

	if ev.text == "asr recognition timeout" {
		if ev.generation == s.Generation() && s.State() == StateListening {
			s.logger.Warn("asr recognition timeout exceeded",
				"session_id", s.SessionID(),
				"generation", ev.generation,
			)
			s.closeWithReason(websocket.StatusPolicyViolation, "asr recognition timeout")
		}
		return
	}

	if ev.generation == s.Generation() || ev.generation == 0 {
		s.logger.Warn("session timeout exceeded",
			"session_id", s.SessionID(),
			"generation", ev.generation,
			"reason", logger.TruncateString(ev.text),
		)
		s.closeWithReason(websocket.StatusPolicyViolation, ev.text)
	}
}

// handleErrorEvent 处理错误事件。
func (s *Session) handleErrorEvent(ev event) {
	if ev.generation != 0 && ev.generation != s.Generation() {
		s.logger.Debug("stale error event discarded",
			"event_gen", ev.generation,
			"current_gen", s.Generation(),
		)
		return
	}

	if ev.fatal {
		s.stopPacer()
		if s.State() == StateSpeaking {
			stopBytes, _ := EncodeTTSStopMessage(s.SessionID())
			if len(stopBytes) > 0 {
				_ = s.sendTextMessage(stopBytes)
			}
		}
		closeCode := ev.closeCode
		if closeCode == 0 {
			closeCode = websocket.StatusInternalError
		}
		reason := "internal error"
		if ev.err != nil {
			reason = ev.err.Error()
		}
		s.closeWithReason(closeCode, reason)
	}
}

// handleCloseEvent 处理连接关闭事件。
func (s *Session) handleCloseEvent(ev event) {
	closeCode := ev.closeCode
	if closeCode == 0 {
		closeCode = websocket.StatusNormalClosure
	}
	s.closeWithReason(closeCode, ev.text)
}

// closeWithReason 执行会话资源清理并安全关闭底层连接。
func (s *Session) closeWithReason(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		s.stopASR()
		s.stopTTS()
		s.stopPacer()

		s.mu.Lock()
		s.state = StateClosed
		s.history = nil

		if s.helloTimer != nil {
			s.helloTimer.Stop()
			s.helloTimer = nil
		}
		if s.listeningTimer != nil {
			s.listeningTimer.Stop()
			s.listeningTimer = nil
		}
		if s.asrResultTimer != nil {
			s.asrResultTimer.Stop()
			s.asrResultTimer = nil
		}

		if s.turnCancel != nil {
			s.turnCancel()
			s.turnCancel = nil
		}
		s.mu.Unlock()

		if s.writer != nil {
			done := make(chan struct{})
			go func() {
				_ = s.writer.Close()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
				s.writer.Stop()
				<-done
			}
		}

		if s.conn != nil {
			if code == 0 {
				code = websocket.StatusNormalClosure
			}
			_ = s.conn.Close(code, reason)
		}

		if s.cancel != nil {
			s.cancel()
		}

		s.mcpMu.Lock()
		clear(s.pendingMCP)
		s.mcpMu.Unlock()
	})
}

// readLoop 单专属 goroutine 循环读取客户端文本与二进制消息。
func (s *Session) readLoop() {
	defer func() {
		s.postEvent(event{
			kind:      eventKindClose,
			closeCode: websocket.StatusNormalClosure,
			text:      "client disconnected",
		})
	}()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		msgType, data, err := s.conn.Read(s.ctx)
		if err != nil {
			var closeErr websocket.CloseError
			if errors.As(err, &closeErr) {
				s.logger.Info("websocket session disconnected by client",
					"session_id", s.SessionID(),
					"status_code", closeErr.Code,
					"reason", closeErr.Reason,
				)
			} else if errors.Is(err, io.EOF) {
				s.logger.Info("websocket session closed by client",
					"session_id", s.SessionID(),
					"reason", "EOF",
				)
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				s.logger.Warn("websocket read error",
					"session_id", s.SessionID(),
					"error", err,
				)
			}
			return
		}

		currentState := s.State()
		if currentState == StateConnected {
			if msgType == websocket.MessageBinary {
				s.postEvent(event{
					kind:     eventKindClientHello,
					isBinary: true,
				})
			} else {
				s.postEvent(event{
					kind:     eventKindClientHello,
					rawBytes: data,
					isBinary: false,
				})
			}
			continue
		}

		if msgType == websocket.MessageBinary {
			s.postEvent(event{
				kind:      eventKindClientAudio,
				audioData: data,
				isBinary:  true,
			})
			continue
		}

		clientMsg, err := ParseClientMessage(data)
		if err != nil {
			s.logger.Warn("failed to parse client text message",
				"error", err,
				"session_id", s.SessionID(),
			)
			s.postEvent(event{
				kind:      eventKindError,
				err:       err,
				fatal:     true,
				closeCode: websocket.StatusPolicyViolation,
			})
			return
		}

		s.postEvent(event{
			kind:      eventKindClientText,
			clientMsg: clientMsg,
		})
	}
}

// startHelloTimer 启动 hello 握手超时定时器。
func (s *Session) startHelloTimer() {
	dur := DefaultHelloTimeout
	if s.cfg != nil && s.cfg.Session.HelloTimeout > 0 {
		dur = s.cfg.Session.HelloTimeout
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateClosed {
		return
	}
	s.helloTimer = time.AfterFunc(dur, func() {
		s.postEvent(event{
			kind: eventKindTimeout,
			text: "hello handshake timeout",
		})
	})
}

// stopHelloTimer 停止 hello 握手超时定时器。
func (s *Session) stopHelloTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.helloTimer != nil {
		s.helloTimer.Stop()
		s.helloTimer = nil
	}
}

// startListeningTimer 启动单次收音最大时限定时器。
func (s *Session) startListeningTimer(gen uint64) {
	dur := DefaultMaxListeningDuration
	if s.cfg != nil && s.cfg.Session.MaxListeningDuration > 0 {
		dur = s.cfg.Session.MaxListeningDuration
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateClosed {
		return
	}
	if s.listeningTimer != nil {
		s.listeningTimer.Stop()
	}
	s.listeningTimer = time.AfterFunc(dur, func() {
		s.postEvent(event{
			kind:       eventKindTimeout,
			generation: gen,
			text:       "max listening duration exceeded",
		})
	})
}

// stopListeningTimer 停止单次收音最大时限定时器。
func (s *Session) stopListeningTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listeningTimer != nil {
		s.listeningTimer.Stop()
		s.listeningTimer = nil
	}
}

// startASRResultTimer 启动手动模式收音结束后等待 ASR 识别结果的超时定时器。
func (s *Session) startASRResultTimer(gen uint64) {
	dur := DefaultASRResultTimeout
	if s.cfg != nil && s.cfg.Session.ASRResultTimeout > 0 {
		dur = s.cfg.Session.ASRResultTimeout
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateClosed {
		return
	}
	if s.asrResultTimer != nil {
		s.asrResultTimer.Stop()
	}
	s.asrResultTimer = time.AfterFunc(dur, func() {
		s.postEvent(event{
			kind:       eventKindTimeout,
			generation: gen,
			text:       "asr recognition timeout",
		})
	})
}

// stopASRResultTimer 停止等待 ASR 识别结果的超时定时器。
func (s *Session) stopASRResultTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.asrResultTimer != nil {
		s.asrResultTimer.Stop()
		s.asrResultTimer = nil
	}
}

// sendTextMessage 向写队列发送文本消息。
func (s *Session) sendTextMessage(payload []byte) error {
	if s.writer == nil {
		return nil
	}
	return s.writer.SendText(s.ctx, payload)
}

// truncatedDeviceID 获取截断后的设备标识用于日志记录。
func (s *Session) truncatedDeviceID() string {
	if s.clientInfo == nil {
		return ""
	}
	return logger.TruncateString(s.clientInfo.DeviceID)
}

// truncatedClientID 获取截断后的客户端标识用于日志记录。
func (s *Session) truncatedClientID() string {
	if s.clientInfo == nil {
		return ""
	}
	return logger.TruncateString(s.clientInfo.ClientID)
}

// truncatedSerialNumber 获取截断后的序列号用于日志记录。
func (s *Session) truncatedSerialNumber() string {
	if s.clientInfo == nil {
		return ""
	}
	return logger.TruncateString(s.clientInfo.SerialNumber)
}

// startASRStream 为指定代次启动 ASR 流式识别与音频消费队列。
func (s *Session) startASRStream(gen uint64, mode string) {
	if s.asrClient == nil {
		return
	}

	turnCtx := s.TurnContext()
	stream, err := s.asrClient.CreateStream(turnCtx)
	if err != nil {
		s.logger.Error("failed to create asr stream",
			"error", err,
			"session_id", s.SessionID(),
			"generation", gen,
		)
		s.postEvent(event{
			kind:       eventKindError,
			generation: gen,
			err:        err,
			fatal:      true,
		})
		return
	}

	queueCap := audio.DefaultASRPCMQueueCapacity
	if s.cfg != nil && s.cfg.Session.ASRPCMQueueCapacity > 0 {
		queueCap = s.cfg.Session.ASRPCMQueueCapacity
	}

	s.mu.Lock()
	s.asrStream = stream
	s.asrQueue = audio.NewASRAudioQueue(turnCtx, stream, queueCap, s.logger)
	s.mu.Unlock()

	if mode == ListenModeAuto {
		go s.readASRResultAuto(turnCtx, stream, gen)
	}
}

// readASRResultAuto 在自动模式后台协程中等待 VAD 识别结果并投递至事件通道。
func (s *Session) readASRResultAuto(ctx context.Context, stream ai.ASRStream, gen uint64) {
	text, err := stream.Result(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		s.logger.Warn("asr recognition failed in auto mode",
			"error", err,
			"session_id", s.SessionID(),
			"generation", gen,
		)
		s.postEvent(event{
			kind:       eventKindError,
			generation: gen,
			err:        err,
			fatal:      true,
		})
		return
	}

	s.postEvent(event{
		kind:       eventKindASRFinal,
		generation: gen,
		text:       text,
	})
}

// readASRResultManual 在手动模式收音结束后等待音频排空与百炼最终识别结果，并投递至事件通道。
func (s *Session) readASRResultManual(ctx context.Context, stream ai.ASRStream, queue *audio.ASRAudioQueue, gen uint64) {
	if queue != nil {
		select {
		case <-queue.Done():
			if qErr := queue.Err(); qErr != nil && !errors.Is(qErr, context.Canceled) {
				if ctx.Err() != nil {
					return
				}
				s.logger.Warn("asr audio queue finished with error",
					"error", qErr,
					"session_id", s.SessionID(),
					"generation", gen,
				)
				s.postEvent(event{
					kind:       eventKindError,
					generation: gen,
					err:        qErr,
					fatal:      true,
				})
				return
			}
		case <-ctx.Done():
			return
		}
	}

	text, err := stream.Result(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		s.logger.Warn("asr recognition failed in manual mode",
			"error", err,
			"session_id", s.SessionID(),
			"generation", gen,
		)
		s.postEvent(event{
			kind:       eventKindError,
			generation: gen,
			err:        err,
			fatal:      true,
		})
		return
	}

	s.postEvent(event{
		kind:       eventKindASRFinal,
		generation: gen,
		text:       text,
	})
}

// finishASR 通知当前轮次的 ASR 音频队列与流结束音频输入。
func (s *Session) finishASR() {
	s.mu.RLock()
	q := s.asrQueue
	stream := s.asrStream
	s.mu.RUnlock()

	if q != nil {
		_ = q.Finish()
	} else if stream != nil {
		_ = stream.Finish(s.TurnContext())
	}
}

// stopASR 停止并清理当前轮次的 ASR 流与音频队列。
func (s *Session) stopASR() {
	s.mu.Lock()
	q := s.asrQueue
	stream := s.asrStream
	s.asrQueue = nil
	s.asrStream = nil
	s.mu.Unlock()

	if q != nil {
		q.Close()
	}
	if stream != nil {
		_ = stream.Close()
	}
}

// logDiag 限频记录诊断日志。
func (s *Session) logDiag(msg string, args ...any) {
	if s.diagLimiter != nil && !s.diagLimiter.Allow() {
		return
	}
	allArgs := append([]any{"session_id", s.SessionID(), "device_id", s.truncatedDeviceID()}, args...)
	s.logger.Warn(msg, allArgs...)
}
