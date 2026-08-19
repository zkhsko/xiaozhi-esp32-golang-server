package bootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// OTA 与配置发现协议相关的常量定义。
const (
	// DefaultMaxBodyBytes 默认请求体上限（64 KiB）。
	DefaultMaxBodyBytes = 64 * 1024

	// MaxSingleHeaderBytes 单个请求头键或值的最大长度（1024 字符）。
	MaxSingleHeaderBytes = 1024

	// MaxTotalHeaderBytes 所有请求头键值对累计最大长度（8192 字符）。
	MaxTotalHeaderBytes = 8192

	// ProtocolVersion 固定协议版本号。
	ProtocolVersion = 1

	// OTAPath 配置发现的固定路由路径。
	OTAPath = "/xiaozhi/ota/"
)

// WebSocketConfig 定义返回给设备的 WebSocket 连接配置。
type WebSocketConfig struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	Version int    `json:"version"`
}

// Response 定义配置发现成功的响应结构。
type Response struct {
	WebSocket WebSocketConfig `json:"websocket"`
}

// Handler 处理设备配置发现 HTTP 请求。
type Handler struct {
	cfg    *config.Config
	logger *slog.Logger
}

// NewHandler 创建配置发现 HTTP 请求处理器。
func NewHandler(cfg *config.Config, l *slog.Logger) *Handler {
	if l == nil {
		l = slog.Default()
	}
	return &Handler{
		cfg:    cfg,
		logger: l,
	}
}

// ServeHTTP 处理设备配置发现请求并返回 WebSocket 连接凭据与配置。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 校验 HTTP 方法，仅允许 GET 与 POST
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 2. 校验请求路径（精确匹配 /xiaozhi/ota/）
	if r.URL.Path != OTAPath {
		http.NotFound(w, r)
		return
	}

	// 3. 校验请求头长度限制
	maxHeaderBytes := MaxSingleHeaderBytes
	maxTotalHeaderBytes := MaxTotalHeaderBytes
	if h.cfg != nil && h.cfg.Server.MaxHTTPHeaderBytes > 0 {
		maxTotalHeaderBytes = h.cfg.Server.MaxHTTPHeaderBytes
	}
	if err := validateHeaders(r.Header, maxHeaderBytes, maxTotalHeaderBytes); err != nil {
		http.Error(w, "request header fields too large", http.StatusBadRequest)
		return
	}

	// 4. 校验并读取请求正文
	maxBodyBytes := int64(DefaultMaxBodyBytes)
	if h.cfg != nil && h.cfg.Server.MaxHTTPBodyBytes > 0 {
		maxBodyBytes = h.cfg.Server.MaxHTTPBodyBytes
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 非空正文必须是有效 JSON
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) > 0 {
		if !json.Valid(trimmedBody) {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
	}

	// 5. 记录有限诊断日志（绝不记录 Token，请求头字段自动截断）
	h.logger.Info("bootstrap request received",
		"method", r.Method,
		"path", r.URL.Path,
		"device_id", logger.TruncateString(r.Header.Get("Device-Id")),
		"client_id", logger.TruncateString(r.Header.Get("Client-Id")),
		"serial_number", logger.TruncateString(r.Header.Get("Serial-Number")),
		"activation_version", logger.TruncateString(r.Header.Get("Activation-Version")),
		"user_agent", logger.TruncateString(r.UserAgent()),
	)

	// 6. 构造配置响应（仅包含 websocket 对象，无任何冗余占位字段）
	var wsURL, token string
	if h.cfg != nil {
		wsURL = h.cfg.Server.WebSocketURL
		token = h.cfg.DeviceSharedToken
	}

	resp := Response{
		WebSocket: WebSocketConfig{
			URL:     wsURL,
			Token:   token,
			Version: ProtocolVersion,
		},
	}

	respData, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respData)
}

// validateHeaders 校验请求头键值是否超出单项与总计长度上限。
func validateHeaders(headers http.Header, maxSingle, maxTotal int) error {
	totalLen := 0
	for key, values := range headers {
		if len(key) > maxSingle {
			return errors.New("header key exceeds limit")
		}
		totalLen += len(key)
		for _, val := range values {
			if len(val) > maxSingle {
				return errors.New("header value exceeds limit")
			}
			totalLen += len(val)
			if totalLen > maxTotal {
				return errors.New("total headers size exceeds limit")
			}
		}
	}
	return nil
}
