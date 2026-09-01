package router

import (
	"bytes"
	"context"
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

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

const (
	// MaxBatchGenerateCount 单次批量生成设备凭据的最大允许数量。
	MaxBatchGenerateCount = 1000
)

// DeviceCredentialStore 定义设备凭据管理所需的窄持久化接口。
type DeviceCredentialStore interface {
	ListDeviceHmacCredentials(ctx context.Context, filter database.DeviceHmacCredentialFilter) ([]*database.DeviceHmacCredential, int64, error)
	UpdateDeviceHmacCredential(ctx context.Context, id uint64, updates map[string]any) error
	DeleteDeviceHmacCredential(ctx context.Context, id uint64) error
	BatchDeleteDeviceHmacCredentials(ctx context.Context, ids []uint64) error
	BatchCreateDeviceHmacCredentials(ctx context.Context, records []*database.DeviceHmacCredential) error
}

// GenerateCredentialRequest 映射 POST /admin-api/device-hmac-credential/generate 提交的请求体与参数。
type GenerateCredentialRequest struct {
	Count      int    `json:"count"`       // 生成数量（默认 1，最大 1000）
	DeviceType string `json:"device_type"` // 设备类型（默认 default，用于关联 agent）
}

// CredentialItem 表示单条生成的设备 HMAC 凭证 DTO。
type CredentialItem struct {
	Id               uint64    `json:"id"`
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

// DeviceCredentialListData 设备凭证列表响应数据。
type DeviceCredentialListData struct {
	Items    []CredentialItem `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

// UpdateCredentialRequest 更新凭据请求体。
type UpdateCredentialRequest struct {
	Id               uint64 `json:"id"`
	DeviceType       string `json:"device_type"`
	CredentialStatus string `json:"credential_status"`
	AuthMethod       string `json:"auth_method"`
}

// DeleteCredentialRequest 删除单条凭据请求体。
type DeleteCredentialRequest struct {
	Id uint64 `json:"id"`
}

// BatchDeleteCredentialRequest 批量删除请求体。
type BatchDeleteCredentialRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminCredentialHandler 处理设备出厂凭据相关的管理端接口。
type AdminCredentialHandler struct {
	cfg    *config.Config
	store  DeviceCredentialStore
	logger *slog.Logger
}

// NewAdminCredentialHandler 创建 AdminCredentialHandler 实例。
func NewAdminCredentialHandler(cfg *config.Config, store DeviceCredentialStore, l *slog.Logger) *AdminCredentialHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminCredentialHandler{
		cfg:    cfg,
		store:  store,
		logger: l,
	}
}

// handleListCredentials 分页获取设备出厂凭据列表。
func (h *AdminCredentialHandler) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

	filter := database.DeviceHmacCredentialFilter{
		SerialNumber:     query.Get("serial_number"),
		DeviceType:       query.Get("device_type"),
		CredentialStatus: query.Get("credential_status"),
		Page:             page,
		PageSize:         pageSize,
	}

	creds, total, err := h.store.ListDeviceHmacCredentials(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list device credentials", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]CredentialItem, 0, len(creds))
	for _, c := range creds {
		items = append(items, CredentialItem{
			Id:               c.Id,
			SerialNumber:     c.SerialNumber,
			HMACKey:          c.HMACKeyCiphertext,
			AuthMethod:       c.AuthMethod,
			DeviceType:       c.DeviceType,
			CredentialStatus: c.CredentialStatus,
			CreatedAt:        c.CreatedAt,
			UpdatedAt:        c.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: DeviceCredentialListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleUpdateCredential 更新指定凭证信息。
func (h *AdminCredentialHandler) handleUpdateCredential(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req UpdateCredentialRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	updates := make(map[string]any)
	if req.DeviceType != "" {
		updates["device_type"] = strings.TrimSpace(req.DeviceType)
	}
	if req.CredentialStatus != "" {
		updates["credential_status"] = strings.TrimSpace(req.CredentialStatus)
	}
	if req.AuthMethod != "" {
		updates["auth_method"] = strings.TrimSpace(req.AuthMethod)
	}

	if len(updates) == 0 {
		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "nothing to update",
		})
		return
	}

	if err := h.store.UpdateDeviceHmacCredential(r.Context(), req.Id, updates); err != nil {
		if errors.Is(err, database.ErrCredentialNotFound) {
			http.Error(w, "credential not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to update credential", "id", req.Id, "error", err)
		http.Error(w, "failed to update credential", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "credential updated successfully",
	})
}

// handleDeleteCredential 删除指定凭据。
func (h *AdminCredentialHandler) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteCredentialRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteDeviceHmacCredential(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrCredentialNotFound) {
			http.Error(w, "credential not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete credential", "id", req.Id, "error", err)
		http.Error(w, "failed to delete credential", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "credential deleted successfully",
	})
}

// handleBatchDeleteCredentials 批量删除凭据。
func (h *AdminCredentialHandler) handleBatchDeleteCredentials(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteCredentialRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteDeviceHmacCredentials(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete credentials", "error", err)
		http.Error(w, "failed to batch delete credentials", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "credentials deleted successfully",
	})
}

// handleGenerateCredential 处理生成设备出厂 HMAC 凭据的请求。
func (h *AdminCredentialHandler) handleGenerateCredential(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
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
			DeviceType:       deviceType,
			CredentialStatus: database.CredentialStatusEnabled,
		})
	}

	if err := h.store.BatchCreateDeviceHmacCredentials(r.Context(), records); err != nil {
		h.logger.Error("failed to save device hmac credentials", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 补充数据库生成的自增 Id 和时间戳
	for i := range records {
		items[i].Id = records[i].Id
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
func (h *AdminCredentialHandler) readAndValidateGenerateRequest(w http.ResponseWriter, r *http.Request) (GenerateCredentialRequest, bool) {
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
