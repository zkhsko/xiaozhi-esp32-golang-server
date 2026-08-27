package router

import (
	"errors"
	"net/http"

	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// handleOTASerialNumber 处理包含 SerialNumber 的设备 OTA 检查/配置发现请求。
// 业务流程：
// 1. 若配置了数据库且包含 SerialNumber，查询 device_activation 表激活状态：
//   - 若设备存在且正常激活：下发可用 WebSocket 连接配置及服务器时间；
//   - 若设备被冻结或撤销：拒绝访问并返回 403 Forbidden；
//   - 若设备未找到：继续下发 6 位随机激活码与 challenge；
//
// 2. 若设备不存在或未配置数据库：
//   - 引入 ttlcache/v3 缓存待激活信息；
//   - 返回 6 位数字激活码、Challenge 挑战值与绑定提示信息。
func (h *OTAHandler) handleOTASerialNumber(w http.ResponseWriter, r *http.Request, headers DeviceHeaders, body []byte) {
	if h.db != nil && headers.SerialNumber != "" {
		act, err := h.db.FindDeviceActivationBySerialNumber(r.Context(), headers.SerialNumber)
		if err != nil {
			if !errors.Is(err, database.ErrActivationNotFound) {
				h.logger.Error("failed to query device activation by serial number",
					"serial_number", logger.TruncateString(headers.SerialNumber),
					"error", err,
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			h.logger.Info("device activation not found, issuing activation code",
				"serial_number", logger.TruncateString(headers.SerialNumber),
				"device_id", logger.TruncateString(headers.DeviceID),
			)
		} else {
			if !act.IsActive() {
				h.logger.Warn("device activation blocked",
					"serial_number", logger.TruncateString(headers.SerialNumber),
					"status", act.ActivationStatus,
				)
				http.Error(w, "device activation is frozen or revoked", http.StatusForbidden)
				return
			}

			h.logger.Info("device activation found",
				"serial_number", logger.TruncateString(act.SerialNumber),
				"device_id", logger.TruncateString(act.DeviceID),
				"client_id", logger.TruncateString(act.ClientID),
				"activation_status", act.ActivationStatus,
				"activated_at", act.ActivatedAt,
			)

			var wsURL, token string
			if h.cfg != nil {
				wsURL = h.cfg.Server.WebSocketURL
				token = h.cfg.DeviceSharedToken
			}

			resp := Response{
				ServerTime: currentServerTime(),
				WebSocket: &WebSocketConfig{
					URL:     wsURL,
					Token:   token,
					Version: ProtocolVersion,
				},
			}

			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	// 设备不存在（未激活）或未配置数据库时，生成新的 6 位激活码并写入内存缓存
	pending, err := h.createPendingActivation(headers.SerialNumber, headers.DeviceID, headers.ClientID)
	if err != nil {
		h.logger.Error("failed to create pending activation",
			"serial_number", logger.TruncateString(headers.SerialNumber),
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	resp := Response{
		ServerTime: currentServerTime(),
		Activation: &ActivationInfo{
			Code:      pending.Code,
			Challenge: pending.Challenge,
			Message:   DefaultActivationMessage,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}
