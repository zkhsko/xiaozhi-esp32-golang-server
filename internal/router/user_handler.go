package router

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

const (
	// MockCurrentUserID 模拟当前登录用户 ID（暂无登录系统）。
	MockCurrentUserID uint64 = 1
)

// BindDeviceRequest 映射 POST /user-api/device/bind 提交的请求体与参数。
type BindDeviceRequest struct {
	Code         string `json:"code"`
	SN           string `json:"sn"`
	SerialNumber string `json:"serial_number"` // 兼容别名
	HMAC         string `json:"hmac"`
}

// BindDeviceResponse 映射绑定设备成功后的响应结构。
type BindDeviceResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	UserID       uint64 `json:"user_id,omitempty"`
}

// UserHandler 处理 /user-api/ 业务接口。
type UserHandler struct {
	cfg        *config.Config
	db         *database.Database
	otaHandler *OTAHandler
	logger     *slog.Logger
}

// NewUserHandler 创建 UserHandler 实例。
func NewUserHandler(cfg *config.Config, db *database.Database, otaHandler *OTAHandler, l *slog.Logger) *UserHandler {
	if l == nil {
		l = slog.Default()
	}
	return &UserHandler{
		cfg:        cfg,
		db:         db,
		otaHandler: otaHandler,
		logger:     l,
	}
}

// Routes 注册 /user-api 路由。
func (h *UserHandler) Routes() http.Handler {
	r := chi.NewRouter()

	r.Post("/device/bind", h.handleBindDevice)

	return r
}

// handleBindDevice 处理绑定设备接口请求入口。
// 业务流程：
// 1. 读取并校验入参中的 code；
// 2. 从 OTA 接口生成的 code 缓存中检索待激活设备信息；
// 3. 将绑定逻辑按 code 是否包含 sn 拆分为两种独立方法分别处理：
//   - 若 code 对应信息中有 sn：进入 bindDeviceWithSN 处理（直接建立绑定关系）；
//   - 若 code 对应信息中无 sn：进入 bindDeviceWithoutSN 处理（sn与hmac必填，查表校验并激活绑定）。
func (h *UserHandler) handleBindDevice(w http.ResponseWriter, r *http.Request) {
	if h.otaHandler == nil || h.db == nil {
		h.logger.Error("handler dependencies not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	req, ok := h.readAndValidateBindRequest(w, r)
	if !ok {
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		http.Error(w, "code is required", http.StatusBadRequest)
		return
	}

	// 1. 从 OTA 生成的 code 缓存中查询待激活设备信息
	pending, found := h.otaHandler.FindPendingActivationByCode(code)
	if !found {
		h.logger.Warn("invalid or expired activation code", "code", logger.TruncateString(code))
		http.Error(w, "invalid or expired activation code", http.StatusBadRequest)
		return
	}

	userID := MockCurrentUserID

	// 2. 将绑定逻辑按 code 是否包含 sn 拆分为两种方法分别处理
	if pending.SerialNumber != "" {
		h.bindDeviceWithSN(w, r, code, pending, userID)
		return
	}

	h.bindDeviceWithoutSN(w, r, code, pending, req, userID)
}

// bindDeviceWithSN 处理 code 对应设备信息已包含 SerialNumber 的绑定逻辑。
// 业务背景：
// 设备已在硬件 OTA 阶段携带 Serial-Number 并通过了 eFuse HMAC 挑战验证，在激活表中已记录合法身份。
// 此处无需用户再次提供 sn 与 hmac，直接将该设备绑定至当前用户。
func (h *UserHandler) bindDeviceWithSN(w http.ResponseWriter, r *http.Request, code string, pending PendingActivation, userID uint64) {
	sn := pending.SerialNumber

	// 1. 确保设备激活记录存在并与当前实例匹配
	act, err := h.db.ActivateDeviceBySerialNumber(r.Context(), sn, pending.DeviceID, pending.ClientID)
	if err != nil {
		h.logger.Error("failed to update device activation during binding",
			"serial_number", logger.TruncateString(sn),
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 2. 直接插入或更新绑定关系
	if _, err := h.db.UpsertDeviceUserRef(r.Context(), sn, userID); err != nil {
		h.logger.Error("failed to bind device to user",
			"serial_number", logger.TruncateString(sn),
			"user_id", userID,
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 3. 清理一次性激活码缓存
	h.otaHandler.DeletePendingActivation(code, pending.Challenge)

	h.logger.Info("device bound successfully with existing sn in code",
		"serial_number", logger.TruncateString(act.SerialNumber),
		"device_id", logger.TruncateString(act.DeviceID),
		"client_id", logger.TruncateString(act.ClientID),
		"user_id", userID,
	)

	writeJSON(w, http.StatusOK, BindDeviceResponse{
		Success:      true,
		Message:      "device bound successfully",
		SerialNumber: act.SerialNumber,
		DeviceID:     act.DeviceID,
		ClientID:     act.ClientID,
		UserID:       userID,
	})
}

// bindDeviceWithoutSN 处理 code 对应设备信息未包含 SerialNumber 的绑定逻辑。
// 业务背景：
// 设备在 OTA 阶段未携带出厂硬件序列号（Legacy 或无 SN 设备），因此由用户在绑定时手动输入 sn 与 hmac 凭证。
// 服务端核验 device_hmac_credential 后，将设备写入设备激活表并绑定至当前用户。
func (h *UserHandler) bindDeviceWithoutSN(w http.ResponseWriter, r *http.Request, code string, pending PendingActivation, req BindDeviceRequest, userID uint64) {
	sn := strings.TrimSpace(req.SN)
	if sn == "" {
		sn = strings.TrimSpace(req.SerialNumber)
	}
	hmacInput := strings.TrimSpace(req.HMAC)

	// 1. 必填校验：code 对应信息无 sn 时，sn 与 hmac 必填
	if sn == "" || hmacInput == "" {
		h.logger.Warn("sn and hmac are required when code has no serial number",
			"code", logger.TruncateString(code),
			"sn_empty", sn == "",
			"hmac_empty", hmacInput == "",
		)
		http.Error(w, "sn and hmac are required when device has no serial number", http.StatusBadRequest)
		return
	}

	// 2. 查询 device_hmac_credential 表中的 sn
	cred, err := h.db.FindDeviceHmacCredentialBySerialNumber(r.Context(), sn)
	if err != nil {
		if errors.Is(err, database.ErrCredentialNotFound) {
			h.logger.Warn("device hmac credential not found for sn",
				"serial_number", logger.TruncateString(sn),
			)
			http.Error(w, "device hmac credential not found", http.StatusForbidden)
			return
		}
		h.logger.Error("failed to query device hmac credential",
			"serial_number", logger.TruncateString(sn),
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if !cred.IsAvailable() {
		h.logger.Warn("device credential is blocked or revoked",
			"serial_number", logger.TruncateString(sn),
			"status", cred.CredentialStatus,
		)
		http.Error(w, "device credential is blocked or revoked", http.StatusForbidden)
		return
	}

	// 3. 比对 hmac
	if !verifyHMAC(cred.HMACKeyCiphertext, hmacInput, pending.Challenge, pending.Code) {
		h.logger.Warn("hmac verification failed during device binding",
			"serial_number", logger.TruncateString(sn),
		)
		http.Error(w, "hmac verification failed", http.StatusForbidden)
		return
	}

	// 4. 比对通过后：插入或更新到设备激活表
	act, err := h.db.ActivateDeviceBySerialNumber(r.Context(), sn, pending.DeviceID, pending.ClientID)
	if err != nil {
		h.logger.Error("failed to activate device during binding",
			"serial_number", logger.TruncateString(sn),
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 5. 插入或更新设备绑定用户（当前登录用户 hardcode 1 模拟）
	if _, err := h.db.UpsertDeviceUserRef(r.Context(), sn, userID); err != nil {
		h.logger.Error("failed to bind device to user",
			"serial_number", logger.TruncateString(sn),
			"user_id", userID,
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 6. 更新凭证状态为 activated
	if cred.CredentialStatus == database.CredentialStatusEnabled {
		_ = h.db.UpdateDeviceHmacCredentialStatus(r.Context(), sn, database.CredentialStatusActivated)
	}

	// 7. 清理一次性激活码缓存
	h.otaHandler.DeletePendingActivation(code, pending.Challenge)

	h.logger.Info("device bound successfully without initial sn in code",
		"serial_number", logger.TruncateString(act.SerialNumber),
		"device_id", logger.TruncateString(act.DeviceID),
		"client_id", logger.TruncateString(act.ClientID),
		"user_id", userID,
	)

	writeJSON(w, http.StatusOK, BindDeviceResponse{
		Success:      true,
		Message:      "device bound successfully",
		SerialNumber: act.SerialNumber,
		DeviceID:     act.DeviceID,
		ClientID:     act.ClientID,
		UserID:       userID,
	})
}

// readAndValidateBindRequest 读取并反序列化绑定请求。
func (h *UserHandler) readAndValidateBindRequest(w http.ResponseWriter, r *http.Request) (BindDeviceRequest, bool) {
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
			return BindDeviceRequest{}, false
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return BindDeviceRequest{}, false
	}

	var req BindDeviceRequest
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) > 0 {
		if !json.Valid(trimmedBody) {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return BindDeviceRequest{}, false
		}
		if err := json.Unmarshal(trimmedBody, &req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return BindDeviceRequest{}, false
		}
	}

	// 补充 URL query 或 Form 参数（若 JSON 中未提供）
	if req.Code == "" {
		req.Code = r.URL.Query().Get("code")
	}
	if req.SN == "" {
		req.SN = r.URL.Query().Get("sn")
	}
	if req.SerialNumber == "" {
		req.SerialNumber = r.URL.Query().Get("serial_number")
	}
	if req.HMAC == "" {
		req.HMAC = r.URL.Query().Get("hmac")
	}

	return req, true
}

// verifyHMAC 校验输入的 HMAC 凭证。
// 支持：
// 1. 直接匹配 HMAC 密钥（密文字节或 hex 编码）；
// 2. 针对 challenge 计算的 HMAC-SHA256 十六进制摘要比对；
// 3. 针对 code 计算的 HMAC-SHA256 十六进制摘要比对。
func verifyHMAC(ciphertextKey []byte, hmacInput, challenge, code string) bool {
	if len(ciphertextKey) == 0 || hmacInput == "" {
		return false
	}

	// 1. 直接匹配密钥（字节或 hex 解码后比对）
	if hmac.Equal(ciphertextKey, []byte(hmacInput)) {
		return true
	}
	if decodedInput, err := hex.DecodeString(hmacInput); err == nil {
		if hmac.Equal(ciphertextKey, decodedInput) {
			return true
		}
	}

	// 2. 针对 challenge 计算 HMAC-SHA256 比对
	if challenge != "" {
		mac := hmac.New(sha256.New, ciphertextKey)
		mac.Write([]byte(challenge))
		expected := mac.Sum(nil)
		if decoded, err := hex.DecodeString(hmacInput); err == nil {
			if hmac.Equal(expected, decoded) {
				return true
			}
		}
	}

	// 3. 针对 code 计算 HMAC-SHA256 比对
	if code != "" {
		mac := hmac.New(sha256.New, ciphertextKey)
		mac.Write([]byte(code))
		expected := mac.Sum(nil)
		if decoded, err := hex.DecodeString(hmacInput); err == nil {
			if hmac.Equal(expected, decoded) {
				return true
			}
		}
	}

	return false
}
