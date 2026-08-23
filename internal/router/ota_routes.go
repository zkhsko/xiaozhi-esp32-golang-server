package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// otaRoutes 注册设备 OTA 与配置发现相关路由。
func (h *Handler) otaRoutes() http.Handler {
	r := chi.NewRouter()

	r.Get("/", h.handleOTA)
	r.Post("/", h.handleOTA)

	return r
}
