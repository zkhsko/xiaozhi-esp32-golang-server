package router

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/session"
)

// Handler 聚合 HTTP 路由层的依赖。
type Handler struct {
	cfg            *config.Config
	logger         *slog.Logger
	sessionHandler *session.Handler
}

// NewHandler 创建 Handler 实例。
func NewHandler(cfg *config.Config, sessionHandler *session.Handler, l *slog.Logger) *Handler {
	if l == nil {
		l = slog.Default()
	}
	return &Handler{
		cfg:            cfg,
		logger:         l,
		sessionHandler: sessionHandler,
	}
}

// NewRouter 构造并返回顶层 HTTP 路由器。
func NewRouter(h *Handler) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)

	r.Route("/xiaozhi", func(r chi.Router) {
		r.Mount("/ota", h.otaRoutes())
		r.Mount("/v1", h.sessionRoutes())
	})

	return r
}
