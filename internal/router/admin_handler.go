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

// AdminResponse 通用管理员 API 返回结构。
type AdminResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
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
	ID               uint64 `json:"id"`
	DeviceType       string `json:"device_type"`
	CredentialStatus string `json:"credential_status"`
	AuthMethod       string `json:"auth_method"`
}

// DeleteCredentialRequest 删除单条凭据请求体。
type DeleteCredentialRequest struct {
	ID uint64 `json:"id"`
}

// BatchDeleteCredentialRequest 批量删除请求体。
type BatchDeleteCredentialRequest struct {
	IDs []uint64 `json:"ids"`
}

// ActivationItem 表示单条设备激活关系 DTO。
type ActivationItem struct {
	ID               uint64    `json:"id"`
	SerialNumber     string    `json:"serial_number"`
	DeviceID         string    `json:"device_id"`
	ClientID         string    `json:"client_id,omitempty"`
	ActivationStatus string    `json:"activation_status"`
	ActivatedAt      time.Time `json:"activated_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DeviceActivationListData 设备激活列表响应数据。
type DeviceActivationListData struct {
	Items    []ActivationItem `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

// UpdateActivationRequest 更新激活记录请求体。
type UpdateActivationRequest struct {
	ID               uint64 `json:"id"`
	DeviceID         string `json:"device_id"`
	ClientID         string `json:"client_id"`
	ActivationStatus string `json:"activation_status"`
}

// DeleteActivationRequest 删除单条激活记录请求体。
type DeleteActivationRequest struct {
	ID uint64 `json:"id"`
}

// BatchDeleteActivationRequest 批量删除激活记录请求体。
type BatchDeleteActivationRequest struct {
	IDs []uint64 `json:"ids"`
}

// ASRConfigItem 表示单条 ASR 配置 DTO（api_key 脱敏为 has_api_key）。
type ASRConfigItem struct {
	ID               uint64    `json:"id"`
	Name             string    `json:"name"`
	Endpoint         string    `json:"endpoint"`
	HasAPIKey        bool      `json:"has_api_key"`
	Model            string    `json:"model"`
	Hotwords         string    `json:"hotwords"`
	ConnectTimeoutMS int64     `json:"connect_timeout_ms"`
	Enabled          bool      `json:"enabled"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ASRConfigListData ASR 配置列表响应数据。
type ASRConfigListData struct {
	Items    []ASRConfigItem `json:"items"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

// SaveASRConfigRequest 保存或更新 ASR 配置请求体。
type SaveASRConfigRequest struct {
	ID               uint64 `json:"id"`
	Name             string `json:"name"`
	Endpoint         string `json:"endpoint"`
	APIKey           string `json:"api_key"` // write-only；更新时留空表示保留原 Key
	Model            string `json:"model"`
	Hotwords         string `json:"hotwords"`
	ConnectTimeoutMS int64  `json:"connect_timeout_ms"`
	Enabled          *bool  `json:"enabled"`
}

// DeleteASRConfigRequest 删除单条 ASR 配置请求体。
type DeleteASRConfigRequest struct {
	ID uint64 `json:"id"`
}

// BatchDeleteASRConfigRequest 批量删除 ASR 配置请求体。
type BatchDeleteASRConfigRequest struct {
	IDs []uint64 `json:"ids"`
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

// Routes 注册 /admin-api 路由，仅使用 GET 和 POST 方法。
func (h *AdminHandler) Routes() http.Handler {
	r := chi.NewRouter()

	// Device HMAC Credential 接口
	r.Get("/device-hmac-credential", h.handleListCredentials)
	r.Get("/device-hmac-credential/list", h.handleListCredentials)
	r.Post("/device-hmac-credential/generate", h.handleGenerateCredential)
	r.Post("/device-hmac-credential/update", h.handleUpdateCredential)
	r.Post("/device-hmac-credential/delete", h.handleDeleteCredential)
	r.Post("/device-hmac-credential/batch-delete", h.handleBatchDeleteCredentials)

	// Device Activation 接口
	r.Get("/device-activation", h.handleListActivations)
	r.Get("/device-activation/list", h.handleListActivations)
	r.Post("/device-activation/update", h.handleUpdateActivation)
	r.Post("/device-activation/delete", h.handleDeleteActivation)
	r.Post("/device-activation/batch-delete", h.handleBatchDeleteActivations)

	// ASR Config 接口
	r.Get("/asr-config", h.handleListASRConfigs)
	r.Get("/asr-config/list", h.handleListASRConfigs)
	r.Post("/asr-config/save", h.handleSaveASRConfig)
	r.Post("/asr-config/update", h.handleSaveASRConfig)
	r.Post("/asr-config/delete", h.handleDeleteASRConfig)
	r.Post("/asr-config/batch-delete", h.handleBatchDeleteASRConfigs)

	return r
}

// handleListCredentials 分页获取设备出厂凭据列表。
func (h *AdminHandler) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	if pageSize <= 0 {
		pageSize = 10
	} else if pageSize > 100 {
		pageSize = 100
	}

	filter := database.DeviceHmacCredentialFilter{
		SerialNumber:     query.Get("serial_number"),
		DeviceType:       query.Get("device_type"),
		CredentialStatus: query.Get("credential_status"),
		Page:             page,
		PageSize:         pageSize,
	}

	creds, total, err := h.db.ListDeviceHmacCredentials(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list device credentials", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]CredentialItem, 0, len(creds))
	for _, c := range creds {
		items = append(items, CredentialItem{
			ID:               c.ID,
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
func (h *AdminHandler) handleUpdateCredential(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req UpdateCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
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

	if err := h.db.UpdateDeviceHmacCredential(r.Context(), req.ID, updates); err != nil {
		if errors.Is(err, database.ErrCredentialNotFound) {
			http.Error(w, "credential not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to update credential", "id", req.ID, "error", err)
		http.Error(w, "failed to update credential", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "credential updated successfully",
	})
}

// handleDeleteCredential 删除指定凭据。
func (h *AdminHandler) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteDeviceHmacCredential(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrCredentialNotFound) {
			http.Error(w, "credential not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete credential", "id", req.ID, "error", err)
		http.Error(w, "failed to delete credential", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "credential deleted successfully",
	})
}

// handleBatchDeleteCredentials 批量删除凭据。
func (h *AdminHandler) handleBatchDeleteCredentials(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.db.BatchDeleteDeviceHmacCredentials(r.Context(), req.IDs); err != nil {
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

// handleListActivations 分页获取设备激活关系列表。
func (h *AdminHandler) handleListActivations(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	if pageSize <= 0 {
		pageSize = 10
	} else if pageSize > 100 {
		pageSize = 100
	}

	filter := database.DeviceActivationFilter{
		SerialNumber:     query.Get("serial_number"),
		DeviceID:         query.Get("device_id"),
		ClientID:         query.Get("client_id"),
		ActivationStatus: query.Get("activation_status"),
		Page:             page,
		PageSize:         pageSize,
	}

	acts, total, err := h.db.ListDeviceActivations(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list device activations", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]ActivationItem, 0, len(acts))
	for _, a := range acts {
		items = append(items, ActivationItem{
			ID:               a.ID,
			SerialNumber:     a.SerialNumber,
			DeviceID:         a.DeviceID,
			ClientID:         a.ClientID,
			ActivationStatus: a.ActivationStatus,
			ActivatedAt:      a.ActivatedAt,
			CreatedAt:        a.CreatedAt,
			UpdatedAt:        a.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: DeviceActivationListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleUpdateActivation 更新指定设备激活记录。
func (h *AdminHandler) handleUpdateActivation(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req UpdateActivationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	updates := make(map[string]any)
	if req.DeviceID != "" {
		updates["device_id"] = strings.TrimSpace(req.DeviceID)
	}
	if req.ClientID != "" {
		updates["client_id"] = strings.TrimSpace(req.ClientID)
	}
	if req.ActivationStatus != "" {
		updates["activation_status"] = strings.TrimSpace(req.ActivationStatus)
	}

	if len(updates) == 0 {
		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "nothing to update",
		})
		return
	}

	if err := h.db.UpdateDeviceActivation(r.Context(), req.ID, updates); err != nil {
		if errors.Is(err, database.ErrActivationNotFound) {
			http.Error(w, "activation not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to update activation", "id", req.ID, "error", err)
		http.Error(w, "failed to update activation", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "device activation updated successfully",
	})
}

// handleDeleteActivation 删除指定设备激活记录。
func (h *AdminHandler) handleDeleteActivation(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteActivationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteDeviceActivation(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrActivationNotFound) {
			http.Error(w, "activation not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete activation", "id", req.ID, "error", err)
		http.Error(w, "failed to delete activation", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "device activation deleted successfully",
	})
}

// handleBatchDeleteActivations 批量删除设备激活记录。
func (h *AdminHandler) handleBatchDeleteActivations(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteActivationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.db.BatchDeleteDeviceActivations(r.Context(), req.IDs); err != nil {
		h.logger.Error("failed to batch delete activations", "error", err)
		http.Error(w, "failed to batch delete activations", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "device activations deleted successfully",
	})
}

// handleListASRConfigs 分页获取 ASR 语音识别配置列表。
func (h *AdminHandler) handleListASRConfigs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	if pageSize <= 0 {
		pageSize = 10
	} else if pageSize > 100 {
		pageSize = 100
	}

	filter := database.ASRConfigFilter{
		Name:     query.Get("name"),
		Page:     page,
		PageSize: pageSize,
	}

	if enabledStr := query.Get("enabled"); enabledStr != "" {
		if enabledVal, err := strconv.ParseBool(enabledStr); err == nil {
			filter.Enabled = &enabledVal
		}
	}

	configs, total, err := h.db.ListASRConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list asr configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]ASRConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, ASRConfigItem{
			ID:               cfg.ID,
			Name:             cfg.Name,
			Endpoint:         cfg.Endpoint,
			HasAPIKey:        len(strings.TrimSpace(cfg.APIKey)) > 0,
			Model:            cfg.Model,
			Hotwords:         cfg.Hotwords,
			ConnectTimeoutMS: cfg.ConnectTimeoutMS,
			Enabled:          cfg.Enabled,
			CreatedAt:        cfg.CreatedAt,
			UpdatedAt:        cfg.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: ASRConfigListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleSaveASRConfig 创建或更新 ASR 语音识别配置（ID 为 0 时创建，ID > 0 时按 ID 覆盖）。
func (h *AdminHandler) handleSaveASRConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveASRConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	connectTimeout := req.ConnectTimeoutMS
	if connectTimeout == 0 {
		connectTimeout = 5000
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if req.ID == 0 {
		// 创建新配置
		cfg := &database.ASRConfig{
			Name:             strings.TrimSpace(req.Name),
			Endpoint:         strings.TrimSpace(req.Endpoint),
			APIKey:           strings.TrimSpace(req.APIKey),
			Model:            strings.TrimSpace(req.Model),
			Hotwords:         req.Hotwords,
			ConnectTimeoutMS: connectTimeout,
			Enabled:          enabled,
		}

		if err := h.db.CreateASRConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "ASR 配置创建成功",
			Data: ASRConfigItem{
				ID:               cfg.ID,
				Name:             cfg.Name,
				Endpoint:         cfg.Endpoint,
				HasAPIKey:        len(cfg.APIKey) > 0,
				Model:            cfg.Model,
				Hotwords:         cfg.Hotwords,
				ConnectTimeoutMS: cfg.ConnectTimeoutMS,
				Enabled:          cfg.Enabled,
				CreatedAt:        cfg.CreatedAt,
				UpdatedAt:        cfg.UpdatedAt,
			},
		})
		return
	}

	// 覆盖更新已有配置
	existing, err := h.db.FindASRConfigByID(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, database.ErrASRConfigNotFound) {
			http.Error(w, "asr config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	apiKey := existing.APIKey
	if strings.TrimSpace(req.APIKey) != "" {
		apiKey = strings.TrimSpace(req.APIKey)
	}

	if req.Enabled == nil {
		enabled = existing.Enabled
	}

	updatedCfg := &database.ASRConfig{
		ID:               req.ID,
		Name:             strings.TrimSpace(req.Name),
		Endpoint:         strings.TrimSpace(req.Endpoint),
		APIKey:           apiKey,
		Model:            strings.TrimSpace(req.Model),
		Hotwords:         req.Hotwords,
		ConnectTimeoutMS: connectTimeout,
		Enabled:          enabled,
	}

	if err := h.db.UpdateASRConfigByID(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "ASR 配置更新成功",
		Data: ASRConfigItem{
			ID:               updatedCfg.ID,
			Name:             updatedCfg.Name,
			Endpoint:         updatedCfg.Endpoint,
			HasAPIKey:        len(apiKey) > 0,
			Model:            updatedCfg.Model,
			Hotwords:         updatedCfg.Hotwords,
			ConnectTimeoutMS: updatedCfg.ConnectTimeoutMS,
			Enabled:          updatedCfg.Enabled,
			UpdatedAt:        time.Now(),
		},
	})
}

// handleDeleteASRConfig 删除指定 ID 的 ASR 配置记录。
func (h *AdminHandler) handleDeleteASRConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteASRConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteASRConfig(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrASRConfigNotFound) {
			http.Error(w, "asr config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete asr config", "id", req.ID, "error", err)
		http.Error(w, "failed to delete asr config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "ASR 配置删除成功",
	})
}

// handleBatchDeleteASRConfigs 批量删除 ASR 配置记录。
func (h *AdminHandler) handleBatchDeleteASRConfigs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteASRConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.db.BatchDeleteASRConfigs(r.Context(), req.IDs); err != nil {
		h.logger.Error("failed to batch delete asr configs", "error", err)
		http.Error(w, "failed to batch delete asr configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 ASR 配置", len(req.IDs)),
	})
}


