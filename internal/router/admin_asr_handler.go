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

// ASRConfigStore 定义 ASR 配置管理所需的窄持久化接口。
type ASRConfigStore interface {
	ListASRConfigs(ctx context.Context, filter database.ASRConfigFilter) ([]*database.ASRConfig, int64, error)
	FindASRConfigById(ctx context.Context, id uint64) (*database.ASRConfig, error)
	CreateASRConfig(ctx context.Context, cfg *database.ASRConfig) error
	UpdateASRConfigById(ctx context.Context, cfg *database.ASRConfig) error
	DeleteASRConfig(ctx context.Context, id uint64) error
	BatchDeleteASRConfigs(ctx context.Context, ids []uint64) error
}

// ASRConfigItem 表示单条 ASR 配置 DTO。
type ASRConfigItem struct {
	Id               uint64    `json:"id"`
	Name             string    `json:"name"`
	Provider         string    `json:"provider"`
	Endpoint         string    `json:"endpoint"`
	HasAPIKey        bool      `json:"has_api_key"` // 是否已配置 API Key（脱敏，不输出明文）
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
	Id               uint64 `json:"id"`
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
	Id uint64 `json:"id"`
}

// BatchDeleteASRConfigRequest 批量删除 ASR 配置请求体。
type BatchDeleteASRConfigRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminASRHandler 处理 ASR 语音识别配置相关的管理端接口。
type AdminASRHandler struct {
	store  ASRConfigStore
	logger *slog.Logger
}

// NewAdminASRHandler 创建 AdminASRHandler 实例。
func NewAdminASRHandler(store ASRConfigStore, l *slog.Logger) *AdminASRHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminASRHandler{
		store:  store,
		logger: l,
	}
}

// handleListASRConfigs 分页获取 ASR 语音识别配置列表。
func (h *AdminASRHandler) handleListASRConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

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

	configs, total, err := h.store.ListASRConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list asr configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]ASRConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, ASRConfigItem{
			Id:               cfg.Id,
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

// handleSaveASRConfig 创建或更新 ASR 语音识别配置（Id 为 0 时创建，Id > 0 时按 Id 覆盖）。
func (h *AdminASRHandler) handleSaveASRConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveASRConfigRequest
	if !decodeJSON(w, r, &req) {
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

	if req.Id == 0 {
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

		if err := h.store.CreateASRConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "ASR 配置创建成功",
			Data: ASRConfigItem{
				Id:               cfg.Id,
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
	existing, err := h.store.FindASRConfigById(r.Context(), req.Id)
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
		Id:               req.Id,
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

	if err := h.store.UpdateASRConfigById(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "ASR 配置更新成功",
		Data: ASRConfigItem{
			Id:               updatedCfg.Id,
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

// handleDeleteASRConfig 删除指定 Id 的 ASR 配置记录。
func (h *AdminASRHandler) handleDeleteASRConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteASRConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteASRConfig(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrASRConfigNotFound) {
			http.Error(w, "asr config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete asr config", "id", req.Id, "error", err)
		http.Error(w, "failed to delete asr config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "ASR 配置删除成功",
	})
}

// handleBatchDeleteASRConfigs 批量删除 ASR 配置记录。
func (h *AdminASRHandler) handleBatchDeleteASRConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteASRConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteASRConfigs(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete asr configs", "error", err)
		http.Error(w, "failed to batch delete asr configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 ASR 配置", len(req.Ids)),
	})
}
