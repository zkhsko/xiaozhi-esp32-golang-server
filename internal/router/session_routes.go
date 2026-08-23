package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// sessionRoutes 注册 WebSocket 会话升级相关路由。
func (h *Handler) sessionRoutes() http.Handler {
	r := chi.NewRouter()

	r.Get("/", h.handleSession)

	return r
}
