package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
)

// AgentKitConfigStore 定义 AgentKit 内建工具管理所需的窄持久化接口。
type AgentKitConfigStore interface {
	ListAgentKitConfigs(ctx context.Context, filter database.AgentKitConfigFilter) ([]*database.AgentKitConfig, int64, error)
	FindAgentKitConfigById(ctx context.Context, id uint64) (*database.AgentKitConfig, error)
	FindAgentKitConfigByToolName(ctx context.Context, toolName string) (*database.AgentKitConfig, error)
	CreateAgentKitConfig(ctx context.Context, cfg *database.AgentKitConfig) error
	UpdateAgentKitConfigById(ctx context.Context, cfg *database.AgentKitConfig) error
	DeleteAgentKitConfig(ctx context.Context, id uint64) error
	BatchDeleteAgentKitConfigs(ctx context.Context, ids []uint64) error
}

// AgentKitConfigItem 表示单条 AgentKit 配置 DTO。
type AgentKitConfigItem struct {
	Id         uint64    `json:"id"`
	ToolName   string    `json:"tool_name"`
	ToolConfig string    `json:"tool_config"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AgentKitConfigListData AgentKit 配置列表响应数据。
type AgentKitConfigListData struct {
	Items    []AgentKitConfigItem `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

// SaveAgentKitConfigRequest 保存或更新 AgentKit 配置请求体。
type SaveAgentKitConfigRequest struct {
	Id         uint64 `json:"id"`
	ToolName   string `json:"tool_name"`
	ToolConfig string `json:"tool_config"`
	Enabled    *bool  `json:"enabled"`
}

// DeleteAgentKitConfigRequest 删除单条 AgentKit 配置请求体。
type DeleteAgentKitConfigRequest struct {
	Id uint64 `json:"id"`
}

// BatchDeleteAgentKitConfigRequest 批量删除 AgentKit 配置请求体。
type BatchDeleteAgentKitConfigRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminAgentKitHandler 处理 AgentKit 内建工具配置相关的管理端接口。
type AdminAgentKitHandler struct {
	store  AgentKitConfigStore
	logger *slog.Logger
}

// NewAdminAgentKitHandler 创建 AdminAgentKitHandler 实例。
func NewAdminAgentKitHandler(store AgentKitConfigStore, l *slog.Logger) *AdminAgentKitHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminAgentKitHandler{
		store:  store,
		logger: l,
	}
}

// handleListAgentKitConfigs 分页获取 AgentKit 内建工具配置列表。
func (h *AdminAgentKitHandler) handleListAgentKitConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

	filter := database.AgentKitConfigFilter{
		ToolName: query.Get("tool_name"),
		Page:     page,
		PageSize: pageSize,
	}

	if enabledStr := query.Get("enabled"); enabledStr != "" {
		if enabledVal, err := strconv.ParseBool(enabledStr); err == nil {
			filter.Enabled = &enabledVal
		}
	}

	configs, total, err := h.store.ListAgentKitConfigs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list agentkit configs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]AgentKitConfigItem, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, AgentKitConfigItem{
			Id:         cfg.Id,
			ToolName:   cfg.ToolName,
			ToolConfig: cfg.ToolConfig,
			Enabled:    cfg.Enabled,
			CreatedAt:  cfg.CreatedAt,
			UpdatedAt:  cfg.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Data: AgentKitConfigListData{
			Items:    items,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// handleSaveAgentKitConfig 创建或更新 AgentKit 内建工具配置（Id 为 0 时创建，Id > 0 时按 Id 覆盖）。
func (h *AdminAgentKitHandler) handleSaveAgentKitConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveAgentKitConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	toolName := strings.TrimSpace(req.ToolName)
	if toolName == "" {
		http.Error(w, database.ErrEmptyAgentKitToolName.Error(), http.StatusBadRequest)
		return
	}
	if len(toolName) > 128 {
		http.Error(w, database.ErrInvalidAgentKitToolNameLength.Error(), http.StatusBadRequest)
		return
	}

	toolConfig := strings.TrimSpace(req.ToolConfig)
	if toolConfig == "" {
		http.Error(w, database.ErrEmptyAgentKitToolConfig.Error(), http.StatusBadRequest)
		return
	}
	if !json.Valid([]byte(toolConfig)) {
		http.Error(w, database.ErrInvalidAgentKitToolConfigJSON.Error(), http.StatusBadRequest)
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if req.Id == 0 {
		// 创建新配置
		cfg := &database.AgentKitConfig{
			ToolName:   toolName,
			ToolConfig: toolConfig,
			Enabled:    enabled,
		}

		if err := h.store.CreateAgentKitConfig(r.Context(), cfg); err != nil {
			if errors.Is(err, database.ErrAgentKitToolNameDuplicate) {
				http.Error(w, "agentkit tool name already exists", http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "AgentKit 工具配置创建成功",
			Data: AgentKitConfigItem{
				Id:         cfg.Id,
				ToolName:   cfg.ToolName,
				ToolConfig: cfg.ToolConfig,
				Enabled:    cfg.Enabled,
				CreatedAt:  cfg.CreatedAt,
				UpdatedAt:  cfg.UpdatedAt,
			},
		})
		return
	}

	// 覆盖更新已有配置
	existing, err := h.store.FindAgentKitConfigById(r.Context(), req.Id)
	if err != nil {
		if errors.Is(err, database.ErrAgentKitConfigNotFound) {
			http.Error(w, "agentkit config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.Enabled == nil {
		enabled = existing.Enabled
	}

	existing.ToolName = toolName
	existing.ToolConfig = toolConfig
	existing.Enabled = enabled

	if err := h.store.UpdateAgentKitConfigById(r.Context(), existing); err != nil {
		if errors.Is(err, database.ErrAgentKitToolNameDuplicate) {
			http.Error(w, "agentkit tool name already exists", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "AgentKit 工具配置更新成功",
		Data: AgentKitConfigItem{
			Id:         existing.Id,
			ToolName:   existing.ToolName,
			ToolConfig: existing.ToolConfig,
			Enabled:    existing.Enabled,
			CreatedAt:  existing.CreatedAt,
			UpdatedAt:  existing.UpdatedAt,
		},
	})
}

// handleDeleteAgentKitConfig 删除指定 Id 的 AgentKit 工具配置。
func (h *AdminAgentKitHandler) handleDeleteAgentKitConfig(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteAgentKitConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "agentkit config id cannot be empty or zero", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteAgentKitConfig(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrAgentKitConfigNotFound) {
			http.Error(w, "agentkit config not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete agentkit config", "id", req.Id, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("AgentKit 配置 (ID: %d) 删除成功", req.Id),
	})
}

// handleBatchDeleteAgentKitConfigs 批量删除 AgentKit 工具配置。
func (h *AdminAgentKitHandler) handleBatchDeleteAgentKitConfigs(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteAgentKitConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteAgentKitConfigs(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete agentkit configs", "ids", req.Ids, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功删除 %d 条 AgentKit 配置", len(req.Ids)),
	})
}
