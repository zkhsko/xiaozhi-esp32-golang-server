package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
)

// Server 封装 HTTP 服务的生命周期管理与优雅退出。
type Server struct {
	cfg           config.ServerConfig
	httpServer    *http.Server
	listener      net.Listener
	shutdownHooks []func(ctx context.Context) error
	mu            sync.Mutex
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

// RegisterOnShutdown 注册在服务收到退出信号时执行的清理回调。
func (s *Server) RegisterOnShutdown(fn func(ctx context.Context) error) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownHooks = append(s.shutdownHooks, fn)
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

	shutdownTimeout := 10 * time.Second
	if s.cfg.ShutdownTimeout > 0 {
		shutdownTimeout = s.cfg.ShutdownTimeout
	}

	done := make(chan struct{})
	shutdownErrCh := make(chan error, 1)

	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()

			s.mu.Lock()
			hooks := make([]func(ctx context.Context) error, len(s.shutdownHooks))
			copy(hooks, s.shutdownHooks)
			s.mu.Unlock()

			var shutdownErr error
			var hookWg sync.WaitGroup
			var errMu sync.Mutex

			for _, hook := range hooks {
				h := hook
				hookWg.Add(1)
				go func() {
					defer hookWg.Done()
					if err := h(shutdownCtx); err != nil {
						errMu.Lock()
						shutdownErr = errors.Join(shutdownErr, err)
						errMu.Unlock()
					}
				}()
			}

			if err := s.httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errMu.Lock()
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("shutdown server: %w", err))
				errMu.Unlock()
			}

			hookWg.Wait()
			shutdownErrCh <- shutdownErr
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
