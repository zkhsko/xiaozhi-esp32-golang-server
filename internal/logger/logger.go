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

// 常量定义：截断长度、限频参数与脱敏占位符。
const (
	DefaultTruncateLimit = 64
	DefaultDiagRate      = 1.0 // 1 msg/s
	DefaultDiagBurst     = 3   // burst 3 msgs
	RedactedValue        = "[REDACTED]"
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

// Truncate 将字符串截断为至多 maxLen 个字符（rune），超出部分以 "..." 结尾。
func Truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// TruncateString 将字符串按默认上限 64 字符截断，超出补 "..."。
func TruncateString(s string) string {
	return Truncate(s, DefaultTruncateLimit)
}

// SessionID 返回截断后的 session_id 日志属性。
func SessionID(id string) slog.Attr {
	return slog.String("session_id", TruncateString(id))
}

// DeviceID 返回截断后的 device_id 日志属性。
func DeviceID(id string) slog.Attr {
	return slog.String("device_id", TruncateString(id))
}

// ClientID 返回截断后的 client_id 日志属性。
func ClientID(id string) slog.Attr {
	return slog.String("client_id", TruncateString(id))
}

// SerialNumber 返回截断后的 serial_number 日志属性。
func SerialNumber(sn string) slog.Attr {
	return slog.String("serial_number", TruncateString(sn))
}

// State 返回状态流转的 state 日志属性。
func State(state string) slog.Attr {
	return slog.String("state", TruncateString(state))
}

// Reason 返回截断后的 reason 日志属性。
func Reason(reason string) slog.Attr {
	return slog.String("reason", TruncateString(reason))
}

// DurationMS 返回毫秒精度的 duration_ms 日志属性。
func DurationMS(d time.Duration) slog.Attr {
	return slog.Int64("duration_ms", d.Milliseconds())
}

// Err 返回统一命名为 err 的错误日志属性。若 err 为 nil 则返回空属性。
func Err(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	return slog.String("err", err.Error())
}

// SanitizeHeaderValue 对单个 HTTP Header 键值对进行脱敏或截断。
func SanitizeHeaderValue(key, val string) string {
	if isSensitiveKey(key) {
		return RedactedValue
	}
	return TruncateString(val)
}

// SanitizeHeaders 对 http.Header 执行脱敏与字段截断，返回安全的映射。
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
		result[k] = TruncateString(strings.Join(vals, ", "))
	}
	return result
}

// SafeHeaderAttr 将 http.Header 脱敏后包装为 slog.Attr。
func SafeHeaderAttr(h http.Header) slog.Attr {
	return slog.Any("headers", SanitizeHeaders(h))
}

// SafeReplaceAttr 是 slog.HandlerOptions 的属性替换过滤函数，执行敏感键脱敏、二进制数据屏蔽及字段截断。
func SafeReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}

	switch a.Value.Kind() {
	case slog.KindAny:
		val := a.Value.Any()
		if val == nil {
			return a
		}
		if b, ok := val.([]byte); ok {
			return slog.String(a.Key, fmt.Sprintf("<binary %d bytes>", len(b)))
		}
		if err, ok := val.(error); ok {
			if err == nil {
				return slog.Attr{}
			}
			return slog.String(a.Key, err.Error())
		}
	case slog.KindString:
		s := a.Value.String()
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "bearer ") {
			return slog.String(a.Key, "Bearer "+RedactedValue)
		}
		if isAutoTruncateKey(a.Key) {
			return slog.String(a.Key, TruncateString(s))
		}
	}

	return a
}

// isSensitiveKey 判断给定的属性键是否属于敏感字段。
func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	k = strings.ReplaceAll(k, " ", "_")

	switch k {
	case "authorization", "proxy_authorization", "cookie", "set_cookie",
		"dashscope_api_key", "device_shared_token",
		"api_key", "apikey", "x_api_key",
		"token", "access_token", "refresh_token", "id_token",
		"secret", "client_secret",
		"password", "passwd", "private_key",
		"prompt", "system_prompt", "full_prompt",
		"conversation", "dialogue", "messages", "history":
		return true
	}

	if strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "_secret") ||
		strings.HasSuffix(k, "_password") || strings.HasSuffix(k, "_api_key") ||
		strings.HasSuffix(k, "_apikey") || (strings.HasSuffix(k, "_key") && strings.Contains(k, "api")) {
		return true
	}
	return false
}

// isAutoTruncateKey 判断是否为需要自动截断长度的字段。
func isAutoTruncateKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	switch k {
	case "device_id", "client_id", "serial_number", "session_id", "reason", "error_summary", "payload_summary", "user_agent":
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
