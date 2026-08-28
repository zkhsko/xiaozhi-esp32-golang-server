package router

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

const (
	// MaxBatchGenerateCount 单次批量生成设备凭据的最大允许数量。
	MaxBatchGenerateCount = 1000
)

// GenerateCredentialRequest 映射 POST /admin-api/device-hmac-credential/generate 提交的请求体与参数。
type GenerateCredentialRequest struct {
	Count      int    `json:"count"`       // 生成数量（默认 1，最大 1000）
	DeviceType string `json:"device_type"` // 设备类型（默认 default，用于关联 agent）
}

// CredentialItem 表示单条生成的设备 HMAC 凭证 DTO。
type CredentialItem struct {
	ID               uint64    `json:"id"`
	SerialNumber     string    `json:"serial_number"`
	HMACKey          string    `json:"hmac_key"` // 16 进制 hex 编码密钥
	AuthMethod       string    `json:"auth_method"`
	DeviceType       string    `json:"device_type"` // 设备类型（用于关联 agent）
	CredentialStatus string    `json:"credential_status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// GenerateCredentialResponse 映射生成凭证成功后的统一响应结构。
type GenerateCredentialResponse struct {
	Success bool             `json:"success"`
	Message string           `json:"message,omitempty"`
	Data    *CredentialItem  `json:"data,omitempty"`
	Items   []CredentialItem `json:"items,omitempty"`
}

// AdminHandler 处理 /admin-api/ 管理接口。
type AdminHandler struct {
	cfg    *config.Config
	db     *database.Database
	logger *slog.Logger
}

// NewAdminHandler 创建 AdminHandler 实例。
func NewAdminHandler(cfg *config.Config, db *database.Database, l *slog.Logger) *AdminHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminHandler{
		cfg:    cfg,
		db:     db,
		logger: l,
	}
}

// Routes 注册 /admin-api 路由。
func (h *AdminHandler) Routes() http.Handler {
	r := chi.NewRouter()

	r.Post("/device-hmac-credential/generate", h.handleGenerateCredential)

	return r
}

// handleGenerateCredential 处理生成设备出厂 HMAC 凭据的请求。
// 业务流程：
// 1. 读取并校验入参中的 count（范围 1~1000，缺省为 1）；
// 2. 为每个设备使用安全随机数生成全局唯一的 32 字符 hex 格式 SN 和 32 字节（256-bit）HMAC Key；
// 3. 批量写入数据库 device_hmac_credential 表（默认 auth_method=efuse_hmac, credential_status=enabled）；
// 4. 返回 200 OK 及生成的凭据列表与 hex 格式 HMAC Key。
func (h *AdminHandler) handleGenerateCredential(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	req, ok := h.readAndValidateGenerateRequest(w, r)
	if !ok {
		return
	}

	count := req.Count
	if count <= 0 {
		count = 1
	}
	if count > MaxBatchGenerateCount {
		http.Error(w, fmt.Sprintf("count exceeds maximum limit of %d", MaxBatchGenerateCount), http.StatusBadRequest)
		return
	}

	deviceType := strings.TrimSpace(req.DeviceType)
	if deviceType == "" {
		deviceType = "default"
	}

	records := make([]*database.DeviceHmacCredential, 0, count)
	items := make([]CredentialItem, 0, count)

	for i := 0; i < count; i++ {
		curSN, err := generateRandomSerialNumber()
		if err != nil {
			h.logger.Error("failed to generate random serial number", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		randKey, err := generateRandomBytes(32)
		if err != nil {
			h.logger.Error("failed to generate random hmac key", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		keyHex := hex.EncodeToString(randKey)

		rec := &database.DeviceHmacCredential{
			SerialNumber:      curSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			DeviceType:        deviceType,
			HMACKeyCiphertext: keyHex,
			CredentialStatus:  database.CredentialStatusEnabled,
		}
		records = append(records, rec)
		items = append(items, CredentialItem{
			SerialNumber:     curSN,
			HMACKey:          keyHex,
			AuthMethod:       database.AuthMethodEfuseHMAC,
			DeviceType:        deviceType,
			CredentialStatus: database.CredentialStatusEnabled,
		})
	}

	if err := h.db.BatchCreateDeviceHmacCredentials(r.Context(), records); err != nil {
		h.logger.Error("failed to save device hmac credentials", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 补充数据库生成的自增 ID 和时间戳
	for i := range records {
		items[i].ID = records[i].ID
		items[i].CreatedAt = records[i].CreatedAt
		items[i].UpdatedAt = records[i].UpdatedAt
	}

	h.logger.Info("device hmac credentials generated successfully", "count", len(items))

	var dataObj *CredentialItem
	if len(items) > 0 {
		dataObj = &items[0]
	}

	writeJSON(w, http.StatusOK, GenerateCredentialResponse{
		Success: true,
		Message: "device hmac credential generated successfully",
		Data:    dataObj,
		Items:   items,
	})
}

// readAndValidateGenerateRequest 读取并反序列化凭证生成请求。
func (h *AdminHandler) readAndValidateGenerateRequest(w http.ResponseWriter, r *http.Request) (GenerateCredentialRequest, bool) {
	maxHeaderBytes := MaxSingleHeaderBytes
	maxTotalHeaderBytes := MaxTotalHeaderBytes
	if h.cfg != nil && h.cfg.Server.MaxHTTPHeaderBytes > 0 {
		maxTotalHeaderBytes = h.cfg.Server.MaxHTTPHeaderBytes
	}
	if err := validateHeaders(r.Header, maxHeaderBytes, maxTotalHeaderBytes); err != nil {
		http.Error(w, "request header fields too large", http.StatusBadRequest)
		return GenerateCredentialRequest{}, false
	}

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
			return GenerateCredentialRequest{}, false
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return GenerateCredentialRequest{}, false
	}

	var req GenerateCredentialRequest
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) > 0 {
		if !json.Valid(trimmedBody) {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return GenerateCredentialRequest{}, false
		}
		if err := json.Unmarshal(trimmedBody, &req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return GenerateCredentialRequest{}, false
		}
	}

	// 补充 URL query 参数（若未在 JSON 请求体中提供）
	if req.Count <= 0 {
		if cStr := r.URL.Query().Get("count"); cStr != "" {
			if c, err := strconv.Atoi(cStr); err == nil {
				req.Count = c
			}
		}
	}
	if req.DeviceType == "" {
		req.DeviceType = r.URL.Query().Get("device_type")
	}

	return req, true
}

// generateRandomBytes 使用 crypto/rand 生成指定长度的安全随机字节切片。
func generateRandomBytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, errors.New("byte length must be positive")
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto rand failed: %w", err)
	}
	return b, nil
}

// generateRandomSerialNumber 使用 crypto/rand 生成 32 字符（16 字节）小写十六进制序列号。
func generateRandomSerialNumber() (string, error) {
	b, err := generateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
