package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/server"
	"xiaozhi-esp32-golang-server/internal/session"
)

func createServerTestConfig(maxSessions int, shutdownTimeout time.Duration) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            "127.0.0.1:0",
			WebSocketURL:          "ws://127.0.0.1:8080/xiaozhi/v1/",
			MaxConcurrentSessions: maxSessions,
			ShutdownTimeout:       shutdownTimeout,
			HTTPReadTimeout:       5 * time.Second,
			HTTPWriteTimeout:      5 * time.Second,
			HTTPIdleTimeout:       10 * time.Second,
			MaxHTTPBodyBytes:      65536,
			MaxHTTPHeaderBytes:    1024,
		},
		Session: config.SessionConfig{
			HelloTimeout:              10 * time.Second,
			MaxWSTextMessageBytes:     32768,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      30 * time.Second,
			ASRPCMQueueCapacity:       50,
			TTSPCMQueueCapacity:       50,
			DownlinkOpusQueueCapacity: 50,
			MaxHistoryTurns:           6,
		},
		DeviceSharedToken: "test-device-token",
	}
}

func dialTestWebSocket(t *testing.T, ctx context.Context, addr, token, deviceID string) *websocket.Conn {
	t.Helper()
	wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{deviceID},
			"Client-Id":        []string{"client-" + deviceID},
			"Serial-Number":    []string{"sn-" + deviceID},
		},
	}
	conn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	return conn
}

func performClientHello(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	helloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	if err := conn.Write(ctx, websocket.MessageText, helloJSON); err != nil {
		t.Fatalf("failed to send client hello: %v", err)
	}

	typ, respBytes, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text message from server hello, got %v", typ)
	}

	var parsed map[string]any
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal server hello: %v", err)
	}
	if parsed["type"] != "hello" {
		t.Fatalf("expected type 'hello', got %v", parsed["type"])
	}
}

// TestServer_Shutdown_WithActiveWebSockets 验证服务停机时活跃 WebSocket 连接被正确取消且名额精确归零。
func TestServer_Shutdown_WithActiveWebSockets(t *testing.T) {
	cfg := createServerTestConfig(10, 2*time.Second)
	limiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	registry := session.NewRegistry(limiter, nil)
	wsHandler := session.NewHandlerWithRegistry(cfg, registry, nil, nil, nil, nil)

	mux := http.NewServeMux()
	mux.Handle(session.WebSocketPath, wsHandler)

	srv := server.New(cfg.Server, mux)
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		return registry.Shutdown(shutdownCtx)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	// 建立 3 个活跃 WebSocket 连接并完成握手，同时启动客户端读取循环
	sessionCount := 3
	conns := make([]*websocket.Conn, sessionCount)
	clientCloseErrs := make(chan error, sessionCount)
	for i := 0; i < sessionCount; i++ {
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn := dialTestWebSocket(t, dialCtx, addr, cfg.DeviceSharedToken, fmt.Sprintf("dev-%d", i))
		performClientHello(t, dialCtx, conn)
		dialCancel()
		conns[i] = conn

		go func(c *websocket.Conn) {
			for {
				_, _, err := c.Read(context.Background())
				if err != nil {
					clientCloseErrs <- err
					return
				}
			}
		}(conn)
	}

	// 验证所有会话均在注册表中激活
	if registry.ActiveCount() != sessionCount {
		t.Fatalf("expected %d registered active sessions, got %d", sessionCount, registry.ActiveCount())
	}
	if limiter.ActiveCount() != sessionCount {
		t.Fatalf("expected %d limiter active sessions, got %d", sessionCount, limiter.ActiveCount())
	}

	// 触发优雅关闭
	cancel()

	// 验证服务端在宽限期内优雅退出
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("server exited with unexpected error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to exit within shutdown timeout")
	}

	// 验证所有客户端连接均已收到服务端关闭信号
	for i := 0; i < sessionCount; i++ {
		select {
		case err := <-clientCloseErrs:
			if err == nil {
				t.Errorf("connection %d expected close error, got nil", i)
			}
		case <-time.After(1 * time.Second):
			t.Errorf("connection %d timed out waiting for close error", i)
		}
		_ = conns[i].Close(websocket.StatusNormalClosure, "")
	}

	// 验证活跃会话与名额精确归零
	if registry.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", limiter.ActiveCount())
	}
}

// TestServer_Shutdown_DuringActiveConversation 验证在会话收音交互中停服时连接优雅断开且资源释放。
func TestServer_Shutdown_DuringActiveConversation(t *testing.T) {
	cfg := createServerTestConfig(5, 2*time.Second)
	limiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	registry := session.NewRegistry(limiter, nil)
	wsHandler := session.NewHandlerWithRegistry(cfg, registry, nil, nil, nil, nil)

	mux := http.NewServeMux()
	mux.Handle(session.WebSocketPath, wsHandler)

	srv := server.New(cfg.Server, mux)
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		return registry.Shutdown(shutdownCtx)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	conn := dialTestWebSocket(t, dialCtx, addr, cfg.DeviceSharedToken, "dev-conv")
	performClientHello(t, dialCtx, conn)
	dialCancel()

	clientCloseErr := make(chan error, 1)
	go func() {
		for {
			_, _, err := conn.Read(context.Background())
			if err != nil {
				clientCloseErr <- err
				return
			}
		}
	}()

	// 发送 listen.start 进入收音状态
	listenMsg := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenMsg); err != nil {
		t.Fatalf("failed to send listen start: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	// 触发停服
	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("server exited with unexpected error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to exit within shutdown timeout")
	}

	select {
	case err := <-clientCloseErr:
		if err == nil {
			t.Error("expected client to receive close error, got nil")
		}
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for client close error")
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")

	if registry.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", limiter.ActiveCount())
	}
}

// TestServer_Shutdown_TimeoutBranchWithHooks 验证清理钩子超时时返回 DeadlineExceeded。
func TestServer_Shutdown_TimeoutBranchWithHooks(t *testing.T) {
	cfg := createServerTestConfig(5, 100*time.Millisecond)
	limiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	registry := session.NewRegistry(limiter, nil)
	wsHandler := session.NewHandlerWithRegistry(cfg, registry, nil, nil, nil, nil)

	mux := http.NewServeMux()
	mux.Handle(session.WebSocketPath, wsHandler)

	srv := server.New(cfg.Server, mux)

	// 注册一个耗时超过 100ms 的钩子
	blockCh := make(chan struct{})
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		select {
		case <-shutdownCtx.Done():
			return shutdownCtx.Err()
		case <-blockCh:
			return nil
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	_ = waitForServerReady(t, srv, 2*time.Second)

	// 触发关闭
	cancel()

	select {
	case runErr := <-errCh:
		if runErr == nil {
			t.Fatal("expected error on shutdown timeout, got nil")
		}
		if !errors.Is(runErr, context.DeadlineExceeded) {
			t.Fatalf("expected DeadlineExceeded error, got: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to return within timeout")
	}

	close(blockCh)
}

// TestServer_UpgradeFailure_QuotaReleased 验证握手认证失败时准入名额立即释放。
func TestServer_UpgradeFailure_QuotaReleased(t *testing.T) {
	cfg := createServerTestConfig(1, 2*time.Second)
	limiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	registry := session.NewRegistry(limiter, nil)
	wsHandler := session.NewHandlerWithRegistry(cfg, registry, nil, nil, nil, nil)

	mux := http.NewServeMux()
	mux.Handle(session.WebSocketPath, wsHandler)

	srv := server.New(cfg.Server, mux)
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		return registry.Shutdown(shutdownCtx)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	// 1. 发送错误 Token 请求，应被认证拒绝 (401)
	wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer invalid-token"},
			"Protocol-Version": []string{"1"},
			"Serial-Number":    []string{"sn-invalid-token"},
		},
	}
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	conn, resp, err := websocket.Dial(dialCtx, wsURL, dialOpts)
	dialCancel()
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected dial to fail with invalid token, got success")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", resp.StatusCode)
	}

	// 2. 验证名额未被占用
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0 after auth rejection, got %d", limiter.ActiveCount())
	}
	if registry.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0 after auth rejection, got %d", registry.ActiveCount())
	}

	// 3. 发送合法连接验证可成功准入与握手
	validDialCtx, validDialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	validConn := dialTestWebSocket(t, validDialCtx, addr, cfg.DeviceSharedToken, "dev-valid")
	performClientHello(t, validDialCtx, validConn)
	validDialCancel()

	if limiter.ActiveCount() != 1 {
		t.Fatalf("expected limiter active count 1, got %d", limiter.ActiveCount())
	}
	_ = validConn.Close(websocket.StatusNormalClosure, "")

	// 退出服务
	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("server exited with error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to exit")
	}
}

// TestServer_HighConcurrency_RegistrationAndShutdown 验证高并发建连与停服竞态安全。
func TestServer_HighConcurrency_RegistrationAndShutdown(t *testing.T) {
	cfg := createServerTestConfig(10, 2*time.Second)
	limiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	registry := session.NewRegistry(limiter, nil)
	wsHandler := session.NewHandlerWithRegistry(cfg, registry, nil, nil, nil, nil)

	mux := http.NewServeMux()
	mux.Handle(session.WebSocketPath, wsHandler)

	srv := server.New(cfg.Server, mux)
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		return registry.Shutdown(shutdownCtx)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	concurrency := 15
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			dialCtx, dialCancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer dialCancel()

			wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)
			dialOpts := &websocket.DialOptions{
				HTTPHeader: http.Header{
					"Authorization":    []string{"Bearer " + cfg.DeviceSharedToken},
					"Protocol-Version": []string{"1"},
					"Device-Id":        []string{fmt.Sprintf("dev-conc-%d", idx)},
					"Serial-Number":    []string{fmt.Sprintf("sn-conc-%d", idx)},
				},
			}
			conn, _, err := websocket.Dial(dialCtx, wsURL, dialOpts)
			if err != nil {
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "")

			helloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
			_ = conn.Write(dialCtx, websocket.MessageText, helloJSON)
			_, _, _ = conn.Read(dialCtx)
			time.Sleep(10 * time.Millisecond)
		}(i)
	}

	// 在并发建连期间触发停服
	time.Sleep(25 * time.Millisecond)
	cancel()

	wg.Wait()

	select {
	case runErr := <-errCh:
		if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) {
			t.Fatalf("server exited with unexpected error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to exit within timeout")
	}

	if registry.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", limiter.ActiveCount())
	}
}
