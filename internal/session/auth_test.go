package session

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xiaozhi-esp32-golang-server/internal/logger"
)

const (
	testSharedToken = "secret-token-123456"
	testDeviceID    = "dev-device-001"
	testClientID    = "client-app-002"
	testSerialNum   = "SN-20260819-ABCD"
	testUserAgent   = "xiaozhi-esp32-firmware/1.0"
)

// TestAuthenticateUpgrade_TokenValidation 验证 Token 各种缺失、格式错误、内容错误与正确情况下的认证行为。
func TestAuthenticateUpgrade_TokenValidation(t *testing.T) {
	tests := []struct {
		name            string
		authHeader      string
		setAuthHeader   bool
		sharedToken     string
		expectedErr     error
		expectedStatus  int
		expectedSuccess bool
	}{
		{
			name:            "缺失 Authorization 头",
			setAuthHeader:   false,
			sharedToken:     testSharedToken,
			expectedErr:     ErrMissingToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "Authorization 头为空字符串",
			authHeader:      "",
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     ErrMissingToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "Bearer 后 Token 为空",
			authHeader:      "Bearer ",
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "Bearer 后仅含空格",
			authHeader:      "Bearer    ",
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "错误的前缀 Basic",
			authHeader:      "Basic " + testSharedToken,
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "小写前缀 bearer",
			authHeader:      "bearer " + testSharedToken,
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "无 Bearer 前缀直接为 Token",
			authHeader:      testSharedToken,
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "错误的 Token 内容",
			authHeader:      "Bearer wrong-token-content",
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "服务端共享 Token 为空时拒绝所有请求",
			authHeader:      "Bearer " + testSharedToken,
			setAuthHeader:   true,
			sharedToken:     "",
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "正确的 Bearer Token 认证成功",
			authHeader:      "Bearer " + testSharedToken,
			setAuthHeader:   true,
			sharedToken:     testSharedToken,
			expectedErr:     nil,
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
			req.Header.Set("Protocol-Version", "1")
			req.Header.Set("Serial-Number", testSerialNum)
			if tc.setAuthHeader {
				req.Header.Set("Authorization", tc.authHeader)
			}

			info, err := AuthenticateUpgrade(req, tc.sharedToken, 0)
			if tc.expectedSuccess {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				if info == nil {
					t.Fatal("expected non-nil ClientHeaderInfo")
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected error to match %v, got %v", tc.expectedErr, err)
				}
				if HTTPStatus(err) != tc.expectedStatus {
					t.Fatalf("expected status code %d, got %d", tc.expectedStatus, HTTPStatus(err))
				}
			}
		})
	}
}

// TestAuthenticateUpgrade_ProtocolVersionValidation 验证 Protocol-Version 请求头的严格校验。
func TestAuthenticateUpgrade_ProtocolVersionValidation(t *testing.T) {
	tests := []struct {
		name            string
		versionHeader   string
		setVersion      bool
		expectedErr     error
		expectedStatus  int
		expectedSuccess bool
	}{
		{
			name:            "缺失 Protocol-Version 头",
			setVersion:      false,
			expectedErr:     ErrInvalidProtocolVersion,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name:            "Protocol-Version 为 0",
			versionHeader:   "0",
			setVersion:      true,
			expectedErr:     ErrInvalidProtocolVersion,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name:            "Protocol-Version 为 3",
			versionHeader:   "3",
			setVersion:      true,
			expectedErr:     ErrInvalidProtocolVersion,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name:            "Protocol-Version 为 v1",
			versionHeader:   "v1",
			setVersion:      true,
			expectedErr:     ErrInvalidProtocolVersion,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name:            "Protocol-Version 为 1.0",
			versionHeader:   "1.0",
			setVersion:      true,
			expectedErr:     ErrInvalidProtocolVersion,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name:            "Protocol-Version 为负数 -1",
			versionHeader:   "-1",
			setVersion:      true,
			expectedErr:     ErrInvalidProtocolVersion,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name:            "Protocol-Version 为 1 校验成功",
			versionHeader:   "1",
			setVersion:      true,
			expectedErr:     nil,
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
		{
			name:            "Protocol-Version 为 2 校验成功",
			versionHeader:   "2",
			setVersion:      true,
			expectedErr:     nil,
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
		{
			name:            "Protocol-Version 带前后空格修剪后校验成功",
			versionHeader:   " 1 ",
			setVersion:      true,
			expectedErr:     nil,
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
			req.Header.Set("Authorization", "Bearer "+testSharedToken)
			req.Header.Set("Serial-Number", testSerialNum)
			if tc.setVersion {
				req.Header.Set("Protocol-Version", tc.versionHeader)
			}

			info, err := AuthenticateUpgrade(req, testSharedToken, 0)
			if tc.expectedSuccess {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				if info == nil {
					t.Fatal("expected non-nil ClientHeaderInfo")
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected error to match %v, got %v", tc.expectedErr, err)
				}
				if HTTPStatus(err) != tc.expectedStatus {
					t.Fatalf("expected status code %d, got %d", tc.expectedStatus, HTTPStatus(err))
				}
			}
		})
	}
}

// TestAuthenticateUpgrade_HeaderSizeLimits 验证请求头单项与总计长度超限校验。
func TestAuthenticateUpgrade_HeaderSizeLimits(t *testing.T) {
	tests := []struct {
		name            string
		setupReq        func(r *http.Request)
		maxHeaderBytes  int
		expectedErr     error
		expectedStatus  int
		expectedSuccess bool
	}{
		{
			name: "单 Header 键超限 (1025 字符)",
			setupReq: func(r *http.Request) {
				longKey := "X-Custom-" + strings.Repeat("k", 1020)
				r.Header.Set(longKey, "valid-val")
			},
			maxHeaderBytes:  0,
			expectedErr:     ErrHeaderTooLarge,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name: "单 Header 值超限 (1025 字符)",
			setupReq: func(r *http.Request) {
				r.Header.Set("Device-Id", strings.Repeat("d", 1025))
			},
			maxHeaderBytes:  0,
			expectedErr:     ErrHeaderTooLarge,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name: "总 Header 长度超限 (> 8192 字符)",
			setupReq: func(r *http.Request) {
				for i := 0; i < 10; i++ {
					k := "X-Header-" + string(rune('A'+i))
					r.Header.Set(k, strings.Repeat("v", 850))
				}
			},
			maxHeaderBytes:  0,
			expectedErr:     ErrHeaderTooLarge,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name: "自定义单 Header 限制超限 (上限 500，输入 600)",
			setupReq: func(r *http.Request) {
				r.Header.Set("Device-Id", strings.Repeat("x", 600))
			},
			maxHeaderBytes:  500,
			expectedErr:     ErrHeaderTooLarge,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name: "请求头长度在边界内正常通过",
			setupReq: func(r *http.Request) {
				r.Header.Set("Device-Id", strings.Repeat("x", 1000))
			},
			maxHeaderBytes:  1024,
			expectedErr:     nil,
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
			req.Header.Set("Protocol-Version", "1")
			req.Header.Set("Authorization", "Bearer "+testSharedToken)
			req.Header.Set("Serial-Number", testSerialNum)
			if tc.setupReq != nil {
				tc.setupReq(req)
			}

			info, err := AuthenticateUpgrade(req, testSharedToken, tc.maxHeaderBytes)
			if tc.expectedSuccess {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				if info == nil {
					t.Fatal("expected non-nil ClientHeaderInfo")
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected error to match %v, got %v", tc.expectedErr, err)
				}
				if HTTPStatus(err) != tc.expectedStatus {
					t.Fatalf("expected status code %d, got %d", tc.expectedStatus, HTTPStatus(err))
				}
			}
		})
	}
}

// TestAuthenticateUpgrade_ClientHeaderExtraction 验证客户端诊断头信息的提取与 v1/v2 协议版本区分校验。
func TestAuthenticateUpgrade_ClientHeaderExtraction(t *testing.T) {
	t.Run("v1 协议缺失 Serial-Number 但携带 Device-Id 握手成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Client-Id", testClientID)
		req.Header.Set("User-Agent", testUserAgent)

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err != nil {
			t.Fatalf("unexpected error for v1 client without Serial-Number: %v", err)
		}
		if info == nil {
			t.Fatal("expected non-nil ClientHeaderInfo")
		}
		if info.DeviceID != testDeviceID {
			t.Errorf("expected DeviceID %q, got %q", testDeviceID, info.DeviceID)
		}
		if info.ClientID != testClientID {
			t.Errorf("expected ClientID %q, got %q", testClientID, info.ClientID)
		}
		if info.SerialNumber != "" {
			t.Errorf("expected empty SerialNumber, got %q", info.SerialNumber)
		}
		if info.ProtocolVersion != "1" {
			t.Errorf("expected ProtocolVersion '1', got %q", info.ProtocolVersion)
		}
	})

	t.Run("v1 协议 Serial-Number 为空字符串握手成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Serial-Number", "")

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err != nil {
			t.Fatalf("unexpected error for v1 empty Serial-Number: %v", err)
		}
		if info.SerialNumber != "" {
			t.Errorf("expected empty SerialNumber, got %q", info.SerialNumber)
		}
	})

	t.Run("v1 协议 Serial-Number 仅包含空格修剪为空且握手成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Serial-Number", "   ")

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err != nil {
			t.Fatalf("unexpected error for v1 whitespace Serial-Number: %v", err)
		}
		if info.SerialNumber != "" {
			t.Errorf("expected empty SerialNumber, got %q", info.SerialNumber)
		}
	})

	t.Run("v1 协议携带合法 Serial-Number 接入成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Serial-Number", testSerialNum)

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.SerialNumber != testSerialNum {
			t.Errorf("expected SerialNumber %q, got %q", testSerialNum, info.SerialNumber)
		}
		if info.DeviceID != "" {
			t.Errorf("expected empty DeviceID, got %q", info.DeviceID)
		}
		if info.ClientID != "" {
			t.Errorf("expected empty ClientID, got %q", info.ClientID)
		}
	})

	t.Run("v1 协议携带完整 Device-Id / Client-Id / Serial-Number 接入成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Client-Id", testClientID)
		req.Header.Set("Serial-Number", testSerialNum)
		req.Header.Set("User-Agent", testUserAgent)

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.DeviceID != testDeviceID {
			t.Errorf("expected DeviceID %q, got %q", testDeviceID, info.DeviceID)
		}
		if info.ClientID != testClientID {
			t.Errorf("expected ClientID %q, got %q", testClientID, info.ClientID)
		}
		if info.SerialNumber != testSerialNum {
			t.Errorf("expected SerialNumber %q, got %q", testSerialNum, info.SerialNumber)
		}
	})

	t.Run("v2 协议缺失 Serial-Number 握手被拒绝", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "2")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Client-Id", testClientID)
		req.Header.Set("User-Agent", testUserAgent)

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err == nil {
			t.Fatal("expected error for missing Serial-Number in v2, got nil")
		}
		if !errors.Is(err, ErrMissingSerialNumber) {
			t.Fatalf("expected ErrMissingSerialNumber, got %v", err)
		}
		if HTTPStatus(err) != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", HTTPStatus(err))
		}
		if info != nil {
			t.Fatalf("expected nil info on error, got %+v", info)
		}
	})

	t.Run("v2 协议 Serial-Number 为空字符串握手被拒绝", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "2")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Serial-Number", "")

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err == nil {
			t.Fatal("expected error for empty Serial-Number in v2, got nil")
		}
		if !errors.Is(err, ErrMissingSerialNumber) {
			t.Fatalf("expected ErrMissingSerialNumber, got %v", err)
		}
		if HTTPStatus(err) != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", HTTPStatus(err))
		}
		if info != nil {
			t.Fatalf("expected nil info on error, got %+v", info)
		}
	})

	t.Run("v2 协议 Serial-Number 仅包含空格握手被拒绝", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "2")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Serial-Number", "   ")

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err == nil {
			t.Fatal("expected error for whitespace Serial-Number in v2, got nil")
		}
		if !errors.Is(err, ErrMissingSerialNumber) {
			t.Fatalf("expected ErrMissingSerialNumber, got %v", err)
		}
		if HTTPStatus(err) != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", HTTPStatus(err))
		}
		if info != nil {
			t.Fatalf("expected nil info on error, got %+v", info)
		}
	})

	t.Run("v2 协议携带合法 Serial-Number 接入成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "2")
		req.Header.Set("Authorization", "Bearer "+testSharedToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Serial-Number", testSerialNum)

		info, err := AuthenticateUpgrade(req, testSharedToken, 0)
		if err != nil {
			t.Fatalf("unexpected error for v2 client with Serial-Number: %v", err)
		}
		if info.SerialNumber != testSerialNum {
			t.Errorf("expected SerialNumber %q, got %q", testSerialNum, info.SerialNumber)
		}
		if info.ProtocolVersion != "2" {
			t.Errorf("expected ProtocolVersion '2', got %q", info.ProtocolVersion)
		}
	})
}

// TestAuthenticateUpgrade_NilRequest 验证传入 nil 请求时的边界处理。
func TestAuthenticateUpgrade_NilRequest(t *testing.T) {
	info, err := AuthenticateUpgrade(nil, testSharedToken, 0)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}
	if info != nil {
		t.Fatal("expected nil info for nil request")
	}
	if HTTPStatus(err) != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", HTTPStatus(err))
	}
}

// TestAuthenticateUpgrade_LogSecurity 验证在认证成功与失败的诊断日志中严禁出现 Token 与 Authorization 明文。
func TestAuthenticateUpgrade_LogSecurity(t *testing.T) {
	secretToken := "super-confidential-secret-token-9988"

	tests := []struct {
		name       string
		authHeader string
		action     func(l *slog.Logger, r *http.Request)
	}{
		{
			name:       "认证拒绝日志不得包含 Token 明文",
			authHeader: "Bearer " + secretToken,
			action: func(l *slog.Logger, r *http.Request) {
				_, err := AuthenticateUpgrade(r, "different-token", 0)
				LogAuthRejection(l, r, err)
			},
		},
		{
			name:       "认证成功日志不得包含 Token 明文",
			authHeader: "Bearer " + secretToken,
			action: func(l *slog.Logger, r *http.Request) {
				info, err := AuthenticateUpgrade(r, secretToken, 0)
				if err != nil {
					t.Fatalf("unexpected auth error: %v", err)
				}
				LogAuthSuccess(l, r, info)
			},
		},
		{
			name:       "通过 RejectUpgrade 输出日志不得包含 Token 明文",
			authHeader: "Bearer " + secretToken,
			action: func(l *slog.Logger, r *http.Request) {
				_, err := AuthenticateUpgrade(r, "different-token", 0)
				rec := httptest.NewRecorder()
				RejectUpgrade(rec, r, l, err)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			testLogger := logger.New(&logBuf, slog.LevelDebug)

			req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
			req.Header.Set("Protocol-Version", "1")
			req.Header.Set("Authorization", tc.authHeader)
			req.Header.Set("Device-Id", testDeviceID)
			req.Header.Set("Client-Id", testClientID)
			req.Header.Set("Serial-Number", testSerialNum)

			tc.action(testLogger, req)

			loggedOutput := logBuf.String()
			if strings.Contains(loggedOutput, secretToken) {
				t.Fatalf("security violation: log contains secret token plaintext: %s", loggedOutput)
			}
			if strings.Contains(loggedOutput, "Bearer "+secretToken) {
				t.Fatalf("security violation: log contains Bearer token header plaintext: %s", loggedOutput)
			}
		})
	}
}

// TestRejectUpgrade_PreUpgradeRejection 验证所有拒绝均发生在 WebSocket 协议升级之前，返回标准 HTTP 错误且无协议切换。
func TestRejectUpgrade_PreUpgradeRejection(t *testing.T) {
	tests := []struct {
		name           string
		reqSetup       func(r *http.Request)
		sharedToken    string
		expectedStatus int
	}{
		{
			name: "缺少 Token 返回 401 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Serial-Number", testSerialNum)
			},
			sharedToken:    testSharedToken,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "错误 Token 返回 401 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer wrong-token")
				r.Header.Set("Serial-Number", testSerialNum)
			},
			sharedToken:    testSharedToken,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "协议版本非法返回 400 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "99")
				r.Header.Set("Authorization", "Bearer "+testSharedToken)
				r.Header.Set("Serial-Number", testSerialNum)
			},
			sharedToken:    testSharedToken,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "Header 超长返回 400 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer "+testSharedToken)
				r.Header.Set("Serial-Number", testSerialNum)
				r.Header.Set("Device-Id", strings.Repeat("x", 2000))
			},
			sharedToken:    testSharedToken,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "v2 缺少 Serial-Number 返回 400 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "2")
				r.Header.Set("Authorization", "Bearer "+testSharedToken)
			},
			sharedToken:    testSharedToken,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			req.Header.Set("Sec-WebSocket-Version", "13")
			tc.reqSetup(req)

			rec := httptest.NewRecorder()
			var logBuf bytes.Buffer
			testLogger := logger.New(&logBuf, slog.LevelDebug)

			_, err := AuthenticateUpgrade(req, tc.sharedToken, 0)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			RejectUpgrade(rec, req, testLogger, err)

			res := rec.Result()
			defer res.Body.Close()

			if res.StatusCode != tc.expectedStatus {
				t.Fatalf("expected HTTP status %d, got %d", tc.expectedStatus, res.StatusCode)
			}

			// 断言未发生协议升级（状态码绝不是 101 Switching Protocols）
			if res.StatusCode == http.StatusSwitchingProtocols {
				t.Fatal("unexpected protocol upgrade: returned 101 Switching Protocols")
			}

			// 断言响应头不含 Upgrade 或 WebSocket 升级响应
			if upgradeHeader := res.Header.Get("Upgrade"); upgradeHeader != "" {
				t.Fatalf("unexpected Upgrade response header: %q", upgradeHeader)
			}
		})
	}
}

// TestAuthError_Helpers 验证 AuthError 结构、Unwrap、HTTPStatus 与 ValidateHeaders 辅助函数。
func TestAuthError_Helpers(t *testing.T) {
	// 测试 HTTPStatus 对 nil 返回 StatusOK
	if code := HTTPStatus(nil); code != http.StatusOK {
		t.Errorf("expected 200 for nil, got %d", code)
	}

	// 测试 AuthError.Error() 与 Unwrap()
	innerErr := errors.New("custom inner error")
	authErr := &AuthError{
		StatusCode: http.StatusForbidden,
		Err:        innerErr,
	}

	if authErr.Error() != "custom inner error" {
		t.Errorf("expected %q, got %q", "custom inner error", authErr.Error())
	}
	if !errors.Is(authErr, innerErr) {
		t.Errorf("expected errors.Is to match inner error")
	}
	if code := HTTPStatus(authErr); code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", code)
	}

	nilAuthErr := &AuthError{StatusCode: http.StatusBadRequest, Err: nil}
	if nilAuthErr.Error() != "authentication error" {
		t.Errorf("expected fallback error message, got %q", nilAuthErr.Error())
	}

	// 测试 ValidateHeaders 默认参数
	h := http.Header{}
	h.Set("Key", "Value")
	if err := ValidateHeaders(h, 0, 0); err != nil {
		t.Errorf("expected nil error for valid headers with default limits, got %v", err)
	}
}
