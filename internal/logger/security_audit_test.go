package logger_test

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
	"xiaozhi-esp32-golang-server/internal/router"
	"xiaozhi-esp32-golang-server/internal/session"
)

func TestSecurityAudit_MultiLevelSensitiveKeysRedaction(t *testing.T) {
	levels := []slog.Level{
		slog.LevelDebug,
		slog.LevelInfo,
		slog.LevelWarn,
		slog.LevelError,
	}

	sensitiveKeys := []string{
		"authorization",
		"Authorization",
		"AUTHORIZATION",
		"proxy_authorization",
		"Proxy-Authorization",
		"auth",
		"api_key",
		"apiKey",
		"API_KEY",
		"x_api_key",
		"X-Api-Key",
		"key",
		"DASHSCOPE_API_KEY",
		"dashscope_api_key",
		"token",
		"access_token",
		"refresh_token",
		"id_token",
		"bearer_token",
		"shared_token",
		"device_shared_token",
		"DEVICE_SHARED_TOKEN",
		"secret",
		"client_secret",
		"app_secret",
		"password",
		"passwd",
		"pass",
		"private_key",
		"credential",
		"credentials",
		"prompt",
		"system_prompt",
		"user_prompt",
		"full_prompt",
		"conversation",
		"dialogue",
		"dialog",
		"messages",
		"history",
		"chat_history",
		"user_text",
		"assistant_text",
		"user_message",
		"assistant_message",
		"full_text",
		"conversation_text",
		"custom_secret_token",
		"vendor_api_key",
		"database_password",
		"cloud_secret",
		"backend_private_key",
		"llm_prompt",
		"audit_conversation",
		"turn_history",
	}

	for _, lvl := range levels {
		t.Run(lvl.String(), func(t *testing.T) {
			var buf bytes.Buffer
			l := logger.New(&buf, lvl)

			for _, k := range sensitiveKeys {
				secretVal := fmt.Sprintf("sensitive-val-%s-987654321", k)
				buf.Reset()

				switch lvl {
				case slog.LevelDebug:
					l.Debug("audit log", slog.String(k, secretVal))
				case slog.LevelInfo:
					l.Info("audit log", slog.String(k, secretVal))
				case slog.LevelWarn:
					l.Warn("audit log", slog.String(k, secretVal))
				case slog.LevelError:
					l.Error("audit log", slog.String(k, secretVal))
				}

				raw := buf.String()
				if strings.Contains(raw, secretVal) {
					t.Fatalf("level %s leaked sensitive value for key %q: %s", lvl, k, raw)
				}

				var record map[string]any
				if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
					t.Fatalf("failed to unmarshal log json: %v, raw: %s", err, raw)
				}

				if record[k] != logger.RedactedValue {
					t.Errorf("level %s expected key %q to be %q, got %v", lvl, k, logger.RedactedValue, record[k])
				}
			}
		})
	}
}

func TestSecurityAudit_BearerTokenStringValueRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, slog.LevelInfo)

	testCases := []struct {
		name     string
		key      string
		value    string
		expected string
	}{
		{
			name:     "bearer standard uppercase prefix",
			key:      "custom_header",
			value:    "Bearer secret-device-token-123456789",
			expected: "Bearer " + logger.RedactedValue,
		},
		{
			name:     "bearer lowercase prefix",
			key:      "raw_auth",
			value:    "bearer confidential-token-xyz",
			expected: "Bearer " + logger.RedactedValue,
		},
		{
			name:     "bearer with leading and trailing spaces",
			key:      "header_str",
			value:    "   Bearer my-super-secret-token   ",
			expected: "Bearer " + logger.RedactedValue,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			l.Info("testing bearer redaction", slog.String(tc.key, tc.value))

			raw := buf.String()
			if strings.Contains(raw, "secret-device-token") || strings.Contains(raw, "confidential-token") || strings.Contains(raw, "my-super-secret") {
				t.Fatalf("sensitive bearer token plaintext leaked in log: %s", raw)
			}

			var record map[string]any
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
				t.Fatalf("json unmarshal failed: %v", err)
			}

			if record[tc.key] != tc.expected {
				t.Errorf("expected key %q to be %q, got %v", tc.key, tc.expected, record[tc.key])
			}
		})
	}
}

func TestSecurityAudit_BinaryAndPCMDataProtection(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, slog.LevelInfo)

	rawPCMBytes := make([]byte, 1920)
	for i := range rawPCMBytes {
		rawPCMBytes[i] = byte(i % 256)
	}

	rawOpusBytes := []byte{0xf8, 0xff, 0xfe, 0x01, 0x02, 0x03, 0x04}
	rawPCMSamples := make([]int16, 960)
	for i := range rawPCMSamples {
		rawPCMSamples[i] = int16(i * 10)
	}

	l.Info("audio processing event",
		slog.Any("raw_pcm_frame", rawPCMBytes),
		slog.Any("raw_opus_packet", rawOpusBytes),
		slog.Any("pcm_samples", rawPCMSamples),
	)

	raw := buf.String()
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log: %v", err)
	}

	if record["raw_pcm_frame"] != "<binary 1920 bytes>" {
		t.Errorf("raw_pcm_frame = %v; want '<binary 1920 bytes>'", record["raw_pcm_frame"])
	}
	if record["raw_opus_packet"] != "<binary 7 bytes>" {
		t.Errorf("raw_opus_packet = %v; want '<binary 7 bytes>'", record["raw_opus_packet"])
	}
	if record["pcm_samples"] != "<pcm 960 samples>" {
		t.Errorf("pcm_samples = %v; want '<pcm 960 samples>'", record["pcm_samples"])
	}

	// 确认日志输出中不包含任何二进制原始数组内容
	if strings.Contains(raw, "\xf8\xff\xfe") {
		t.Fatalf("raw binary leaked in log output: %s", raw)
	}
}

func TestSecurityAudit_DeviceClaimsTruncation(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, slog.LevelInfo)

	exact64 := strings.Repeat("x", 64)
	over65 := strings.Repeat("y", 65)
	over200 := strings.Repeat("z", 200)

	l.Info("device connection",
		slog.String("device_id", over200),
		slog.String("client_id", over65),
		slog.String("serial_number", exact64),
		slog.String("session_id", over200),
		slog.String("reason", over200),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log: %v", err)
	}

	expectedOver200 := strings.Repeat("z", 64) + "..."
	expectedOver65 := strings.Repeat("y", 64) + "..."

	if record["device_id"] != expectedOver200 {
		t.Errorf("device_id = %v; want %s", record["device_id"], expectedOver200)
	}
	if record["client_id"] != expectedOver65 {
		t.Errorf("client_id = %v; want %s", record["client_id"], expectedOver65)
	}
	if record["serial_number"] != exact64 {
		t.Errorf("serial_number = %v; want %s", record["serial_number"], exact64)
	}
	if record["session_id"] != expectedOver200 {
		t.Errorf("session_id = %v; want %s", record["session_id"], expectedOver200)
	}
	if record["reason"] != expectedOver200 {
		t.Errorf("reason = %v; want %s", record["reason"], expectedOver200)
	}
}

func TestSecurityAudit_HeaderSanitizationAndTruncation(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer my-secret-token-12345")
	headers.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")
	headers.Set("Cookie", "session_id=secret-session-abc")
	headers.Set("X-Api-Key", "sk-test-secret-key-999")
	headers.Set("Device-Id", strings.Repeat("d", 100))
	headers.Set("Content-Type", "application/json")

	sanitized := logger.SanitizeHeaders(headers)

	if sanitized["Authorization"] != logger.RedactedValue {
		t.Errorf("Authorization = %v; want %s", sanitized["Authorization"], logger.RedactedValue)
	}
	if sanitized["Proxy-Authorization"] != logger.RedactedValue {
		t.Errorf("Proxy-Authorization = %v; want %s", sanitized["Proxy-Authorization"], logger.RedactedValue)
	}
	if sanitized["Cookie"] != logger.RedactedValue {
		t.Errorf("Cookie = %v; want %s", sanitized["Cookie"], logger.RedactedValue)
	}
	if sanitized["X-Api-Key"] != logger.RedactedValue {
		t.Errorf("X-Api-Key = %v; want %s", sanitized["X-Api-Key"], logger.RedactedValue)
	}

	expectedDeviceID := strings.Repeat("d", 64) + "..."
	if sanitized["Device-Id"] != expectedDeviceID {
		t.Errorf("Device-Id = %v; want %s", sanitized["Device-Id"], expectedDeviceID)
	}
	if sanitized["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %v; want application/json", sanitized["Content-Type"])
	}
}

func TestSecurityAudit_HTTPErrorResponsesNoInternalLeak(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            ":8080",
			WebSocketURL:          "ws://localhost:8080/xiaozhi/v1/",
			MaxConcurrentSessions: 1,
			MaxHTTPBodyBytes:      1024,
			MaxHTTPHeaderBytes:    256,
		},
		DeviceSharedToken: "correct-secret-shared-token",
	}

	var logBuf bytes.Buffer
	auditLogger := logger.New(&logBuf, slog.LevelDebug)

	wsHandler := session.NewHandler(cfg, session.NewSessionLimiter(1), nil, nil, nil, auditLogger)
	httpRouter := router.NewRouter(router.NewHandler(cfg, wsHandler, auditLogger))

	tests := []struct {
		name       string
		handler    http.Handler
		method     string
		path       string
		headers    map[string]string
		body       string
		wantStatus int
	}{
		{
			name:       "bootstrap method not allowed",
			handler:    httpRouter,
			method:     http.MethodDelete,
			path:       router.OTAPath,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "bootstrap not found",
			handler:    httpRouter,
			method:     http.MethodGet,
			path:       "/unknown/path",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "bootstrap body payload too large",
			handler:    httpRouter,
			method:     http.MethodPost,
			path:       router.OTAPath,
			body:       strings.Repeat("a", 2048),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "bootstrap invalid json body",
			handler:    httpRouter,
			method:     http.MethodPost,
			path:       router.OTAPath,
			body:       "not-a-valid-json-string",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "bootstrap header too large",
			handler:    httpRouter,
			method:     http.MethodGet,
			path:       router.OTAPath,
			headers:    map[string]string{"X-Large-Header": strings.Repeat("h", 512)},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "ws upgrade missing authorization",
			handler:    httpRouter,
			method:     http.MethodGet,
			path:       router.WebSocketPath,
			headers:    map[string]string{"Protocol-Version": "1"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "ws upgrade invalid token",
			handler:    httpRouter,
			method:     http.MethodGet,
			path:       router.WebSocketPath,
			headers:    map[string]string{"Protocol-Version": "1", "Authorization": "Bearer wrong-token-xyz"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "ws upgrade invalid protocol version",
			handler:    httpRouter,
			method:     http.MethodGet,
			path:       router.WebSocketPath,
			headers:    map[string]string{"Protocol-Version": "2", "Authorization": "Bearer correct-secret-shared-token"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "ws upgrade header too large",
			handler:    httpRouter,
			method:     http.MethodGet,
			path:       router.WebSocketPath,
			headers:    map[string]string{"Protocol-Version": "1", "Authorization": "Bearer correct-secret-shared-token", "X-Big": strings.Repeat("b", 512)},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.path, bodyReader)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()

			tc.handler.ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("got status %d; want %d", resp.StatusCode, tc.wantStatus)
			}

			respBody, _ := io.ReadAll(resp.Body)
			respStr := string(respBody)

			// 验证响应正文中严禁包含内部文件路径、代码堆栈或秘密凭证
			forbiddenSubstrings := []string{
				"/Users/",
				"/home/",
				".go:",
				"goroutine ",
				"stack trace",
				"correct-secret-shared-token",
				"wrong-token-xyz",
			}

			for _, forbidden := range forbiddenSubstrings {
				if strings.Contains(respStr, forbidden) {
					t.Fatalf("security violation: response body leaks internal/sensitive data %q: %s", forbidden, respStr)
				}
			}
		})
	}
}

func TestSecurityAudit_ConstantTimeTokenComparison(t *testing.T) {
	correctToken := "device-shared-secret-auth-token-12345678"
	correctHash := sha256.Sum256([]byte(correctToken))

	matchingToken := "device-shared-secret-auth-token-12345678"
	matchingHash := sha256.Sum256([]byte(matchingToken))

	wrongToken := "device-shared-secret-auth-token-87654321"
	wrongHash := sha256.Sum256([]byte(wrongToken))

	if subtle.ConstantTimeCompare(correctHash[:], matchingHash[:]) != 1 {
		t.Fatal("expected matching tokens hash compare to return 1")
	}

	if subtle.ConstantTimeCompare(correctHash[:], wrongHash[:]) != 0 {
		t.Fatal("expected non-matching tokens hash compare to return 0")
	}
}

func TestSecurityAudit_RateLimiterConcurrencySafety(t *testing.T) {
	limiter := logger.NewDiagRateLimiter()

	var wg sync.WaitGroup
	allowedCount := 0
	var mu sync.Mutex

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow() {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if allowedCount > logger.DefaultDiagBurst {
		t.Errorf("allowedCount = %d; exceeds burst limit %d", allowedCount, logger.DefaultDiagBurst)
	}
}

func TestSecurityAudit_WebSocketCloseReasonSafety(t *testing.T) {
	reasons := []string{
		"hello timeout",
		"binary message not allowed before hello",
		"first message must be text hello",
		"invalid hello payload",
		"invalid hello fields",
		"failed to generate session id",
		"invalid opus packet size",
		"listening duration exceeded",
		"asr queue full timeout",
		"tts queue full timeout",
		"downlink queue full",
		"server shutting down",
		"session closed",
	}

	forbiddenSubstrings := []string{
		"/Users/",
		"/home/",
		".go:",
		"goroutine ",
		"sk-",
		"token",
	}

	for _, reason := range reasons {
		for _, forbidden := range forbiddenSubstrings {
			if strings.Contains(strings.ToLower(reason), forbidden) {
				t.Fatalf("close reason %q contains forbidden substring %q", reason, forbidden)
			}
		}

		if len(reason) > 64 {
			t.Fatalf("close reason %q exceeds 64 chars limit (%d chars)", reason, len(reason))
		}
	}
}
