package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			maxLen:   64,
			expected: "",
		},
		{
			name:     "zero maxLen",
			input:    "hello",
			maxLen:   0,
			expected: "",
		},
		{
			name:     "negative maxLen",
			input:    "hello",
			maxLen:   -1,
			expected: "",
		},
		{
			name:     "short ascii string within limit",
			input:    "esp32-device-01",
			maxLen:   64,
			expected: "esp32-device-01",
		},
		{
			name:     "exact 64 ascii characters",
			input:    strings.Repeat("a", 64),
			maxLen:   64,
			expected: strings.Repeat("a", 64),
		},
		{
			name:     "65 ascii characters exceeds limit",
			input:    strings.Repeat("a", 65),
			maxLen:   64,
			expected: strings.Repeat("a", 64) + "...",
		},
		{
			name:     "100 ascii characters exceeds limit",
			input:    strings.Repeat("x", 100),
			maxLen:   64,
			expected: strings.Repeat("x", 64) + "...",
		},
		{
			name:     "chinese unicode within limit",
			input:    strings.Repeat("小", 20),
			maxLen:   64,
			expected: strings.Repeat("小", 20),
		},
		{
			name:     "chinese unicode exact 64 runes",
			input:    strings.Repeat("智", 64),
			maxLen:   64,
			expected: strings.Repeat("智", 64),
		},
		{
			name:     "chinese unicode 65 runes exceeds limit",
			input:    strings.Repeat("智", 65),
			maxLen:   64,
			expected: strings.Repeat("智", 64) + "...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.input, tc.maxLen)
			if got != tc.expected {
				t.Fatalf("Truncate(%q, %d) = %q; want %q", tc.input, tc.maxLen, got, tc.expected)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	longStr := strings.Repeat("b", 100)
	got := TruncateString(longStr)
	expected := strings.Repeat("b", 64) + "..."
	if got != expected {
		t.Fatalf("TruncateString(%q) = %q; want %q", longStr, got, expected)
	}
}

func TestLogger_SuccessEvent(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	l.Info("session started",
		SessionID("sess-10001"),
		DeviceID("device-esp32-abc"),
		State("listening"),
		DurationMS(120*time.Millisecond),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, buf.String())
	}

	if record["level"] != "INFO" {
		t.Errorf("level = %v; want INFO", record["level"])
	}
	if record["msg"] != "session started" {
		t.Errorf("msg = %v; want 'session started'", record["msg"])
	}
	if record["session_id"] != "sess-10001" {
		t.Errorf("session_id = %v; want 'sess-10001'", record["session_id"])
	}
	if record["device_id"] != "device-esp32-abc" {
		t.Errorf("device_id = %v; want 'device-esp32-abc'", record["device_id"])
	}
	if record["state"] != "listening" {
		t.Errorf("state = %v; want 'listening'", record["state"])
	}
	if val, ok := record["duration_ms"].(float64); !ok || int64(val) != 120 {
		t.Errorf("duration_ms = %v; want 120", record["duration_ms"])
	}
}

func TestLogger_RejectionEvent(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn)

	l.Warn("handshake rejected",
		ClientID("client-xyz-888"),
		Reason("unsupported protocol version"),
		Err(errors.New("protocol mismatch")),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, buf.String())
	}

	if record["level"] != "WARN" {
		t.Errorf("level = %v; want WARN", record["level"])
	}
	if record["msg"] != "handshake rejected" {
		t.Errorf("msg = %v; want 'handshake rejected'", record["msg"])
	}
	if record["client_id"] != "client-xyz-888" {
		t.Errorf("client_id = %v; want 'client-xyz-888'", record["client_id"])
	}
	if record["reason"] != "unsupported protocol version" {
		t.Errorf("reason = %v; want 'unsupported protocol version'", record["reason"])
	}
	if record["err"] != "protocol mismatch" {
		t.Errorf("err = %v; want 'protocol mismatch'", record["err"])
	}
}

func TestLogger_ErrorEvent(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelError)

	l.Error("asr stream terminated abnormally",
		SessionID("sess-err-999"),
		Err(errors.New("connection reset by peer")),
		DurationMS(3400*time.Millisecond),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, buf.String())
	}

	if record["level"] != "ERROR" {
		t.Errorf("level = %v; want ERROR", record["level"])
	}
	if record["msg"] != "asr stream terminated abnormally" {
		t.Errorf("msg = %v; want 'asr stream terminated abnormally'", record["msg"])
	}
	if record["session_id"] != "sess-err-999" {
		t.Errorf("session_id = %v; want 'sess-err-999'", record["session_id"])
	}
	if record["err"] != "connection reset by peer" {
		t.Errorf("err = %v; want 'connection reset by peer'", record["err"])
	}
	if val, ok := record["duration_ms"].(float64); !ok || int64(val) != 3400 {
		t.Errorf("duration_ms = %v; want 3400", record["duration_ms"])
	}
}

func TestLogger_ErrNilOmitted(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	l.Info("operation completed",
		SessionID("sess-no-err"),
		Err(nil),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, buf.String())
	}

	if _, exists := record["err"]; exists {
		t.Errorf("expected 'err' to be omitted when err is nil, got: %v", record["err"])
	}
}

func TestLogger_SensitiveDataRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	sensitiveAPIKey := "sk-dashscope-secret-api-key-987654321"
	sensitiveToken := "device-shared-secret-token-abcdef123456"
	sensitiveAuthHeader := "Bearer my-secret-auth-bearer-token"
	sensitivePassword := "super-top-secret-password"
	sensitiveSecret := "client-secret-value-xyz"
	sensitivePrompt := "You are a secret AI. Full system prompt instructions here."
	sensitiveConversation := "User: secret conversation line 1\nAssistant: confidential reply"
	rawPCMData := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	rawOpusData := make([]byte, 1920)

	l.Info("diagnostic inspection",
		slog.String("Authorization", sensitiveAuthHeader),
		slog.String("DASHSCOPE_API_KEY", sensitiveAPIKey),
		slog.String("DEVICE_SHARED_TOKEN", sensitiveToken),
		slog.String("api_key", "secret-key-111"),
		slog.String("token", "access-token-222"),
		slog.String("password", sensitivePassword),
		slog.String("secret", sensitiveSecret),
		slog.String("prompt", sensitivePrompt),
		slog.String("system_prompt", "confidential system instructions"),
		slog.String("conversation", sensitiveConversation),
		slog.String("auth_header", sensitiveAuthHeader),
		slog.Any("audio_pcm", rawPCMData),
		slog.Any("audio_opus", rawOpusData),
	)

	logOutput := buf.String()

	// 敏感原始数据绝对不得出现在日志中
	forbiddenStrings := []string{
		sensitiveAPIKey,
		sensitiveToken,
		sensitiveAuthHeader,
		sensitivePassword,
		sensitiveSecret,
		sensitivePrompt,
		"confidential system instructions",
		sensitiveConversation,
		"secret-key-111",
		"access-token-222",
	}

	for _, forbidden := range forbiddenStrings {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("sensitive string leaked in log output: %q\nFull log: %s", forbidden, logOutput)
		}
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, logOutput)
	}

	if record["Authorization"] != RedactedValue {
		t.Errorf("Authorization = %v; want %s", record["Authorization"], RedactedValue)
	}
	if record["DASHSCOPE_API_KEY"] != RedactedValue {
		t.Errorf("DASHSCOPE_API_KEY = %v; want %s", record["DASHSCOPE_API_KEY"], RedactedValue)
	}
	if record["DEVICE_SHARED_TOKEN"] != RedactedValue {
		t.Errorf("DEVICE_SHARED_TOKEN = %v; want %s", record["DEVICE_SHARED_TOKEN"], RedactedValue)
	}
	if record["api_key"] != RedactedValue {
		t.Errorf("api_key = %v; want %s", record["api_key"], RedactedValue)
	}
	if record["token"] != RedactedValue {
		t.Errorf("token = %v; want %s", record["token"], RedactedValue)
	}
	if record["password"] != RedactedValue {
		t.Errorf("password = %v; want %s", record["password"], RedactedValue)
	}
	if record["secret"] != RedactedValue {
		t.Errorf("secret = %v; want %s", record["secret"], RedactedValue)
	}
	if record["prompt"] != RedactedValue {
		t.Errorf("prompt = %v; want %s", record["prompt"], RedactedValue)
	}
	if record["conversation"] != RedactedValue {
		t.Errorf("conversation = %v; want %s", record["conversation"], RedactedValue)
	}
	if record["auth_header"] != "Bearer "+RedactedValue {
		t.Errorf("auth_header = %v; want 'Bearer %s'", record["auth_header"], RedactedValue)
	}
	if record["audio_pcm"] != "<binary 8 bytes>" {
		t.Errorf("audio_pcm = %v; want '<binary 8 bytes>'", record["audio_pcm"])
	}
	if record["audio_opus"] != "<binary 1920 bytes>" {
		t.Errorf("audio_opus = %v; want '<binary 1920 bytes>'", record["audio_opus"])
	}
}

func TestLogger_HeaderSanitization(t *testing.T) {
	headers := http.Header{
		"Authorization": []string{"Bearer super-secret-access-token"},
		"Cookie":        []string{"session_id=12345; secure_auth=token"},
		"Set-Cookie":    []string{"session_id=67890; HttpOnly"},
		"X-Api-Key":     []string{"secret-x-api-key"},
		"Device-Id":     []string{"esp32-valid-device-id"},
		"Client-Id":     []string{"client-device-001"},
		"User-Agent":    []string{"xiaozhi-esp32-firmware/1.0.0"},
		"X-Custom-Long": []string{strings.Repeat("h", 100)},
	}

	sanitized := SanitizeHeaders(headers)

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
	if sanitized["Device-Id"] != "esp32-valid-device-id" {
		t.Errorf("Device-Id = %v; want 'esp32-valid-device-id'", sanitized["Device-Id"])
	}
	if sanitized["Client-Id"] != "client-device-001" {
		t.Errorf("Client-Id = %v; want 'client-device-001'", sanitized["Client-Id"])
	}
	if sanitized["User-Agent"] != "xiaozhi-esp32-firmware/1.0.0" {
		t.Errorf("User-Agent = %v; want 'xiaozhi-esp32-firmware/1.0.0'", sanitized["User-Agent"])
	}
	expectedLong := strings.Repeat("h", 64) + "..."
	if sanitized["X-Custom-Long"] != expectedLong {
		t.Errorf("X-Custom-Long = %v; want %s", sanitized["X-Custom-Long"], expectedLong)
	}

	// 测试空 Header
	if got := SanitizeHeaders(nil); got != nil {
		t.Errorf("SanitizeHeaders(nil) = %v; want nil", got)
	}

	// 测试通过 SafeHeaderAttr 写入 Logger
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)
	l.Info("request received", SafeHeaderAttr(headers))

	out := buf.String()
	if strings.Contains(out, "super-secret-access-token") {
		t.Fatalf("token leaked via SafeHeaderAttr: %s", out)
	}
}

func TestLogger_OverlongTruncationInLog(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	overlongDeviceID := strings.Repeat("D", 100)
	overlongClientID := strings.Repeat("C", 100)
	overlongSerialNumber := strings.Repeat("S", 100)
	overlongSessionID := strings.Repeat("E", 100)
	overlongReason := strings.Repeat("R", 100)

	l.Info("device state report",
		DeviceID(overlongDeviceID),
		ClientID(overlongClientID),
		SerialNumber(overlongSerialNumber),
		SessionID(overlongSessionID),
		Reason(overlongReason),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, buf.String())
	}

	expectedDeviceID := strings.Repeat("D", 64) + "..."
	expectedClientID := strings.Repeat("C", 64) + "..."
	expectedSerialNumber := strings.Repeat("S", 64) + "..."
	expectedSessionID := strings.Repeat("E", 64) + "..."
	expectedReason := strings.Repeat("R", 64) + "..."

	if record["device_id"] != expectedDeviceID {
		t.Errorf("device_id = %q; want %q", record["device_id"], expectedDeviceID)
	}
	if record["client_id"] != expectedClientID {
		t.Errorf("client_id = %q; want %q", record["client_id"], expectedClientID)
	}
	if record["serial_number"] != expectedSerialNumber {
		t.Errorf("serial_number = %q; want %q", record["serial_number"], expectedSerialNumber)
	}
	if record["session_id"] != expectedSessionID {
		t.Errorf("session_id = %q; want %q", record["session_id"], expectedSessionID)
	}
	if record["reason"] != expectedReason {
		t.Errorf("reason = %q; want %q", record["reason"], expectedReason)
	}

	// 同时验证如果不通过辅助函数直接传 slog.String("device_id", overlongDeviceID)，SafeReplaceAttr 也会自动截断
	buf.Reset()
	l.Info("raw attribute report",
		slog.String("device_id", overlongDeviceID),
		slog.String("client_id", overlongClientID),
		slog.String("serial_number", overlongSerialNumber),
		slog.String("session_id", overlongSessionID),
		slog.String("reason", overlongReason),
	)

	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, buf.String())
	}

	if record["device_id"] != expectedDeviceID {
		t.Errorf("raw device_id = %q; want %q", record["device_id"], expectedDeviceID)
	}
	if record["client_id"] != expectedClientID {
		t.Errorf("raw client_id = %q; want %q", record["client_id"], expectedClientID)
	}
	if record["serial_number"] != expectedSerialNumber {
		t.Errorf("raw serial_number = %q; want %q", record["serial_number"], expectedSerialNumber)
	}
	if record["session_id"] != expectedSessionID {
		t.Errorf("raw session_id = %q; want %q", record["session_id"], expectedSessionID)
	}
	if record["reason"] != expectedReason {
		t.Errorf("raw reason = %q; want %q", record["reason"], expectedReason)
	}
}

func TestRateLimiter_TokenBucket(t *testing.T) {
	rl := NewDiagRateLimiter()

	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	rl.lastCheck = baseTime

	// 初始容量 3 个 token，连续 3 次应成功
	if !rl.AllowN(baseTime, 1) {
		t.Errorf("1st call should be allowed")
	}
	if !rl.AllowN(baseTime, 1) {
		t.Errorf("2nd call should be allowed")
	}
	if !rl.AllowN(baseTime, 1) {
		t.Errorf("3rd call should be allowed")
	}

	// 第 4 次应被限频拒绝
	if rl.AllowN(baseTime, 1) {
		t.Errorf("4th call in same timestamp should be rejected")
	}

	// 500ms 后补充 0.5 个 token，仍不足 1.0 个，应拒绝
	halfSec := baseTime.Add(500 * time.Millisecond)
	if rl.AllowN(halfSec, 1) {
		t.Errorf("call at +500ms should be rejected because tokens = 0.5 < 1.0")
	}

	// 1000ms（1 秒）后累计补充 1.0 个 token，应允许
	oneSec := baseTime.Add(1000 * time.Millisecond)
	if !rl.AllowN(oneSec, 1) {
		t.Errorf("call at +1000ms should be allowed")
	}
	// 消耗完后紧接着第 2 次应拒绝
	if rl.AllowN(oneSec, 1) {
		t.Errorf("immediate second call at +1000ms should be rejected")
	}

	// 闲置 10 秒后，token 数量上限为 burst（3 个）
	tenSec := baseTime.Add(10 * time.Second)
	if !rl.AllowN(tenSec, 1) {
		t.Errorf("1st call at +10s should be allowed")
	}
	if !rl.AllowN(tenSec, 1) {
		t.Errorf("2nd call at +10s should be allowed")
	}
	if !rl.AllowN(tenSec, 1) {
		t.Errorf("3rd call at +10s should be allowed")
	}
	if rl.AllowN(tenSec, 1) {
		t.Errorf("4th call at +10s should be rejected (burst limit is 3)")
	}
}

func TestRateLimiter_Concurrency(t *testing.T) {
	rl := NewRateLimiter(100, 10)
	var wg sync.WaitGroup
	allowedCount := 0
	var mu sync.Mutex

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rl.Allow() {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if allowedCount == 0 {
		t.Errorf("expected at least 1 request to be allowed, got 0")
	}
}

func TestInitDefault(t *testing.T) {
	var buf bytes.Buffer
	l := InitDefault(&buf, slog.LevelInfo)
	if l == nil {
		t.Fatal("InitDefault returned nil")
	}

	slog.Info("global logger message",
		slog.String("Authorization", "secret-token"),
		DeviceID("my-device"),
	)

	out := buf.String()
	if strings.Contains(out, "secret-token") {
		t.Fatalf("sensitive token leaked via slog.Default(): %s", out)
	}
	if !strings.Contains(out, RedactedValue) {
		t.Fatalf("expected redacted placeholder in output: %s", out)
	}
	if !strings.Contains(out, "my-device") {
		t.Fatalf("expected device id in output: %s", out)
	}
}
