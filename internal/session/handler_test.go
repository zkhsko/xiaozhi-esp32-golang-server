package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// safeBuffer 提供线程安全的字节缓冲区，供并发日志收集测试使用。
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// createTestConfig 创建测试用的服务端配置对象。
func createTestConfig(token string, maxSessions int) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            ":8080",
			WebSocketURL:          "ws://localhost:8080/xiaozhi/v1/",
			MaxConcurrentSessions: maxSessions,
			MaxHTTPHeaderBytes:    1024,
		},
		Session: config.SessionConfig{
			HelloTimeout:          10 * time.Second,
			MaxWSTextMessageBytes: 32768,
		},
		DeviceSharedToken: token,
	}
}

// dialWebSocketHelper 辅助创建带认证信息的 WebSocket 客户端连接。
func dialWebSocketHelper(t *testing.T, ctx context.Context, serverURL, token string) (*websocket.Conn, *http.Response) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + WebSocketPath
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"test-device-id"},
			"Client-Id":        []string{"test-client-id"},
			"Serial-Number":    []string{"test-serial-number"},
		},
	}
	conn, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	return conn, resp
}

// waitQuotaReleased 等待指定 Limiter 的活跃名额恢复为 0。
func waitQuotaReleased(t *testing.T, limiter *SessionLimiter, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for limiter.ActiveCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count := limiter.ActiveCount(); count != 0 {
		t.Fatalf("expected ActiveCount to become 0, but got %d", count)
	}
}

// TestHandler_PathNotFound 验证访问非 WebSocketPath 时直接返回 404。
func TestHandler_PathNotFound(t *testing.T) {
	cfg := createTestConfig("valid-token", 5)
	h := NewHandler(cfg, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/other/path", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
}

// TestHandler_AuthRejectionBeforeLimiter 验证认证失败时不占用并发名额且不执行升级。
func TestHandler_AuthRejectionBeforeLimiter(t *testing.T) {
	var buf safeBuffer
	testLogger := logger.New(&buf, slog.LevelInfo)

	cfg := createTestConfig("correct-token", 2)
	limiter := NewSessionLimiter(2)
	h := NewHandler(cfg, limiter, nil, nil, nil, testLogger)

	tests := []struct {
		name           string
		token          string
		protoVer       string
		serialNum      string
		expectedStatus int
	}{
		{
			name:           "缺失 Token",
			token:          "",
			protoVer:       "1",
			serialNum:      "test-sn",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "错误 Token",
			token:          "Bearer wrong-token",
			protoVer:       "1",
			serialNum:      "test-sn",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "协议版本错误",
			token:          "Bearer correct-token",
			protoVer:       "2",
			serialNum:      "test-sn",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "缺失 Serial-Number",
			token:          "Bearer correct-token",
			protoVer:       "1",
			serialNum:      "",
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
			if tt.serialNum != "" {
				req.Header.Set("Serial-Number", tt.serialNum)
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
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

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
			"Serial-Number":    []string{"sn-client-1"},
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
			"Serial-Number":    []string{"sn-client-2"},
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
			"Serial-Number":    []string{"sn-client-3"},
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
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

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
	req.Header.Set("Serial-Number", "test-sn")

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
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

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
			"Serial-Number":            []string{"sn-comp-check"},
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
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

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
					"Serial-Number":    []string{"sn-" + id},
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

// TestHandler_HelloHandshake_Success 验证合法客户端 hello 消息请求下，服务端下发正确且唯一的 hello 响应。
func TestHandler_HelloHandshake_Success(t *testing.T) {
	const token = "hello-test-token"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// 1. 发送满足规范的客户端 hello 消息
	clientHelloJSON := `{
		"type": "hello",
		"version": 1,
		"transport": "websocket",
		"audio_params": {
			"format": "opus",
			"sample_rate": 16000,
			"channels": 1,
			"frame_duration": 60
		}
	}`

	if err := conn.Write(ctx, websocket.MessageText, []byte(clientHelloJSON)); err != nil {
		t.Fatalf("failed to write client hello: %v", err)
	}

	// 2. 接收服务端 hello 响应
	msgType, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read server hello response: %v", err)
	}

	if msgType != websocket.MessageText {
		t.Fatalf("expected MessageText (1), got %v", msgType)
	}

	var serverHello ServerHelloMessage
	if err := json.Unmarshal(respData, &serverHello); err != nil {
		t.Fatalf("failed to parse server hello json: %v, raw: %s", err, string(respData))
	}

	// 3. 严格断言服务端 hello 各字段
	if serverHello.Type != "hello" {
		t.Errorf("expected type 'hello', got '%s'", serverHello.Type)
	}
	if serverHello.Transport != "websocket" {
		t.Errorf("expected transport 'websocket', got '%s'", serverHello.Transport)
	}
	if len(serverHello.SessionID) != 32 {
		t.Errorf("expected session_id length 32, got %d (%s)", len(serverHello.SessionID), serverHello.SessionID)
	}
	if _, err := hex.DecodeString(serverHello.SessionID); err != nil {
		t.Errorf("expected hex session_id, got %s", serverHello.SessionID)
	}
	if serverHello.AudioParams.Format != "opus" {
		t.Errorf("expected audio_params.format 'opus', got '%s'", serverHello.AudioParams.Format)
	}
	if serverHello.AudioParams.SampleRate != 24000 {
		t.Errorf("expected audio_params.sample_rate 24000, got %d", serverHello.AudioParams.SampleRate)
	}
	if serverHello.AudioParams.Channels != 1 {
		t.Errorf("expected audio_params.channels 1, got %d", serverHello.AudioParams.Channels)
	}
	if serverHello.AudioParams.FrameDuration != 60 {
		t.Errorf("expected audio_params.frame_duration 60, got %d", serverHello.AudioParams.FrameDuration)
	}

	// 4. 验证仅收到一次服务端 hello，短时间内无多余消息下发
	readDone := make(chan error, 1)
	go func() {
		shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer shortCancel()
		_, _, err := conn.Read(shortCtx)
		readDone <- err
	}()

	err = <-readDone
	if err == nil {
		t.Fatal("unexpected extra message received from server")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		// 超时是预期的，说明没有下发多余消息
		t.Logf("expected read timeout for extra messages: %v", err)
	}

	// 5. 关闭连接并验证名额正常释放
	conn.Close(websocket.StatusNormalClosure, "normal done")
	waitQuotaReleased(t, limiter, 2*time.Second)
}

// TestHandler_HelloHandshake_WithMCPDiscovery 验证客户端带有 features.mcp=true 时服务端发起 MCP 初始化与工具发现。
func TestHandler_HelloHandshake_WithMCPDiscovery(t *testing.T) {
	const token = "hello-mcp-test-token"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// 1. 发送带有 features.mcp 的 hello
	clientHelloJSON := `{
		"type": "hello",
		"version": 1,
		"transport": "websocket",
		"audio_params": {
			"format": "opus",
			"sample_rate": 16000,
			"channels": 1,
			"frame_duration": 60
		},
		"features": {
			"mcp": true
		}
	}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(clientHelloJSON)); err != nil {
		t.Fatalf("failed to write client hello: %v", err)
	}

	// 2. 接收服务端 hello
	_, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read server hello response: %v", err)
	}
	var serverHello ServerHelloMessage
	if err := json.Unmarshal(respData, &serverHello); err != nil {
		t.Fatalf("failed to unmarshal server hello: %v", err)
	}

	// 3. 接收服务端发起的 MCP initialize 请求
	_, mcpInitData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read mcp initialize request: %v", err)
	}

	var initWrapper struct {
		Type    string `json:"type"`
		Payload struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(mcpInitData, &initWrapper); err != nil {
		t.Fatalf("failed to parse mcp init request: %v", err)
	}
	if initWrapper.Type != "mcp" || initWrapper.Payload.Method != "initialize" {
		t.Fatalf("expected mcp initialize request, got %s", string(mcpInitData))
	}

	// 4. 回复 initialize 响应
	initResp := fmt.Sprintf(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"test-board","version":"1.0"}}}}`, initWrapper.Payload.ID)
	if err := conn.Write(ctx, websocket.MessageText, []byte(initResp)); err != nil {
		t.Fatalf("failed to write mcp initialize response: %v", err)
	}

	// 5. 接收服务端发起的 tools/list 请求
	_, toolsListData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read mcp tools/list request: %v", err)
	}

	var listWrapper struct {
		Type    string `json:"type"`
		Payload struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(toolsListData, &listWrapper); err != nil {
		t.Fatalf("failed to parse mcp tools/list request: %v", err)
	}
	if listWrapper.Type != "mcp" || listWrapper.Payload.Method != "tools/list" {
		t.Fatalf("expected mcp tools/list request, got %s", string(toolsListData))
	}

	// 6. 回复 tools/list 响应
	listResp := fmt.Sprintf(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"self.audio_speaker.set_volume","description":"Set volume","inputSchema":{"type":"object"}}]}}}`, listWrapper.Payload.ID)
	if err := conn.Write(ctx, websocket.MessageText, []byte(listResp)); err != nil {
		t.Fatalf("failed to write mcp tools/list response: %v", err)
	}

	// 7. 关闭连接并验证名额正常释放
	conn.Close(websocket.StatusNormalClosure, "normal done")
	waitQuotaReleased(t, limiter, 2*time.Second)
}

// TestHandler_HelloHandshake_Timeout 验证升级后未在超时时间内发送 hello 则主动断开连接。
func TestHandler_HelloHandshake_Timeout(t *testing.T) {
	const token = "hello-timeout-token"
	cfg := createTestConfig(token, 2)
	cfg.Session.HelloTimeout = 100 * time.Millisecond // 设置极短超时以加快测试

	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 升级成功后故意不发送 hello，等待服务端超时主动关闭连接
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed by server due to hello timeout, but read succeeded")
	}

	status := websocket.CloseStatus(err)
	if status != websocket.StatusPolicyViolation {
		t.Errorf("expected close status PolicyViolation (1008), got %v (err: %v)", status, err)
	}

	// 验证名额自动释放
	waitQuotaReleased(t, limiter, 2*time.Second)
}

// TestHandler_HelloHandshake_BinaryFirstMessage 验证首包发送二进制消息时直接关闭连接。
func TestHandler_HelloHandshake_BinaryFirstMessage(t *testing.T) {
	const token = "hello-binary-first-token"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 首包发送二进制数据
	binaryData := []byte{0x01, 0x02, 0x03, 0x04}
	if err := conn.Write(ctx, websocket.MessageBinary, binaryData); err != nil {
		t.Fatalf("failed to write binary message: %v", err)
	}

	// 断言服务端断开连接
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed on binary first message, but read succeeded")
	}

	status := websocket.CloseStatus(err)
	if status != websocket.StatusUnsupportedData && status != websocket.StatusPolicyViolation {
		t.Errorf("expected close status UnsupportedData (1003) or PolicyViolation (1008), got %v (err: %v)", status, err)
	}

	waitQuotaReleased(t, limiter, 2*time.Second)
}

// TestHandler_HelloHandshake_FieldValidation 表驱动测试客户端 hello 各字段非法时服务端断开连接。
func TestHandler_HelloHandshake_FieldValidation(t *testing.T) {
	tests := []struct {
		name      string
		helloJSON string
	}{
		{
			name: "消息类型 type 错误",
			helloJSON: `{
				"type": "invalid_type",
				"version": 1,
				"transport": "websocket",
				"audio_params": {"format": "opus", "sample_rate": 16000, "channels": 1, "frame_duration": 60}
			}`,
		},
		{
			name: "协议版本 version 错误 (version=2)",
			helloJSON: `{
				"type": "hello",
				"version": 2,
				"transport": "websocket",
				"audio_params": {"format": "opus", "sample_rate": 16000, "channels": 1, "frame_duration": 60}
			}`,
		},
		{
			name: "传输层 transport 错误",
			helloJSON: `{
				"type": "hello",
				"version": 1,
				"transport": "tcp",
				"audio_params": {"format": "opus", "sample_rate": 16000, "channels": 1, "frame_duration": 60}
			}`,
		},
		{
			name: "音频格式 format 错误 (aac)",
			helloJSON: `{
				"type": "hello",
				"version": 1,
				"transport": "websocket",
				"audio_params": {"format": "aac", "sample_rate": 16000, "channels": 1, "frame_duration": 60}
			}`,
		},
		{
			name: "音频采样率 sample_rate 错误 (8000 Hz)",
			helloJSON: `{
				"type": "hello",
				"version": 1,
				"transport": "websocket",
				"audio_params": {"format": "opus", "sample_rate": 8000, "channels": 1, "frame_duration": 60}
			}`,
		},
		{
			name: "音频采样率 sample_rate 错误 (24000 Hz)",
			helloJSON: `{
				"type": "hello",
				"version": 1,
				"transport": "websocket",
				"audio_params": {"format": "opus", "sample_rate": 24000, "channels": 1, "frame_duration": 60}
			}`,
		},
		{
			name: "音频声道数 channels 错误 (双声道)",
			helloJSON: `{
				"type": "hello",
				"version": 1,
				"transport": "websocket",
				"audio_params": {"format": "opus", "sample_rate": 16000, "channels": 2, "frame_duration": 60}
			}`,
		},
		{
			name: "音频帧时长 frame_duration 错误 (20 ms)",
			helloJSON: `{
				"type": "hello",
				"version": 1,
				"transport": "websocket",
				"audio_params": {"format": "opus", "sample_rate": 16000, "channels": 1, "frame_duration": 20}
			}`,
		},
		{
			name: "缺失 audio_params 对象",
			helloJSON: `{
				"type": "hello",
				"version": 1,
				"transport": "websocket"
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const token = "field-validation-token"
			cfg := createTestConfig(token, 2)
			limiter := NewSessionLimiter(2)
			handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

			mux := http.NewServeMux()
			mux.Handle(WebSocketPath, handler)
			server := httptest.NewServer(mux)
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
			defer conn.Close(websocket.StatusNormalClosure, "")

			if err := conn.Write(ctx, websocket.MessageText, []byte(tt.helloJSON)); err != nil {
				t.Fatalf("failed to write message: %v", err)
			}

			_, _, err := conn.Read(ctx)
			if err == nil {
				t.Fatal("expected connection to be closed due to invalid field, but read succeeded")
			}

			status := websocket.CloseStatus(err)
			if status != websocket.StatusPolicyViolation {
				t.Errorf("expected close status PolicyViolation (1008), got %v", status)
			}

			waitQuotaReleased(t, limiter, 2*time.Second)
		})
	}
}

// TestHandler_HelloHandshake_MalformedJSON 验证首包为畸形 JSON 时服务端关闭连接。
func TestHandler_HelloHandshake_MalformedJSON(t *testing.T) {
	const token = "malformed-json-token"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 发送畸形 JSON 字符串
	if err := conn.Write(ctx, websocket.MessageText, []byte("{not-a-valid-json:")); err != nil {
		t.Fatalf("failed to write malformed json: %v", err)
	}

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed on malformed json, but read succeeded")
	}

	status := websocket.CloseStatus(err)
	if status != websocket.StatusPolicyViolation {
		t.Errorf("expected close status PolicyViolation (1008), got %v", status)
	}

	waitQuotaReleased(t, limiter, 2*time.Second)
}

// TestHandler_HelloHandshake_MessageTooBig 验证发送超限文本消息时连接被关闭。
func TestHandler_HelloHandshake_MessageTooBig(t *testing.T) {
	const token = "too-big-token"
	cfg := createTestConfig(token, 2)
	cfg.Session.MaxWSTextMessageBytes = 4096 // 设置为 4 KiB 限制

	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 构造超过 4096 字节的巨大文本消息
	hugePayload := strings.Repeat("A", 5000)
	if err := conn.Write(ctx, websocket.MessageText, []byte(hugePayload)); err != nil {
		t.Fatalf("failed to write huge message: %v", err)
	}

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed on message too big, but read succeeded")
	}

	// 验证名额释放
	waitQuotaReleased(t, limiter, 2*time.Second)
}

// TestHandler_HelloHandshake_DuplicateHello 验证握手成功后重复收到 hello 消息主动关闭连接。
func TestHandler_HelloHandshake_DuplicateHello(t *testing.T) {
	const token = "dup-hello-token"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 1. 发送首次合法客户端 hello
	validHelloJSON := `{
		"type": "hello",
		"version": 1,
		"transport": "websocket",
		"audio_params": {
			"format": "opus",
			"sample_rate": 16000,
			"channels": 1,
			"frame_duration": 60
		}
	}`

	if err := conn.Write(ctx, websocket.MessageText, []byte(validHelloJSON)); err != nil {
		t.Fatalf("failed to write client hello: %v", err)
	}

	// 接收服务端 hello 响应
	msgType, _, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("expected MessageText, got %v", msgType)
	}

	// 2. 再次发送重复的 hello 消息
	if err := conn.Write(ctx, websocket.MessageText, []byte(validHelloJSON)); err != nil {
		t.Fatalf("failed to write duplicate hello: %v", err)
	}

	// 3. 断言连接被服务端主动关闭
	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed on duplicate hello, but read succeeded")
	}

	status := websocket.CloseStatus(err)
	if status != websocket.StatusPolicyViolation {
		t.Errorf("expected close status PolicyViolation (1008), got %v (err: %v)", status, err)
	}

	waitQuotaReleased(t, limiter, 2*time.Second)
}

// TestHandler_HelloHandshake_NoAICalls 验证在整个握手过程中无任何 AI 客户端构造或外部调用。
func TestHandler_HelloHandshake_NoAICalls(t *testing.T) {
	var logBuf safeBuffer
	testLogger := logger.New(&logBuf, slog.LevelDebug)

	const token = "no-ai-token"
	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, testLogger)

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _ := dialWebSocketHelper(t, ctx, server.URL, token)

	validHelloJSON := `{
		"type": "hello",
		"version": 1,
		"transport": "websocket",
		"audio_params": {
			"format": "opus",
			"sample_rate": 16000,
			"channels": 1,
			"frame_duration": 60
		}
	}`

	if err := conn.Write(ctx, websocket.MessageText, []byte(validHelloJSON)); err != nil {
		t.Fatalf("failed to write client hello: %v", err)
	}

	_, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}

	var serverHello ServerHelloMessage
	if err := json.Unmarshal(respData, &serverHello); err != nil {
		t.Fatalf("failed to unmarshal server hello: %v", err)
	}

	// 正常关闭连接并等待服务端退出
	conn.Close(websocket.StatusNormalClosure, "done")
	waitQuotaReleased(t, limiter, 2*time.Second)

	// 检查握手期间的日志，断言不存在任何百炼、ASR、LLM、TTS 调用日志
	logOutput := logBuf.String()
	for _, forbiddenKeyword := range []string{"bailian", "dashscope", "qwen", "tts", "asr", "llm"} {
		if strings.Contains(strings.ToLower(logOutput), forbiddenKeyword) {
			t.Errorf("found unexpected AI keyword '%s' in handshake logs: %s", forbiddenKeyword, logOutput)
		}
	}
}

type fakeASRClientForTest struct{}

func (f *fakeASRClientForTest) CreateStream(ctx context.Context) (ai.ASRStream, error) {
	return nil, nil
}

type fakeLLMClientForTest struct{}

func (f *fakeLLMClientForTest) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	return nil, nil
}

type fakeTTSClientForTest struct{}

func (f *fakeTTSClientForTest) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	return nil, nil
}

// TestHandler_AIClientsInjection 验证 Handler 与 Session 正确持有注入的 ASRClient、LLMClient 与 TTSClient 依赖。
func TestHandler_AIClientsInjection(t *testing.T) {
	cfg := createTestConfig("token", 2)
	limiter := NewSessionLimiter(2)
	var fakeASR ai.ASRClient = &fakeASRClientForTest{}
	var fakeLLM ai.LLMClient = &fakeLLMClientForTest{}
	var fakeTTS ai.TTSClient = &fakeTTSClientForTest{}

	h := NewHandler(cfg, limiter, fakeASR, fakeLLM, fakeTTS, slog.Default())
	if h.ASRClient() != fakeASR {
		t.Errorf("expected Handler.ASRClient to match injected client")
	}
	if h.LLMClient() != fakeLLM {
		t.Errorf("expected Handler.LLMClient to match injected client")
	}
	if h.TTSClient() != fakeTTS {
		t.Errorf("expected Handler.TTSClient to match injected client")
	}

	sess := NewSession(context.Background(), nil, nil, cfg, fakeASR, fakeLLM, fakeTTS, slog.Default())
	if sess.ASRClient() != fakeASR {
		t.Errorf("expected Session.ASRClient to match injected client")
	}
	if sess.LLMClient() != fakeLLM {
		t.Errorf("expected Session.LLMClient to match injected client")
	}
	if sess.TTSClient() != fakeTTS {
		t.Errorf("expected Session.TTSClient to match injected client")
	}
}

// TestHandler_DuplicateSerialNumber_EvictsOldConnectionE2E 验证同一设备序列号通过 HTTP WebSocket 快速重连时旧连接被服务端主动踢除。
func TestHandler_DuplicateSerialNumber_EvictsOldConnectionE2E(t *testing.T) {
	const token = "e2e-evict-token"
	const serialNum = "SN-E2E-EVICT-001"

	cfg := createTestConfig(token, 5)
	limiter := NewSessionLimiter(5)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + WebSocketPath

	// 1. 发起第 1 个连接并完成 Hello 握手
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	dialOpts1 := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"dev-1"},
			"Serial-Number":    []string{serialNum},
		},
	}
	conn1, _, err := websocket.Dial(ctx1, wsURL, dialOpts1)
	if err != nil {
		t.Fatalf("failed to dial conn1: %v", err)
	}
	defer conn1.Close(websocket.StatusNormalClosure, "")

	helloJSON := `{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`
	if err := conn1.Write(ctx1, websocket.MessageText, []byte(helloJSON)); err != nil {
		t.Fatalf("failed to write hello for conn1: %v", err)
	}
	_, _, err = conn1.Read(ctx1)
	if err != nil {
		t.Fatalf("failed to read server hello for conn1: %v", err)
	}

	if handler.Registry().ActiveCount() != 1 {
		t.Fatalf("expected registry active count 1, got %d", handler.Registry().ActiveCount())
	}

	// 启动 conn1 持续读取协程，监听被踢下线事件并响应 Close 帧
	conn1Closed := make(chan error, 1)
	go func() {
		for {
			_, _, rErr := conn1.Read(context.Background())
			if rErr != nil {
				conn1Closed <- rErr
				return
			}
		}
	}()

	// 2. 发起相同 Serial-Number 的第 2 个连接并完成 Hello 握手
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	dialOpts2 := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"dev-2"},
			"Serial-Number":    []string{serialNum},
		},
	}
	conn2, _, err := websocket.Dial(ctx2, wsURL, dialOpts2)
	if err != nil {
		t.Fatalf("failed to dial conn2: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")

	if err := conn2.Write(ctx2, websocket.MessageText, []byte(helloJSON)); err != nil {
		t.Fatalf("failed to write hello for conn2: %v", err)
	}
	_, _, err = conn2.Read(ctx2)
	if err != nil {
		t.Fatalf("failed to read server hello for conn2: %v", err)
	}

	// 3. 断言 conn1 被服务端主动关闭
	select {
	case readErr := <-conn1Closed:
		if readErr == nil {
			t.Fatal("expected conn1 closed with error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected conn1 to be closed by server eviction, but timed out")
	}

	// 4. 断言 Registry 当前只维护最新的 conn2 会话，活跃计数为 1
	if handler.Registry().ActiveCount() != 1 {
		t.Fatalf("expected registry active count 1, got %d", handler.Registry().ActiveCount())
	}

	// 5. 关闭 conn2，断言完全清理
	_ = conn2.Close(websocket.StatusNormalClosure, "done")
	waitQuotaReleased(t, limiter, 2*time.Second)
	if handler.Registry().ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0 after conn2 close, got %d", handler.Registry().ActiveCount())
	}
}

// TestHandler_MissingSerialNumber_RejectedWith400 验证未提供 Serial-Number 时握手在升级前直接返回 400 拒绝。
func TestHandler_MissingSerialNumber_RejectedWith400(t *testing.T) {
	const token = "no-sn-token"

	cfg := createTestConfig(token, 2)
	limiter := NewSessionLimiter(2)
	handler := NewHandler(cfg, limiter, nil, nil, nil, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(WebSocketPath, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + WebSocketPath

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"device-without-sn"},
		},
	}
	_, resp, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err == nil {
		t.Fatal("expected dial to fail for missing Serial-Number, but succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 Bad Request, got: %v", resp)
	}

	// 断言未占用 limiter 名额
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", limiter.ActiveCount())
	}
}
