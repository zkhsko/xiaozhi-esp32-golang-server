package logger

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 常量定义：限频参数与脱敏占位符。
const (
	DefaultDiagRate = 1.0 // 1 msg/s
	DefaultDiagBurst = 3   // burst 3 msgs
	RedactedValue    = "[REDACTED]"
)

// NewHandler 创建内置敏感字段脱敏与超长截断过滤的 slog.Handler。
func NewHandler(w io.Writer, level slog.Level) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: SafeReplaceAttr,
	})
}

// New 创建使用 JSON 格式并配置安全脱敏过滤的 Logger。
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(NewHandler(w, level))
}

// InitDefault 初始化并设置全局默认 Logger。
func InitDefault(w io.Writer, level slog.Level) *slog.Logger {
	l := New(w, level)
	slog.SetDefault(l)
	return l
}

// SanitizeHeaders 对 http.Header 执行超级敏感字段脱敏，普通请求头保留完整内容不做截断。
func SanitizeHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	result := make(map[string]string, len(h))
	for k, vals := range h {
		if isSensitiveKey(k) {
			result[k] = RedactedValue
			continue
		}
		if len(vals) == 0 {
			result[k] = ""
			continue
		}
		result[k] = strings.Join(vals, ", ")
	}
	return result
}

// SafeReplaceAttr 是 slog.HandlerOptions 的属性替换过滤函数，仅对超级敏感字段和二进制数据进行脱敏与屏蔽，普通日志全部保留完整内容不做截断。
func SafeReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindAny:
		val := a.Value.Any()
		if val == nil {
			if isSensitiveKey(a.Key) {
				return slog.String(a.Key, RedactedValue)
			}
			return a
		}
		if b, ok := val.([]byte); ok {
			return slog.String(a.Key, fmt.Sprintf("<binary %d bytes>", len(b)))
		}
		if samples, ok := val.([]int16); ok {
			return slog.String(a.Key, fmt.Sprintf("<pcm %d samples>", len(samples)))
		}
		if h, ok := val.(http.Header); ok {
			return slog.Any(a.Key, SanitizeHeaders(h))
		}
		if isSensitiveKey(a.Key) {
			return slog.String(a.Key, RedactedValue)
		}
		if str, ok := val.(string); ok {
			if isSensitiveString(str) {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(str)), "bearer ") {
					return slog.String(a.Key, "Bearer "+RedactedValue)
				}
				return slog.String(a.Key, RedactedValue)
			}
			return slog.String(a.Key, str)
		}
		if err, ok := val.(error); ok {
			if err == nil {
				return slog.Attr{}
			}
			errStr := err.Error()
			if isSensitiveString(errStr) {
				return slog.String(a.Key, RedactedValue)
			}
			return slog.String(a.Key, errStr)
		}
	case slog.KindString:
		if isSensitiveKey(a.Key) {
			return slog.String(a.Key, RedactedValue)
		}
		s := a.Value.String()
		if isSensitiveString(s) {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "bearer ") {
				return slog.String(a.Key, "Bearer "+RedactedValue)
			}
			return slog.String(a.Key, RedactedValue)
		}
		return a
	default:
		if isSensitiveKey(a.Key) {
			return slog.String(a.Key, RedactedValue)
		}
	}

	return a
}

// isSensitiveString 判断字符串内容是否包含明显的 Bearer Token 或密钥特征。
func isSensitiveString(s string) bool {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "bearer ") {
		return true
	}
	return false
}

// isSensitiveKey 判断给定的属性键是否属于超级敏感字段（如凭据、密钥、Token、密码）。
func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	k = strings.ReplaceAll(k, " ", "_")

	switch k {
	case "authorization", "proxy_authorization", "auth", "cookie", "set_cookie",
		"dashscope_api_key",
		"api_key", "apikey", "x_api_key",
		"token", "access_token", "refresh_token", "id_token", "bearer_token", "session_token",
		"device_access_token", "device_shared_token",
		"secret", "client_secret", "app_secret",
		"password", "passwd", "pass", "private_key",
		"credential", "credentials":
		return true
	}

	if strings.HasSuffix(k, "_token") ||
		strings.HasSuffix(k, "_secret") ||
		strings.HasSuffix(k, "_password") ||
		strings.HasSuffix(k, "_passwd") ||
		strings.HasSuffix(k, "_api_key") ||
		strings.HasSuffix(k, "_apikey") ||
		strings.HasSuffix(k, "_credential") ||
		strings.HasSuffix(k, "_credentials") {
		return true
	}
	return false
}

// RateLimiter 实现线程安全的令牌桶限频器，用于高频诊断日志控频。
type RateLimiter struct {
	mu        sync.Mutex
	rate      float64
	burst     float64
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter 创建指定速率与突发容量的令牌桶限频器。
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	if rate <= 0 {
		rate = DefaultDiagRate
	}
	if burst <= 0 {
		burst = DefaultDiagBurst
	}
	return &RateLimiter{
		rate:      rate,
		burst:     float64(burst),
		tokens:    float64(burst),
		lastCheck: time.Now(),
	}
}

// NewDiagRateLimiter 创建默认速率（1 msg/s，burst 3）的诊断限频器。
func NewDiagRateLimiter() *RateLimiter {
	return NewRateLimiter(DefaultDiagRate, DefaultDiagBurst)
}

// Allow 判断当前是否允许输出一条日志。
func (r *RateLimiter) Allow() bool {
	return r.AllowN(time.Now(), 1)
}

// AllowN 允许注入时间戳进行限频判断，方便测试与精确控制。
func (r *RateLimiter) AllowN(now time.Time, n float64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	elapsed := now.Sub(r.lastCheck).Seconds()
	r.lastCheck = now

	r.tokens += elapsed * r.rate
	if r.tokens > r.burst {
		r.tokens = r.burst
	}

	if r.tokens >= n {
		r.tokens -= n
		return true
	}
	return false
}
