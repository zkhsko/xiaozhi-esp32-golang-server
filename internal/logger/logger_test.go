package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogger_SystemPrompt_NotRedactedAndNotTruncated(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	// 构造一个超过 1024 字符的超长提示词
	longPrompt := "你是一个智能语音管家。" + strings.Repeat("请根据用户的需求调用对应的设备工具。", 100)
	if len([]rune(longPrompt)) <= 1024 {
		t.Fatalf("expected longPrompt length > 1024, got %d", len([]rune(longPrompt)))
	}

	l.Info("llm system prompt for turn",
		"session_id", "sess-test-12345",
		"system_prompt", longPrompt,
		"api_key", "sk-secret-key-123456",
	)

	var logMap map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
		t.Fatalf("failed to unmarshal log json: %v\ncontent: %s", err, buf.String())
	}

	// 1. 验证 system_prompt 未被脱敏且未被截断（保持完整长度）
	loggedPrompt, ok := logMap["system_prompt"].(string)
	if !ok {
		t.Fatalf("expected system_prompt in log output, got %v", logMap["system_prompt"])
	}
	if loggedPrompt != longPrompt {
		t.Fatalf("expected full system_prompt without truncation, expected len=%d, got len=%d", len(longPrompt), len(loggedPrompt))
	}
	if strings.Contains(loggedPrompt, RedactedValue) {
		t.Fatalf("system_prompt must not contain redacted value %s", RedactedValue)
	}

	// 2. 验证敏感字段 api_key 依然被正确脱敏
	loggedAPIKey, ok := logMap["api_key"].(string)
	if !ok || loggedAPIKey != RedactedValue {
		t.Fatalf("expected api_key to be redacted as %q, got %q", RedactedValue, loggedAPIKey)
	}
}

func TestLogger_PromptVariants_NoTruncation(t *testing.T) {
	testKeys := []string{
		"prompt",
		"system_prompt",
		"full_prompt",
		"user_prompt",
		"raw_prompt",
		"custom_agent_prompt",
		"tools_json",
		"tools",
	}

	for _, key := range testKeys {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			l := New(&buf, slog.LevelInfo)
			longContent := "Prompt content: " + strings.Repeat("ABCD ", 300)

			l.Info("test prompt key", key, longContent)

			var logMap map[string]any
			if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}

			val, ok := logMap[key].(string)
			if !ok {
				t.Fatalf("expected key %s in log, got %v", key, logMap[key])
			}
			if val != longContent {
				t.Fatalf("key %s was unexpectedly truncated: got len %d, expected len %d", key, len(val), len(longContent))
			}
		})
	}
}

func TestLogger_OrdinaryLogs_NoTruncation(t *testing.T) {
	// 普通日志必须完整输出，任何超长字符串（包括超过 64 或 1024 字符）均不做任何截断处理
	testCases := []struct {
		key   string
		value string
	}{
		{"serial_number", "SN-" + strings.Repeat("1234567890", 20)},
		{"device_id", "dev-" + strings.Repeat("abcdefghij", 20)},
		{"client_id", "cli-" + strings.Repeat("klmnopqrst", 20)},
		{"session_id", "sess-" + strings.Repeat("uvwxyz0123", 20)},
		{"device_key", "key-" + strings.Repeat("9876543210", 20)},
		{"user_agent", "Mozilla/5.0 " + strings.Repeat("esp32-client/1.0.0-build-debug ", 10)},
		{"activation_version", "v" + strings.Repeat("1.2.3.4.5.6.7.8.9.0", 10)},
		{"code", "12345678901234567890"},
		{"reason", "long reason " + strings.Repeat("error detail information ", 20)},
		{"text", "user text " + strings.Repeat("你好小智助手今天天气怎么样 ", 30)},
		{"msg", "server msg " + strings.Repeat("processing speech recognition frame ", 20)},
		{"path", "/api/v1/very/long/nested/resource/path/" + strings.Repeat("segment/", 20)},
		{"remote_addr", "192.168.1.100:12345"},
		{"url", "https://example.com/stream?query=" + strings.Repeat("param=value&", 20)},
		{"user_text", "hello " + strings.Repeat("this is my speech ", 50)},
		{"assistant_text", "reply " + strings.Repeat("this is the ai answer ", 50)},
		{"conversation", "history " + strings.Repeat("round 1 round 2 round 3 ", 50)},
		{"messages", "messages " + strings.Repeat("role user role assistant ", 50)},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			var buf bytes.Buffer
			l := New(&buf, slog.LevelInfo)

			l.Info("test ordinary log", tc.key, tc.value)

			var logMap map[string]any
			if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}

			val, ok := logMap[tc.key].(string)
			if !ok {
				t.Fatalf("expected key %s in log, got %v", tc.key, logMap[tc.key])
			}
			if val != tc.value {
				t.Fatalf("key %s was truncated: got len %d, expected len %d", tc.key, len(val), len(tc.value))
			}
			if strings.HasSuffix(val, "...") {
				t.Fatalf("key %s should not have been truncated with ellipsis", tc.key)
			}
		})
	}
}

func TestLogger_OrdinaryError_NoTruncation(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelError)

	longErrText := "dial tcp failed: " + strings.Repeat("connection refused by remote host; ", 50)
	longErr := errors.New(longErrText)

	l.Error("operation failed", "error", longErr)

	var logMap map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	val, ok := logMap["error"].(string)
	if !ok {
		t.Fatalf("expected error key in log, got %v", logMap["error"])
	}
	if val != longErrText {
		t.Fatalf("error was truncated: got len %d, expected len %d", len(val), len(longErrText))
	}
}

func TestLogger_SuperSensitiveInformation_Redacted(t *testing.T) {
	sensitiveKeys := []string{
		"api_key",
		"apikey",
		"x_api_key",
		"dashscope_api_key",
		"custom_api_key",
		"token",
		"access_token",
		"refresh_token",
		"id_token",
		"bearer_token",
		"session_token",
		"device_access_token",
		"device_shared_token",
		"secret",
		"client_secret",
		"app_secret",
		"password",
		"passwd",
		"pass",
		"private_key",
		"credential",
		"credentials",
		"device_credential",
		"authorization",
		"proxy_authorization",
		"cookie",
		"set_cookie",
	}

	for _, key := range sensitiveKeys {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			l := New(&buf, slog.LevelInfo)
			secretVal := "super-secret-password-or-token-1234567890"

			l.Info("sensitive audit", key, secretVal)

			var logMap map[string]any
			if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}

			val, ok := logMap[key].(string)
			if !ok {
				t.Fatalf("expected key %s in log, got %v", key, logMap[key])
			}
			if val != RedactedValue {
				t.Fatalf("key %s was not redacted: got %q, expected %q", key, val, RedactedValue)
			}
			if strings.Contains(buf.String(), secretVal) {
				t.Fatalf("secret plaintext leaked in log output for key %s", key)
			}
		})
	}
}

func TestLogger_BearerTokenInString_Redacted(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	secretToken := "sk-live-abcdef1234567890"
	l.Info("auth header test", "raw_header", "Bearer "+secretToken)

	var logMap map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	val, ok := logMap["raw_header"].(string)
	if !ok {
		t.Fatalf("expected raw_header in log, got %v", logMap["raw_header"])
	}
	if val != "Bearer "+RedactedValue {
		t.Fatalf("expected 'Bearer %s', got %q", RedactedValue, val)
	}
	if strings.Contains(buf.String(), secretToken) {
		t.Fatalf("bearer token plaintext leaked in log output")
	}
}

func TestLogger_SanitizeHeaders(t *testing.T) {
	longUserAgent := "Mozilla/5.0 " + strings.Repeat("esp32-device/1.0 ", 20)
	longCustomHeader := strings.Repeat("X-Value-", 30)

	headers := http.Header{
		"Authorization":   []string{"Bearer super-secret-access-token"},
		"Cookie":          []string{"session_id=12345; auth=secret"},
		"Set-Cookie":      []string{"token=99999; HttpOnly"},
		"X-Api-Key":       []string{"secret-x-api-key"},
		"User-Agent":      []string{longUserAgent},
		"X-Custom-Long":   []string{longCustomHeader},
		"Device-Id":       []string{"esp32-device-001"},
		"Client-Id":       []string{"client-esp32-12345"},
		"Empty-Header":    []string{},
	}

	sanitized := SanitizeHeaders(headers)

	// 敏感头部必须被脱敏
	if sanitized["Authorization"] != RedactedValue {
		t.Errorf("Authorization = %v; want %s", sanitized["Authorization"], RedactedValue)
	}
	if sanitized["Cookie"] != RedactedValue {
		t.Errorf("Cookie = %v; want %s", sanitized["Cookie"], RedactedValue)
	}
	if sanitized["Set-Cookie"] != RedactedValue {
		t.Errorf("Set-Cookie = %v; want %s", sanitized["Set-Cookie"], RedactedValue)
	}
	if sanitized["X-Api-Key"] != RedactedValue {
		t.Errorf("X-Api-Key = %v; want %s", sanitized["X-Api-Key"], RedactedValue)
	}

	// 普通头部不得截断
	if sanitized["User-Agent"] != longUserAgent {
		t.Errorf("User-Agent was truncated: got len %d, want len %d", len(sanitized["User-Agent"]), len(longUserAgent))
	}
	if sanitized["X-Custom-Long"] != longCustomHeader {
		t.Errorf("X-Custom-Long was truncated: got len %d, want len %d", len(sanitized["X-Custom-Long"]), len(longCustomHeader))
	}
	if sanitized["Device-Id"] != "esp32-device-001" {
		t.Errorf("Device-Id = %v; want 'esp32-device-001'", sanitized["Device-Id"])
	}
	if sanitized["Client-Id"] != "client-esp32-12345" {
		t.Errorf("Client-Id = %v; want 'client-esp32-12345'", sanitized["Client-Id"])
	}
	if sanitized["Empty-Header"] != "" {
		t.Errorf("Empty-Header = %v; want ''", sanitized["Empty-Header"])
	}

	// 空 Header 返回 nil
	if got := SanitizeHeaders(nil); got != nil {
		t.Errorf("SanitizeHeaders(nil) = %v; want nil", got)
	}
}

func TestLogger_BinaryAndPCM(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	rawBytes := make([]byte, 256)
	samples := make([]int16, 960)

	l.Info("audio payload", "raw_audio", rawBytes, "pcm_samples", samples)

	var logMap map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if logMap["raw_audio"] != "<binary 256 bytes>" {
		t.Errorf("raw_audio = %v; want '<binary 256 bytes>'", logMap["raw_audio"])
	}
	if logMap["pcm_samples"] != "<pcm 960 samples>" {
		t.Errorf("pcm_samples = %v; want '<pcm 960 samples>'", logMap["pcm_samples"])
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewDiagRateLimiter()
	if rl == nil {
		t.Fatal("NewDiagRateLimiter returned nil")
	}

	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	rl.lastCheck = baseTime

	// burst 为 3，连续 3 次应允许
	if !rl.AllowN(baseTime, 1) {
		t.Errorf("1st call should be allowed")
	}
	if !rl.AllowN(baseTime, 1) {
		t.Errorf("2nd call should be allowed")
	}
	if !rl.AllowN(baseTime, 1) {
		t.Errorf("3rd call should be allowed")
	}
	// 第 4 次应被拒绝
	if rl.AllowN(baseTime, 1) {
		t.Errorf("4th call at same timestamp should be rejected")
	}

	// 并发测试
	crl := NewRateLimiter(100, 10)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if crl.Allow() {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed == 0 {
		t.Errorf("expected at least 1 call to be allowed, got 0")
	}
}

func TestInitDefault(t *testing.T) {
	var buf bytes.Buffer
	l := InitDefault(&buf, slog.LevelInfo)
	if l == nil {
		t.Fatal("InitDefault returned nil")
	}

	slog.Info("global logger message",
		"device_id", "my-test-device-12345",
		"api_key", "secret-key-123",
	)

	out := buf.String()
	if strings.Contains(out, "secret-key-123") {
		t.Fatalf("sensitive key leaked: %s", out)
	}
	if !strings.Contains(out, RedactedValue) {
		t.Fatalf("expected redacted placeholder in output: %s", out)
	}
	if !strings.Contains(out, "my-test-device-12345") {
		t.Fatalf("expected full device id in output: %s", out)
	}
}
