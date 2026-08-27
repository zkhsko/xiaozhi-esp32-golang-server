package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
	"xiaozhi-esp32-golang-server/internal/session"
)

func setupTestDB(t *testing.T) *database.Database {
	t.Helper()

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "ota_router_test.db")
	dsn := "file:" + dbPath + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"

	cfg := config.DatabaseConfig{
		Driver:                "sqlite",
		MaxOpenConns:          1,
		MaxIdleConns:          1,
		ConnectionMaxLifetime: 0,
		ConnectionMaxIdleTime: 0,
		PingTimeout:           3 * time.Second,
		DSN:                   dsn,
	}

	db, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close test database: %v", err)
		}
	})

	return db
}

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
		wantActivation     bool
		wantEmptyBodyError bool
	}{
		{
			name:   "GET request without Serial-Number returns activation code when unactivated",
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
			wantActivation: true,
		},
		{
			name:   "GET request with Serial-Number returns activation code when unactivated",
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
			wantActivation: true,
		},
		{
			name:   "POST request with Serial-Number and json body returns activation code when unactivated",
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
			wantActivation: true,
		},
		{
			name:   "GET request without trailing slash returns activation code when unactivated",
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
			wantActivation: true,
		},
		{
			name:           "POST empty body returns activation code when unactivated",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           "",
			wantStatusCode: http.StatusOK,
			wantActivation: true,
		},
		{
			name:           "POST whitespace only body returns activation code when unactivated",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           "   \n\t  ",
			wantStatusCode: http.StatusOK,
			wantActivation: true,
		},
		{
			name:           "POST valid json body returns activation code when unactivated",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           `{"version":"1.0.0","mac":"AA:BB:CC:DD:EE:FF","flash_size":4194304}`,
			wantStatusCode: http.StatusOK,
			wantActivation: true,
		},
		{
			name:           "POST valid json array body returns activation code when unactivated",
			method:         http.MethodPost,
			path:           "/xiaozhi/ota/",
			body:           `[1, 2, "three"]`,
			wantStatusCode: http.StatusOK,
			wantActivation: true,
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
			wantActivation: true,
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
			wantActivation: true,
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

			otaHandler := NewOTAHandler(cfg, nil, testLogger)
			r := NewRouter(Options{
				OTA: otaHandler,
			})
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
				if resp.ServerTime == nil {
					t.Fatalf("expected ServerTime to be non-nil")
				}
				if resp.ServerTime.Timestamp <= 0 {
					t.Errorf("expected positive ServerTime.Timestamp, got %d", resp.ServerTime.Timestamp)
				}

				// 2. 严格字段断言：确保无冗余占位字段
				var rawMap map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &rawMap); err != nil {
					t.Fatalf("failed to unmarshal raw map: %v", err)
				}

				if len(rawMap) != 2 {
					t.Errorf("expected exactly 2 top-level fields (websocket, server_time), got %d fields: %+v", len(rawMap), rawMap)
				}

				if _, ok := rawMap["websocket"]; !ok {
					t.Errorf("missing top-level 'websocket' field in response")
				}
				if _, ok := rawMap["server_time"]; !ok {
					t.Errorf("missing top-level 'server_time' field in response")
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

				var rawServerTime map[string]any
				if err := json.Unmarshal(rawMap["server_time"], &rawServerTime); err != nil {
					t.Fatalf("failed to unmarshal server_time map: %v", err)
				}
				if _, exists := rawServerTime["timestamp"]; !exists {
					t.Errorf("missing expected field 'timestamp' in server_time object")
				}
			}

			if tt.wantActivation {
				contentType := rec.Header().Get("Content-Type")
				if !strings.Contains(contentType, "application/json") {
					t.Errorf("expected Content-Type to contain application/json, got %q", contentType)
				}

				var resp Response
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}

				if resp.WebSocket != nil {
					t.Errorf("expected WebSocket to be nil in activation response, got: %+v", resp.WebSocket)
				}
				if resp.Activation == nil {
					t.Fatalf("expected Activation to be non-nil")
				}
				if len(resp.Activation.Code) != 6 {
					t.Errorf("expected 6-digit activation code, got %q", resp.Activation.Code)
				}
				for _, c := range resp.Activation.Code {
					if c < '0' || c > '9' {
						t.Errorf("expected all characters in activation code to be digits, got %q", resp.Activation.Code)
						break
					}
				}
				if resp.Activation.Message != DefaultActivationMessage {
					t.Errorf("expected activation message %q, got %q", DefaultActivationMessage, resp.Activation.Message)
				}
				if resp.ServerTime == nil {
					t.Fatalf("expected ServerTime to be non-nil")
				}

				var rawMap map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &rawMap); err != nil {
					t.Fatalf("failed to unmarshal raw map: %v", err)
				}

				if len(rawMap) != 2 {
					t.Errorf("expected exactly 2 top-level fields (activation, server_time), got %d fields: %+v", len(rawMap), rawMap)
				}
				if _, ok := rawMap["activation"]; !ok {
					t.Errorf("missing top-level 'activation' field in response")
				}
				if _, ok := rawMap["server_time"]; !ok {
					t.Errorf("missing top-level 'server_time' field in response")
				}
				if _, exists := rawMap["websocket"]; exists {
					t.Errorf("response contains unwanted field 'websocket'")
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

	otaHandler := NewOTAHandler(cfg, nil, testLogger)
	r := NewRouter(Options{
		OTA: otaHandler,
	})
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

	otaHandler := NewOTAHandler(cfg, nil, testLogger)
	r := NewRouter(Options{
		OTA: otaHandler,
	})
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
	websocketSessionHandler := session.NewHandler(session.HandlerOptions{
		Config:  cfg,
		Limiter: sessionLimiter,
		Logger:  slog.Default(),
	})

	r := NewRouter(Options{
		WebsocketSession: websocketSessionHandler,
	})

	// 非 GET 方法请求 /xiaozhi/v1/ 应返回 405
	reqPost := httptest.NewRequest(http.MethodPost, "/xiaozhi/v1/", nil)
	recPost := httptest.NewRecorder()
	r.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST on /xiaozhi/v1/, got %d", recPost.Code)
	}

	// 未配置 WebsocketSession 模块时的路由未挂载测试 (404)
	rUnmounted := NewRouter(Options{})
	reqUnmounted := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	recUnmounted := httptest.NewRecorder()
	rUnmounted.ServeHTTP(recUnmounted, reqUnmounted)
	if recUnmounted.Code != http.StatusNotFound {
		t.Errorf("expected 404 when session handler is nil in Options, got %d", recUnmounted.Code)
	}
}

func TestRouter_OTA_DeviceActivationQuery(t *testing.T) {
	const (
		testToken = "secret-token-act-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	db := setupTestDB(t)
	ctx := context.Background()

	// 插入预置激活记录
	activatedSN := "SN-ACTIVATED-001"
	deviceID := "DEV-ESP32-AA-BB-CC"
	clientID := "CLIENT-UUID-12345"
	actRecord := &database.DeviceActivation{
		SerialNumber:     activatedSN,
		DeviceID:         deviceID,
		ClientID:         clientID,
		ActivationStatus: database.ActivationStatusActive,
		ActivatedAt:      time.Now().Truncate(time.Millisecond),
	}
	if err := db.CreateDeviceActivation(ctx, actRecord); err != nil {
		t.Fatalf("failed to create seed device activation: %v", err)
	}

	t.Run("with SN and found in database outputs activation info", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Device-Id", deviceID)
		req.Header.Set("Client-Id", clientID)
		req.Header.Set("Serial-Number", activatedSN)

		rec := httptest.NewRecorder()
		var logBuf bytes.Buffer
		testLogger := logger.New(&logBuf, slog.LevelDebug)

		otaHandler := NewOTAHandler(cfg, db, testLogger)
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status code %d, got %d, body: %s", http.StatusOK, rec.Code, rec.Body.String())
		}

		var resp Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.WebSocket == nil {
			t.Fatalf("expected WebSocket config to be non-nil for activated device")
		}
		if resp.WebSocket.URL != testWsURL {
			t.Errorf("expected WebSocket URL %q, got %q", testWsURL, resp.WebSocket.URL)
		}
		if resp.Activation != nil {
			t.Errorf("expected Activation to be nil for activated device, got: %+v", resp.Activation)
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "device activation found") {
			t.Errorf("expected log to contain 'device activation found', got: %s", logOutput)
		}
		if !strings.Contains(logOutput, activatedSN) {
			t.Errorf("expected log to contain serial number %q, got: %s", activatedSN, logOutput)
		}
		if !strings.Contains(logOutput, deviceID) {
			t.Errorf("expected log to contain device ID %q, got: %s", deviceID, logOutput)
		}
	})

	t.Run("with SN and not found in database outputs not found and returns 6-digit code in ttlcache", func(t *testing.T) {
		unactivatedSN := "SN-UNACTIVATED-999"
		unactivatedDevID := "DEV-OTHER"
		unactivatedCliID := "CLI-OTHER"
		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Device-Id", unactivatedDevID)
		req.Header.Set("Client-Id", unactivatedCliID)
		req.Header.Set("Serial-Number", unactivatedSN)

		rec := httptest.NewRecorder()
		var logBuf bytes.Buffer
		testLogger := logger.New(&logBuf, slog.LevelDebug)

		otaHandler := NewOTAHandler(cfg, db, testLogger)
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status code %d, got %d, body: %s", http.StatusOK, rec.Code, rec.Body.String())
		}

		var resp Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.WebSocket != nil {
			t.Errorf("expected WebSocket config to be nil for unactivated device, got: %+v", resp.WebSocket)
		}
		if resp.Activation == nil {
			t.Fatalf("expected Activation to be non-nil for unactivated device")
		}
		if len(resp.Activation.Code) != 6 {
			t.Errorf("expected 6-digit activation code, got %q", resp.Activation.Code)
		}
		for _, c := range resp.Activation.Code {
			if c < '0' || c > '9' {
				t.Errorf("expected code to contain only digits, got %q", resp.Activation.Code)
				break
			}
		}
		if resp.Activation.Message != DefaultActivationMessage {
			t.Errorf("expected activation message %q, got %q", DefaultActivationMessage, resp.Activation.Message)
		}

		// 验证 ttlcache 中成功保存了该待激活记录
		pending, ok := otaHandler.FindPendingActivationByCode(resp.Activation.Code)
		if !ok {
			t.Fatalf("expected pending activation to be found in ttlcache by code %q", resp.Activation.Code)
		}
		if pending.SerialNumber != unactivatedSN {
			t.Errorf("expected pending SerialNumber %q, got %q", unactivatedSN, pending.SerialNumber)
		}
		if pending.DeviceID != unactivatedDevID {
			t.Errorf("expected pending DeviceID %q, got %q", unactivatedDevID, pending.DeviceID)
		}
		if pending.ClientID != unactivatedCliID {
			t.Errorf("expected pending ClientID %q, got %q", unactivatedCliID, pending.ClientID)
		}

		// 验证再次请求时生成新的激活码并存入 ttlcache
		req2 := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req2.Header.Set("Device-Id", unactivatedDevID)
		req2.Header.Set("Client-Id", unactivatedCliID)
		req2.Header.Set("Serial-Number", unactivatedSN)
		rec2 := httptest.NewRecorder()
		r.ServeHTTP(rec2, req2)

		var resp2 Response
		if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
			t.Fatalf("failed to unmarshal second response: %v", err)
		}
		if resp2.Activation == nil {
			t.Fatalf("expected second activation response to be non-nil")
		}
		if len(resp2.Activation.Code) != 6 {
			t.Errorf("expected 6-digit activation code on second request, got %q", resp2.Activation.Code)
		}
		pending2, ok := otaHandler.FindPendingActivationByCode(resp2.Activation.Code)
		if !ok {
			t.Fatalf("expected pending activation for second code %q in ttlcache", resp2.Activation.Code)
		}
		if pending2.SerialNumber != unactivatedSN {
			t.Errorf("expected pending2 SerialNumber %q, got %q", unactivatedSN, pending2.SerialNumber)
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "device activation not found") {
			t.Errorf("expected log to contain 'device activation not found', got: %s", logOutput)
		}
		if !strings.Contains(logOutput, unactivatedSN) {
			t.Errorf("expected log to contain serial number %q, got: %s", unactivatedSN, logOutput)
		}
	})

	t.Run("with SN and frozen activation returns 403 forbidden", func(t *testing.T) {
		frozenSN := "SN-FROZEN-001"
		frozenRecord := &database.DeviceActivation{
			SerialNumber:     frozenSN,
			DeviceID:         "DEV-FROZEN-001",
			ClientID:         "CLI-FROZEN-001",
			ActivationStatus: database.ActivationStatusFrozen,
			ActivatedAt:      time.Now().Truncate(time.Millisecond),
		}
		if err := db.CreateDeviceActivation(ctx, frozenRecord); err != nil {
			t.Fatalf("failed to create frozen device activation: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Device-Id", "DEV-FROZEN-001")
		req.Header.Set("Serial-Number", frozenSN)

		rec := httptest.NewRecorder()
		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected status code %d, got %d, body: %s", http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("without SN and found in database outputs activation info", func(t *testing.T) {
		legacySN := "SN-LEGACY-001"
		legacyDevID := "DEV-LEGACY-ACTIVATED"
		legacyCliID := "CLI-LEGACY-ACTIVATED"
		legacyRecord := &database.DeviceActivation{
			SerialNumber:     legacySN,
			DeviceID:         legacyDevID,
			ClientID:         legacyCliID,
			ActivationStatus: database.ActivationStatusActive,
			ActivatedAt:      time.Now().Truncate(time.Millisecond),
		}
		if err := db.CreateDeviceActivation(ctx, legacyRecord); err != nil {
			t.Fatalf("failed to create legacy device activation: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Device-Id", legacyDevID)
		req.Header.Set("Client-Id", legacyCliID)

		rec := httptest.NewRecorder()
		var logBuf bytes.Buffer
		testLogger := logger.New(&logBuf, slog.LevelDebug)

		otaHandler := NewOTAHandler(cfg, db, testLogger)
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status code %d, got %d, body: %s", http.StatusOK, rec.Code, rec.Body.String())
		}

		var resp Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.WebSocket == nil {
			t.Fatalf("expected WebSocket config to be non-nil for activated legacy device")
		}
		if resp.WebSocket.URL != testWsURL {
			t.Errorf("expected WebSocket URL %q, got %q", testWsURL, resp.WebSocket.URL)
		}
		if resp.Activation != nil {
			t.Errorf("expected Activation to be nil for activated legacy device, got: %+v", resp.Activation)
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "legacy device activation found") {
			t.Errorf("expected log to contain 'legacy device activation found', got: %s", logOutput)
		}
		if !strings.Contains(logOutput, legacyDevID) {
			t.Errorf("expected log to contain device ID %q, got: %s", legacyDevID, logOutput)
		}
		if !strings.Contains(logOutput, legacyCliID) {
			t.Errorf("expected log to contain client ID %q, got: %s", legacyCliID, logOutput)
		}
	})

	t.Run("without SN and not found in database outputs not found and returns 6-digit code in ttlcache", func(t *testing.T) {
		unactivatedDevID := "DEV-LEGACY-NOT-EXIST"
		unactivatedCliID := "CLI-LEGACY-NOT-EXIST"
		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Device-Id", unactivatedDevID)
		req.Header.Set("Client-Id", unactivatedCliID)

		rec := httptest.NewRecorder()
		var logBuf bytes.Buffer
		testLogger := logger.New(&logBuf, slog.LevelDebug)

		otaHandler := NewOTAHandler(cfg, db, testLogger)
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status code %d, got %d, body: %s", http.StatusOK, rec.Code, rec.Body.String())
		}

		var resp Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.WebSocket != nil {
			t.Errorf("expected WebSocket config to be nil for unactivated legacy device, got: %+v", resp.WebSocket)
		}
		if resp.Activation == nil {
			t.Fatalf("expected Activation to be non-nil for unactivated legacy device")
		}
		if len(resp.Activation.Code) != 6 {
			t.Errorf("expected 6-digit activation code, got %q", resp.Activation.Code)
		}
		for _, c := range resp.Activation.Code {
			if c < '0' || c > '9' {
				t.Errorf("expected code to contain only digits, got %q", resp.Activation.Code)
				break
			}
		}
		if resp.Activation.Message != DefaultActivationMessage {
			t.Errorf("expected activation message %q, got %q", DefaultActivationMessage, resp.Activation.Message)
		}

		// 验证 ttlcache 中成功保存了该待激活记录，且 SerialNumber 为空
		pending, ok := otaHandler.FindPendingActivationByCode(resp.Activation.Code)
		if !ok {
			t.Fatalf("expected pending activation to be found in ttlcache by code %q", resp.Activation.Code)
		}
		if pending.SerialNumber != "" {
			t.Errorf("expected empty pending SerialNumber, got %q", pending.SerialNumber)
		}
		if pending.DeviceID != unactivatedDevID {
			t.Errorf("expected pending DeviceID %q, got %q", unactivatedDevID, pending.DeviceID)
		}
		if pending.ClientID != unactivatedCliID {
			t.Errorf("expected pending ClientID %q, got %q", unactivatedCliID, pending.ClientID)
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "legacy device activation not found") {
			t.Errorf("expected log to contain 'legacy device activation not found', got: %s", logOutput)
		}
	})

	t.Run("without SN and frozen activation returns 403 forbidden", func(t *testing.T) {
		frozenLegacySN := "SN-LEGACY-FROZEN-001"
		frozenLegacyDevID := "DEV-LEGACY-FROZEN-001"
		frozenLegacyCliID := "CLI-LEGACY-FROZEN-001"
		frozenRecord := &database.DeviceActivation{
			SerialNumber:     frozenLegacySN,
			DeviceID:         frozenLegacyDevID,
			ClientID:         frozenLegacyCliID,
			ActivationStatus: database.ActivationStatusFrozen,
			ActivatedAt:      time.Now().Truncate(time.Millisecond),
		}
		if err := db.CreateDeviceActivation(ctx, frozenRecord); err != nil {
			t.Fatalf("failed to create frozen legacy device activation: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Device-Id", frozenLegacyDevID)
		req.Header.Set("Client-Id", frozenLegacyCliID)

		rec := httptest.NewRecorder()
		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected status code %d, got %d, body: %s", http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("without SN and revoked activation returns 403 forbidden", func(t *testing.T) {
		revokedLegacySN := "SN-LEGACY-REVOKED-001"
		revokedLegacyDevID := "DEV-LEGACY-REVOKED-001"
		revokedLegacyCliID := "CLI-LEGACY-REVOKED-001"
		revokedRecord := &database.DeviceActivation{
			SerialNumber:     revokedLegacySN,
			DeviceID:         revokedLegacyDevID,
			ClientID:         revokedLegacyCliID,
			ActivationStatus: database.ActivationStatusRevoked,
			ActivatedAt:      time.Now().Truncate(time.Millisecond),
		}
		if err := db.CreateDeviceActivation(ctx, revokedRecord); err != nil {
			t.Fatalf("failed to create revoked legacy device activation: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Device-Id", revokedLegacyDevID)
		req.Header.Set("Client-Id", revokedLegacyCliID)

		rec := httptest.NewRecorder()
		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected status code %d, got %d, body: %s", http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("nil database does not panic and succeeds", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		req.Header.Set("Serial-Number", "SN-ANY-1234")

		rec := httptest.NewRecorder()
		var logBuf bytes.Buffer
		testLogger := logger.New(&logBuf, slog.LevelDebug)

		otaHandler := NewOTAHandler(cfg, nil, testLogger)
		r := NewRouter(Options{
			OTA: otaHandler,
		})
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status code %d, got %d", http.StatusOK, rec.Code)
		}

		var resp Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.Activation == nil {
			t.Fatalf("expected Activation to be non-nil for nil database with SN")
		}
		if len(resp.Activation.Code) != 6 {
			t.Errorf("expected 6-digit code, got %q", resp.Activation.Code)
		}
	})
}
