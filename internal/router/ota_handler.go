package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"xiaozhi-esp32-golang-server/internal/logger"
)

// handleOTA 处理设备 OTA 配置检查/版本发现入口，根据请求头中的 Serial-Number 分流。
func (h *Handler) handleOTA(w http.ResponseWriter, r *http.Request) {
	headers, body, ok := h.readAndValidateOTARequest(w, r)
	if !ok {
		return
	}

	h.logger.Info("ota check request received",
		"method", r.Method,
		"path", r.URL.Path,
		"device_id", logger.TruncateString(headers.DeviceID),
		"client_id", logger.TruncateString(headers.ClientID),
		"serial_number", logger.TruncateString(headers.SerialNumber),
		"activation_version", logger.TruncateString(headers.ActivationVersion),
		"user_agent", logger.TruncateString(headers.UserAgent),
	)

	// 根据请求头是否包含 SerialNumber 分流到对应处理框架
	if headers.SerialNumber != "" {
		h.handleOTASerialNumber(w, r, headers, body)
		return
	}

	h.handleOTALegacy(w, r, headers, body)
}

// readAndValidateOTARequest 执行请求头长度、正文大小及 JSON 格式的基础校验。
func (h *Handler) readAndValidateOTARequest(w http.ResponseWriter, r *http.Request) (DeviceHeaders, []byte, bool) {
	// 1. 校验请求头长度限制
	maxHeaderBytes := MaxSingleHeaderBytes
	maxTotalHeaderBytes := MaxTotalHeaderBytes
	if h.cfg != nil && h.cfg.Server.MaxHTTPHeaderBytes > 0 {
		maxTotalHeaderBytes = h.cfg.Server.MaxHTTPHeaderBytes
	}
	if err := validateHeaders(r.Header, maxHeaderBytes, maxTotalHeaderBytes); err != nil {
		http.Error(w, "request header fields too large", http.StatusBadRequest)
		return DeviceHeaders{}, nil, false
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
			return DeviceHeaders{}, nil, false
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return DeviceHeaders{}, nil, false
	}

	// 3. 非空正文必须是有效 JSON
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) > 0 {
		if !json.Valid(trimmedBody) {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return DeviceHeaders{}, nil, false
		}
	}

	headers := extractDeviceHeaders(r)
	return headers, trimmedBody, true
}
