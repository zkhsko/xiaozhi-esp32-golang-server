package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

type mockDeviceAgentResolver struct {
	tokens    map[string]*database.DeviceAccessToken
	snapshots map[string]*database.AgentRuntimeSnapshot
	snapErr   error
}

func (m *mockDeviceAgentResolver) FindDeviceAccessTokenByAccessToken(ctx context.Context, token string) (*database.DeviceAccessToken, error) {
	if tok, ok := m.tokens[token]; ok {
		return tok, nil
	}
	return nil, database.ErrAccessTokenNotFound
}

func (m *mockDeviceAgentResolver) ResolveAgentRuntimeSnapshotByDeviceType(ctx context.Context, deviceType string) (*database.AgentRuntimeSnapshot, error) {
	if m.snapErr != nil {
		return nil, m.snapErr
	}
	if snap, ok := m.snapshots[deviceType]; ok {
		return snap, nil
	}
	return nil, fmt.Errorf("device type %q: %w", deviceType, database.ErrDeviceTypeNotFound)
}

func TestAuthenticateUpgrade_Success_TableDataIsAuthority(t *testing.T) {
	tokenStr := "valid-bearer-token-12345678901234567890"
	resolver := &mockDeviceAgentResolver{
		tokens: map[string]*database.DeviceAccessToken{
			tokenStr: {
				SerialNumber: "sn-real-from-table-001",
				AccessToken:  tokenStr,
				DeviceType:   "test-device",
				IssuedAt:     time.Now(),
			},
		},
	}

	// 请求头传入相互冲突的 fake 标识，也不影响最终以表里的 SN 与 DeviceType 为准
	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Protocol-Version", "1")
	req.Header.Set("Serial-Number", "fake-header-sn")
	req.Header.Set("Device-Id", "fake-header-device-id")
	req.Header.Set("Client-Id", "fake-header-client-id")
	req.Header.Set("User-Agent", "ESP32-Client/1.0")

	tok, err := AuthenticateUpgrade(req, resolver, 0)
	if err != nil {
		t.Fatalf("expected authentication to succeed, got error: %v", err)
	}

	if tok == nil {
		t.Fatal("expected non-nil token entity")
	}
	if tok.SerialNumber != "sn-real-from-table-001" {
		t.Errorf("expected SerialNumber %q from table, got %q", "sn-real-from-table-001", tok.SerialNumber)
	}
	if tok.DeviceType != "test-device" {
		t.Errorf("expected DeviceType %q, got %q", "test-device", tok.DeviceType)
	}
}

func TestAuthenticateUpgrade_Success_NoDeviceHeaders(t *testing.T) {
	tokenStr := "token-without-headers-001"
	resolver := &mockDeviceAgentResolver{
		tokens: map[string]*database.DeviceAccessToken{
			tokenStr: {
				SerialNumber: "sn-no-header-001",
				AccessToken:  tokenStr,
				DeviceType:   "default",
				IssuedAt:     time.Now(),
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Protocol-Version", "1")

	tok, err := AuthenticateUpgrade(req, resolver, 0)
	if err != nil {
		t.Fatalf("expected auth success without device headers, got: %v", err)
	}

	if tok.SerialNumber != "sn-no-header-001" {
		t.Errorf("expected SerialNumber %q, got %q", "sn-no-header-001", tok.SerialNumber)
	}
}

func TestAuthenticateUpgrade_ValidationAndAuthFailures(t *testing.T) {
	validToken := "valid-test-token"
	revokedAt := time.Now().Add(-1 * time.Hour)
	resolver := &mockDeviceAgentResolver{
		tokens: map[string]*database.DeviceAccessToken{
			validToken: {
				SerialNumber: "sn-valid",
				AccessToken:  validToken,
				IssuedAt:     time.Now(),
			},
			"revoked-token": {
				SerialNumber: "sn-revoked",
				AccessToken:  "revoked-token",
				IssuedAt:     time.Now().Add(-2 * time.Hour),
				RevokedAt:    &revokedAt,
			},
		},
	}

	tests := []struct {
		name       string
		req        func() *http.Request
		wantStatus int
		wantErr    error
	}{
		{
			name: "NilRequest",
			req: func() *http.Request {
				return nil
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "MissingProtocolVersion",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Authorization", "Bearer "+validToken)
				return r
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    ErrInvalidProtocolVersion,
		},
		{
			name: "InvalidProtocolVersion",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Authorization", "Bearer "+validToken)
				r.Header.Set("Protocol-Version", "2")
				return r
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    ErrInvalidProtocolVersion,
		},
		{
			name: "MissingAuthorizationHeader",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Protocol-Version", "1")
				return r
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    ErrMissingToken,
		},
		{
			name: "InvalidAuthorizationFormat_NoBearer",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
				return r
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    ErrInvalidTokenFormat,
		},
		{
			name: "InvalidAuthorizationFormat_EmptyToken",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer   ")
				return r
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    ErrInvalidTokenFormat,
		},
		{
			name: "TokenNotFoundInDatabase",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer non-existent-token")
				return r
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    ErrInvalidToken,
		},
		{
			name: "TokenRevoked",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer revoked-token")
				return r
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    ErrInvalidToken,
		},
		{
			name: "HeaderTooLarge",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
				r.Header.Set("Protocol-Version", "1")
				r.Header.Set("Authorization", "Bearer "+validToken)
				r.Header.Set("X-Custom-Large", strings.Repeat("a", 2048))
				return r
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    ErrHeaderTooLarge,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.req()
			tok, err := AuthenticateUpgrade(r, resolver, 0)
			if err == nil {
				t.Fatalf("expected error, got tok=%v", tok)
			}
			if HTTPStatus(err) != tc.wantStatus {
				t.Errorf("expected HTTP status %d, got %d", tc.wantStatus, HTTPStatus(err))
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("expected error wrapping %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestHandler_DynamicLoading_FailFast(t *testing.T) {
	tokenStr := "valid-token-for-handler"
	resolver := &mockDeviceAgentResolver{
		tokens: map[string]*database.DeviceAccessToken{
			tokenStr: {
				SerialNumber: "sn-handler-001",
				AccessToken:  tokenStr,
				DeviceType:   "unconfigured-device",
				IssuedAt:     time.Now(),
			},
		},
	}

	h := NewHandler(HandlerOptions{
		Config: &config.Config{
			Server: config.ServerConfig{
				ListenAddr:            ":8080",
				WebSocketURL:          "ws://localhost:8080/xiaozhi/v1/",
				MaxConcurrentSessions: 10,
			},
		},
		DB: resolver,
	})

	// 1. 设备类型不存在 -> HTTP 400
	req := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Protocol-Version", "1")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected HTTP 400 for unconfigured device type, got %d", w.Code)
	}

	// 2. 智能体不存在 -> HTTP 500
	resolver.snapErr = database.ErrAgentConfigNotFound
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected HTTP 500 for missing agent config, got %d", w.Code)
	}

	// 3. ASR 被禁用 -> HTTP 500
	resolver.snapErr = database.ErrReferencedASRDisabled
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected HTTP 500 for disabled asr, got %d", w.Code)
	}

	// 4. LLM 被禁用 -> HTTP 500
	resolver.snapErr = database.ErrReferencedLLMDisabled
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected HTTP 500 for disabled llm, got %d", w.Code)
	}

	// 5. TTS 被禁用 -> HTTP 500
	resolver.snapErr = database.ErrReferencedTTSDisabled
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected HTTP 500 for disabled tts, got %d", w.Code)
	}
}

func TestSession_DeviceKey_SNIsUniqueIdentity(t *testing.T) {
	sess := NewSession(context.Background(), Options{
		SerialNumber: "sn-unique-identity-001",
	})

	if sess.DeviceKey() != "sn-unique-identity-001" {
		t.Errorf("expected DeviceKey %q, got %q", "sn-unique-identity-001", sess.DeviceKey())
	}
}

func TestRegistry_SNBasedExclusion(t *testing.T) {
	limiter := NewSessionLimiter(10)
	reg := NewRegistry(limiter, nil)

	s1 := NewSession(context.Background(), Options{
		SerialNumber: "sn-common-001",
	})
	s2 := NewSession(context.Background(), Options{
		SerialNumber: "sn-common-001",
	})
	s3 := NewSession(context.Background(), Options{
		SerialNumber: "sn-another-002",
	})

	// 注册 s1
	cleanup1, ok1 := reg.Register(s1)
	if !ok1 {
		t.Fatalf("failed to register s1")
	}
	defer cleanup1()

	if reg.GetBySerial("sn-common-001") != s1 {
		t.Errorf("expected to find s1 for sn-common-001")
	}

	// 注册 s2 (相同 SN): s1 应被踢掉，s2 成为活跃会话
	cleanup2, ok2 := reg.Register(s2)
	if !ok2 {
		t.Fatalf("failed to register s2")
	}
	defer cleanup2()

	if reg.GetBySerial("sn-common-001") != s2 {
		t.Errorf("expected to find s2 for sn-common-001")
	}

	// 注册 s3 (不同 SN): 不应踢掉 s2，两者共存
	cleanup3, ok3 := reg.Register(s3)
	if !ok3 {
		t.Fatalf("failed to register s3")
	}
	defer cleanup3()

	if reg.GetBySerial("sn-common-001") != s2 {
		t.Errorf("expected s2 to still be active for sn-common-001")
	}
	if reg.GetBySerial("sn-another-002") != s3 {
		t.Errorf("expected s3 to be active for sn-another-002")
	}
}
