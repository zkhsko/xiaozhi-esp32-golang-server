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
	Provider         string    `json:"provider"`
	Endpoint         string    `json:"endpoint"`
	HasAPIKey        bool      `json:"has_api_key"`
	Model            string    `json:"model"`
	Hotwords         string    `json:"hotwords"`
	ProxyURL         string    `json:"proxy_url"`
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
	Provider         string `json:"provider"`
	Endpoint         string `json:"endpoint"`
	APIKey           string `json:"api_key"` // write-only；更新时留空表示保留原 Key
	Model            string `json:"model"`
	Hotwords         string `json:"hotwords"`
	ProxyURL         string `json:"proxy_url"`
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

// LLMConfigItem 表示单条 LLM 配置 DTO（api_key 脱敏为 has_api_key）。
type LLMConfigItem struct {
	ID                  uint64    `json:"id"`
	Name                string    `json:"name"`
	Provider            string    `json:"provider"`
	Endpoint            string    `json:"endpoint"`
	HasAPIKey           bool      `json:"has_api_key"`
	Model               string    `json:"model"`
	ProxyURL            string    `json:"proxy_url"`
	FirstTokenTimeoutMS int64     `json:"first_token_timeout_ms"`
	OverallTimeoutMS    int64     `json:"overall_timeout_ms"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// LLMConfigListData LLM 配置列表响应数据。
type LLMConfigListData struct {
	Items    []LLMConfigItem `json:"items"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

// SaveLLMConfigRequest 保存或更新 LLM 配置请求体。
type SaveLLMConfigRequest struct {
	ID                  uint64 `json:"id"`
	Name                string `json:"name"`
	Provider            string `json:"provider"`
	Endpoint            string `json:"endpoint"`
	APIKey              string `json:"api_key"` // write-only；更新时留空表示保留原 Key
	Model               string `json:"model"`
	ProxyURL            string `json:"proxy_url"`
	FirstTokenTimeoutMS int64  `json:"first_token_timeout_ms"`
	OverallTimeoutMS    int64  `json:"overall_timeout_ms"`
	Enabled             *bool  `json:"enabled"`
}

// DeleteLLMConfigRequest 删除单条 LLM 配置请求体。
type DeleteLLMConfigRequest struct {
	ID uint64 `json:"id"`
}

// BatchDeleteLLMConfigRequest 批量删除 LLM 配置请求体。
type BatchDeleteLLMConfigRequest struct {
	IDs []uint64 `json:"ids"`
}

// TTSConfigItem 表示单条 TTS 配置 DTO（api_key 脱敏为 has_api_key）。
type TTSConfigItem struct {
	ID                  uint64    `json:"id"`
	Name                string    `json:"name"`
	Provider            string    `json:"provider"`
	Endpoint            string    `json:"endpoint"`
	HasAPIKey           bool      `json:"has_api_key"`
	Model               string    `json:"model"`
	Voices              string    `json:"voices"`
	ProxyURL            string    `json:"proxy_url"`
	ConnectTimeoutMS    int64     `json:"connect_timeout_ms"`
	FirstAudioTimeoutMS int64     `json:"first_audio_timeout_ms"`
	SentenceTimeoutMS   int64     `json:"sentence_timeout_ms"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// TTSConfigListData TTS 配置列表响应数据。
type TTSConfigListData struct {
	Items    []TTSConfigItem `json:"items"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

// SaveTTSConfigRequest 保存或更新 TTS 配置请求体。
type SaveTTSConfigRequest struct {
	ID                  uint64 `json:"id"`
	Name                string `json:"name"`
	Provider            string `json:"provider"`
	Endpoint            string `json:"endpoint"`
	APIKey              string `json:"api_key"` // write-only；更新时留空表示保留原 Key
	Model               string `json:"model"`
	Voices              string `json:"voices"`
	ProxyURL            string `json:"proxy_url"`
	ConnectTimeoutMS    int64  `json:"connect_timeout_ms"`
	FirstAudioTimeoutMS int64  `json:"first_audio_timeout_ms"`
	SentenceTimeoutMS   int64  `json:"sentence_timeout_ms"`
	Enabled             *bool  `json:"enabled"`
}

// DeleteTTSConfigRequest 删除单条 TTS 配置请求体。
type DeleteTTSConfigRequest struct {
	ID uint64 `json:"id"`
}

// BatchDeleteTTSConfigRequest 批量删除 TTS 配置请求体。
type BatchDeleteTTSConfigRequest struct {
	IDs []uint64 `json:"ids"`
}

// AgentConfigItem 表示单条 Agent 配置 DTO。
type AgentConfigItem struct {
	ID           uint64    `json:"id"`
	Name         string    `json:"name"`
	ASRConfigID  uint64    `json:"asr_config_id"`
	ASRName      string    `json:"asr_name,omitempty"`
	LLMConfigID  uint64    `json:"llm_config_id"`
	LLMName      string    `json:"llm_name,omitempty"`
	TTSConfigID  uint64    `json:"tts_config_id"`
	TTSName      string    `json:"tts_name,omitempty"`
	SystemPrompt string    `json:"system_prompt"`
	Voice        string    `json:"voice"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AgentConfigListData Agent 配置列表响应数据。
type AgentConfigListData struct {
	Items    []AgentConfigItem `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// SaveAgentConfigRequest 保存或更新 Agent 配置请求体。
type SaveAgentConfigRequest struct {
	ID           uint64 `json:"id"`
	Name         string `json:"name"`
	ASRConfigID  uint64 `json:"asr_config_id"`
	LLMConfigID  uint64 `json:"llm_config_id"`
	TTSConfigID  uint64 `json:"tts_config_id"`
	SystemPrompt string `json:"system_prompt"`
	Voice        string `json:"voice"`
	Enabled      *bool  `json:"enabled"`
}

// DeleteAgentConfigRequest 删除单条 Agent 配置请求体。
type DeleteAgentConfigRequest struct {
	ID uint64 `json:"id"`
}

// BatchDeleteAgentConfigRequest 批量删除 Agent 配置请求体。
type BatchDeleteAgentConfigRequest struct {
	IDs []uint64 `json:"ids"`
}

// ActivateAgentConfigRequest 激活单条 Agent 配置请求体。
type ActivateAgentConfigRequest struct {
	ID uint64 `json:"id"`
}

// DeviceTypeItem 表示单条设备类型与 Agent 关联配置 DTO。
type DeviceTypeItem struct {
	ID            uint64    `json:"id"`
	DeviceType    string    `json:"device_type"`
	AgentConfigID uint64    `json:"agent_config_id"`
	AgentName     string    `json:"agent_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DeviceTypeListData 设备类型配置列表响应数据。
type DeviceTypeListData struct {
	Items    []DeviceTypeItem `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

// SaveDeviceTypeRequest 保存或更新设备类型配置请求体。
type SaveDeviceTypeRequest struct {
	ID            uint64 `json:"id"`
	DeviceType    string `json:"device_type"`
	AgentConfigID uint64 `json:"agent_config_id"`
}

// DeleteDeviceTypeRequest 删除单条设备类型配置请求体。
type DeleteDeviceTypeRequest struct {
	ID uint64 `json:"id"`
}

// BatchDeleteDeviceTypeRequest 批量删除设备类型配置请求体。
type BatchDeleteDeviceTypeRequest struct {
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

	// LLM Config 接口
	r.Get("/llm-config", h.handleListLLMConfigs)
	r.Get("/llm-config/list", h.handleListLLMConfigs)
	r.Post("/llm-config/save", h.handleSaveLLMConfig)
	r.Post("/llm-config/update", h.handleSaveLLMConfig)
	r.Post("/llm-config/delete", h.handleDeleteLLMConfig)
	r.Post("/llm-config/batch-delete", h.handleBatchDeleteLLMConfigs)

	// TTS Config 接口
	r.Get("/tts-config", h.handleListTTSConfigs)
	r.Get("/tts-config/list", h.handleListTTSConfigs)
	r.Post("/tts-config/save", h.handleSaveTTSConfig)
	r.Post("/tts-config/update", h.handleSaveTTSConfig)
	r.Post("/tts-config/delete", h.handleDeleteTTSConfig)
	r.Post("/tts-config/batch-delete", h.handleBatchDeleteTTSConfigs)

	// Agent Config 接口
	r.Get("/agent-config", h.handleListAgentConfigs)
	r.Get("/agent-config/list", h.handleListAgentConfigs)
	r.Post("/agent-config/save", h.handleSaveAgentConfig)
	r.Post("/agent-config/update", h.handleSaveAgentConfig)
	r.Post("/agent-config/delete", h.handleDeleteAgentConfig)
	r.Post("/agent-config/batch-delete", h.handleBatchDeleteAgentConfigs)
	r.Post("/agent-config/activate", h.handleActivateAgentConfig)

	// Device Type 接口
	r.Get("/device-type", h.handleListDeviceTypes)
	r.Get("/device-type/list", h.handleListDeviceTypes)
	r.Post("/device-type/save", h.handleSaveDeviceType)
	r.Post("/device-type/update", h.handleSaveDeviceType)
	r.Post("/device-type/delete", h.handleDeleteDeviceType)
	r.Post("/device-type/batch-delete", h.handleBatchDeleteDeviceTypes)

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
		Provider: query.Get("provider"),
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
			Provider:         cfg.Provider,
			Endpoint:         cfg.Endpoint,
			HasAPIKey:        len(strings.TrimSpace(cfg.APIKey)) > 0,
			Model:            cfg.Model,
			Hotwords:         cfg.Hotwords,
			ProxyURL:         cfg.ProxyURL,
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

	provider := strings.TrimSpace(req.Provider)

	if req.ID == 0 {
		// 创建新配置
		cfg := &database.ASRConfig{
			Name:             strings.TrimSpace(req.Name),
			Provider:         provider,
			Endpoint:         strings.TrimSpace(req.Endpoint),
			APIKey:           strings.TrimSpace(req.APIKey),
			Model:            strings.TrimSpace(req.Model),
			Hotwords:         req.Hotwords,
			ProxyURL:         strings.TrimSpace(req.ProxyURL),
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
				Provider:         cfg.Provider,
				Endpoint:         cfg.Endpoint,
				HasAPIKey:        len(cfg.APIKey) > 0,
				Model:            cfg.Model,
				Hotwords:         cfg.Hotwords,
				ProxyURL:         cfg.ProxyURL,
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

	if strings.TrimSpace(req.Provider) == "" {
		provider = existing.Provider
	}

	if req.Enabled == nil {
		enabled = existing.Enabled
	}

	updatedCfg := &database.ASRConfig{
		ID:               req.ID,
		Name:             strings.TrimSpace(req.Name),
		Provider:         provider,
		Endpoint:         strings.TrimSpace(req.Endpoint),
		APIKey:           apiKey,
		Model:            strings.TrimSpace(req.Model),
		Hotwords:         req.Hotwords,
		ProxyURL:         strings.TrimSpace(req.ProxyURL),
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
			Provider:         updatedCfg.Provider,
			Endpoint:         updatedCfg.Endpoint,
			HasAPIKey:        len(apiKey) > 0,
			Model:            updatedCfg.Model,
			Hotwords:         updatedCfg.Hotwords,
			ProxyURL:         updatedCfg.ProxyURL,
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

// handleListLLMConfigs 分页获取 LLM 大语言模型配置列表。
func (h *AdminHandler) handleListLLMConfigs(w http.ResponseWriter, r *http.Request) {
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

	filter := database.LLMConfigFilter{
		Name:     query.Get("name"),
		Provider: query.Get("provider"),
		Page:     page,
		PageSize: pageSize,
	}

	if enabledStr := query.Get("enabled"); enabledStr != "" {
		if enabledVal, err := strconv.ParseBool(enabledStr); err == nil {
			filter.Enabled = &enabledVal
		}
	}

	configs, total, err := h.db.ListLLMConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list llm configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]LLMConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, LLMConfigItem{
			ID:                  cfg.ID,
			Name:                cfg.Name,
			Provider:            cfg.Provider,
			Endpoint:            cfg.Endpoint,
			HasAPIKey:           len(strings.TrimSpace(cfg.APIKey)) > 0,
			Model:               cfg.Model,
			ProxyURL:            cfg.ProxyURL,
			FirstTokenTimeoutMS: cfg.FirstTokenTimeoutMS,
			OverallTimeoutMS:    cfg.OverallTimeoutMS,
			Enabled:             cfg.Enabled,
			CreatedAt:           cfg.CreatedAt,
			UpdatedAt:           cfg.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: LLMConfigListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleSaveLLMConfig 创建或更新 LLM 大语言模型配置（ID 为 0 时创建，ID > 0 时按 ID 覆盖）。
func (h *AdminHandler) handleSaveLLMConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveLLMConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	firstTokenTimeout := req.FirstTokenTimeoutMS
	if firstTokenTimeout == 0 {
		firstTokenTimeout = 5000
	}

	overallTimeout := req.OverallTimeoutMS
	if overallTimeout == 0 {
		overallTimeout = 30000
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	provider := strings.TrimSpace(req.Provider)

	if req.ID == 0 {
		// 创建新配置
		cfg := &database.LLMConfig{
			Name:                strings.TrimSpace(req.Name),
			Provider:            provider,
			Endpoint:            strings.TrimSpace(req.Endpoint),
			APIKey:              strings.TrimSpace(req.APIKey),
			Model:               strings.TrimSpace(req.Model),
			ProxyURL:            strings.TrimSpace(req.ProxyURL),
			FirstTokenTimeoutMS: firstTokenTimeout,
			OverallTimeoutMS:    overallTimeout,
			Enabled:             enabled,
		}

		if err := h.db.CreateLLMConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "LLM 配置创建成功",
			Data: LLMConfigItem{
				ID:                  cfg.ID,
				Name:                cfg.Name,
				Provider:            cfg.Provider,
				Endpoint:            cfg.Endpoint,
				HasAPIKey:           len(cfg.APIKey) > 0,
				Model:               cfg.Model,
				ProxyURL:            cfg.ProxyURL,
				FirstTokenTimeoutMS: cfg.FirstTokenTimeoutMS,
				OverallTimeoutMS:    cfg.OverallTimeoutMS,
				Enabled:             cfg.Enabled,
				CreatedAt:           cfg.CreatedAt,
				UpdatedAt:           cfg.UpdatedAt,
			},
		})
		return
	}

	// 覆盖更新已有配置
	existing, err := h.db.FindLLMConfigByID(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, database.ErrLLMConfigNotFound) {
			http.Error(w, "llm config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	apiKey := existing.APIKey
	if strings.TrimSpace(req.APIKey) != "" {
		apiKey = strings.TrimSpace(req.APIKey)
	}

	if strings.TrimSpace(req.Provider) == "" {
		provider = existing.Provider
	}

	if req.Enabled == nil {
		enabled = existing.Enabled
	}

	updatedCfg := &database.LLMConfig{
		ID:                  req.ID,
		Name:                strings.TrimSpace(req.Name),
		Provider:            provider,
		Endpoint:            strings.TrimSpace(req.Endpoint),
		APIKey:              apiKey,
		Model:               strings.TrimSpace(req.Model),
		ProxyURL:            strings.TrimSpace(req.ProxyURL),
		FirstTokenTimeoutMS: firstTokenTimeout,
		OverallTimeoutMS:    overallTimeout,
		Enabled:             enabled,
	}

	if err := h.db.UpdateLLMConfigByID(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "LLM 配置更新成功",
		Data: LLMConfigItem{
			ID:                  updatedCfg.ID,
			Name:                updatedCfg.Name,
			Provider:            updatedCfg.Provider,
			Endpoint:            updatedCfg.Endpoint,
			HasAPIKey:           len(apiKey) > 0,
			Model:               updatedCfg.Model,
			ProxyURL:            updatedCfg.ProxyURL,
			FirstTokenTimeoutMS: updatedCfg.FirstTokenTimeoutMS,
			OverallTimeoutMS:    updatedCfg.OverallTimeoutMS,
			Enabled:             updatedCfg.Enabled,
			UpdatedAt:           time.Now(),
		},
	})
}

// handleDeleteLLMConfig 删除指定 ID 的 LLM 配置记录。
func (h *AdminHandler) handleDeleteLLMConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteLLMConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteLLMConfig(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrLLMConfigNotFound) {
			http.Error(w, "llm config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete llm config", "id", req.ID, "error", err)
		http.Error(w, "failed to delete llm config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "LLM 配置删除成功",
	})
}

// handleBatchDeleteLLMConfigs 批量删除 LLM 配置记录。
func (h *AdminHandler) handleBatchDeleteLLMConfigs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteLLMConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.db.BatchDeleteLLMConfigs(r.Context(), req.IDs); err != nil {
		h.logger.Error("failed to batch delete llm configs", "error", err)
		http.Error(w, "failed to batch delete llm configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 LLM 配置", len(req.IDs)),
	})
}

// handleListTTSConfigs 分页获取 TTS 语音合成配置列表。
func (h *AdminHandler) handleListTTSConfigs(w http.ResponseWriter, r *http.Request) {
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

	filter := database.TTSConfigFilter{
		Name:     query.Get("name"),
		Provider: query.Get("provider"),
		Page:     page,
		PageSize: pageSize,
	}

	if enabledStr := query.Get("enabled"); enabledStr != "" {
		if enabledVal, err := strconv.ParseBool(enabledStr); err == nil {
			filter.Enabled = &enabledVal
		}
	}

	configs, total, err := h.db.ListTTSConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list tts configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]TTSConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, TTSConfigItem{
			ID:                  cfg.ID,
			Name:                cfg.Name,
			Provider:            cfg.Provider,
			Endpoint:            cfg.Endpoint,
			HasAPIKey:           len(strings.TrimSpace(cfg.APIKey)) > 0,
			Model:               cfg.Model,
			Voices:              cfg.Voices,
			ProxyURL:            cfg.ProxyURL,
			ConnectTimeoutMS:    cfg.ConnectTimeoutMS,
			FirstAudioTimeoutMS: cfg.FirstAudioTimeoutMS,
			SentenceTimeoutMS:   cfg.SentenceTimeoutMS,
			Enabled:             cfg.Enabled,
			CreatedAt:           cfg.CreatedAt,
			UpdatedAt:           cfg.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: TTSConfigListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleSaveTTSConfig 创建或更新 TTS 语音合成配置（ID 为 0 时创建，ID > 0 时按 ID 覆盖）。
func (h *AdminHandler) handleSaveTTSConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveTTSConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	connectTimeout := req.ConnectTimeoutMS
	if connectTimeout == 0 {
		connectTimeout = 5000
	}

	firstAudioTimeout := req.FirstAudioTimeoutMS
	if firstAudioTimeout == 0 {
		firstAudioTimeout = 5000
	}

	sentenceTimeout := req.SentenceTimeoutMS
	if sentenceTimeout == 0 {
		sentenceTimeout = 10000
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	provider := strings.TrimSpace(req.Provider)

	voices := strings.TrimSpace(req.Voices)
	if voices == "" {
		voices = "[]"
	}

	if req.ID == 0 {
		// 创建新配置
		cfg := &database.TTSConfig{
			Name:                strings.TrimSpace(req.Name),
			Provider:            provider,
			Endpoint:            strings.TrimSpace(req.Endpoint),
			APIKey:              strings.TrimSpace(req.APIKey),
			Model:               strings.TrimSpace(req.Model),
			Voices:              voices,
			ProxyURL:            strings.TrimSpace(req.ProxyURL),
			ConnectTimeoutMS:    connectTimeout,
			FirstAudioTimeoutMS: firstAudioTimeout,
			SentenceTimeoutMS:   sentenceTimeout,
			Enabled:             enabled,
		}

		if err := h.db.CreateTTSConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "TTS 配置创建成功",
			Data: TTSConfigItem{
				ID:                  cfg.ID,
				Name:                cfg.Name,
				Provider:            cfg.Provider,
				Endpoint:            cfg.Endpoint,
				HasAPIKey:           len(cfg.APIKey) > 0,
				Model:               cfg.Model,
				Voices:              cfg.Voices,
				ProxyURL:            cfg.ProxyURL,
				ConnectTimeoutMS:    cfg.ConnectTimeoutMS,
				FirstAudioTimeoutMS: cfg.FirstAudioTimeoutMS,
				SentenceTimeoutMS:   cfg.SentenceTimeoutMS,
				Enabled:             cfg.Enabled,
				CreatedAt:           cfg.CreatedAt,
				UpdatedAt:           cfg.UpdatedAt,
			},
		})
		return
	}

	// 覆盖更新已有配置
	existing, err := h.db.FindTTSConfigByID(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, database.ErrTTSConfigNotFound) {
			http.Error(w, "tts config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	apiKey := existing.APIKey
	if strings.TrimSpace(req.APIKey) != "" {
		apiKey = strings.TrimSpace(req.APIKey)
	}

	if strings.TrimSpace(req.Provider) == "" {
		provider = existing.Provider
	}

	if req.Enabled == nil {
		enabled = existing.Enabled
	}

	if strings.TrimSpace(req.Voices) == "" {
		voices = existing.Voices
	}

	updatedCfg := &database.TTSConfig{
		ID:                  req.ID,
		Name:                strings.TrimSpace(req.Name),
		Provider:            provider,
		Endpoint:            strings.TrimSpace(req.Endpoint),
		APIKey:              apiKey,
		Model:               strings.TrimSpace(req.Model),
		Voices:              voices,
		ProxyURL:            strings.TrimSpace(req.ProxyURL),
		ConnectTimeoutMS:    connectTimeout,
		FirstAudioTimeoutMS: firstAudioTimeout,
		SentenceTimeoutMS:   sentenceTimeout,
		Enabled:             enabled,
	}

	if err := h.db.UpdateTTSConfigByID(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "TTS 配置更新成功",
		Data: TTSConfigItem{
			ID:                  updatedCfg.ID,
			Name:                updatedCfg.Name,
			Provider:            updatedCfg.Provider,
			Endpoint:            updatedCfg.Endpoint,
			HasAPIKey:           len(apiKey) > 0,
			Model:               updatedCfg.Model,
			Voices:              updatedCfg.Voices,
			ProxyURL:            updatedCfg.ProxyURL,
			ConnectTimeoutMS:    updatedCfg.ConnectTimeoutMS,
			FirstAudioTimeoutMS: updatedCfg.FirstAudioTimeoutMS,
			SentenceTimeoutMS:   updatedCfg.SentenceTimeoutMS,
			Enabled:             updatedCfg.Enabled,
			UpdatedAt:           time.Now(),
		},
	})
}

// handleDeleteTTSConfig 删除指定 ID 的 TTS 配置记录。
func (h *AdminHandler) handleDeleteTTSConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteTTSConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteTTSConfig(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrTTSConfigNotFound) {
			http.Error(w, "tts config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete tts config", "id", req.ID, "error", err)
		http.Error(w, "failed to delete tts config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "TTS 配置删除成功",
	})
}

// handleBatchDeleteTTSConfigs 批量删除 TTS 配置记录。
func (h *AdminHandler) handleBatchDeleteTTSConfigs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteTTSConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.db.BatchDeleteTTSConfigs(r.Context(), req.IDs); err != nil {
		h.logger.Error("failed to batch delete tts configs", "error", err)
		http.Error(w, "failed to batch delete tts configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 TTS 配置", len(req.IDs)),
	})
}

// handleListAgentConfigs 分页获取 Agent 配置列表。
func (h *AdminHandler) handleListAgentConfigs(w http.ResponseWriter, r *http.Request) {
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

	filter := database.AgentConfigFilter{
		Name:     query.Get("name"),
		Page:     page,
		PageSize: pageSize,
	}

	if enabledStr := query.Get("enabled"); enabledStr != "" {
		if enabledVal, err := strconv.ParseBool(enabledStr); err == nil {
			filter.Enabled = &enabledVal
		}
	}

	configs, total, err := h.db.ListAgentConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list agent configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 收集引用的 ASR, LLM, TTS ID 并填充名称
	asrMap := make(map[uint64]string)
	llmMap := make(map[uint64]string)
	ttsMap := make(map[uint64]string)
	for _, cfg := range configs {
		if cfg.ASRConfigID > 0 {
			if _, ok := asrMap[cfg.ASRConfigID]; !ok {
				if asr, err := h.db.FindASRConfigByID(r.Context(), cfg.ASRConfigID); err == nil && asr != nil {
					asrMap[cfg.ASRConfigID] = asr.Name
				}
			}
		}
		if cfg.LLMConfigID > 0 {
			if _, ok := llmMap[cfg.LLMConfigID]; !ok {
				if llm, err := h.db.FindLLMConfigByID(r.Context(), cfg.LLMConfigID); err == nil && llm != nil {
					llmMap[cfg.LLMConfigID] = llm.Name
				}
			}
		}
		if cfg.TTSConfigID > 0 {
			if _, ok := ttsMap[cfg.TTSConfigID]; !ok {
				if tts, err := h.db.FindTTSConfigByID(r.Context(), cfg.TTSConfigID); err == nil && tts != nil {
					ttsMap[cfg.TTSConfigID] = tts.Name
				}
			}
		}
	}

	items := make([]AgentConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, AgentConfigItem{
			ID:           cfg.ID,
			Name:         cfg.Name,
			ASRConfigID:  cfg.ASRConfigID,
			ASRName:      asrMap[cfg.ASRConfigID],
			LLMConfigID:  cfg.LLMConfigID,
			LLMName:      llmMap[cfg.LLMConfigID],
			TTSConfigID:  cfg.TTSConfigID,
			TTSName:      ttsMap[cfg.TTSConfigID],
			SystemPrompt: cfg.SystemPrompt,
			Voice:        cfg.Voice,
			Enabled:      cfg.Enabled,
			CreatedAt:    cfg.CreatedAt,
			UpdatedAt:    cfg.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: AgentConfigListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleSaveAgentConfig 创建或更新 Agent 配置（ID 为 0 时创建，ID > 0 时按 ID 覆盖）。
func (h *AdminHandler) handleSaveAgentConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveAgentConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if req.ID == 0 {
		// 创建新配置
		cfg := &database.AgentConfig{
			Name:         strings.TrimSpace(req.Name),
			ASRConfigID:  req.ASRConfigID,
			LLMConfigID:  req.LLMConfigID,
			TTSConfigID:  req.TTSConfigID,
			SystemPrompt: strings.TrimSpace(req.SystemPrompt),
			Voice:        strings.TrimSpace(req.Voice),
			Enabled:      false,
		}

		if err := h.db.CreateAgentConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if enabled {
			if err := h.db.ActivateAgent(r.Context(), cfg.ID); err != nil {
				h.logger.Warn("failed to activate newly created agent", "id", cfg.ID, "error", err)
			} else {
				cfg.Enabled = true
			}
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "Agent 配置创建成功",
			Data: AgentConfigItem{
				ID:           cfg.ID,
				Name:         cfg.Name,
				ASRConfigID:  cfg.ASRConfigID,
				LLMConfigID:  cfg.LLMConfigID,
				TTSConfigID:  cfg.TTSConfigID,
				SystemPrompt: cfg.SystemPrompt,
				Voice:        cfg.Voice,
				Enabled:      cfg.Enabled,
				CreatedAt:    cfg.CreatedAt,
				UpdatedAt:    cfg.UpdatedAt,
			},
		})
		return
	}

	// 覆盖更新已有配置
	existing, err := h.db.FindAgentConfigByID(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, database.ErrAgentConfigNotFound) {
			http.Error(w, "agent config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	asrID := existing.ASRConfigID
	if req.ASRConfigID > 0 {
		asrID = req.ASRConfigID
	}
	llmID := existing.LLMConfigID
	if req.LLMConfigID > 0 {
		llmID = req.LLMConfigID
	}
	ttsID := existing.TTSConfigID
	if req.TTSConfigID > 0 {
		ttsID = req.TTSConfigID
	}

	name := existing.Name
	if strings.TrimSpace(req.Name) != "" {
		name = strings.TrimSpace(req.Name)
	}

	systemPrompt := existing.SystemPrompt
	if strings.TrimSpace(req.SystemPrompt) != "" {
		systemPrompt = strings.TrimSpace(req.SystemPrompt)
	}

	voice := existing.Voice
	if strings.TrimSpace(req.Voice) != "" {
		voice = strings.TrimSpace(req.Voice)
	}

	updatedCfg := &database.AgentConfig{
		ID:           req.ID,
		Name:         name,
		ASRConfigID:  asrID,
		LLMConfigID:  llmID,
		TTSConfigID:  ttsID,
		SystemPrompt: systemPrompt,
		Voice:        voice,
		Enabled:      existing.Enabled,
	}

	if err := h.db.UpdateAgentConfigByID(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Enabled != nil && *req.Enabled && !existing.Enabled {
		if err := h.db.ActivateAgent(r.Context(), req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updatedCfg.Enabled = true
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "Agent 配置更新成功",
		Data: AgentConfigItem{
			ID:           updatedCfg.ID,
			Name:         updatedCfg.Name,
			ASRConfigID:  updatedCfg.ASRConfigID,
			LLMConfigID:  updatedCfg.LLMConfigID,
			TTSConfigID:  updatedCfg.TTSConfigID,
			SystemPrompt: updatedCfg.SystemPrompt,
			Voice:        updatedCfg.Voice,
			Enabled:      updatedCfg.Enabled,
			UpdatedAt:    time.Now(),
		},
	})
}

// handleDeleteAgentConfig 删除指定 ID 的 Agent 配置记录。
func (h *AdminHandler) handleDeleteAgentConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteAgentConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteAgentConfig(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrAgentConfigNotFound) {
			http.Error(w, "agent config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete agent config", "id", req.ID, "error", err)
		http.Error(w, "failed to delete agent config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "Agent 配置删除成功",
	})
}

// handleBatchDeleteAgentConfigs 批量删除 Agent 配置记录。
func (h *AdminHandler) handleBatchDeleteAgentConfigs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteAgentConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.db.BatchDeleteAgentConfigs(r.Context(), req.IDs); err != nil {
		h.logger.Error("failed to batch delete agent configs", "error", err)
		http.Error(w, "failed to batch delete agent configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 Agent 配置", len(req.IDs)),
	})
}

// handleActivateAgentConfig 激活指定 ID 的 Agent 配置。
func (h *AdminHandler) handleActivateAgentConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req ActivateAgentConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.ActivateAgent(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrAgentConfigNotFound) {
			http.Error(w, "agent config not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, database.ErrReferencedASRNotFound) ||
			errors.Is(err, database.ErrReferencedASRDisabled) ||
			errors.Is(err, database.ErrReferencedLLMNotFound) ||
			errors.Is(err, database.ErrReferencedLLMDisabled) ||
			errors.Is(err, database.ErrReferencedTTSNotFound) ||
			errors.Is(err, database.ErrReferencedTTSDisabled) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.logger.Error("failed to activate agent config", "id", req.ID, "error", err)
		http.Error(w, "failed to activate agent config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "Agent 配置激活成功",
	})
}

// handleListDeviceTypes 分页获取设备类型与 Agent 关联配置列表。
func (h *AdminHandler) handleListDeviceTypes(w http.ResponseWriter, r *http.Request) {
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

	var agentConfigID uint64
	if agentIDStr := query.Get("agent_config_id"); agentIDStr != "" {
		if id, err := strconv.ParseUint(agentIDStr, 10, 64); err == nil {
			agentConfigID = id
		}
	}

	filter := database.DeviceTypeFilter{
		DeviceType:    query.Get("device_type"),
		AgentConfigID: agentConfigID,
		Page:          page,
		PageSize:      pageSize,
	}

	types, total, err := h.db.ListDeviceTypes(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list device types", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	agentMap := make(map[uint64]string)
	for _, dt := range types {
		if dt.AgentConfigID > 0 {
			if _, ok := agentMap[dt.AgentConfigID]; !ok {
				if agent, err := h.db.FindAgentConfigByID(r.Context(), dt.AgentConfigID); err == nil && agent != nil {
					agentMap[dt.AgentConfigID] = agent.Name
				}
			}
		}
	}

	items := make([]DeviceTypeItem, 0, len(types))
	for _, dt := range types {
		items = append(items, DeviceTypeItem{
			ID:            dt.ID,
			DeviceType:    dt.DeviceType,
			AgentConfigID: dt.AgentConfigID,
			AgentName:     agentMap[dt.AgentConfigID],
			CreatedAt:     dt.CreatedAt,
			UpdatedAt:     dt.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: DeviceTypeListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleSaveDeviceType 创建或更新设备类型配置（ID 为 0 时创建，ID > 0 时按 ID 覆盖）。
func (h *AdminHandler) handleSaveDeviceType(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveDeviceTypeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	req.DeviceType = strings.TrimSpace(req.DeviceType)
	if req.DeviceType == "" {
		http.Error(w, "device_type cannot be empty", http.StatusBadRequest)
		return
	}
	if req.AgentConfigID == 0 {
		http.Error(w, "agent_config_id is required and must be positive", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		// 创建新配置
		dt := &database.DeviceType{
			DeviceType:    req.DeviceType,
			AgentConfigID: req.AgentConfigID,
		}

		if err := h.db.CreateDeviceType(r.Context(), dt); err != nil {
			if errors.Is(err, database.ErrDeviceTypeAlreadyExists) ||
				errors.Is(err, database.ErrReferencedAgentNotFound) ||
				errors.Is(err, database.ErrEmptyDeviceType) ||
				errors.Is(err, database.ErrInvalidDeviceTypeLength) ||
				errors.Is(err, database.ErrInvalidAgentConfigID) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			h.logger.Error("failed to create device type", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var agentName string
		if agent, err := h.db.FindAgentConfigByID(r.Context(), dt.AgentConfigID); err == nil && agent != nil {
			agentName = agent.Name
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "设备类型配置创建成功",
			Data: DeviceTypeItem{
				ID:            dt.ID,
				DeviceType:    dt.DeviceType,
				AgentConfigID: dt.AgentConfigID,
				AgentName:     agentName,
				CreatedAt:     dt.CreatedAt,
				UpdatedAt:     dt.UpdatedAt,
			},
		})
		return
	}

	// 覆盖更新已有配置
	existing, err := h.db.FindDeviceTypeByID(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, database.ErrDeviceTypeNotFound) {
			http.Error(w, "device type not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	updatedDt := &database.DeviceType{
		ID:            existing.ID,
		DeviceType:    req.DeviceType,
		AgentConfigID: req.AgentConfigID,
	}

	if err := h.db.UpdateDeviceTypeByID(r.Context(), updatedDt); err != nil {
		if errors.Is(err, database.ErrDeviceTypeAlreadyExists) ||
			errors.Is(err, database.ErrReferencedAgentNotFound) ||
			errors.Is(err, database.ErrEmptyDeviceType) ||
			errors.Is(err, database.ErrInvalidDeviceTypeLength) ||
			errors.Is(err, database.ErrInvalidAgentConfigID) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, database.ErrDeviceTypeNotFound) {
			http.Error(w, "device type not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to update device type", "id", req.ID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var agentName string
	if agent, err := h.db.FindAgentConfigByID(r.Context(), updatedDt.AgentConfigID); err == nil && agent != nil {
		agentName = agent.Name
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "设备类型配置更新成功",
		Data: DeviceTypeItem{
			ID:            updatedDt.ID,
			DeviceType:    updatedDt.DeviceType,
			AgentConfigID: updatedDt.AgentConfigID,
			AgentName:     agentName,
			CreatedAt:     existing.CreatedAt,
			UpdatedAt:     time.Now(),
		},
	})
}

// handleDeleteDeviceType 删除指定 ID 的设备类型配置记录。
func (h *AdminHandler) handleDeleteDeviceType(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteDeviceTypeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.ID == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteDeviceType(r.Context(), req.ID); err != nil {
		if errors.Is(err, database.ErrDeviceTypeNotFound) {
			http.Error(w, "device type not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete device type", "id", req.ID, "error", err)
		http.Error(w, "failed to delete device type", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "设备类型配置删除成功",
	})
}

// handleBatchDeleteDeviceTypes 批量删除设备类型配置记录。
func (h *AdminHandler) handleBatchDeleteDeviceTypes(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteDeviceTypeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.db.BatchDeleteDeviceTypes(r.Context(), req.IDs); err != nil {
		h.logger.Error("failed to batch delete device types", "error", err)
		http.Error(w, "failed to batch delete device types", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条设备类型配置", len(req.IDs)),
	})
}




