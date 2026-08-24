package router

import (
	"net/http"
)

// handleOTALegacy 处理不含 SerialNumber 的设备（Legacy 设备）OTA 检查/配置发现请求框架。
// 业务流程（待接入具体业务）：
// 1. 校验 Device-Id（MAC 地址作为后端设备唯一标识）与 Client-Id（安装实例标识）；
// 2. 查询设备激活记录：
//   - 若未激活：生成 6 位一次性激活码并下发 activation.code 与 message；
//   - 若已激活：首次下发独立 Token，后续轮询省略 Token 返回 WebSocket 配置。
func (h *Handler) handleOTALegacy(w http.ResponseWriter, r *http.Request, headers DeviceHeaders, body []byte) {
	// 框架占位：当前默认返回可用 WebSocket 配置及服务器时间
	var wsURL, token string
	if h.cfg != nil {
		wsURL = h.cfg.Server.WebSocketURL
		token = h.cfg.DeviceSharedToken
	}

	resp := Response{
		ServerTime: currentServerTime(),
		WebSocket: WebSocketConfig{
			URL:     wsURL,
			Token:   token,
			Version: ProtocolVersion,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}
