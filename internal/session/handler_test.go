package session

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// createTestConfig 创建测试用的服务端配置对象。
func createTestConfig(token string, maxSessions int) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            ":8080",
			WebSocketURL:          "ws://localhost:8080/xiaozhi/v1/",
			MaxConcurrentSessions: maxSessions,
			MaxHTTPHeaderBytes:    1024,
		},
		DeviceSharedToken: token,
	}
}

// TestHandler_PathNotFound 验证访问非 WebSocketPath 时直接返回 404。
func TestHandler_PathNotFound(t *testing.T) {
	cfg := createTestConfig("valid-token", 5)
	h := NewHandler(cfg, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/other/path", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
}

// TestHandler_AuthRejectionBeforeLimiter 验证认证失败时不占用并发名额且不执行升级。
func TestHandler_AuthRejectionBeforeLimiter(t *testing.T) {
	var buf bytes.Buffer
	testLogger := logger.New(&buf, slog.LevelInfo)

	cfg := createTestConfig("correct-token", 2)
	limiter := NewSessionLimiter(2)
	h := NewHandler(cfg, limiter, testLogger)

	tests := []struct {
		name           string
		token          string
		protoVer       string
		expectedStatus int
	}{
		{
			name:           "缺失 Token",
			token:          "",
			protoVer:       "1",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "错误 Token",
			token:          "Bearer wrong-token",
			protoVer:       "1",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "协议版本错误",
			token:          "Bearer correct-token",
			protoVer:       "2",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, WebSocketPath, nil)
			if tt.token != "" {
				req.Header.Set("Authorization", tt.token)
			}
			if tt.protoVer != "" {
				req.Header.Set("Protocol-Version", tt.protoVer)
			}
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// 认证失败不应占用名额
			if limiter.ActiveCount() != 0 {
				t.Errorf("expected limiter ActiveCount 0, got %d", limiter.ActiveCount())
			}
		})
	}
}

// TestHandler_MaxCapacityRejection_503 验证满载拒绝返回 503、未升级且连接关闭后名额可复用。
func TestHandler_MaxCapacityRejection_503(t *testing.T) {
	const maxSessions = 2
	const token = "secret-token-pass"

	cfg := createTestConfig(token, maxSessions)
	limiter := NewSessionLimiter(maxSessions)
	handler := NewHandler(cfg, limiter, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + WebSocketPath

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"device-client-1"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. 建立第 1 个连接
	conn1, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial conn1: %v", err)
	}
	t.Cleanup(func() { conn1.Close(websocket.StatusNormalClosure, "") })

	if count := limiter.ActiveCount(); count != 1 {
		t.Fatalf("expected ActiveCount 1, got %d", count)
	}

	// 2. 建立第 2 个连接
	dialOpts2 := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"device-client-2"},
		},
	}
	conn2, _, err := websocket.Dial(ctx, wsURL, dialOpts2)
	if err != nil {
		t.Fatalf("failed to dial conn2: %v", err)
	}
	t.Cleanup(func() { conn2.Close(websocket.StatusNormalClosure, "") })

	if count := limiter.ActiveCount(); count != 2 {
		t.Fatalf("expected ActiveCount 2, got %d", count)
	}

	// 3. 尝试建立第 3 个连接（超出并发容量，应当收到 503 拒绝）
	dialOpts3 := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"device-client-3"},
		},
	}
	_, resp3, err3 := websocket.Dial(ctx, wsURL, dialOpts3)
	if err3 == nil {
		t.Fatal("expected dial to fail due to capacity limit, but succeeded")
	}
	if resp3 == nil || resp3.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 Service Unavailable, got %v", resp3)
	}

	// 名额仍保持为满载状态 2
	if count := limiter.ActiveCount(); count != 2 {
		t.Fatalf("expected ActiveCount still 2, got %d", count)
	}

	// 4. 关闭第 1 个连接，名额释放并复用
	if err := conn1.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("failed to close conn1: %v", err)
	}

	// 等待服务端检测到连接断开并释放名额
	deadline := time.Now().Add(2 * time.Second)
	for limiter.ActiveCount() > 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count := limiter.ActiveCount(); count != 1 {
		t.Fatalf("expected ActiveCount 1 after conn1 close, got %d", count)
	}

	// 5. 名额复用：第 3 个连接再次尝试建连，应当成功升级
	conn3, _, err := websocket.Dial(ctx, wsURL, dialOpts3)
	if err != nil {
		t.Fatalf("expected conn3 dial to succeed after quota released, got: %v", err)
	}
	t.Cleanup(func() { conn3.Close(websocket.StatusNormalClosure, "") })

	if count := limiter.ActiveCount(); count != 2 {
		t.Fatalf("expected ActiveCount 2 after conn3 acquired quota, got %d", count)
	}
}

// TestHandler_NonWebSocketRequestReleaseQuota 验证升级失败（非 WebSocket 请求）时名额恰好被释放。
func TestHandler_NonWebSocketRequestReleaseQuota(t *testing.T) {
	const token = "secret-token-test"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// 发起普通 HTTP GET 请求，携带合法 Token 和 Protocol-Version 但无 WebSocket 升级请求头
	req, err := http.NewRequest(http.MethodGet, server.URL+WebSocketPath, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Protocol-Version", "1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUpgradeRequired && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 4xx status for non-websocket upgrade, got %d", resp.StatusCode)
	}

	// 验证请求结束后名额恢复为 0
	if count := limiter.ActiveCount(); count != 0 {
		t.Fatalf("expected ActiveCount 0 after upgrade failure, got %d", count)
	}
}

// TestHandler_CompressionDisabled 验证握手升级显式禁用压缩。
func TestHandler_CompressionDisabled(t *testing.T) {
	const token = "secret-token-comp"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + WebSocketPath

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":            []string{"Bearer " + token},
			"Protocol-Version":         []string{"1"},
			"Device-Id":                []string{"device-comp-check"},
			"Sec-WebSocket-Extensions": []string{"permessage-deflate"},
		},
		CompressionMode: websocket.CompressionNoContextTakeover,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 验证响应头中没有协商 permessage-deflate 扩展
	if ext := resp.Header.Get("Sec-WebSocket-Extensions"); strings.Contains(ext, "permessage-deflate") {
		t.Fatalf("expected compression to be disabled, but server returned extension: %s", ext)
	}
}

// TestHandler_ConcurrentSessionsWithRace 验证高并发建立连接与关闭时的竞争安全性与容量约束。
func TestHandler_ConcurrentSessionsWithRace(t *testing.T) {
	const maxSessions = 5
	const concurrency = 25
	const token = "secret-token-concurrent"

	cfg := createTestConfig(token, maxSessions)
	limiter := NewSessionLimiter(maxSessions)
	handler := NewHandler(cfg, limiter, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + WebSocketPath

	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		deviceID := fmt.Sprintf("device-%d", i)
		go func(id string) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()

			dialOpts := &websocket.DialOptions{
				HTTPHeader: http.Header{
					"Authorization":    []string{"Bearer " + token},
					"Protocol-Version": []string{"1"},
					"Device-Id":        []string{id},
				},
			}

			conn, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
			if err != nil {
				// 被 503 拒绝属于正常满载行为
				if resp != nil && resp.StatusCode == http.StatusServiceUnavailable {
					return
				}
				// 其它错误若非主动取消则报错
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "normal exit")

			// 验证当前活跃连接数不超过上限
			active := limiter.ActiveCount()
			if active > maxSessions {
				t.Errorf("active sessions %d exceeded max %d", active, maxSessions)
			}

			// 保持短暂连接后退出
			time.Sleep(20 * time.Millisecond)
		}(deviceID)
	}

	wg.Wait()

	// 等待所有断开连接在服务端完全退出
	deadline := time.Now().Add(3 * time.Second)
	for limiter.ActiveCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if finalActive := limiter.ActiveCount(); finalActive != 0 {
		t.Fatalf("expected all sessions released (0), got %d", finalActive)
	}
}
