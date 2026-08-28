package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"xiaozhi-esp32-golang-server/internal/admin"
	"xiaozhi-esp32-golang-server/internal/session"
)

// Options 聚合顶层路由依赖的各个业务模块 Handler。
type Options struct {
	Admin            *AdminHandler
	OTA              *OTAHandler
	User             *UserHandler
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

	if opts.User != nil {
		r.Mount("/user-api", opts.User.Routes())
	}

	if opts.Admin != nil {
		r.Mount("/admin-api", opts.Admin.Routes())
	}

	// 挂载管理端单页前端静态资源
	r.Mount("/admin", http.StripPrefix("/admin", admin.Handler()))

	return r
}
