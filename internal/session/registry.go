package session

import (
	"context"
	"log/slog"
	"sync"

	"xiaozhi-esp32-golang-server/internal/logger"
)

// Registry 负责进程内活跃 WebSocket 会话的所有权注册、单设备连接互斥、生命周期跟踪与优雅停机协调。
type Registry struct {
	mu       sync.Mutex
	limiter  *SessionLimiter
	sessions map[*Session]struct{}
	bySerial map[string]*Session
	wg       sync.WaitGroup
	closed   bool
	logger   *slog.Logger
}

// NewRegistry 创建配置就绪的会话注册表实例。
func NewRegistry(limiter *SessionLimiter, l *slog.Logger) *Registry {
	if limiter == nil {
		limiter = NewSessionLimiter(1)
	}
	if l == nil {
		l = slog.Default()
	}
	return &Registry{
		limiter:  limiter,
		sessions: make(map[*Session]struct{}),
		bySerial: make(map[string]*Session),
		logger:   l,
	}
}

// Limiter 返回当前关联的会话准入控制器。
func (r *Registry) Limiter() *SessionLimiter {
	return r.limiter
}

// ActiveCount 返回当前已注册的活跃会话数。
func (r *Registry) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// GetBySerial 查询指定序列号当前关联的活跃会话，若不存在则返回 nil。
func (r *Registry) GetBySerial(serialNumber string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bySerial[serialNumber]
}

// IsClosed 返回会话注册表是否已进入关闭状态。
func (r *Registry) IsClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Acquire 尝试为新会话获取准入名额。
// 若注册表已关闭或并发已满，返回 (nil, false)；
// 成功时返回释放函数与 true。释放函数保证幂等执行。
func (r *Registry) Acquire() (func(), bool) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, false
	}
	r.mu.Unlock()

	return r.limiter.TryAcquire()
}

// Register 将活跃会话登记到注册表中并维护按设备序列号的单设备连接互斥。
// 若相同序列号已有活跃旧会话，将自动断开旧会话以确保同一设备全局唯一连接。
// 可选传入准入释放函数，将在会话注销且名额释放后触发等待组完成。
// 若注册表已关闭，返回 (nil, false)；
// 成功时返回注销函数与 true。注销函数保证幂等执行。
func (r *Registry) Register(s *Session, release ...func()) (func(), bool) {
	if s == nil {
		return nil, false
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, false
	}

	serial := s.SerialNumber()
	var oldSession *Session
	if serial != "" {
		if old, exists := r.bySerial[serial]; exists && old != s {
			oldSession = old
			r.logger.Info("evicting duplicate session for serial number",
				"serial_number", logger.TruncateString(serial),
				"old_session_id", old.SessionID(),
				"new_session_id", s.SessionID(),
			)
		}
		r.bySerial[serial] = s
	}

	r.sessions[s] = struct{}{}
	r.wg.Add(1)
	r.mu.Unlock()

	// 在锁外优雅断开旧会话，避免持锁调用造成潜在阻塞或死锁
	if oldSession != nil {
		oldSession.Close()
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.sessions, s)
			if serial != "" && r.bySerial[serial] == s {
				delete(r.bySerial, serial)
			}
			r.mu.Unlock()
			for _, rel := range release {
				if rel != nil {
					rel()
				}
			}
			r.wg.Done()
		})
	}
	return cleanup, true
}

// Sessions 返回当前所有活跃会话的快照副本。
func (r *Registry) Sessions() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*Session, 0, len(r.sessions))
	for s := range r.sessions {
		result = append(result, s)
	}
	return result
}

// Shutdown 执行优雅关闭：停止新会话准入、广播取消所有活跃会话，并等待会话协程退出。
// 整个等待过程受传入的 ctx（通常携带 shutdown_timeout）保护。
func (r *Registry) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.wait(ctx)
	}
	r.closed = true

	activeSessions := make([]*Session, 0, len(r.sessions))
	for s := range r.sessions {
		activeSessions = append(activeSessions, s)
	}
	r.mu.Unlock()

	r.logger.Info("shutting down session registry",
		"active_sessions", len(activeSessions),
	)

	// 并发取消所有活跃会话
	for _, s := range activeSessions {
		sess := s
		go sess.Close()
	}

	return r.wait(ctx)
}

// wait 等待所有会话协程退出或上下文超时。
func (r *Registry) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.logger.Info("all active sessions closed gracefully")
		return nil
	case <-ctx.Done():
		r.logger.Warn("session registry shutdown timed out",
			"error", ctx.Err(),
			"remaining_sessions", r.ActiveCount(),
		)
		return ctx.Err()
	}
}
