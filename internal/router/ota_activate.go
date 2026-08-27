package router

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// ActivateRequest 映射设备向 POST /xiaozhi/ota/activate 提交的激活请求体。
type ActivateRequest struct {
	Algorithm    string `json:"algorithm"`
	SerialNumber string `json:"serial_number"`
	Challenge    string `json:"challenge"`
	HMAC         string `json:"hmac"`
}

// handleActivate 处理 POST /xiaozhi/ota/activate 接口请求。
// 业务流程：
// 1. 读取并校验 HTTP 请求头与 JSON 请求体；
// 2. 查 device_hmac_credential 表确认 hmac：
//   - 根据 serial_number 查询凭证记录，验证凭证存在且可用；
//   - 使用凭证中的密钥与 challenge 计算 expected HMAC-SHA256，与设备提交的 HMAC 做常量时间比对；
//   - 校验成功后清理一次性 Challenge 缓存；
//
// 3. 确认是否已经激活过，如果没有直接插入，如果已经激活更新这条数据并删除绑定的用户记录：
//   - 调用 ActivateDeviceBySerialNumber 事务方法完成激活关系写入/更新及旧用户绑定解绑；
//   - 将凭证状态更新为 activated；
//
// 4. 返回 200 OK 与服务端时间信息。
func (h *OTAHandler) handleActivate(w http.ResponseWriter, r *http.Request) {
	headers, body, ok := h.readAndValidateOTARequest(w, r)
	if !ok {
		return
	}

	if len(body) == 0 {
		http.Error(w, "request body cannot be empty", http.StatusBadRequest)
		return
	}

	var req ActivateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	serialNumber := strings.TrimSpace(req.SerialNumber)
	if serialNumber == "" {
		serialNumber = headers.SerialNumber
	}
	if serialNumber == "" {
		http.Error(w, "serial_number is required", http.StatusBadRequest)
		return
	}

	challenge := strings.TrimSpace(req.Challenge)
	if challenge == "" {
		http.Error(w, "challenge is required", http.StatusBadRequest)
		return
	}

	hmacHex := strings.TrimSpace(req.HMAC)
	if hmacHex == "" {
		http.Error(w, "hmac is required", http.StatusBadRequest)
		return
	}

	if h.db == nil {
		h.logger.Error("database is not configured for activation")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 1. 查 device_hmac_credential 表确认 hmac
	cred, err := h.db.FindDeviceHmacCredentialBySerialNumber(r.Context(), serialNumber)
	if err != nil {
		if errors.Is(err, database.ErrCredentialNotFound) {
			h.logger.Warn("device hmac credential not found",
				"serial_number", logger.TruncateString(serialNumber),
			)
			http.Error(w, "device credential not found", http.StatusForbidden)
			return
		}
		h.logger.Error("failed to query device hmac credential",
			"serial_number", logger.TruncateString(serialNumber),
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if !cred.IsAvailable() {
		h.logger.Warn("device hmac credential is not available",
			"serial_number", logger.TruncateString(serialNumber),
			"status", cred.CredentialStatus,
		)
		http.Error(w, "device credential is blocked or revoked", http.StatusForbidden)
		return
	}

	mac := hmac.New(sha256.New, cred.HMACKeyCiphertext)
	mac.Write([]byte(challenge))
	expectedHMAC := mac.Sum(nil)

	receivedHMAC, err := hex.DecodeString(hmacHex)
	if err != nil || !hmac.Equal(expectedHMAC, receivedHMAC) {
		h.logger.Warn("hmac challenge verification failed",
			"serial_number", logger.TruncateString(serialNumber),
		)
		http.Error(w, "hmac verification failed", http.StatusForbidden)
		return
	}

	// 校验成功后清理 Challenge 缓存
	if h.challengeCache != nil {
		if pending, ok := h.FindPendingActivationByChallenge(challenge); ok {
			h.challengeCache.Delete(challenge)
			if h.codeCache != nil && pending.Code != "" {
				h.codeCache.Delete(pending.Code)
			}
		}
	}

	// 2. 确认是否已经激活过，如果没有直接插入，如果已经激活更新这条数据并删除绑定的用户记录
	deviceID := headers.DeviceID
	clientID := headers.ClientID
	authSN := cred.SerialNumber

	act, err := h.db.ActivateDeviceBySerialNumber(r.Context(), authSN, deviceID, clientID)
	if err != nil {
		h.logger.Error("failed to activate device",
			"serial_number", logger.TruncateString(authSN),
			"device_id", logger.TruncateString(deviceID),
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if cred.CredentialStatus == database.CredentialStatusEnabled {
		_ = h.db.UpdateDeviceHmacCredentialStatus(r.Context(), authSN, database.CredentialStatusActivated)
	}

	h.logger.Info("device activated successfully via hmac challenge",
		"serial_number", logger.TruncateString(act.SerialNumber),
		"device_id", logger.TruncateString(act.DeviceID),
		"client_id", logger.TruncateString(act.ClientID),
		"activation_status", act.ActivationStatus,
	)

	resp := Response{
		ServerTime: currentServerTime(),
	}
	writeJSON(w, http.StatusOK, resp)
}
