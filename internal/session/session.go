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
	"xiaozhi-esp32-golang-server/internal/logger"
)

// sessionEventKind 定义进入会话监督循环的统一事件类型。
type sessionEventKind int

const (
	eventKindClientFrame sessionEventKind = iota
	eventKindTurnEvent
	eventKindTimeout
	eventKindCloseRequest
)

// sessionEvent 封装投递给 supervisor 主循环的统一事件对象。
type sessionEvent struct {
	kind        sessionEventKind
	isBinary    bool
	data        []byte
	turnEv      turnEvent
	timeoutText string
	turnId      uint64
	closeCode   websocket.StatusCode
	closeReason string
}

// runtimeState 封装 supervisor 内部独占维护的局部状态，无共享锁。
type runtimeState struct {
	state          State
	sessionId      string
	mode           string
	currentTurnId  uint64
	helloTimer     *time.Timer
	listeningTimer *time.Timer
	asrResultTimer *time.Timer
}

// Session 负责管理单个 WebSocket 连接的生命周期、协议事件循环与 Supervisor 状态机。
type Session struct {
	conn         *websocket.Conn
	writer       *Writer
	voiceStream  *VoiceStream
	events       chan sessionEvent
	serialNumber string
	cfg          SessionConfig
	logger       *slog.Logger
	diagLimiter  *logger.RateLimiter

	pipeline  *TurnPipeline
	mcpBridge *MCPBridge

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}

	atomicState     atomic.Int32
	atomicSessionId atomic.Value // string
}

// Options 聚合构造单个 WebSocket 会话的依赖与上下文。
type Options struct {
	Conn          *websocket.Conn
	Writer        *Writer
	SerialNumber  string
	SystemPrompt  string
	Config        SessionConfig
	ASRClient     ai.ASRClient
	LLMClient     ai.LLMClient
	TTSClient     ai.TTSClient
	VoiceStream   *VoiceStream
	AgentKitStore AgentKitStore
	Logger        *slog.Logger
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

	cfg := NormalizeConfig(opts.Config)
	sessionCtx, cancel := context.WithCancel(ctx)

	w := opts.Writer
	if w == nil && opts.Conn != nil {
		w = NewWriter(sessionCtx, opts.Conn, cfg.DownlinkOpusQueueCapacity, l)
	}

	events := make(chan sessionEvent, DefaultEventChannelCapacity)
	diagLimiter := logger.NewDiagRateLimiter()
	mcpBridge := NewMCPBridge(l, diagLimiter)
	toolProvider := NewToolProvider(mcpBridge, opts.AgentKitStore, l)
	history := NewConversationHistory(cfg.MaxHistoryTurns)

	vs := opts.VoiceStream
	if vs == nil {
		vs = NewVoiceStream(VoiceStreamOptions{
			SessionId: "",
			TTSClient: opts.TTSClient,
			Writer:    w,
			Config:    cfg,
			Logger:    l,
		})
	}

	pipeline := NewTurnPipeline(PipelineOptions{
		ASRClient:    opts.ASRClient,
		LLMClient:    opts.LLMClient,
		VoiceStream:  vs,
		SystemPrompt: opts.SystemPrompt,
		Config:       cfg,
		History:      history,
		ToolProvider: toolProvider,
		Logger:       l,
		PostEvent: func(ev turnEvent) {
			select {
			case <-sessionCtx.Done():
			case events <- sessionEvent{
				kind:   eventKindTurnEvent,
				turnEv: ev,
			}:
			}
		},
	})

	s := &Session{
		conn:         opts.Conn,
		writer:       w,
		voiceStream:  vs,
		events:       events,
		serialNumber: opts.SerialNumber,
		cfg:          cfg,
		logger:       l,
		diagLimiter:  diagLimiter,
		pipeline:     pipeline,
		mcpBridge:    mcpBridge,
		ctx:          sessionCtx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	s.atomicState.Store(int32(StateConnected))
	s.atomicSessionId.Store("")

	return s
}

// State 返回当前会话的状态（原子只读）。
func (s *Session) State() State {
	return State(s.atomicState.Load())
}

// SessionId 返回协商成功的会话标识。
func (s *Session) SessionId() string {
	if val := s.atomicSessionId.Load(); val != nil {
		return val.(string)
	}
	return ""
}

// DeviceKey 返回用于全局唯一标识单设备连接的键（SN 作为设备唯一身份）。
func (s *Session) DeviceKey() string {
	if s == nil {
		return ""
	}
	return s.serialNumber
}

// Close 主动请求关闭会话。
func (s *Session) Close() {
	s.postEvent(sessionEvent{
		kind:        eventKindCloseRequest,
		closeCode:   websocket.StatusNormalClosure,
		closeReason: "session closed",
	})
}

// postEvent 向事件通道投递事件，若会话已取消则返回 false。
func (s *Session) postEvent(ev sessionEvent) bool {
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

	if s.conn != nil {
		s.conn.SetReadLimit(s.cfg.MaxWSTextMessageBytes)
		go s.readLoop()
	}

	return s.supervisorLoop()
}

// supervisorLoop 单一主事件循环，按序处理所有事件并驱动状态转换。
func (s *Session) supervisorLoop() error {
	st := &runtimeState{
		state: StateConnected,
		mode:  ListenModeAuto,
	}

	// 启动 hello 超时定时器
	s.startHelloTimer(st)

	var writerErrCh <-chan error
	if s.writer != nil {
		writerErrCh = s.writer.ErrorNotify()
	}

	for {
		select {
		case <-s.ctx.Done():
			s.stopAllTimers(st)
			return s.ctx.Err()

		case err := <-writerErrCh:
			s.logger.Warn("websocket writer async error received",
				"session_id", st.sessionId,
				"error", err,
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusInternalError, "writer write failed")
			s.stopAllTimers(st)
			return nil

		case ev, ok := <-s.events:
			if !ok {
				s.stopAllTimers(st)
				return nil
			}

			s.handleEvent(st, ev)
			if st.state == StateClosed {
				s.stopAllTimers(st)
				return nil
			}
		}
	}
}

// setState 统一更新 supervisor 局部状态与外部原子状态。
func (s *Session) setState(st *runtimeState, newState State) {
	st.state = newState
	s.atomicState.Store(int32(newState))
}

// handleEvent 分发处理 supervisor 收到的各类事件。
func (s *Session) handleEvent(st *runtimeState, ev sessionEvent) {
	if st.state == StateClosed {
		return
	}

	switch ev.kind {
	case eventKindClientFrame:
		s.handleClientFrame(st, ev)
	case eventKindTurnEvent:
		s.handleTurnEvent(st, ev.turnEv)
	case eventKindTimeout:
		s.handleTimeoutEvent(st, ev)
	case eventKindCloseRequest:
		s.setState(st, StateClosed)
		s.closeWithReason(ev.closeCode, ev.closeReason)
	}
}

// handleClientFrame 处理由 readLoop 投递的原始 WebSocket 消息帧。
func (s *Session) handleClientFrame(st *runtimeState, ev sessionEvent) {
	if st.state == StateConnected {
		// 握手阶段首包必须为文本 hello
		if ev.isBinary {
			s.logger.Warn("first message is not text hello",
				"serial_number", s.truncatedSerialNumber(),
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusUnsupportedData, "first message must be text hello")
			return
		}

		var clientHello ClientHelloMessage
		if err := json.Unmarshal(ev.data, &clientHello); err != nil {
			s.logger.Warn("invalid json in hello message",
				"error", err,
				"serial_number", s.truncatedSerialNumber(),
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusPolicyViolation, "invalid json in hello message")
			return
		}

		if err := ValidateClientHello(&clientHello); err != nil {
			s.logger.Warn("invalid hello message fields",
				"error", err,
				"serial_number", s.truncatedSerialNumber(),
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusPolicyViolation, err.Error())
			return
		}

		s.stopHelloTimer(st)

		sessionId, err := GenerateSessionId()
		if err != nil {
			s.logger.Error("failed to generate session id",
				"error", err,
				"serial_number", s.truncatedSerialNumber(),
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusInternalError, "internal server error")
			return
		}

		respBytes, err := EncodeServerHelloMessage(sessionId)
		if err != nil {
			s.logger.Error("failed to encode server hello",
				"error", err,
				"session_id", sessionId,
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusInternalError, "internal server error")
			return
		}

		if err := s.sendTextMessage(respBytes); err != nil {
			s.logger.Warn("failed to write server hello",
				"error", err,
				"session_id", sessionId,
				"serial_number", s.truncatedSerialNumber(),
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusInternalError, "failed to write server hello")
			return
		}

		st.sessionId = sessionId
		s.atomicSessionId.Store(sessionId)
		if s.voiceStream != nil {
			s.voiceStream.SetSessionId(sessionId)
		}
		s.setState(st, StateReady)

		mcpSupported := clientHello.SupportsMCP()
		s.logger.Info("websocket hello handshake succeeded",
			"session_id", sessionId,
			"serial_number", s.truncatedSerialNumber(),
			"mcp_supported", mcpSupported,
		)

		if mcpSupported {
			s.mcpBridge.Enable(sessionId, s.writer)
		}
		return
	}

	// 握手完成后的消息处理
	if ev.isBinary {
		s.handleClientAudio(st, ev.data)
		return
	}

	clientMsg, err := ParseClientMessage(ev.data)
	if err != nil {
		s.logger.Warn("failed to parse client text message",
			"error", err,
			"session_id", st.sessionId,
		)
		s.setState(st, StateClosed)
		s.closeWithReason(websocket.StatusPolicyViolation, "invalid client message")
		return
	}

	s.handleClientText(st, clientMsg)
}

// handleClientAudio 处理上行二进制 Opus 音频包。
func (s *Session) handleClientAudio(st *runtimeState, data []byte) {
	packetLen := len(data)
	if packetLen == 0 || packetLen > s.cfg.MaxOpusPacketBytes {
		s.logger.Warn("invalid opus packet size",
			"size", packetLen,
			"max", s.cfg.MaxOpusPacketBytes,
			"session_id", st.sessionId,
		)
		s.setState(st, StateClosed)
		s.closeWithReason(websocket.StatusPolicyViolation, "invalid opus packet size")
		return
	}

	if st.state != StateListening {
		// 非 Listening 状态上行音频直接丢弃
		return
	}

	if err := s.pipeline.PushOpus(st.currentTurnId, data); err != nil {
		s.logger.Warn("failed to process uplink opus packet",
			"error", err,
			"session_id", st.sessionId,
			"turn_id", st.currentTurnId,
		)
		s.setState(st, StateClosed)
		s.closeWithReason(websocket.StatusPolicyViolation, err.Error())
	}
}

// handleClientText 处理上行文本协议消息。
func (s *Session) handleClientText(st *runtimeState, msg *ClientMessage) {
	if msg == nil {
		return
	}

	switch msg.Kind {
	case KindHello:
		s.logger.Warn("duplicate hello received after handshake",
			"session_id", st.sessionId,
			"serial_number", s.truncatedSerialNumber(),
		)
		s.setState(st, StateClosed)
		s.closeWithReason(websocket.StatusPolicyViolation, ErrDuplicateHello.Error())

	case KindListenStart:
		switch st.state {
		case StateReady:
			mode := msg.Mode
			if mode == "" {
				mode = ListenModeAuto
			}
			st.currentTurnId++
			turnId := st.currentTurnId

			s.setState(st, StateListening)
			st.mode = mode

			s.startListeningTimer(st, turnId)
			if err := s.pipeline.StartListening(s.ctx, turnId, st.sessionId, mode); err != nil {
				s.logger.Error("failed to start listening", "error", err, "session_id", st.sessionId)
				s.setState(st, StateClosed)
				s.closeWithReason(websocket.StatusInternalError, err.Error())
				return
			}
			s.logger.Info("session entered listening state",
				"session_id", st.sessionId,
				"turn_id", turnId,
				"mode", mode,
			)

		case StateListening:
			s.logDiag(st.sessionId, "duplicate listen.start ignored in listening state",
				"turn_id", st.currentTurnId,
			)

		default:
			s.logDiag(st.sessionId, "listen.start ignored in active state",
				"state", st.state.String(),
				"turn_id", st.currentTurnId,
			)
		}

	case KindListenStop:
		switch st.state {
		case StateListening:
			if st.mode == ListenModeAuto {
				s.logDiag(st.sessionId, "listen.stop ignored in auto mode",
					"turn_id", st.currentTurnId,
				)
			} else {
				s.stopListeningTimer(st)
				s.startASRResultTimer(st, st.currentTurnId)
				s.logger.Info("manual listen.stop received",
					"session_id", st.sessionId,
					"turn_id", st.currentTurnId,
				)
				_ = s.pipeline.FinishListening(st.currentTurnId, st.sessionId)
			}
		default:
			s.logDiag(st.sessionId, "listen.stop ignored in non-listening state",
				"state", st.state.String(),
			)
		}

	case KindListenDetect:
		s.logDiag(st.sessionId, "listen.detect received",
			"text", logger.TruncateString(msg.DetectText),
		)

	case KindAbort:
		s.handleAbort(st, msg.AbortReason)

	case KindMCP:
		s.mcpBridge.HandleInbound(st.sessionId, msg)

	case KindUnknownExtension:
		s.logDiag(st.sessionId, "unknown extension message received",
			"raw_type", logger.TruncateString(msg.RawType),
		)
	}
}

// handleTurnEvent 处理流水线投递的轮次事件。
func (s *Session) handleTurnEvent(st *runtimeState, ev turnEvent) {
	if ev.turnId != st.currentTurnId {
		s.logger.Debug("stale turn event discarded",
			"event_turn_id", ev.turnId,
			"current_turn_id", st.currentTurnId,
			"event_type", ev.typ,
		)
		return
	}

	switch ev.typ {
	case turnEventASRFinal:
		if st.state != StateListening {
			s.logger.Debug("asr final event ignored in non-listening state",
				"state", st.state.String(),
				"turn_id", ev.turnId,
			)
			return
		}

		s.stopListeningTimer(st)
		s.stopASRResultTimer(st)

		if ev.text == "" {
			s.setState(st, StateReady)
			s.pipeline.Abort(ev.turnId)
			s.logger.Info("empty asr result, session returned to ready state",
				"session_id", st.sessionId,
				"turn_id", ev.turnId,
			)
			return
		}

		sttBytes, err := EncodeSTTMessage(st.sessionId, ev.text)
		if err != nil {
			s.logger.Error("failed to encode stt message",
				"error", err,
				"session_id", st.sessionId,
				"turn_id", ev.turnId,
			)
		} else {
			_ = s.sendTextMessage(sttBytes)
		}

		s.setState(st, StateProcessing)
		s.logger.Info("session entered processing state",
			"session_id", st.sessionId,
			"turn_id", ev.turnId,
		)
		_ = s.pipeline.StartResponse(ev.turnId, st.sessionId, ev.text)

	case turnEventTurnCompleted:
		if st.state != StateSpeaking && st.state != StateProcessing {
			return
		}

		s.setState(st, StateReady)

		if ev.closeSession {
			s.logger.Info("closing session after turn finished as requested by tool",
				"session_id", st.sessionId,
				"turn_id", ev.turnId,
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusNormalClosure, "session closed by user command")
			return
		}

		s.logger.Info("session returned to ready state",
			"session_id", st.sessionId,
			"turn_id", ev.turnId,
		)

	case turnEventTurnFailed:
		if ev.fatal {
			s.pipeline.Abort(ev.turnId)
			reason := "internal error"
			if ev.err != nil {
				reason = ev.err.Error()
			}
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusInternalError, reason)
		}
	}
}

// handleAbort 处理显式中止逻辑。
func (s *Session) handleAbort(st *runtimeState, reason string) {
	if st.state == StateConnected || st.state == StateClosed {
		return
	}

	oldTurnId := st.currentTurnId
	st.currentTurnId++
	s.setState(st, StateReady)

	s.stopListeningTimer(st)
	s.stopASRResultTimer(st)
	s.pipeline.Abort(0)
	if s.voiceStream != nil {
		s.voiceStream.CancelTurn(oldTurnId)
	}

	if s.writer != nil {
		s.writer.InvalidateVoiceTurn(oldTurnId)
	}

	s.logger.Info("session aborted and reset to ready",
		"session_id", st.sessionId,
		"old_turn_id", oldTurnId,
		"new_turn_id", st.currentTurnId,
		"reason", logger.TruncateString(reason),
	)
}

// handleTimeoutEvent 处理各类超时事件。
func (s *Session) handleTimeoutEvent(st *runtimeState, ev sessionEvent) {
	if ev.timeoutText == "hello handshake timeout" {
		if st.state == StateConnected {
			s.logger.Warn("hello handshake timeout",
				"serial_number", s.truncatedSerialNumber(),
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusPolicyViolation, "hello handshake timeout")
		}
		return
	}

	if ev.timeoutText == "max listening duration exceeded" {
		if ev.turnId == st.currentTurnId && st.state == StateListening {
			s.logger.Warn("max listening duration exceeded",
				"session_id", st.sessionId,
				"turn_id", ev.turnId,
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusPolicyViolation, "max listening duration exceeded")
		}
		return
	}

	if ev.timeoutText == "asr recognition timeout" {
		if ev.turnId == st.currentTurnId && st.state == StateListening {
			s.logger.Warn("asr recognition timeout exceeded",
				"session_id", st.sessionId,
				"turn_id", ev.turnId,
			)
			s.setState(st, StateClosed)
			s.closeWithReason(websocket.StatusPolicyViolation, "asr recognition timeout")
		}
		return
	}
}

// readLoop 单专属 goroutine 循环读取客户端文本与二进制消息。
func (s *Session) readLoop() {
	defer func() {
		s.postEvent(sessionEvent{
			kind:        eventKindCloseRequest,
			closeCode:   websocket.StatusNormalClosure,
			closeReason: "client disconnected",
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
					"session_id", s.SessionId(),
					"status_code", closeErr.Code,
					"reason", closeErr.Reason,
				)
			} else if errors.Is(err, io.EOF) {
				s.logger.Info("websocket session closed by client",
					"session_id", s.SessionId(),
					"reason", "EOF",
				)
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				s.logger.Warn("websocket read error",
					"session_id", s.SessionId(),
					"error", err,
				)
			}
			return
		}

		s.postEvent(sessionEvent{
			kind:     eventKindClientFrame,
			isBinary: (msgType == websocket.MessageBinary),
			data:     data,
		})
	}
}

// closeWithReason 执行会话资源清理并安全关闭底层连接。
func (s *Session) closeWithReason(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		if s.voiceStream != nil {
			_ = s.voiceStream.Close()
		}
		s.pipeline.Close()
		s.mcpBridge.Close()

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
	})
}

// startHelloTimer 启动握手超时定时器。
func (s *Session) startHelloTimer(st *runtimeState) {
	st.helloTimer = time.AfterFunc(s.cfg.HelloTimeout, func() {
		s.postEvent(sessionEvent{
			kind:        eventKindTimeout,
			timeoutText: "hello handshake timeout",
		})
	})
}

// stopHelloTimer 停止握手超时定时器。
func (s *Session) stopHelloTimer(st *runtimeState) {
	if st.helloTimer != nil {
		st.helloTimer.Stop()
		st.helloTimer = nil
	}
}

// startListeningTimer 启动最大收音时限定时器。
func (s *Session) startListeningTimer(st *runtimeState, turnId uint64) {
	if st.listeningTimer != nil {
		st.listeningTimer.Stop()
	}
	st.listeningTimer = time.AfterFunc(s.cfg.MaxListeningDuration, func() {
		s.postEvent(sessionEvent{
			kind:        eventKindTimeout,
			turnId:      turnId,
			timeoutText: "max listening duration exceeded",
		})
	})
}

// stopListeningTimer 停止最大收音时限定时器。
func (s *Session) stopListeningTimer(st *runtimeState) {
	if st.listeningTimer != nil {
		st.listeningTimer.Stop()
		st.listeningTimer = nil
	}
}

// startASRResultTimer 启动 ASR 结果等待超时定时器。
func (s *Session) startASRResultTimer(st *runtimeState, turnId uint64) {
	if st.asrResultTimer != nil {
		st.asrResultTimer.Stop()
	}
	st.asrResultTimer = time.AfterFunc(s.cfg.ASRResultTimeout, func() {
		s.postEvent(sessionEvent{
			kind:        eventKindTimeout,
			turnId:      turnId,
			timeoutText: "asr recognition timeout",
		})
	})
}

// stopASRResultTimer 停止 ASR 结果等待超时定时器。
func (s *Session) stopASRResultTimer(st *runtimeState) {
	if st.asrResultTimer != nil {
		st.asrResultTimer.Stop()
		st.asrResultTimer = nil
	}
}

// stopAllTimers 停止全部定时器。
func (s *Session) stopAllTimers(st *runtimeState) {
	s.stopHelloTimer(st)
	s.stopListeningTimer(st)
	s.stopASRResultTimer(st)
}

// sendTextMessage 向写队列发送文本消息。
func (s *Session) sendTextMessage(payload []byte) error {
	if s.writer == nil {
		return nil
	}
	return s.writer.SendText(s.ctx, payload)
}

// truncatedSerialNumber 获取截断后的序列号。
func (s *Session) truncatedSerialNumber() string {
	return logger.TruncateString(s.serialNumber)
}

// logDiag 限频记录诊断日志。
func (s *Session) logDiag(sessionId string, msg string, args ...any) {
	if s.diagLimiter != nil && !s.diagLimiter.Allow() {
		return
	}
	allArgs := append([]any{"session_id", sessionId, "serial_number", s.truncatedSerialNumber()}, args...)
	s.logger.Warn(msg, allArgs...)
}
