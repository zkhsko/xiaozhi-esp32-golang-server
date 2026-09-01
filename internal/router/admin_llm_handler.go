package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
)

// LLMConfigStore 定义 LLM 配置管理所需的窄持久化接口。
type LLMConfigStore interface {
	ListLLMConfigs(ctx context.Context, filter database.LLMConfigFilter) ([]*database.LLMConfig, int64, error)
	FindLLMConfigById(ctx context.Context, id uint64) (*database.LLMConfig, error)
	CreateLLMConfig(ctx context.Context, cfg *database.LLMConfig) error
	UpdateLLMConfigById(ctx context.Context, cfg *database.LLMConfig) error
	DeleteLLMConfig(ctx context.Context, id uint64) error
	BatchDeleteLLMConfigs(ctx context.Context, ids []uint64) error
}

// LLMConfigItem 表示单条 LLM 配置 DTO。
type LLMConfigItem struct {
	Id                  uint64    `json:"id"`
	Name                string    `json:"name"`
	Provider            string    `json:"provider"`
	Endpoint            string    `json:"endpoint"`
	HasAPIKey           bool      `json:"has_api_key"` // 是否已配置 API Key（脱敏，不输出明文）
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
	Id                  uint64 `json:"id"`
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
	Id uint64 `json:"id"`
}

// BatchDeleteLLMConfigRequest 批量删除 LLM 配置请求体。
type BatchDeleteLLMConfigRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminLLMHandler 处理 LLM 大语言模型配置相关的管理端接口。
type AdminLLMHandler struct {
	store  LLMConfigStore
	logger *slog.Logger
}

// NewAdminLLMHandler 创建 AdminLLMHandler 实例。
func NewAdminLLMHandler(store LLMConfigStore, l *slog.Logger) *AdminLLMHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminLLMHandler{
		store:  store,
		logger: l,
	}
}

// handleListLLMConfigs 分页获取 LLM 大语言模型配置列表。
func (h *AdminLLMHandler) handleListLLMConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

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

	configs, total, err := h.store.ListLLMConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list llm configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]LLMConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, LLMConfigItem{
			Id:                  cfg.Id,
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

// handleSaveLLMConfig 创建或更新 LLM 大语言模型配置（Id 为 0 时创建，Id > 0 时按 Id 覆盖）。
func (h *AdminLLMHandler) handleSaveLLMConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveLLMConfigRequest
	if !decodeJSON(w, r, &req) {
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

	if req.Id == 0 {
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

		if err := h.store.CreateLLMConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "LLM 配置创建成功",
			Data: LLMConfigItem{
				Id:                  cfg.Id,
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
	existing, err := h.store.FindLLMConfigById(r.Context(), req.Id)
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
		Id:                  req.Id,
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

	if err := h.store.UpdateLLMConfigById(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "LLM 配置更新成功",
		Data: LLMConfigItem{
			Id:                  updatedCfg.Id,
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

// handleDeleteLLMConfig 删除指定 Id 的 LLM 配置记录。
func (h *AdminLLMHandler) handleDeleteLLMConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteLLMConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteLLMConfig(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrLLMConfigNotFound) {
			http.Error(w, "llm config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete llm config", "id", req.Id, "error", err)
		http.Error(w, "failed to delete llm config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "LLM 配置删除成功",
	})
}

// handleBatchDeleteLLMConfigs 批量删除 LLM 配置记录。
func (h *AdminLLMHandler) handleBatchDeleteLLMConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteLLMConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteLLMConfigs(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete llm configs", "error", err)
		http.Error(w, "failed to batch delete llm configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 LLM 配置", len(req.Ids)),
	})
}
