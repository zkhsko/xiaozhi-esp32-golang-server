package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/logger"
	"xiaozhi-esp32-golang-server/internal/voice"
)

// sessionEventKind 定义进入会话监督循环的统一事件类型。
type sessionEventKind int

const (
	eventKindClientFrame sessionEventKind = iota
	eventKindTurnInputClosed
	eventKindTurnFinished
	eventKindTimeout
	eventKindOutboundFailed
	eventKindCloseRequest
)

// sessionEvent 封装投递给 supervisor 主循环的统一事件对象。
type sessionEvent struct {
	kind        sessionEventKind
	isBinary    bool
	data        []byte
	turnId      uint64
	turnResult  voice.TurnResult
	timeoutText string
	closeCode   websocket.StatusCode
	closeReason string
}

// PendingTurn 暂存输出阶段或 Abort 过程中到达的下一个轮次及其预缓冲音频。
type PendingTurn struct {
	mode         string
	manualStop   bool
	audioBuffers [][]byte
}

// runtimeState 封装 Session Actor 内部独占维护的局部运行时状态，无跨协程共享锁。
type runtimeState struct {
	state            State
	sessionId        string
	currentTurnId    uint64
	turnCancel       context.CancelFunc
	turnInputCh      chan []byte
	turnInputClosed  bool
	turnEffectsCh    chan voice.TurnEffect
	pendingTurn      *PendingTurn
	history          *ConversationHistory
	helloTimer       *time.Timer
	listeningTimer   *time.Timer
}

// Session 负责管理单个 WebSocket 连接的生命周期、协议事件循环与 Actor 状态机。
type Session struct {
	conn         *websocket.Conn
	outbound     *OutboundActor
	events       chan sessionEvent
	serialNumber string
	systemPrompt string
	cfg          SessionConfig
	logger       *slog.Logger
	diagLimiter  *logger.RateLimiter

	asrClient    ai.ASRClient
	llmClient    ai.LLMClient
	ttsClient    ai.TTSClient
	mcpBridge    *MCPBridge
	toolProvider *ToolProvider
	voiceEngine  *voice.TurnEngine

	runtime runtimeState

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}

	atomicSessionId atomic.Value // 存储 string
}

// Options 聚合构造单个 WebSocket 会话的依赖与上下文。
type Options struct {
	Conn          *websocket.Conn
	SerialNumber  string
	SystemPrompt  string
	Config        SessionConfig
	ASRClient     ai.ASRClient
	LLMClient     ai.LLMClient
	TTSClient     ai.TTSClient
	AgentKitStore AgentKitStore
	Logger        *slog.Logger
	Outbound      *OutboundActor
	VoiceEngine   *voice.TurnEngine
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

	events := make(chan sessionEvent, DefaultEventChannelCapacity)
	diagLimiter := logger.NewDiagRateLimiter()
	mcpBridge := NewMCPBridge(l, diagLimiter)
	toolProvider := NewToolProvider(mcpBridge, opts.AgentKitStore, l)
	engine := opts.VoiceEngine
	if engine == nil {
		engine = voice.NewEngine()
	}

	out := opts.Outbound
	if out == nil && opts.Conn != nil {
		out = NewOutboundActor(
			sessionCtx,
			opts.Conn,
			cfg.DownlinkOpusQueueCapacity,
			cfg.WebsocketWriteTimeout,
			l,
			func(err error) {
				select {
				case events <- sessionEvent{kind: eventKindOutboundFailed}:
				default:
				}
			},
		)
	}

	sess := &Session{
		conn:         opts.Conn,
		outbound:     out,
		events:       events,
		serialNumber: opts.SerialNumber,
		systemPrompt: opts.SystemPrompt,
		cfg:          cfg,
		logger:       l,
		diagLimiter:  diagLimiter,
		asrClient:    opts.ASRClient,
		llmClient:    opts.LLMClient,
		ttsClient:    opts.TTSClient,
		mcpBridge:    mcpBridge,
		toolProvider: toolProvider,
		voiceEngine:  engine,
		runtime: runtimeState{
			state:   StateAwaitHello,
			history: NewConversationHistory(cfg.MaxHistoryTurns),
		},
		ctx:    sessionCtx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	return sess
}

// Run 启动会话的主事件循环与读取协程，阻塞直至连接断开或上下文被取消。
func (s *Session) Run() error {
	defer s.cleanup()

	// 启动 Hello 超时定时器
	s.runtime.helloTimer = time.AfterFunc(s.cfg.HelloTimeout, func() {
		s.postEvent(sessionEvent{
			kind:        eventKindTimeout,
			timeoutText: "hello timeout",
			closeCode:   websocket.StatusPolicyViolation,
			closeReason: "hello timeout",
		})
	})

	go s.readPump()

	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case ev := <-s.events:
			shouldExit := s.handleEvent(ev)
			if shouldExit {
				return nil
			}
		}
	}
}

// postEvent 向会话 Actor 内部事件队列投递事件。
func (s *Session) postEvent(ev sessionEvent) {
	select {
	case <-s.ctx.Done():
	case s.events <- ev:
	}
}

// readPump 独占从底层 WebSocket 读取文本与二进制帧并投递给 Actor。
func (s *Session) readPump() {
	if s.conn == nil {
		return
	}

	for {
		msgType, data, err := s.conn.Read(s.ctx)
		if err != nil {
			var closeErr websocket.CloseError
			if errors.As(err, &closeErr) {
				s.postEvent(sessionEvent{
					kind:        eventKindCloseRequest,
					closeCode:   closeErr.Code,
					closeReason: closeErr.Reason,
				})
				return
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
				s.postEvent(sessionEvent{
					kind:        eventKindCloseRequest,
					closeCode:   websocket.StatusNormalClosure,
					closeReason: "connection closed",
				})
				return
			}

			s.postEvent(sessionEvent{
				kind:        eventKindCloseRequest,
				closeCode:   websocket.StatusInternalError,
				closeReason: "read error",
			})
			return
		}

		if msgType == websocket.MessageBinary {
			copied := make([]byte, len(data))
			copy(copied, data)
			s.postEvent(sessionEvent{
				kind:     eventKindClientFrame,
				isBinary: true,
				data:     copied,
			})
		} else if msgType == websocket.MessageText {
			s.postEvent(sessionEvent{
				kind:     eventKindClientFrame,
				isBinary: false,
				data:     data,
			})
		}
	}
}

// handleEvent 是 Session Actor 的唯一状态机转移收口。
func (s *Session) handleEvent(ev sessionEvent) bool {
	switch ev.kind {
	case eventKindTimeout:
		s.logger.Warn("session actor timeout",
			"serial_number", s.serialNumber,
			"state", s.runtime.state.String(),
			"timeout_text", ev.timeoutText,
		)
		s.closeWithReason(ev.closeCode, ev.closeReason)
		return true

	case eventKindOutboundFailed:
		s.logger.Warn("session outbound actor failed",
			"serial_number", s.serialNumber,
			"state", s.runtime.state.String(),
		)
		s.closeWithReason(websocket.StatusInternalError, "outbound failed")
		return true

	case eventKindCloseRequest:
		s.closeWithReason(ev.closeCode, ev.closeReason)
		return true

	case eventKindTurnInputClosed:
		if ev.turnId != s.runtime.currentTurnId {
			// 旧代次迟到事件丢弃
			return false
		}
		s.runtime.turnInputClosed = true
		if s.runtime.listeningTimer != nil {
			s.runtime.listeningTimer.Stop()
			s.runtime.listeningTimer = nil
		}
		if s.runtime.turnInputCh != nil {
			close(s.runtime.turnInputCh)
			s.runtime.turnInputCh = nil
		}
		return false

	case eventKindTurnFinished:
		if ev.turnId != s.runtime.currentTurnId {
			// 旧代次迟到事件丢弃
			return false
		}
		return s.handleTurnFinished(ev.turnResult)

	case eventKindClientFrame:
		if ev.isBinary {
			s.handleAudioFrame(ev.data)
			return false
		}
		return s.handleTextMessage(ev.data)
	}

	return false
}

// handleTextMessage 处理客户端文本控制帧。
func (s *Session) handleTextMessage(data []byte) bool {
	if s.runtime.state == StateAwaitHello {
		return s.handleHello(data)
	}

	msg, err := ParseClientMessageWithLimit(data, int(s.cfg.MaxWSTextMessageBytes))
	if err != nil {
		s.logger.Warn("malformed client text message",
			"serial_number", s.serialNumber,
			"error", err,
		)
		s.closeWithReason(websocket.StatusPolicyViolation, "malformed client message")
		return true
	}

	switch msg.Kind {
	case KindHello:
		s.logger.Warn("duplicate hello received",
			"serial_number", s.serialNumber,
			"state", s.runtime.state.String(),
		)
		s.closeWithReason(websocket.StatusPolicyViolation, "duplicate hello")
		return true

	case KindListenStart:
		s.handleListenStart(msg.Mode)
		return false

	case KindListenStop:
		s.handleListenStop()
		return false

	case KindAbort:
		s.handleAbort()
		return false

	case KindMCP:
		if s.mcpBridge != nil {
			s.mcpBridge.HandleInbound(s.runtime.sessionId, msg)
		}
		return false

	case KindListenDetect:
		s.logger.Debug("client wake detect message",
			"serial_number", s.serialNumber,
			"text", msg.DetectText,
		)
		return false

	default:
		s.logger.Warn("unknown client message kind ignored",
			"serial_number", s.serialNumber,
			"kind", string(msg.Kind),
		)
		return false
	}
}

// handleHello 处理客户端 hello 握手。
func (s *Session) handleHello(data []byte) bool {
	var helloMsg ClientHelloMessage
	if err := json.Unmarshal(data, &helloMsg); err != nil {
		s.logger.Warn("client hello parse failed",
			"serial_number", s.serialNumber,
			"error", err,
		)
		s.closeWithReason(websocket.StatusPolicyViolation, err.Error())
		return true
	}

	if err := ValidateClientHello(&helloMsg); err != nil {
		s.logger.Warn("client hello validation failed",
			"serial_number", s.serialNumber,
			"error", err,
		)
		s.closeWithReason(websocket.StatusPolicyViolation, err.Error())
		return true
	}

	if s.runtime.helloTimer != nil {
		s.runtime.helloTimer.Stop()
		s.runtime.helloTimer = nil
	}

	sessionId, _ := GenerateSessionId()
	s.runtime.sessionId = sessionId
	s.atomicSessionId.Store(sessionId)

	helloRespBytes, err := EncodeServerHelloMessage(sessionId)
	if err != nil {
		s.closeWithReason(websocket.StatusInternalError, "encode hello failed")
		return true
	}

	// Hello 必须通过 Outbound Actor 实际写出
	if s.outbound != nil {
		writeCtx, cancel := context.WithTimeout(s.ctx, s.cfg.WebsocketWriteTimeout)
		err := s.outbound.SendTextSession(writeCtx, helloRespBytes)
		cancel()
		if err != nil {
			s.logger.Error("failed to write server hello",
				"serial_number", s.serialNumber,
				"error", err,
			)
			s.closeWithReason(websocket.StatusInternalError, "write hello failed")
			return true
		}
	}

	s.runtime.state = StateReady

	// 客户端若支持 MCP，使用 session 上下文启动后台发现
	if helloMsg.SupportsMCP() && s.mcpBridge != nil && s.outbound != nil {
		s.mcpBridge.Enable(s.ctx, sessionId, s.outbound)
	}

	s.logger.Info("session ready after handshake",
		"serial_number", s.serialNumber,
		"session_id", sessionId,
	)

	return false
}

// handleListenStart 处理客户端 listen.start。
func (s *Session) handleListenStart(mode string) {
	if s.runtime.state == StateAwaitHello || s.runtime.state == StateClosed {
		return
	}

	if mode == "" {
		mode = "auto"
	}
	if mode == "realtime" {
		s.logger.Warn("realtime listen mode rejected",
			"serial_number", s.serialNumber,
		)
		return
	}

	if s.runtime.state == StateReady {
		s.startTurn(mode, nil, false)
		return
	}

	if s.runtime.state == StateTurnActive {
		if !s.runtime.turnInputClosed {
			// 还在收音输入阶段，重复 start 忽略
			return
		}
		// 已进入回答播报阶段，暂存下一轮
		s.runtime.pendingTurn = &PendingTurn{
			mode: mode,
		}
	}
}

// handleListenStop 处理客户端 listen.stop。
func (s *Session) handleListenStop() {
	if s.runtime.state == StateTurnActive {
		if !s.runtime.turnInputClosed && s.runtime.turnInputCh != nil {
			close(s.runtime.turnInputCh)
			s.runtime.turnInputCh = nil
			s.runtime.turnInputClosed = true
		}
	}
	if s.runtime.pendingTurn != nil {
		s.runtime.pendingTurn.manualStop = true
	}
}

// handleAbort 处理客户端 abort 打断。
func (s *Session) handleAbort() {
	if s.runtime.state != StateTurnActive {
		return
	}

	// 1. 取消当前 Turn Context
	if s.runtime.turnCancel != nil {
		s.runtime.turnCancel()
	}

	// 2. 精准失效当前 Turn 尚未开始写入的下行
	if s.outbound != nil {
		s.outbound.InvalidateTurn(s.runtime.currentTurnId)
	}

	if s.runtime.listeningTimer != nil {
		s.runtime.listeningTimer.Stop()
		s.runtime.listeningTimer = nil
	}

	if s.runtime.turnInputCh != nil {
		close(s.runtime.turnInputCh)
		s.runtime.turnInputCh = nil
	}
	s.runtime.turnInputClosed = true
}

// handleAudioFrame 处理客户端上行 Opus 音频帧。
func (s *Session) handleAudioFrame(data []byte) {
	if len(data) == 0 || len(data) > s.cfg.MaxOpusPacketBytes {
		return
	}

	if s.runtime.state != StateTurnActive {
		return
	}

	if !s.runtime.turnInputClosed && s.runtime.turnInputCh != nil {
		select {
		case s.runtime.turnInputCh <- data:
		default:
			// 上行有界队列满，背压保护，视为链路故障并关闭 Session
			s.logger.Error("uplink audio queue full, closing session for backpressure protection",
				"serial_number", s.serialNumber,
				"turn_id", s.runtime.currentTurnId,
			)
			s.closeWithReason(websocket.StatusPolicyViolation, "uplink audio buffer overflow")
		}
		return
	}

	// 已处于回答阶段（turnInputClosed=true）
	if s.runtime.pendingTurn != nil {
		capacity := s.cfg.ASRPCMQueueCapacity
		if len(s.runtime.pendingTurn.audioBuffers) < capacity {
			s.runtime.pendingTurn.audioBuffers = append(s.runtime.pendingTurn.audioBuffers, data)
		} else {
			s.logger.Error("pending turn audio buffer overflow",
				"serial_number", s.serialNumber,
			)
			s.closeWithReason(websocket.StatusPolicyViolation, "pending audio buffer overflow")
		}
	}
}

// startTurn 启动新一轮语音问答。
func (s *Session) startTurn(mode string, prebuffer [][]byte, manualStop bool) {
	s.runtime.currentTurnId++
	turnId := s.runtime.currentTurnId

	turnCtx, turnCancel := context.WithCancel(s.ctx)
	s.runtime.turnCancel = turnCancel
	s.runtime.turnInputClosed = false

	inputCh := make(chan []byte, s.cfg.ASRPCMQueueCapacity)
	s.runtime.turnInputCh = inputCh

	effectsCh := make(chan voice.TurnEffect, 8)
	s.runtime.turnEffectsCh = effectsCh

	// 注入预缓冲音频
	for _, pkt := range prebuffer {
		inputCh <- pkt
	}

	if manualStop && strings.EqualFold(mode, "manual") {
		close(inputCh)
		s.runtime.turnInputCh = nil
		s.runtime.turnInputClosed = true
	} else if strings.EqualFold(mode, "auto") {
		s.runtime.listeningTimer = time.AfterFunc(s.cfg.MaxListeningDuration, func() {
			s.postEvent(sessionEvent{
				kind:        eventKindTimeout,
				timeoutText: "max listening duration exceeded",
				closeCode:   websocket.StatusPolicyViolation,
				closeReason: "listening timeout",
			})
		})
	}

	s.runtime.state = StateTurnActive

	var turnOutput voice.TurnOutput
	if s.outbound != nil {
		turnOutput = s.outbound.NewTurnOutput(turnId, s.runtime.sessionId)
	}

	var toolSnapshotFn voice.ToolSnapshotFunc
	if s.toolProvider != nil {
		sessionId := s.runtime.sessionId
		toolSnapshotFn = func(ctx context.Context) []ai.Tool {
			return s.toolProvider.BuildSnapshot(ctx, turnId, sessionId, effectsCh)
		}
	}

	req := voice.TurnRequest{
		TurnId:             turnId,
		Mode:               mode,
		SystemPrompt:       s.systemPrompt,
		History:            s.runtime.history.MessagesSnapshot(),
		ToolSnapshot:       toolSnapshotFn,
		ASRClient:          s.asrClient,
		LLMClient:          s.llmClient,
		TTSClient:          s.ttsClient,
		EffectsCh:          effectsCh,
		MaxOpusPacketBytes: s.cfg.MaxOpusPacketBytes,
		TTSSentenceTimeout: s.cfg.TTSSentenceTimeout,
		Logger:             s.logger,
		OnInputClosed: func() {
			s.postEvent(sessionEvent{
				kind:   eventKindTurnInputClosed,
				turnId: turnId,
			})
		},
	}

	go func() {
		res := s.voiceEngine.HandleTurn(turnCtx, req, inputCh, turnOutput)
		s.postEvent(sessionEvent{
			kind:       eventKindTurnFinished,
			turnId:     turnId,
			turnResult: res,
		})
	}()
}

// handleTurnFinished 针对单轮问答终态收口。
func (s *Session) handleTurnFinished(res voice.TurnResult) bool {
	if s.runtime.listeningTimer != nil {
		s.runtime.listeningTimer.Stop()
		s.runtime.listeningTimer = nil
	}

	s.runtime.turnCancel = nil
	s.runtime.turnInputCh = nil
	s.runtime.turnInputClosed = false
	s.runtime.turnEffectsCh = nil

	switch res.Status {
	case voice.TurnCompleted:
		// 历史只在完整交付后追加，保留 Tool Call 痕迹
		s.runtime.history.AppendTurn(res.UserText, res.AssistantText, res.TurnMessages)

		// 检查是否有 close_session 副作用
		for _, eff := range res.Effects {
			if eff.Type == voice.EffectCloseSession {
				s.runtime.pendingTurn = nil
				s.closeWithReason(websocket.StatusNormalClosure, "session closed by agent")
				return true
			}
		}

		// 检查是否有暂存的下一轮
		if s.runtime.pendingTurn != nil {
			pending := s.runtime.pendingTurn
			s.runtime.pendingTurn = nil
			s.startTurn(pending.mode, pending.audioBuffers, pending.manualStop)
			return false
		}

		s.runtime.state = StateReady
		return false

	case voice.TurnAborted:
		if s.runtime.pendingTurn != nil {
			pending := s.runtime.pendingTurn
			s.runtime.pendingTurn = nil
			s.startTurn(pending.mode, pending.audioBuffers, pending.manualStop)
			return false
		}
		s.runtime.state = StateReady
		return false

	case voice.TurnNoSpeech:
		// manual 模式下空 ASR，不提交历史，回到 Ready
		if s.runtime.pendingTurn != nil {
			pending := s.runtime.pendingTurn
			s.runtime.pendingTurn = nil
			s.startTurn(pending.mode, pending.audioBuffers, pending.manualStop)
			return false
		}
		s.runtime.state = StateReady
		return false

	case voice.TurnFailed:
		s.logger.Error("turn failed, closing session",
			"serial_number", s.serialNumber,
			"turn_id", res.TurnId,
			"error", res.Err,
		)
		s.runtime.pendingTurn = nil
		s.closeWithReason(websocket.StatusInternalError, "turn execution failed")
		return true
	}

	return false
}

func (s *Session) closeWithReason(code websocket.StatusCode, reason string) {
	s.runtime.state = StateClosed
	s.cancel()

	if s.conn != nil {
		_ = s.conn.Close(code, reason)
	}
}

func (s *Session) cleanup() {
	s.closeOnce.Do(func() {
		s.cancel()

		if s.runtime.helloTimer != nil {
			s.runtime.helloTimer.Stop()
		}
		if s.runtime.listeningTimer != nil {
			s.runtime.listeningTimer.Stop()
		}

		if s.runtime.turnCancel != nil {
			s.runtime.turnCancel()
		}

		if s.outbound != nil {
			s.outbound.Close()
		}

		if s.mcpBridge != nil {
			s.mcpBridge.Close()
		}

		if s.conn != nil {
			_ = s.conn.Close(websocket.StatusNormalClosure, "session closed")
		}

		close(s.done)
	})
}

// Close 主动关闭会话。
func (s *Session) Close() {
	s.cancel()
}

// Done 返回会话结束完成通道。
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// SessionId 返回当前握手完成后的会话 Id。
func (s *Session) SessionId() string {
	if v := s.atomicSessionId.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// SerialNumber 返回当前会话绑定的设备硬件序列号。
func (s *Session) SerialNumber() string {
	return s.serialNumber
}

// DeviceKey 返回用于单设备互斥注册的设备唯一键。
func (s *Session) DeviceKey() string {
	return s.serialNumber
}
