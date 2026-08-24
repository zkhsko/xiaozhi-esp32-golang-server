package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"xiaozhi-esp32-golang-server/internal/session"
)

// Options 聚合顶层路由依赖的各个业务模块 Handler。
type Options struct {
	OTA              *OTAHandler
	WebsocketSession *session.Handler
}

// NewRouter 构造并返回顶层 HTTP 路由器。
func NewRouter(opts Options) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)

	r.Route("/xiaozhi", func(r chi.Router) {
		if opts.OTA != nil {
			r.Mount("/ota", opts.OTA.Routes())
		}
		if opts.WebsocketSession != nil {
			r.Route("/v1", func(r chi.Router) {
				r.Get("/", opts.WebsocketSession.ServeHTTP)
			})
		}
	})

	return r
}
