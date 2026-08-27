package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

const (
	testValidToken = "secret-token-123456"
	testRevokedTok = "revoked-token-111"
	testExpiredTok = "expired-token-222"
	testDeviceID   = "dev-device-001"
	testClientID   = "client-app-002"
	testSerialNum  = "SN-20260819-ABCD"
	testUserAgent  = "xiaozhi-esp32-firmware/1.0"
)

// fakeTokenFinder 实现 TokenFinder 接口，用于测试。
type fakeTokenFinder struct {
	tokens map[string]*database.DeviceAccessToken
	err    error
}

func (f *fakeTokenFinder) FindDeviceAccessTokenByAccessToken(ctx context.Context, accessToken string) (*database.DeviceAccessToken, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.tokens == nil {
		return nil, database.ErrAccessTokenNotFound
	}
	tok, ok := f.tokens[accessToken]
	if !ok {
		return nil, database.ErrAccessTokenNotFound
	}
	return tok, nil
}

func newTestTokenFinder() *fakeTokenFinder {
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	return &fakeTokenFinder{
		tokens: map[string]*database.DeviceAccessToken{
			testValidToken: {
				ID:           1,
				SerialNumber: testSerialNum,
				AccessToken:  testValidToken,
				IssuedAt:     time.Now().Add(-10 * time.Minute),
			},
			testRevokedTok: {
				ID:           2,
				SerialNumber: testSerialNum,
				AccessToken:  testRevokedTok,
				IssuedAt:     time.Now().Add(-10 * time.Minute),
				RevokedAt:    &past,
			},
			testExpiredTok: {
				ID:           3,
				SerialNumber: testSerialNum,
				AccessToken:  testExpiredTok,
				IssuedAt:     time.Now().Add(-10 * time.Minute),
				ExpiresAt:    &past,
			},
			"future-valid-token": {
				ID:           4,
				SerialNumber: testSerialNum,
				AccessToken:  "future-valid-token",
				IssuedAt:     time.Now().Add(-10 * time.Minute),
				ExpiresAt:    &future,
			},
		},
	}
}

// TestAuthenticateUpgrade_TokenValidation 验证 Token 各种缺失、格式错误、内容错误、状态错误与正确情况下的认证行为。
func TestAuthenticateUpgrade_TokenValidation(t *testing.T) {
	tests := []struct {
		name            string
		authHeader      string
		setAuthHeader   bool
		reqSN           string
		finder          TokenFinder
		expectedErr     error
		expectedStatus  int
		expectedSuccess bool
	}{
		{
			name:            "缺失 Authorization 头",
			setAuthHeader:   false,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrMissingToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "Authorization 头为空字符串",
			authHeader:      "",
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrMissingToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "Bearer 后 Token 为空",
			authHeader:      "Bearer ",
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "Bearer 后仅含空格",
			authHeader:      "Bearer    ",
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "错误的前缀 Basic",
			authHeader:      "Basic " + testValidToken,
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "小写前缀 bearer",
			authHeader:      "bearer " + testValidToken,
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "无 Bearer 前缀直接为 Token",
			authHeader:      testValidToken,
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidTokenFormat,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "数据库中不存在的 Token",
			authHeader:      "Bearer non-existent-token",
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "TokenFinder 为 nil 时拒绝所有请求",
			authHeader:      "Bearer " + testValidToken,
			setAuthHeader:   true,
			finder:          nil,
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "已撤销的 Token 拒绝连接",
			authHeader:      "Bearer " + testRevokedTok,
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "已过期的 Token 拒绝连接",
			authHeader:      "Bearer " + testExpiredTok,
			setAuthHeader:   true,
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "数据库查询发生系统错误时拒绝连接",
			authHeader:      "Bearer " + testValidToken,
			setAuthHeader:   true,
			finder:          &fakeTokenFinder{err: errors.New("db connection pool exhausted")},
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "请求头中 Serial-Number 与数据库 Token 记录不匹配时拒绝",
			authHeader:      "Bearer " + testValidToken,
			setAuthHeader:   true,
			reqSN:           "SN-MISMATCH-999",
			finder:          newTestTokenFinder(),
			expectedErr:     ErrInvalidToken,
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name:            "正确的 Bearer Token 且 SN 匹配认证成功",
			authHeader:      "Bearer " + testValidToken,
			setAuthHeader:   true,
			reqSN:           testSerialNum,
			finder:          newTestTokenFinder(),
			expectedErr:     nil,
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
		{
			name:            "未过期的带有效期 Token 认证成功",
			authHeader:      "Bearer future-valid-token",
			setAuthHeader:   true,
			reqSN:           testSerialNum,
			finder:          newTestTokenFinder(),
			expectedErr:     nil,
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
			req.Header.Set("Protocol-Version", "1")
			if tc.reqSN != "" {
				req.Header.Set("Serial-Number", tc.reqSN)
			} else {
				req.Header.Set("Serial-Number", testSerialNum)
			}
			if tc.setAuthHeader {
				req.Header.Set("Authorization", tc.authHeader)
			}

			info, err := AuthenticateUpgrade(req, tc.finder, 0)
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
	finder := newTestTokenFinder()
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
			name:            "Protocol-Version 为 2",
			versionHeader:   "2",
			setVersion:      true,
			expectedErr:     ErrInvalidProtocolVersion,
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
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
			req.Header.Set("Authorization", "Bearer "+testValidToken)
			req.Header.Set("Serial-Number", testSerialNum)
			if tc.setVersion {
				req.Header.Set("Protocol-Version", tc.versionHeader)
			}

			info, err := AuthenticateUpgrade(req, finder, 0)
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
	finder := newTestTokenFinder()
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
			req.Header.Set("Authorization", "Bearer "+testValidToken)
			req.Header.Set("Serial-Number", testSerialNum)
			if tc.setupReq != nil {
				tc.setupReq(req)
			}

			info, err := AuthenticateUpgrade(req, finder, tc.maxHeaderBytes)
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

// TestAuthenticateUpgrade_ClientHeaderExtraction 验证客户端诊断头信息的提取校验与 SN 自动推导。
func TestAuthenticateUpgrade_ClientHeaderExtraction(t *testing.T) {
	finder := newTestTokenFinder()

	t.Run("缺失 Serial-Number 但携带 Device-Id 时握手成功且自动填充 Serial-Number", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testValidToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Client-Id", testClientID)
		req.Header.Set("User-Agent", testUserAgent)

		info, err := AuthenticateUpgrade(req, finder, 0)
		if err != nil {
			t.Fatalf("unexpected error for client without Serial-Number: %v", err)
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
		if info.SerialNumber != testSerialNum {
			t.Errorf("expected SerialNumber %q from database token, got %q", testSerialNum, info.SerialNumber)
		}
		if info.ProtocolVersion != "1" {
			t.Errorf("expected ProtocolVersion '1', got %q", info.ProtocolVersion)
		}
	})

	t.Run("Serial-Number 为空字符串握手成功且自动填充 Serial-Number", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testValidToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Serial-Number", "")

		info, err := AuthenticateUpgrade(req, finder, 0)
		if err != nil {
			t.Fatalf("unexpected error for empty Serial-Number: %v", err)
		}
		if info.SerialNumber != testSerialNum {
			t.Errorf("expected SerialNumber %q from token record, got %q", testSerialNum, info.SerialNumber)
		}
	})

	t.Run("Serial-Number 仅包含空格修剪为空且握手成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testValidToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Serial-Number", "   ")

		info, err := AuthenticateUpgrade(req, finder, 0)
		if err != nil {
			t.Fatalf("unexpected error for whitespace Serial-Number: %v", err)
		}
		if info.SerialNumber != testSerialNum {
			t.Errorf("expected SerialNumber %q from token record, got %q", testSerialNum, info.SerialNumber)
		}
	})

	t.Run("携带合法且匹配的 Serial-Number 接入成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testValidToken)
		req.Header.Set("Serial-Number", testSerialNum)

		info, err := AuthenticateUpgrade(req, finder, 0)
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

	t.Run("携带完整 Device-Id / Client-Id / Serial-Number 接入成功", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
		req.Header.Set("Protocol-Version", "1")
		req.Header.Set("Authorization", "Bearer "+testValidToken)
		req.Header.Set("Device-Id", testDeviceID)
		req.Header.Set("Client-Id", testClientID)
		req.Header.Set("Serial-Number", testSerialNum)
		req.Header.Set("User-Agent", testUserAgent)

		info, err := AuthenticateUpgrade(req, finder, 0)
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
}

// TestAuthenticateUpgrade_NilRequest 验证传入 nil 请求时的边界处理。
func TestAuthenticateUpgrade_NilRequest(t *testing.T) {
	finder := newTestTokenFinder()
	info, err := AuthenticateUpgrade(nil, finder, 0)
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
	finder := newTestTokenFinder()
	secretToken := testValidToken

	tests := []struct {
		name       string
		authHeader string
		action     func(l *slog.Logger, r *http.Request)
	}{
		{
			name:       "认证拒绝日志不得包含 Token 明文",
			authHeader: "Bearer non-existent-secret-token",
			action: func(l *slog.Logger, r *http.Request) {
				_, err := AuthenticateUpgrade(r, finder, 0)
				LogAuthRejection(l, r, err)
			},
		},
		{
			name:       "认证成功日志不得包含 Token 明文",
			authHeader: "Bearer " + secretToken,
			action: func(l *slog.Logger, r *http.Request) {
				info, err := AuthenticateUpgrade(r, finder, 0)
				if err != nil {
					t.Fatalf("unexpected auth error: %v", err)
				}
				LogAuthSuccess(l, r, info)
			},
		},
		{
			name:       "通过 RejectUpgrade 输出日志不得包含 Token 明文",
			authHeader: "Bearer non-existent-secret-token",
			action: func(l *slog.Logger, r *http.Request) {
				_, err := AuthenticateUpgrade(r, finder, 0)
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
	finder := newTestTokenFinder()

	tests := []struct {
		name           string
		reqSetup       func(r *http.Request)
		expectedStatus int
	}{
		{
			name: "缺少 Token 返回 401 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Serial-Number", testSerialNum)
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "错误 Token 返回 401 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer wrong-token")
				r.Header.Set("Serial-Number", testSerialNum)
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "协议版本非法返回 400 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "99")
				r.Header.Set("Authorization", "Bearer "+testValidToken)
				r.Header.Set("Serial-Number", testSerialNum)
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "Header 超长返回 400 且不执行升级",
			reqSetup: func(r *http.Request) {
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer "+testValidToken)
				r.Header.Set("Serial-Number", testSerialNum)
				r.Header.Set("Device-Id", strings.Repeat("x", 2000))
			},
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

			_, err := AuthenticateUpgrade(req, finder, 0)
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
