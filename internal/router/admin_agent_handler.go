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

// AgentConfigStore 定义 Agent 配置管理所需的窄持久化接口。
type AgentConfigStore interface {
	ListAgentConfigs(ctx context.Context, filter database.AgentConfigFilter) ([]*database.AgentConfig, int64, error)
	FindAgentConfigById(ctx context.Context, id uint64) (*database.AgentConfig, error)
	CreateAgentConfig(ctx context.Context, cfg *database.AgentConfig) error
	UpdateAgentConfigById(ctx context.Context, cfg *database.AgentConfig) error
	DeleteAgentConfig(ctx context.Context, id uint64) error
	BatchDeleteAgentConfigs(ctx context.Context, ids []uint64) error
	FindASRConfigById(ctx context.Context, id uint64) (*database.ASRConfig, error)
	FindLLMConfigById(ctx context.Context, id uint64) (*database.LLMConfig, error)
	FindTTSConfigById(ctx context.Context, id uint64) (*database.TTSConfig, error)
}

// AgentConfigItem 表示单条 Agent 配置 DTO。
type AgentConfigItem struct {
	Id           uint64    `json:"id"`
	Name         string    `json:"name"`
	ASRConfigId  uint64    `json:"asr_config_id"`
	ASRName      string    `json:"asr_name,omitempty"`
	LLMConfigId  uint64    `json:"llm_config_id"`
	LLMName      string    `json:"llm_name,omitempty"`
	TTSConfigId  uint64    `json:"tts_config_id"`
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
	Id           uint64 `json:"id"`
	Name         string `json:"name"`
	ASRConfigId  uint64 `json:"asr_config_id"`
	LLMConfigId  uint64 `json:"llm_config_id"`
	TTSConfigId  uint64 `json:"tts_config_id"`
	SystemPrompt string `json:"system_prompt"`
	Voice        string `json:"voice"`
	Enabled      *bool  `json:"enabled"`
}

// DeleteAgentConfigRequest 删除单条 Agent 配置请求体。
type DeleteAgentConfigRequest struct {
	Id uint64 `json:"id"`
}

// BatchDeleteAgentConfigRequest 批量删除 Agent 配置请求体。
type BatchDeleteAgentConfigRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminAgentHandler 处理 Agent 智能体配置相关的管理端接口。
type AdminAgentHandler struct {
	store  AgentConfigStore
	logger *slog.Logger
}

// NewAdminAgentHandler 创建 AdminAgentHandler 实例。
func NewAdminAgentHandler(store AgentConfigStore, l *slog.Logger) *AdminAgentHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminAgentHandler{
		store:  store,
		logger: l,
	}
}

// handleListAgentConfigs 分页获取 Agent 配置列表。
func (h *AdminAgentHandler) handleListAgentConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

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

	configs, total, err := h.store.ListAgentConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list agent configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 收集引用的 ASR, LLM, TTS Id 并填充名称
	asrMap := make(map[uint64]string)
	llmMap := make(map[uint64]string)
	ttsMap := make(map[uint64]string)
	for _, cfg := range configs {
		if cfg.ASRConfigId > 0 {
			if _, ok := asrMap[cfg.ASRConfigId]; !ok {
				if asr, err := h.store.FindASRConfigById(r.Context(), cfg.ASRConfigId); err == nil && asr != nil {
					asrMap[cfg.ASRConfigId] = asr.Name
				}
			}
		}
		if cfg.LLMConfigId > 0 {
			if _, ok := llmMap[cfg.LLMConfigId]; !ok {
				if llm, err := h.store.FindLLMConfigById(r.Context(), cfg.LLMConfigId); err == nil && llm != nil {
					llmMap[cfg.LLMConfigId] = llm.Name
				}
			}
		}
		if cfg.TTSConfigId > 0 {
			if _, ok := ttsMap[cfg.TTSConfigId]; !ok {
				if tts, err := h.store.FindTTSConfigById(r.Context(), cfg.TTSConfigId); err == nil && tts != nil {
					ttsMap[cfg.TTSConfigId] = tts.Name
				}
			}
		}
	}

	items := make([]AgentConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, AgentConfigItem{
			Id:           cfg.Id,
			Name:         cfg.Name,
			ASRConfigId:  cfg.ASRConfigId,
			ASRName:      asrMap[cfg.ASRConfigId],
			LLMConfigId:  cfg.LLMConfigId,
			LLMName:      llmMap[cfg.LLMConfigId],
			TTSConfigId:  cfg.TTSConfigId,
			TTSName:      ttsMap[cfg.TTSConfigId],
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

// handleSaveAgentConfig 创建或更新 Agent 配置（Id 为 0 时创建，Id > 0 时按 Id 覆盖）。
func (h *AdminAgentHandler) handleSaveAgentConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveAgentConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if req.Id == 0 {
		// 创建新配置
		cfg := &database.AgentConfig{
			Name:         strings.TrimSpace(req.Name),
			ASRConfigId:  req.ASRConfigId,
			LLMConfigId:  req.LLMConfigId,
			TTSConfigId:  req.TTSConfigId,
			SystemPrompt: strings.TrimSpace(req.SystemPrompt),
			Voice:        strings.TrimSpace(req.Voice),
			Enabled:      enabled,
		}

		if err := h.store.CreateAgentConfig(r.Context(), cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "Agent 配置创建成功",
			Data: AgentConfigItem{
				Id:           cfg.Id,
				Name:         cfg.Name,
				ASRConfigId:  cfg.ASRConfigId,
				LLMConfigId:  cfg.LLMConfigId,
				TTSConfigId:  cfg.TTSConfigId,
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
	existing, err := h.store.FindAgentConfigById(r.Context(), req.Id)
	if err != nil {
		if errors.Is(err, database.ErrAgentConfigNotFound) {
			http.Error(w, "agent config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	asrId := existing.ASRConfigId
	if req.ASRConfigId > 0 {
		asrId = req.ASRConfigId
	}
	llmId := existing.LLMConfigId
	if req.LLMConfigId > 0 {
		llmId = req.LLMConfigId
	}
	ttsId := existing.TTSConfigId
	if req.TTSConfigId > 0 {
		ttsId = req.TTSConfigId
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

	enabled = existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	updatedCfg := &database.AgentConfig{
		Id:           req.Id,
		Name:         name,
		ASRConfigId:  asrId,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: systemPrompt,
		Voice:        voice,
		Enabled:      enabled,
	}

	if err := h.store.UpdateAgentConfigById(r.Context(), updatedCfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "Agent 配置更新成功",
		Data: AgentConfigItem{
			Id:           updatedCfg.Id,
			Name:         updatedCfg.Name,
			ASRConfigId:  updatedCfg.ASRConfigId,
			LLMConfigId:  updatedCfg.LLMConfigId,
			TTSConfigId:  updatedCfg.TTSConfigId,
			SystemPrompt: updatedCfg.SystemPrompt,
			Voice:        updatedCfg.Voice,
			Enabled:      updatedCfg.Enabled,
			UpdatedAt:    time.Now(),
		},
	})
}

// handleDeleteAgentConfig 删除指定 Id 的 Agent 配置记录。
func (h *AdminAgentHandler) handleDeleteAgentConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteAgentConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteAgentConfig(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrAgentConfigNotFound) {
			http.Error(w, "agent config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete agent config", "id", req.Id, "error", err)
		http.Error(w, "failed to delete agent config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "Agent 配置删除成功",
	})
}

// handleBatchDeleteAgentConfigs 批量删除 Agent 配置记录。
func (h *AdminAgentHandler) handleBatchDeleteAgentConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteAgentConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteAgentConfigs(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete agent configs", "error", err)
		http.Error(w, "failed to batch delete agent configs", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条 Agent 配置", len(req.Ids)),
	})
}
