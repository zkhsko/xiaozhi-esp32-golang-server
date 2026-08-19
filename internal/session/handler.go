package session

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// Handler 处理 WebSocket 协议升级、会话准入控制与连接生命周期。
type Handler struct {
	cfg     *config.Config
	limiter *SessionLimiter
	logger  *slog.Logger
}

// NewHandler 创建配置就绪的 WebSocket HTTP 升级处理器。
func NewHandler(cfg *config.Config, limiter *SessionLimiter, l *slog.Logger) *Handler {
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
	return &Handler{
		cfg:     cfg,
		limiter: limiter,
		logger:  l,
	}
}

// Limiter 返回当前关联的会话准入控制器。
func (h *Handler) Limiter() *SessionLimiter {
	return h.limiter
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

	// 3. 活跃会话并发准入控制（满载时拒绝升级并返回 503）
	release, ok := h.limiter.TryAcquire()
	if !ok {
		h.logger.Warn("websocket upgrade rejected: max concurrent sessions reached",
			"device_id", logger.TruncateString(clientInfo.DeviceID),
			"client_id", logger.TruncateString(clientInfo.ClientID),
			"serial_number", logger.TruncateString(clientInfo.SerialNumber),
			"active_sessions", h.limiter.ActiveCount(),
			"max_sessions", h.limiter.MaxSessions(),
		)
		http.Error(w, "service unavailable: max concurrent sessions reached", http.StatusServiceUnavailable)
		return
	}
	defer release()

	// 4. 执行 WebSocket 协议升级，显式禁用压缩
	opts := &websocket.AcceptOptions{
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
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
		"active_sessions", h.limiter.ActiveCount(),
	)

	// 5. 移交会话监督流程处理状态机生命周期，直至客户端断开或上下文取消
	h.serveConn(r.Context(), conn, clientInfo)

	h.logger.Info("websocket session closed",
		"device_id", logger.TruncateString(clientInfo.DeviceID),
		"client_id", logger.TruncateString(clientInfo.ClientID),
		"serial_number", logger.TruncateString(clientInfo.SerialNumber),
	)
}

// serveConn 创建并运行会话状态机。
func (h *Handler) serveConn(ctx context.Context, conn *websocket.Conn, info *ClientHeaderInfo) {
	sess := NewSession(ctx, conn, info, h.cfg, h.logger)
	_ = sess.Run()
}
