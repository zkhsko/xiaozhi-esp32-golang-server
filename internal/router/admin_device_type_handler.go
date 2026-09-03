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

// DeviceTypeStore 定义设备类型配置管理所需的窄持久化接口。
type DeviceTypeStore interface {
	ListDeviceTypes(ctx context.Context, filter database.DeviceTypeFilter) ([]*database.DeviceType, int64, error)
	FindDeviceTypeById(ctx context.Context, id uint64) (*database.DeviceType, error)
	CreateDeviceType(ctx context.Context, dt *database.DeviceType) error
	UpdateDeviceTypeById(ctx context.Context, dt *database.DeviceType) error
	DeleteDeviceType(ctx context.Context, id uint64) error
	BatchDeleteDeviceTypes(ctx context.Context, ids []uint64) error
	FindAgentConfigById(ctx context.Context, id uint64) (*database.AgentConfig, error)
}

// DeviceTypeItem 表示单条设备类型与 Agent 关联配置 DTO。
type DeviceTypeItem struct {
	Id            uint64    `json:"id"`
	DeviceType    string    `json:"device_type"`
	AgentConfigId uint64    `json:"agent_config_id"`
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
	Id            uint64 `json:"id"`
	DeviceType    string `json:"device_type"`
	AgentConfigId uint64 `json:"agent_config_id"`
}

// DeleteDeviceTypeRequest 删除单条设备类型配置请求体。
type DeleteDeviceTypeRequest struct {
	Id uint64 `json:"id"`
}

// BatchDeleteDeviceTypeRequest 批量删除设备类型配置请求体。
type BatchDeleteDeviceTypeRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminDeviceTypeHandler 处理设备类型与 Agent 关联配置相关的管理端接口。
type AdminDeviceTypeHandler struct {
	store  DeviceTypeStore
	logger *slog.Logger
}

// NewAdminDeviceTypeHandler 创建 AdminDeviceTypeHandler 实例。
func NewAdminDeviceTypeHandler(store DeviceTypeStore, l *slog.Logger) *AdminDeviceTypeHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminDeviceTypeHandler{
		store:  store,
		logger: l,
	}
}

// handleListDeviceTypes 分页获取设备类型与 Agent 关联配置列表。
func (h *AdminDeviceTypeHandler) handleListDeviceTypes(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

	var agentConfigId uint64
	if agentIdStr := query.Get("agent_config_id"); agentIdStr != "" {
		if id, err := strconv.ParseUint(agentIdStr, 10, 64); err == nil {
			agentConfigId = id
		}
	}

	filter := database.DeviceTypeFilter{
		DeviceType:    query.Get("device_type"),
		AgentConfigId: agentConfigId,
		Page:          page,
		PageSize:      pageSize,
	}

	types, total, err := h.store.ListDeviceTypes(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list device types", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	agentMap := make(map[uint64]string)
	for _, dt := range types {
		if dt.AgentConfigId > 0 {
			if _, ok := agentMap[dt.AgentConfigId]; !ok {
				if agent, err := h.store.FindAgentConfigById(r.Context(), dt.AgentConfigId); err == nil && agent != nil {
					agentMap[dt.AgentConfigId] = agent.Name
				}
			}
		}
	}

	items := make([]DeviceTypeItem, 0, len(types))
	for _, dt := range types {
		items = append(items, DeviceTypeItem{
			Id:            dt.Id,
			DeviceType:    dt.DeviceType,
			AgentConfigId: dt.AgentConfigId,
			AgentName:     agentMap[dt.AgentConfigId],
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

// handleSaveDeviceType 创建或更新设备类型配置（Id 为 0 时创建，Id > 0 时按 Id 覆盖）。
func (h *AdminDeviceTypeHandler) handleSaveDeviceType(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req SaveDeviceTypeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	req.DeviceType = strings.TrimSpace(req.DeviceType)
	if req.DeviceType == "" {
		http.Error(w, "device_type cannot be empty", http.StatusBadRequest)
		return
	}
	if req.AgentConfigId == 0 {
		http.Error(w, "agent_config_id is required and must be positive", http.StatusBadRequest)
		return
	}

	if req.Id == 0 {
		// 创建新配置
		dt := &database.DeviceType{
			DeviceType:    req.DeviceType,
			AgentConfigId: req.AgentConfigId,
		}

		if err := h.store.CreateDeviceType(r.Context(), dt); err != nil {
			if errors.Is(err, database.ErrDeviceTypeAlreadyExists) ||
				errors.Is(err, database.ErrReferencedAgentNotFound) ||
				errors.Is(err, database.ErrReferencedAgentDisabled) ||
				errors.Is(err, database.ErrEmptyDeviceType) ||
				errors.Is(err, database.ErrInvalidDeviceTypeLength) ||
				errors.Is(err, database.ErrInvalidAgentConfigId) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			h.logger.Error("failed to create device type", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var agentName string
		if agent, err := h.store.FindAgentConfigById(r.Context(), dt.AgentConfigId); err == nil && agent != nil {
			agentName = agent.Name
		}

		writeJSON(w, http.StatusOK, AdminResponse{
			Success: true,
			Message: "设备类型配置创建成功",
			Data: DeviceTypeItem{
				Id:            dt.Id,
				DeviceType:    dt.DeviceType,
				AgentConfigId: dt.AgentConfigId,
				AgentName:     agentName,
				CreatedAt:     dt.CreatedAt,
				UpdatedAt:     dt.UpdatedAt,
			},
		})
		return
	}

	// 覆盖更新已有配置
	existing, err := h.store.FindDeviceTypeById(r.Context(), req.Id)
	if err != nil {
		if errors.Is(err, database.ErrDeviceTypeNotFound) {
			http.Error(w, "device type not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	updatedDt := &database.DeviceType{
		Id:            existing.Id,
		DeviceType:    req.DeviceType,
		AgentConfigId: req.AgentConfigId,
	}

	if err := h.store.UpdateDeviceTypeById(r.Context(), updatedDt); err != nil {
		if errors.Is(err, database.ErrDeviceTypeAlreadyExists) ||
			errors.Is(err, database.ErrReferencedAgentNotFound) ||
			errors.Is(err, database.ErrReferencedAgentDisabled) ||
			errors.Is(err, database.ErrEmptyDeviceType) ||
			errors.Is(err, database.ErrInvalidDeviceTypeLength) ||
			errors.Is(err, database.ErrInvalidAgentConfigId) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, database.ErrDeviceTypeNotFound) {
			http.Error(w, "device type not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to update device type", "id", req.Id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var agentName string
	if agent, err := h.store.FindAgentConfigById(r.Context(), updatedDt.AgentConfigId); err == nil && agent != nil {
		agentName = agent.Name
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "设备类型配置更新成功",
		Data: DeviceTypeItem{
			Id:            updatedDt.Id,
			DeviceType:    updatedDt.DeviceType,
			AgentConfigId: updatedDt.AgentConfigId,
			AgentName:     agentName,
			CreatedAt:     existing.CreatedAt,
			UpdatedAt:     time.Now(),
		},
	})
}

// handleDeleteDeviceType 删除指定 Id 的设备类型配置记录。
func (h *AdminDeviceTypeHandler) handleDeleteDeviceType(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteDeviceTypeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteDeviceType(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrDeviceTypeNotFound) {
			http.Error(w, "device type not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete device type", "id", req.Id, "error", err)
		http.Error(w, "failed to delete device type", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "设备类型配置删除成功",
	})
}

// handleBatchDeleteDeviceTypes 批量删除设备类型配置记录。
func (h *AdminDeviceTypeHandler) handleBatchDeleteDeviceTypes(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteDeviceTypeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteDeviceTypes(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete device types", "error", err)
		http.Error(w, "failed to batch delete device types", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: fmt.Sprintf("成功批量删除 %d 条设备类型配置", len(req.Ids)),
	})
}
