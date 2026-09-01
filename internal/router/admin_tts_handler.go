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

// TTSConfigStore 定义 TTS 配置管理所需的窄持久化接口。
type TTSConfigStore interface {
	ListTTSConfigs(ctx context.Context, filter database.TTSConfigFilter) ([]*database.TTSConfig, int64, error)
	FindTTSConfigById(ctx context.Context, id uint64) (*database.TTSConfig, error)
	CreateTTSConfig(ctx context.Context, cfg *database.TTSConfig) error
	UpdateTTSConfigById(ctx context.Context, cfg *database.TTSConfig) error
	DeleteTTSConfig(ctx context.Context, id uint64) error
	BatchDeleteTTSConfigs(ctx context.Context, ids []uint64) error
}

// TTSConfigItem 表示单条 TTS 配置 DTO。
type TTSConfigItem struct {
	Id                  uint64    `json:"id"`
	Name                string    `json:"name"`
	Provider            string    `json:"provider"`
	Endpoint            string    `json:"endpoint"`
	HasAPIKey           bool      `json:"has_api_key"` // 是否已配置 API Key（脱敏，不输出明文）
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
	Id                  uint64 `json:"id"`
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
	Id uint64 `json:"id"`
}

// BatchDeleteTTSConfigRequest 批量删除 TTS 配置请求体。
type BatchDeleteTTSConfigRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminTTSHandler 处理 TTS 语音合成配置相关的管理端接口。
type AdminTTSHandler struct {
	store  TTSConfigStore
	logger *slog.Logger
}

// NewAdminTTSHandler 创建 AdminTTSHandler 实例。
func NewAdminTTSHandler(store TTSConfigStore, l *slog.Logger) *AdminTTSHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminTTSHandler{
		store:  store,
		logger: l,
	}
}

// handleListTTSConfigs 分页获取 TTS 语音合成配置列表。
func (h *AdminTTSHandler) handleListTTSConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

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

	configs, total, err := h.store.ListTTSConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list tts configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]TTSConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, TTSConfigItem{
			Id:                  cfg.Id,
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

// handleSaveTTSConfig 创建或更新 TTS 语音合成配置（Id 为 0 时创建，Id > 0 时按 Id 覆盖）。
func (h *AdminTTSHandler) handleSaveTTSConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveTTSConfigRequest
	if !decodeJSON(w, r, &req) {
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

	if req.Id == 0 {
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

		if err := h.store.CreateTTSConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "TTS 配置创建成功",
			Data: TTSConfigItem{
				Id:                  cfg.Id,
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
	existing, err := h.store.FindTTSConfigById(r.Context(), req.Id)
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
		Id:                  req.Id,
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

	if err := h.store.UpdateTTSConfigById(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "TTS 配置更新成功",
		Data: TTSConfigItem{
			Id:                  updatedCfg.Id,
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

// handleDeleteTTSConfig 删除指定 Id 的 TTS 配置记录。
func (h *AdminTTSHandler) handleDeleteTTSConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteTTSConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteTTSConfig(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrTTSConfigNotFound) {
			http.Error(w, "tts config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete tts config", "id", req.Id, "error", err)
		http.Error(w, "failed to delete tts config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "TTS 配置删除成功",
	})
}

// handleBatchDeleteTTSConfigs 批量删除 TTS 配置记录。
func (h *AdminTTSHandler) handleBatchDeleteTTSConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteTTSConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteTTSConfigs(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete tts configs", "error", err)
		http.Error(w, "failed to batch delete tts configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 TTS 配置", len(req.Ids)),
	})
}
