package router

import (
	"errors"
	"net/http"

	"xiaozhi-esp32-golang-server/internal/database"
)

// handleOTASerialNumber 处理包含 SerialNumber 的设备 OTA 检查/配置发现请求框架。
// 业务流程：
// 1. 若配置了数据库且包含 SerialNumber，查询 device_activation 表激活状态并输出；
// 2. 当前下发可用 WebSocket 连接配置及服务器时间。
func (h *OTAHandler) handleOTASerialNumber(w http.ResponseWriter, r *http.Request, headers DeviceHeaders, body []byte) {
	if h.db != nil && headers.SerialNumber != "" {
		act, err := h.db.FindDeviceActivationBySerialNumber(r.Context(), headers.SerialNumber)
		if err != nil {
			if errors.Is(err, database.ErrActivationNotFound) {
				h.logger.Info("device activation not found",
					"serial_number", headers.SerialNumber,
				)
			} else {
				h.logger.Error("failed to query device activation by serial number",
					"serial_number", headers.SerialNumber,
					"error", err,
				)
			}
		} else {
			h.logger.Info("device activation found",
				"serial_number", act.SerialNumber,
				"device_id", act.DeviceID,
				"client_id", act.ClientID,
				"activation_status", act.ActivationStatus,
				"activated_at", act.ActivatedAt,
			)
		}
	}

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
