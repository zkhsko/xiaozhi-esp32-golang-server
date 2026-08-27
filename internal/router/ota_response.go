package router

import (
	"encoding/json"
	"net/http"
	"time"
)

const (
	// ProtocolVersion 固定协议版本号。
	ProtocolVersion = 1
)

// ServerTimeInfo 定义服务端下发的时间同步信息。
type ServerTimeInfo struct {
	Timestamp      int64 `json:"timestamp"`                 // UTC 毫秒时间戳
	TimezoneOffset int   `json:"timezone_offset,omitempty"` // 时区偏移量（分钟），例如东八区为 480
}

// WebSocketConfig 定义返回给设备的 WebSocket 连接配置。
type WebSocketConfig struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	Version int    `json:"version"`
}

// ActivationInfo 定义设备激活相关的响应字段。
type ActivationInfo struct {
	Code      string `json:"code,omitempty"`
	Challenge string `json:"challenge,omitempty"`
	Message   string `json:"message,omitempty"`
}

// FirmwareInfo 定义固件升级相关的响应字段。
type FirmwareInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Force   int    `json:"force"`
}

// Response 定义配置发现响应结构。
type Response struct {
	ServerTime *ServerTimeInfo  `json:"server_time,omitempty"`
	WebSocket  *WebSocketConfig `json:"websocket,omitempty"`
	Activation *ActivationInfo  `json:"activation,omitempty"`
	Firmware   *FirmwareInfo    `json:"firmware,omitempty"`
}

// currentServerTime 获取当前服务端时间信息。
func currentServerTime() *ServerTimeInfo {
	now := time.Now()
	_, offsetSec := now.Zone()
	return &ServerTimeInfo{
		Timestamp:      now.UnixMilli(),
		TimezoneOffset: offsetSec / 60,
	}
}

// writeJSON 将数据序列化为 JSON 并写入 HTTP 响应。
func writeJSON(w http.ResponseWriter, statusCode int, data any) {
	respData, err := json.Marshal(data)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write(respData)
}
