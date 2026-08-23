package router

import (
	"net/http"
)

// handleOTASerialNumber 处理包含 SerialNumber 的设备 OTA 检查/配置发现请求框架。
// 业务流程（待接入具体业务）：
// 1. 校验请求参数（SerialNumber, Device-Id, Client-Id 等）；
// 2. 根据 SerialNumber 查询 device_hmac_credential 凭证记录并校验状态（不可用/未激活时下发 challenge 挑战字符串）；
// 3. 按权威 SN 加锁查询 device_activation 绑定记录，校验 device_id / client_id 绑定关系；
// 4. 已激活设备返回 WebSocket 连接配置及固件升级信息。
func (h *Handler) handleOTASerialNumber(w http.ResponseWriter, r *http.Request, headers DeviceHeaders, body []byte) {
	// 框架占位：当前默认返回可用 WebSocket 配置
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
