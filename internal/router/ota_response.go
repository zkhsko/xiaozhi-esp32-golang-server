package router

import (
	"encoding/json"
	"net/http"
)

const (
	// ProtocolVersion 固定协议版本号。
	ProtocolVersion = 1
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
