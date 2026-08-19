package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

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

	// 5. 保持连接并处理消息生命周期，直至客户端断开或上下文取消
	h.serveConn(r.Context(), conn, clientInfo)

	h.logger.Info("websocket session closed",
		"device_id", logger.TruncateString(clientInfo.DeviceID),
		"client_id", logger.TruncateString(clientInfo.ClientID),
		"serial_number", logger.TruncateString(clientInfo.SerialNumber),
	)
}

// serveConn 处理已建立连接的 hello 握手与消息循环生命周期。
func (h *Handler) serveConn(ctx context.Context, conn *websocket.Conn, info *ClientHeaderInfo) {
	helloTimeout := DefaultHelloTimeout
	maxWSTextBytes := int64(DefaultMaxWSTextMessageBytes)
	if h.cfg != nil {
		if h.cfg.Session.HelloTimeout > 0 {
			helloTimeout = h.cfg.Session.HelloTimeout
		}
		if h.cfg.Session.MaxWSTextMessageBytes > 0 {
			maxWSTextBytes = h.cfg.Session.MaxWSTextMessageBytes
		}
	}
	conn.SetReadLimit(maxWSTextBytes)

	sessionID, err := h.handleHelloHandshake(ctx, conn, info, helloTimeout)
	if err != nil {
		return
	}

	// 握手成功后进入正常会话消息循环
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			var closeErr websocket.CloseError
			if errors.As(err, &closeErr) {
				h.logger.Info("websocket session disconnected by client",
					"session_id", sessionID,
					"status_code", closeErr.Code,
					"reason", closeErr.Reason,
				)
				return
			}
			if !errors.Is(err, context.Canceled) {
				h.logger.Warn("websocket read error",
					"session_id", sessionID,
					"error", err,
				)
			}
			return
		}

		if msgType == websocket.MessageText {
			var header genericMessageHeader
			if err := json.Unmarshal(data, &header); err == nil {
				if header.Type == MessageTypeHello {
					h.logger.Warn("duplicate hello received after handshake",
						"session_id", sessionID,
						"device_id", logger.TruncateString(info.DeviceID),
					)
					_ = conn.Close(websocket.StatusPolicyViolation, ErrDuplicateHello.Error())
					return
				}
			}
		}
	}
}

// handleHelloHandshake 执行客户端 hello 首包读取、严格字段校验与服务端 hello 响应下发。
func (h *Handler) handleHelloHandshake(ctx context.Context, conn *websocket.Conn, info *ClientHeaderInfo, timeout time.Duration) (string, error) {
	// 启动 hello 超时定时器，超时未收到首包则以 StatusPolicyViolation 主动关闭连接
	timer := time.AfterFunc(timeout, func() {
		h.logger.Warn("hello handshake timeout",
			"device_id", logger.TruncateString(info.DeviceID),
			"client_id", logger.TruncateString(info.ClientID),
			"serial_number", logger.TruncateString(info.SerialNumber),
			"timeout", timeout,
		)
		_ = conn.Close(websocket.StatusPolicyViolation, "hello handshake timeout")
	})
	defer timer.Stop()

	msgType, data, err := conn.Read(ctx)
	if err != nil {
		var closeErr websocket.CloseError
		if errors.As(err, &closeErr) {
			h.logger.Warn("websocket closed during hello handshake",
				"status_code", closeErr.Code,
				"reason", closeErr.Reason,
				"device_id", logger.TruncateString(info.DeviceID),
				"client_id", logger.TruncateString(info.ClientID),
				"serial_number", logger.TruncateString(info.SerialNumber),
			)
			return "", err
		}

		h.logger.Warn("failed to read hello message",
			"error", err,
			"device_id", logger.TruncateString(info.DeviceID),
			"client_id", logger.TruncateString(info.ClientID),
			"serial_number", logger.TruncateString(info.SerialNumber),
		)
		_ = conn.Close(websocket.StatusPolicyViolation, "failed to read hello")
		return "", err
	}

	// 1. 首包必须为文本消息
	if msgType != websocket.MessageText {
		h.logger.Warn("first message is not text hello",
			"message_type", msgType.String(),
			"device_id", logger.TruncateString(info.DeviceID),
			"client_id", logger.TruncateString(info.ClientID),
			"serial_number", logger.TruncateString(info.SerialNumber),
		)
		_ = conn.Close(websocket.StatusUnsupportedData, "first message must be text hello")
		return "", ErrBinaryFirstMessage
	}

	// 2. 解析 JSON
	var clientHello ClientHelloMessage
	if err := json.Unmarshal(data, &clientHello); err != nil {
		h.logger.Warn("invalid json in hello message",
			"error", err,
			"device_id", logger.TruncateString(info.DeviceID),
			"client_id", logger.TruncateString(info.ClientID),
			"serial_number", logger.TruncateString(info.SerialNumber),
		)
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid json in hello message")
		return "", err
	}

	// 3. 严格校验客户端 hello 各字段
	if err := ValidateClientHello(&clientHello); err != nil {
		h.logger.Warn("invalid hello message fields",
			"error", err,
			"device_id", logger.TruncateString(info.DeviceID),
			"client_id", logger.TruncateString(info.ClientID),
			"serial_number", logger.TruncateString(info.SerialNumber),
		)
		_ = conn.Close(websocket.StatusPolicyViolation, err.Error())
		return "", err
	}

	// 4. 生成加密安全的会话 ID
	sessionID, err := GenerateSessionID()
	if err != nil {
		h.logger.Error("failed to generate session id",
			"error", err,
			"device_id", logger.TruncateString(info.DeviceID),
			"client_id", logger.TruncateString(info.ClientID),
			"serial_number", logger.TruncateString(info.SerialNumber),
		)
		_ = conn.Close(websocket.StatusInternalError, "internal server error")
		return "", err
	}

	// 5. 下发服务端 hello 响应
	serverHello := NewServerHello(sessionID)
	respBytes, err := json.Marshal(serverHello)
	if err != nil {
		h.logger.Error("failed to marshal server hello",
			"error", err,
			"session_id", sessionID,
		)
		_ = conn.Close(websocket.StatusInternalError, "internal server error")
		return "", err
	}

	if err := conn.Write(ctx, websocket.MessageText, respBytes); err != nil {
		h.logger.Warn("failed to write server hello",
			"error", err,
			"session_id", sessionID,
			"device_id", logger.TruncateString(info.DeviceID),
			"client_id", logger.TruncateString(info.ClientID),
			"serial_number", logger.TruncateString(info.SerialNumber),
		)
		return "", err
	}

	h.logger.Info("websocket hello handshake succeeded",
		"session_id", sessionID,
		"device_id", logger.TruncateString(info.DeviceID),
		"client_id", logger.TruncateString(info.ClientID),
		"serial_number", logger.TruncateString(info.SerialNumber),
	)

	return sessionID, nil
}
