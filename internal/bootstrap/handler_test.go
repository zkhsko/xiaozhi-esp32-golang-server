package bootstrap

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
)

func newTestConfig(token string, wsURL string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            "127.0.0.1:8080",
			WebSocketURL:          wsURL,
			MaxConcurrentSessions: 100,
			ShutdownTimeout:       10 * time.Second,
			HTTPReadTimeout:       15 * time.Second,
			HTTPWriteTimeout:      30 * time.Second,
			HTTPIdleTimeout:       60 * time.Second,
			MaxHTTPBodyBytes:      65536,
			MaxHTTPHeaderBytes:    8192,
		},
		DeviceSharedToken: token,
	}
}

func TestBootstrapHandler_TableDriven(t *testing.T) {
	const (
		testToken = "test-secret-token-xyz-12345678"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)

	tests := []struct {
		name               string
		method             string
		path               string
		headers            map[string]string
		body               string
		wantStatusCode     int
		wantAllowHeader    string
		wantExactWS        bool
		wantEmptyBodyError bool
	}{
		{
			name:   "GET request success",
			method: http.MethodGet,
			path:   "/xiaozhi/ota/",
			headers: map[string]string{
				"Device-Id":          "AA:BB:CC:DD:EE:FF",
				"Client-Id":          "uuid-device-client-123",
				"Activation-Version": "1",
				"User-Agent":         "xiaozhi-esp32/1.0.0",
			},
			body:           "",
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:           "POST empty body success",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           "",
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:           "POST whitespace only body success",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           "   \n\t  ",
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:           "POST valid json body success",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           `{"version":"1.0.0","mac":"AA:BB:CC:DD:EE:FF","flash_size":4194304}`,
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:           "POST valid json array body success",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           `[1, 2, "three"]`,
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:           "POST invalid json body returns 400",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           `not-valid-json-string`,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:           "POST malformed json body returns 400",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           `{"version": "1.0.0", incomplete`,
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:            "PUT method returns 405",
			method:          http.MethodPut,
			path:            "/xiaozhi/ota/",
			body:            `{}`,
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantAllowHeader: "GET, POST",
		},
		{
			name:            "DELETE method returns 405",
			method:          http.MethodDelete,
			path:            "/xiaozhi/ota/",
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantAllowHeader: "GET, POST",
		},
		{
			name:            "PATCH method returns 405",
			method:          http.MethodPatch,
			path:            "/xiaozhi/ota/",
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantAllowHeader: "GET, POST",
		},
		{
			name:            "HEAD method returns 405",
			method:          http.MethodHead,
			path:            "/xiaozhi/ota/",
			wantStatusCode:  http.StatusMethodNotAllowed,
			wantAllowHeader: "GET, POST",
		},
		{
			name:           "Unmatched subpath returns 404",
			method:         http.MethodGet,
			path:           "/xiaozhi/ota/extra",
			wantStatusCode: http.StatusNotFound,
		},
		{
			name:           "Different path returns 404",
			method:         http.MethodPost,
			path:           "/other/path",
			body:           `{}`,
			wantStatusCode: http.StatusNotFound,
		},
		{
			name:   "Single header value exactly 1024 bytes success",
			method: http.MethodGet,
			path:   "/xiaozhi/ota/",
			headers: map[string]string{
				"Device-Id": strings.Repeat("A", 1024),
			},
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:   "Untrusted Host and Forwarded headers ignored in response URL",
			method: http.MethodGet,
			path:   "/xiaozhi/ota/",
			headers: map[string]string{
				"Host":              "attacker.example.com",
				"X-Forwarded-Host":  "spoofed.example.com",
				"X-Forwarded-Proto": "http",
			},
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:   "Single header value exceeds 1024 bytes returns 400",
			method: http.MethodGet,
			path:   "/xiaozhi/ota/",
			headers: map[string]string{
				"Device-Id": strings.Repeat("A", 1025),
			},
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:   "Single header key exceeds 1024 bytes returns 400",
			method: http.MethodGet,
			path:   "/xiaozhi/ota/",
			headers: map[string]string{
				strings.Repeat("X-Header-", 120): "normal-val",
			},
			wantStatusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tt.body != "" {
				bodyReader = strings.NewReader(tt.body)
			}

			req := httptest.NewRequest(tt.method, tt.path, bodyReader)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			var logBuf bytes.Buffer
			testLogger := logger.New(&logBuf, slog.LevelDebug)

			h := NewHandler(cfg, testLogger)
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatusCode {
				t.Fatalf("expected status code %d, got %d, body: %s", tt.wantStatusCode, rec.Code, rec.Body.String())
			}

			if tt.wantAllowHeader != "" {
				gotAllow := rec.Header().Get("Allow")
				if gotAllow != tt.wantAllowHeader {
					t.Errorf("expected Allow header %q, got %q", tt.wantAllowHeader, gotAllow)
				}
			}

			if tt.wantExactWS {
				contentType := rec.Header().Get("Content-Type")
				if !strings.Contains(contentType, "application/json") {
					t.Errorf("expected Content-Type to contain application/json, got %q", contentType)
				}

				// 1. 结构体反序列化验证
				var resp Response
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}

				if resp.WebSocket.URL != testWsURL {
					t.Errorf("expected WebSocket URL %q, got %q", testWsURL, resp.WebSocket.URL)
				}
				if resp.WebSocket.Token != testToken {
					t.Errorf("expected WebSocket Token %q, got %q", testToken, resp.WebSocket.Token)
				}
				if resp.WebSocket.Version != ProtocolVersion {
					t.Errorf("expected WebSocket Version %d, got %d", ProtocolVersion, resp.WebSocket.Version)
				}

				// 2. 严格字段断言：确保无 activation, mqtt, firmware 等占位字段
				var rawMap map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &rawMap); err != nil {
					t.Fatalf("failed to unmarshal raw map: %v", err)
				}

				if len(rawMap) != 1 {
					t.Errorf("expected exactly 1 top-level field (websocket), got %d fields: %+v", len(rawMap), rawMap)
				}

				if _, ok := rawMap["websocket"]; !ok {
					t.Errorf("missing top-level 'websocket' field in response")
				}

				forbiddenKeys := []string{"activation", "mqtt", "firmware", "device", "server", "tools"}
				for _, key := range forbiddenKeys {
					if _, exists := rawMap[key]; exists {
						t.Errorf("response contains forbidden field %q", key)
					}
				}

				var rawWS map[string]any
				if err := json.Unmarshal(rawMap["websocket"], &rawWS); err != nil {
					t.Fatalf("failed to unmarshal websocket map: %v", err)
				}

				if len(rawWS) != 3 {
					t.Errorf("expected exactly 3 fields in websocket object, got %d fields: %+v", len(rawWS), rawWS)
				}

				for _, key := range []string{"url", "token", "version"} {
					if _, exists := rawWS[key]; !exists {
						t.Errorf("missing expected field %q in websocket object", key)
					}
				}
			}

			// 日志安全性断言：绝不记录 Token
			logOutput := logBuf.String()
			if strings.Contains(logOutput, testToken) {
				t.Errorf("log output leaked sensitive token! log: %s", logOutput)
			}
		})
	}
}

func TestBootstrapHandler_PayloadTooLarge(t *testing.T) {
	const (
		testToken = "secret-token-payload-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	cfg.Server.MaxHTTPBodyBytes = 1024 // 设置为 1 KiB 测试超限

	// 构造 2 KiB 的请求正文
	largeBody := strings.Repeat(`{"key":"value"}`, 150)
	req := httptest.NewRequest(http.MethodPost, "/xiaozhi/ota/", strings.NewReader(largeBody))

	rec := httptest.NewRecorder()
	var logBuf bytes.Buffer
	testLogger := logger.New(&logBuf, slog.LevelDebug)

	h := NewHandler(cfg, testLogger)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status code %d (413 Payload Too Large), got %d, body: %s",
			http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	}

	if strings.Contains(logBuf.String(), testToken) {
		t.Errorf("log output leaked sensitive token! log: %s", logBuf.String())
	}
}

func TestBootstrapHandler_TotalHeadersTooLarge(t *testing.T) {
	const (
		testToken = "secret-token-header-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	cfg.Server.MaxHTTPHeaderBytes = 2048 // 限制总请求头 2 KiB

	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
	// 构造多个不超过 1024 但累计超过 2048 的 headers
	for i := 0; i < 5; i++ {
		key := strings.Repeat("X", 20) + string(rune('A'+i))
		req.Header.Set(key, strings.Repeat("V", 500))
	}

	rec := httptest.NewRecorder()
	var logBuf bytes.Buffer
	testLogger := logger.New(&logBuf, slog.LevelDebug)

	h := NewHandler(cfg, testLogger)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status code %d, got %d", http.StatusBadRequest, rec.Code)
	}

	if strings.Contains(logBuf.String(), testToken) {
		t.Errorf("log output leaked sensitive token! log: %s", logBuf.String())
	}
}

func TestBootstrapHandler_DefaultLogger(t *testing.T) {
	// 测试传入 nil logger 时的安全性与默认行为
	cfg := newTestConfig("sample-token", "wss://sample.example.com/xiaozhi/v1/")
	h := NewHandler(cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}
