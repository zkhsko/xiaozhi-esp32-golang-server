package router

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
	"xiaozhi-esp32-golang-server/internal/session"
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

func TestRouter_OTATableDriven(t *testing.T) {
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
			name:   "GET request without Serial-Number success",
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
			name:   "GET request with Serial-Number success",
			method: http.MethodGet,
			path:   "/xiaozhi/ota/",
			headers: map[string]string{
				"Device-Id":          "AA:BB:CC:DD:EE:FF",
				"Client-Id":          "uuid-device-client-123",
				"Serial-Number":      "SN-DEVICE-12345678",
				"Activation-Version": "2",
				"User-Agent":         "xiaozhi-esp32/2.0.0",
			},
			body:           "",
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:   "POST request with Serial-Number and json body success",
			method: http.MethodPost,
			path:   "/xiaozhi/ota/",
			headers: map[string]string{
				"Device-Id":          "AA:BB:CC:DD:EE:FF",
				"Client-Id":          "uuid-device-client-123",
				"Serial-Number":      "SN-DEVICE-12345678",
				"Activation-Version": "2",
			},
			body:           `{"version":2,"mac_address":"AA:BB:CC:DD:EE:FF","uuid":"uuid-device-client-123"}`,
			wantStatusCode: http.StatusOK,
			wantExactWS:    true,
		},
		{
			name:   "GET request without trailing slash success",
			method: http.MethodGet,
			path:   "/xiaozhi/ota",
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
			name:           "PUT method returns 405",
			method:         http.MethodPut,
			path:           "/xiaozhi/ota/",
			body:           `{}`,
			wantStatusCode: http.StatusMethodNotAllowed,
		},
		{
			name:           "DELETE method returns 405",
			method:         http.MethodDelete,
			path:           "/xiaozhi/ota/",
			wantStatusCode: http.StatusMethodNotAllowed,
		},
		{
			name:           "PATCH method returns 405",
			method:         http.MethodPatch,
			path:           "/xiaozhi/ota/",
			wantStatusCode: http.StatusMethodNotAllowed,
		},
		{
			name:           "Unmatched subpath returns 404",
			method:         http.MethodGet,
			path:           "/xiaozhi/ota/extra",
			wantStatusCode: http.StatusNotFound,
		},
		{
			name:           "Different root path returns 404",
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

			h := NewHandler(cfg, nil, testLogger)
			r := NewRouter(h)
			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatusCode {
				t.Fatalf("expected status code %d, got %d, body: %s", tt.wantStatusCode, rec.Code, rec.Body.String())
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

				// 2. 严格字段断言：确保无冗余占位字段
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
					t.Errorf("expected fields length 3, got %d: %+v", len(rawWS), rawWS)
				}

				for _, key := range []string{"url", "token", "version"} {
					if _, exists := rawWS[key]; !exists {
						t.Errorf("missing expected field %q in websocket object", key)
					}
				}
			}

			// 日志安全性断言：绝不泄露敏感 Token
			logOutput := logBuf.String()
			if strings.Contains(logOutput, testToken) {
				t.Errorf("log output leaked sensitive token! log: %s", logOutput)
			}
		})
	}
}

func TestRouter_PayloadTooLarge(t *testing.T) {
	const (
		testToken = "secret-token-payload-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	cfg.Server.MaxHTTPBodyBytes = 1024

	largeBody := strings.Repeat(`{"key":"value"}`, 150)
	req := httptest.NewRequest(http.MethodPost, "/xiaozhi/ota/", strings.NewReader(largeBody))

	rec := httptest.NewRecorder()
	var logBuf bytes.Buffer
	testLogger := logger.New(&logBuf, slog.LevelDebug)

	h := NewHandler(cfg, nil, testLogger)
	r := NewRouter(h)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status code %d (413 Payload Too Large), got %d, body: %s",
			http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	}

	if strings.Contains(logBuf.String(), testToken) {
		t.Errorf("log output leaked sensitive token! log: %s", logBuf.String())
	}
}

func TestRouter_TotalHeadersTooLarge(t *testing.T) {
	const (
		testToken = "secret-token-header-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	cfg.Server.MaxHTTPHeaderBytes = 2048

	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
	for i := 0; i < 5; i++ {
		key := strings.Repeat("X", 20) + string(rune('A'+i))
		req.Header.Set(key, strings.Repeat("V", 500))
	}

	rec := httptest.NewRecorder()
	var logBuf bytes.Buffer
	testLogger := logger.New(&logBuf, slog.LevelDebug)

	h := NewHandler(cfg, nil, testLogger)
	r := NewRouter(h)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status code %d, got %d", http.StatusBadRequest, rec.Code)
	}

	if strings.Contains(logBuf.String(), testToken) {
		t.Errorf("log output leaked sensitive token! log: %s", logBuf.String())
	}
}

func TestRouter_SessionEndpointRouting(t *testing.T) {
	cfg := newTestConfig("test-token", "ws://localhost:8080/xiaozhi/v1/")
	sessionLimiter := session.NewSessionLimiter(10)
	sessionHandler := session.NewHandler(cfg, sessionLimiter, nil, nil, nil, slog.Default())

	h := NewHandler(cfg, sessionHandler, nil)
	r := NewRouter(h)

	// 非 GET 方法请求 /xiaozhi/v1/ 应返回 405
	reqPost := httptest.NewRequest(http.MethodPost, "/xiaozhi/v1/", nil)
	recPost := httptest.NewRecorder()
	r.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST on /xiaozhi/v1/, got %d", recPost.Code)
	}

	// 未配置 sessionHandler 时的安全性测试
	hNil := NewHandler(cfg, nil, nil)
	rNil := NewRouter(hNil)
	reqGet := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	recGet := httptest.NewRecorder()
	rNil.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when sessionHandler is nil, got %d", recGet.Code)
	}
}
