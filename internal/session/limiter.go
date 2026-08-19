package session

import (
	"sync"
)

// SessionLimiter 负责进程内活跃 WebSocket 会话的并发准入与名额控制。
type SessionLimiter struct {
	mu     sync.Mutex
	active int
	max    int
}

// NewSessionLimiter 创建指定最大并发容量的会话准入控制器。
// 若 maxConcurrentSessions 小于等于 0，则默认设置为 1。
func NewSessionLimiter(maxConcurrentSessions int) *SessionLimiter {
	if maxConcurrentSessions <= 0 {
		maxConcurrentSessions = 1
	}
	return &SessionLimiter{
		max: maxConcurrentSessions,
	}
}

// TryAcquire 尝试获取一个会话名额。
// 若当前活跃会话未达到上限，返回释放函数与 true；
// 若已满载，返回 nil 与 false。
// 返回的 release 函数通过 sync.Once 保证幂等执行，可安全重复调用。
func (l *SessionLimiter) TryAcquire() (func(), bool) {
	l.mu.Lock()
	if l.active >= l.max {
		l.mu.Unlock()
		return nil, false
	}
	l.active++
	l.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mu.Lock()
			l.active--
			l.mu.Unlock()
		})
	}
	return release, true
}

// ActiveCount 返回当前活跃占用的会话数量。
func (l *SessionLimiter) ActiveCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

// MaxSessions 返回配置的最大并发会话数。
func (l *SessionLimiter) MaxSessions() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.max
}
