package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"xiaozhi-esp32-golang-server/internal/config"
)

// Server 封装 HTTP 服务的生命周期管理与优雅退出。
type Server struct {
	cfg        config.ServerConfig
	httpServer *http.Server
	listener   net.Listener
	mu         sync.Mutex
}

// New 创建配置就绪的 Server 实例。
// 若 handler 为 nil，则默认使用标准库 http.NewServeMux()。
func New(cfg config.ServerConfig, handler http.Handler) *Server {
	if handler == nil {
		handler = http.NewServeMux()
	}

	return &Server{
		cfg: cfg,
		httpServer: &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           handler,
			ReadHeaderTimeout: cfg.HTTPReadTimeout,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
		},
	}
}

// Run 监听配置的 TCP 地址并运行 HTTP 服务，当 ctx 取消时执行优雅退出。
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.ListenAddr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve 在传入的 net.Listener 上运行 HTTP 服务，当 ctx 取消时执行优雅退出。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	done := make(chan struct{})
	shutdownErrCh := make(chan error, 1)

	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
			defer cancel()

			if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
				shutdownErrCh <- fmt.Errorf("shutdown server: %w", err)
				return
			}
			shutdownErrCh <- nil
		case <-done:
			shutdownErrCh <- nil
		}
	}()

	err := s.httpServer.Serve(ln)
	close(done)

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		<-shutdownErrCh
		return fmt.Errorf("serve: %w", err)
	}

	if shutdownErr := <-shutdownErrCh; shutdownErr != nil {
		return shutdownErr
	}

	return nil
}

// Addr 返回当前监听的实际网络地址；若尚未建立监听则返回配置中的 ListenAddr。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.cfg.ListenAddr
}
