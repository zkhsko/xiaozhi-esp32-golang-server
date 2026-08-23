package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"xiaozhi-esp32-golang-server/internal/logger"
)

// handleOTA 处理设备配置发现 HTTP 请求并返回 WebSocket 连接凭据与配置。
func (h *Handler) handleOTA(w http.ResponseWriter, r *http.Request) {
	// 1. 校验请求头长度限制
	maxHeaderBytes := MaxSingleHeaderBytes
	maxTotalHeaderBytes := MaxTotalHeaderBytes
	if h.cfg != nil && h.cfg.Server.MaxHTTPHeaderBytes > 0 {
		maxTotalHeaderBytes = h.cfg.Server.MaxHTTPHeaderBytes
	}
	if err := validateHeaders(r.Header, maxHeaderBytes, maxTotalHeaderBytes); err != nil {
		http.Error(w, "request header fields too large", http.StatusBadRequest)
		return
	}

	// 2. 校验并读取请求正文
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

	// 3. 记录有限诊断日志（绝不记录 Token，请求头字段自动截断）
	h.logger.Info("bootstrap request received",
		"method", r.Method,
		"path", r.URL.Path,
		"device_id", logger.TruncateString(r.Header.Get("Device-Id")),
		"client_id", logger.TruncateString(r.Header.Get("Client-Id")),
		"serial_number", logger.TruncateString(r.Header.Get("Serial-Number")),
		"activation_version", logger.TruncateString(r.Header.Get("Activation-Version")),
		"user_agent", logger.TruncateString(r.UserAgent()),
	)

	// 4. 构造配置响应（仅包含 websocket 对象，无任何冗余占位字段）
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

	writeJSON(w, http.StatusOK, resp)
}
