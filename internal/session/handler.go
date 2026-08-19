package session

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// Handler 处理 WebSocket 协议升级、会话准入控制与连接生命周期。
type Handler struct {
	cfg       *config.Config
	registry  *Registry
	asrClient ai.ASRClient
	llmClient ai.LLMClient
	ttsClient ai.TTSClient
	logger    *slog.Logger
}

// NewHandler 创建配置就绪的 WebSocket HTTP 升级处理器。
func NewHandler(cfg *config.Config, limiter *SessionLimiter, asrClient ai.ASRClient, llmClient ai.LLMClient, ttsClient ai.TTSClient, l *slog.Logger) *Handler {
	if l == nil {
		l = slog.Default()
	}
	if limiter == nil {
		maxSessions := 1
		if cfg != nil && cfg.Server.MaxConcurrentSessions > 0 {
			maxSessions = cfg.Server.MaxConcurrentSessions
		}
		limiter = NewSessionLimiter(maxSessions)
	}
	reg := NewRegistry(limiter, l)
	return NewHandlerWithRegistry(cfg, reg, asrClient, llmClient, ttsClient, l)
}

// NewHandlerWithRegistry 使用指定会话注册表创建 WebSocket HTTP 升级处理器。
func NewHandlerWithRegistry(cfg *config.Config, registry *Registry, asrClient ai.ASRClient, llmClient ai.LLMClient, ttsClient ai.TTSClient, l *slog.Logger) *Handler {
	if l == nil {
		l = slog.Default()
	}
	if registry == nil {
		maxSessions := 1
		if cfg != nil && cfg.Server.MaxConcurrentSessions > 0 {
			maxSessions = cfg.Server.MaxConcurrentSessions
		}
		registry = NewRegistry(NewSessionLimiter(maxSessions), l)
	}
	return &Handler{
		cfg:       cfg,
		registry:  registry,
		asrClient: asrClient,
		llmClient: llmClient,
		ttsClient: ttsClient,
		logger:    l,
	}
}

// Registry 返回当前关联的会话注册表。
func (h *Handler) Registry() *Registry {
	return h.registry
}

// Limiter 返回当前关联的会话准入控制器。
func (h *Handler) Limiter() *SessionLimiter {
	if h.registry != nil {
		return h.registry.Limiter()
	}
	return nil
}

// ASRClient 返回当前关联的 ASR 客户端。
func (h *Handler) ASRClient() ai.ASRClient {
	return h.asrClient
}

// LLMClient 返回当前关联的大语言模型客户端。
func (h *Handler) LLMClient() ai.LLMClient {
	return h.llmClient
}

// TTSClient 返回当前关联的流式语音合成客户端。
func (h *Handler) TTSClient() ai.TTSClient {
	return h.ttsClient
}

// Close 优雅关闭会话处理器及其关联的注册表。
func (h *Handler) Close(ctx context.Context) error {
	if h.registry != nil {
		return h.registry.Shutdown(ctx)
	}
	return nil
}

// Shutdown 优雅关闭会话处理器及其关联的注册表。
func (h *Handler) Shutdown(ctx context.Context) error {
	return h.Close(ctx)
}

// ServeHTTP 校验 HTTP 认证并执行会话准入控制与 WebSocket 升级。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 校验请求路径（精确匹配 /xiaozhi/v1/）
	if r.URL.Path != WebSocketPath {
		http.NotFound(w, r)
		return
	}

	// 2. 升级前认证与请求头校验
	sharedToken := ""
	maxHeaderBytes := MaxSingleHeaderBytes
	if h.cfg != nil {
		sharedToken = h.cfg.DeviceSharedToken
		if h.cfg.Server.MaxHTTPHeaderBytes > 0 {
			maxHeaderBytes = h.cfg.Server.MaxHTTPHeaderBytes
		}
	}

	clientInfo, err := AuthenticateUpgrade(r, sharedToken, maxHeaderBytes)
	if err != nil {
		LogAuthRejection(h.logger, r, err)
		http.Error(w, err.Error(), HTTPStatus(err))
		return
	}

	// 3. 活跃会话并发准入控制（满载或停服时拒绝升级并返回 503）
	release, ok := h.registry.Acquire()
	if !ok {
		h.logger.Warn("websocket upgrade rejected: max concurrent sessions reached or shutting down",
			"device_id", logger.TruncateString(clientInfo.DeviceID),
			"client_id", logger.TruncateString(clientInfo.ClientID),
			"serial_number", logger.TruncateString(clientInfo.SerialNumber),
			"active_sessions", h.registry.Limiter().ActiveCount(),
			"max_sessions", h.registry.Limiter().MaxSessions(),
		)
		http.Error(w, "service unavailable: max concurrent sessions reached", http.StatusServiceUnavailable)
		return
	}

	// 4. 执行 WebSocket 协议升级，显式禁用压缩
	opts := &websocket.AcceptOptions{
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		release()
		h.logger.Error("websocket upgrade failed",
			"error", err,
			"device_id", logger.TruncateString(clientInfo.DeviceID),
			"client_id", logger.TruncateString(clientInfo.ClientID),
			"serial_number", logger.TruncateString(clientInfo.SerialNumber),
		)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "session closed")

	h.logger.Info("websocket session connected",
		"device_id", logger.TruncateString(clientInfo.DeviceID),
		"client_id", logger.TruncateString(clientInfo.ClientID),
		"serial_number", logger.TruncateString(clientInfo.SerialNumber),
		"active_sessions", h.registry.Limiter().ActiveCount(),
	)

	// 5. 构造会话并注册到会话注册表，统一注销与名额释放顺序
	sess := NewSession(r.Context(), conn, clientInfo, h.cfg, h.asrClient, h.llmClient, h.ttsClient, h.logger)
	unregister, registered := h.registry.Register(sess, release)
	if !registered {
		release()
		sess.Close()
		return
	}
	defer unregister()

	// 6. 移交会话监督流程处理状态机生命周期，直至客户端断开或上下文取消
	_ = sess.Run()

	h.logger.Info("websocket session closed",
		"device_id", logger.TruncateString(clientInfo.DeviceID),
		"client_id", logger.TruncateString(clientInfo.ClientID),
		"serial_number", logger.TruncateString(clientInfo.SerialNumber),
	)
}

// serveConn 创建、注册并运行会话状态机（供测试或独立连接驱动调用）。
func (h *Handler) serveConn(ctx context.Context, conn *websocket.Conn, info *ClientHeaderInfo) {
	sess := NewSession(ctx, conn, info, h.cfg, h.asrClient, h.llmClient, h.ttsClient, h.logger)
	unregister, registered := h.registry.Register(sess)
	if !registered {
		sess.Close()
		return
	}
	defer unregister()

	_ = sess.Run()
}
