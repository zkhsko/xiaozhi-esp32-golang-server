package router

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
)

// DeviceActivationStore 定义设备激活管理所需的窄持久化接口。
type DeviceActivationStore interface {
	ListDeviceActivations(ctx context.Context, filter database.DeviceActivationFilter) ([]*database.DeviceActivation, int64, error)
	UpdateDeviceActivation(ctx context.Context, id uint64, updates map[string]any) error
	DeleteDeviceActivation(ctx context.Context, id uint64) error
	BatchDeleteDeviceActivations(ctx context.Context, ids []uint64) error
}

// ActivationItem 表示单条设备激活关系 DTO。
type ActivationItem struct {
	Id               uint64    `json:"id"`
	SerialNumber     string    `json:"serial_number"`
	DeviceId         string    `json:"device_id"`
	ClientId         string    `json:"client_id,omitempty"`
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
	Id               uint64 `json:"id"`
	DeviceId         string `json:"device_id"`
	ClientId         string `json:"client_id"`
	ActivationStatus string `json:"activation_status"`
}

// DeleteActivationRequest 删除单条激活记录请求体。
type DeleteActivationRequest struct {
	Id uint64 `json:"id"`
}

// BatchDeleteActivationRequest 批量删除激活记录请求体。
type BatchDeleteActivationRequest struct {
	Ids []uint64 `json:"ids"`
}

// AdminActivationHandler 处理设备激活关系相关的管理端接口。
type AdminActivationHandler struct {
	store  DeviceActivationStore
	logger *slog.Logger
}

// NewAdminActivationHandler 创建 AdminActivationHandler 实例。
func NewAdminActivationHandler(store DeviceActivationStore, l *slog.Logger) *AdminActivationHandler {
	if l == nil {
		l = slog.Default()
	}
	return &AdminActivationHandler{
		store:  store,
		logger: l,
	}
}

// handleListActivations 分页获取设备激活关系列表。
func (h *AdminActivationHandler) handleListActivations(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	page, pageSize := parsePagination(r)
	query := r.URL.Query()

	filter := database.DeviceActivationFilter{
		SerialNumber:     query.Get("serial_number"),
		DeviceId:         query.Get("device_id"),
		ClientId:         query.Get("client_id"),
		ActivationStatus: query.Get("activation_status"),
		Page:             page,
		PageSize:         pageSize,
	}

	acts, total, err := h.store.ListDeviceActivations(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list device activations", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]ActivationItem, 0, len(acts))
	for _, a := range acts {
		items = append(items, ActivationItem{
			Id:               a.Id,
			SerialNumber:     a.SerialNumber,
			DeviceId:         a.DeviceId,
			ClientId:         a.ClientId,
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
func (h *AdminActivationHandler) handleUpdateActivation(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req UpdateActivationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	updates := make(map[string]any)
	if req.DeviceId != "" {
		updates["device_id"] = strings.TrimSpace(req.DeviceId)
	}
	if req.ClientId != "" {
		updates["client_id"] = strings.TrimSpace(req.ClientId)
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

	if err := h.store.UpdateDeviceActivation(r.Context(), req.Id, updates); err != nil {
		if errors.Is(err, database.ErrActivationNotFound) {
			http.Error(w, "activation not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to update activation", "id", req.Id, "error", err)
		http.Error(w, "failed to update activation", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "device activation updated successfully",
	})
}

// handleDeleteActivation 删除指定设备激活记录。
func (h *AdminActivationHandler) handleDeleteActivation(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req DeleteActivationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Id == 0 {
		http.Error(w, "id is required and must be positive", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteDeviceActivation(r.Context(), req.Id); err != nil {
		if errors.Is(err, database.ErrActivationNotFound) {
			http.Error(w, "activation not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete activation", "id", req.Id, "error", err)
		http.Error(w, "failed to delete activation", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "device activation deleted successfully",
	})
}

// handleBatchDeleteActivations 批量删除设备激活记录。
func (h *AdminActivationHandler) handleBatchDeleteActivations(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.logger.Error("database dependency not properly initialized")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req BatchDeleteActivationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Ids) == 0 {
		http.Error(w, "ids array cannot be empty", http.StatusBadRequest)
		return
	}

	if err := h.store.BatchDeleteDeviceActivations(r.Context(), req.Ids); err != nil {
		h.logger.Error("failed to batch delete activations", "error", err)
		http.Error(w, "failed to batch delete activations", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, AdminResponse{
		Success: true,
		Message: "device activations deleted successfully",
	})
}
