package router

import (
	"errors"
	"net/http"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// handleOTASerialNumber 处理包含 SerialNumber 的设备 OTA 检查/配置发现请求。
// 业务流程：
// 1. 若配置了数据库且包含 SerialNumber，查询 device_activation 表激活状态：
//   - 若设备被冻结或撤销：拒绝访问并返回 403 Forbidden；
//   - 若设备存在且正常激活：
//   - 进一步查询 device_user_ref 表用户绑定关系：
//   - 若未绑定用户：生成 6 位随机激活码，返回 code 和 message；
//   - 若已绑定用户：下发可用 WebSocket 连接配置及服务器时间，正常返回；
//   - 若设备未找到（激活表中不存在记录）：生成 Challenge 挑战值，仅返回 challenge；
//
// 2. 若未配置数据库：按未激活处理，生成 Challenge 挑战值，仅返回 challenge。
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
			h.logger.Info("device activation not found, issuing challenge only",
				"serial_number", logger.TruncateString(headers.SerialNumber),
				"device_id", logger.TruncateString(headers.DeviceId),
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

			// 查询设备与用户绑定关系
			userRef, err := h.db.FindDeviceUserRefBySerialNumber(r.Context(), headers.SerialNumber)
			if err != nil {
				if !errors.Is(err, database.ErrBindingNotFound) {
					h.logger.Error("failed to query device user binding by serial number",
						"serial_number", logger.TruncateString(headers.SerialNumber),
						"error", err,
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
					return
				}

				// 设备已激活但未绑定用户：生成 6 位激活码并返回 code 和 message
				deviceId := headers.DeviceId
				if deviceId == "" {
					deviceId = act.DeviceId
				}
				clientId := headers.ClientId
				if clientId == "" {
					clientId = act.ClientId
				}

				pending, err := h.createPendingActivation(headers.SerialNumber, deviceId, clientId)
				if err != nil {
					h.logger.Error("failed to create pending binding code",
						"serial_number", logger.TruncateString(headers.SerialNumber),
						"error", err,
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
					return
				}

				h.logger.Info("device activated but unbound to user, issuing binding code",
					"serial_number", logger.TruncateString(headers.SerialNumber),
					"device_id", logger.TruncateString(deviceId),
					"code", logger.TruncateString(pending.Code),
				)

				resp := Response{
					ServerTime: currentServerTime(),
					Activation: &ActivationInfo{
						Code:    pending.Code,
						Message: DefaultActivationMessage,
					},
				}

				writeJSON(w, http.StatusOK, resp)
				return
			}

			// 设备已激活且已绑定用户：正常返回 WebSocket 配置
			h.logger.Info("device activation and user binding verified",
				"serial_number", logger.TruncateString(act.SerialNumber),
				"device_id", logger.TruncateString(act.DeviceId),
				"user_id", userRef.UserId,
				"activation_status", act.ActivationStatus,
				"activated_at", act.ActivatedAt,
			)

			var wsURL, token string
			if h.cfg != nil {
				wsURL = h.cfg.Server.WebSocketURL
			}

			// 查询 device_access_token 表：若存在未展示过的 Token，则仅在第一次请求校验全部通过后展示一次，随后更新标记为已展示
			tok, err := h.db.FindDeviceAccessTokenBySerialNumber(r.Context(), headers.SerialNumber)
			if err == nil && tok != nil && tok.IsValid(time.Now()) {
				if !tok.HasExposed {
					token = tok.AccessToken
					if markErr := h.db.UpdateDeviceAccessTokenHasExposed(r.Context(), headers.SerialNumber, true); markErr != nil {
						h.logger.Error("failed to mark device access token as exposed",
							"serial_number", logger.TruncateString(headers.SerialNumber),
							"error", markErr,
						)
					}
				}
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

	// 设备未激活（激活表中不存在记录）或未配置数据库：生成 challenge 挑战值并仅返回 challenge
	pending, err := h.createPendingActivation(headers.SerialNumber, headers.DeviceId, headers.ClientId)
	if err != nil {
		h.logger.Error("failed to create pending challenge",
			"serial_number", logger.TruncateString(headers.SerialNumber),
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	resp := Response{
		ServerTime: currentServerTime(),
		Activation: &ActivationInfo{
			Challenge: pending.Challenge,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}
