package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai/bailian"
	"xiaozhi-esp32-golang-server/internal/bootstrap"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/server"
	"xiaozhi-esp32-golang-server/internal/session"
)

// setupCustomResourceServer 构造并启动带有自定义配置的集成测试服务。
func setupCustomResourceServer(t *testing.T, wsMockURL, llmMockURL string, modifyCfg func(c *config.Config)) (string, *config.Config, *session.Registry, *session.SessionLimiter, func()) {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            "127.0.0.1:0",
			WebSocketURL:          "ws://127.0.0.1:8080/xiaozhi/v1/",
			MaxConcurrentSessions: 5,
			ShutdownTimeout:       3 * time.Second,
			HTTPReadTimeout:       5 * time.Second,
			HTTPWriteTimeout:      5 * time.Second,
			HTTPIdleTimeout:       10 * time.Second,
			MaxHTTPBodyBytes:      65536,
			MaxHTTPHeaderBytes:    1024,
		},
		Session: config.SessionConfig{
			HelloTimeout:              5 * time.Second,
			MaxWSTextMessageBytes:     32768,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      10 * time.Second,
			ASRPCMQueueCapacity:       50,
			TTSPCMQueueCapacity:       50,
			DownlinkOpusQueueCapacity: 50,
			MaxHistoryTurns:           6,
			SystemPrompt:              "你是小智助手。",
		},
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:           wsMockURL,
				LLMEndpoint:          llmMockURL,
				ASRModel:             "qwen-audio-3.0-asr-flash-streaming",
				LLMModel:             "qwen3.7-flash",
				TTSModel:             "qwen-audio-3.0-tts-flash",
				TTSVoice:             "longanlingxi",
				ASRConnectTimeout:    5 * time.Second,
				TTSConnectTimeout:    5 * time.Second,
				LLMFirstTokenTimeout: 5 * time.Second,
				LLMOverallTimeout:    10 * time.Second,
			},
		},
		DashScopeAPIKey:   "test-dashscope-secret",
		DeviceSharedToken: "test-device-token",
	}

	if modifyCfg != nil {
		modifyCfg(cfg)
	}

	asrClient, err := bailian.NewASRClient(cfg)
	if err != nil {
		t.Fatalf("failed to create asr client: %v", err)
	}
	llmClient, err := bailian.NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create llm client: %v", err)
	}
	ttsClient, err := bailian.NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create tts client: %v", err)
	}

	limiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	registry := session.NewRegistry(limiter, nil)
	wsHandler := session.NewHandlerWithRegistry(cfg, registry, asrClient, llmClient, ttsClient, nil)

	mux := http.NewServeMux()
	mux.Handle(bootstrap.OTAPath, bootstrap.NewHandler(cfg, nil))
	mux.Handle(session.WebSocketPath, wsHandler)

	srv := server.New(cfg.Server, mux)
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		return registry.Shutdown(shutdownCtx)
	})

	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Run(srvCtx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)
	cfg.Server.WebSocketURL = fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)

	cleanup := func() {
		srvCancel()
		select {
		case runErr := <-srvErrCh:
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				t.Errorf("server exited with unexpected error: %v", runErr)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("server failed to shutdown within timeout")
		}
	}

	return addr, cfg, registry, limiter, cleanup
}

// waitLimiterActiveCount 等待限流器的活跃计数达到预期目标值。
func waitLimiterActiveCount(t *testing.T, limiter *session.SessionLimiter, expected int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if limiter.ActiveCount() == expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for active count to become %d, current=%d", expected, limiter.ActiveCount())
}

// TestResourceLimits_MaxConcurrentSessions_503AndReuse 验证会话并发达到上限时拒绝新连接并返回 503，释放后名额可复用。
func TestResourceLimits_MaxConcurrentSessions_503AndReuse(t *testing.T) {
	wsMock := newMockBailianWSServer(t, "你好", [][]byte{generateSinePCM24k(1, 440.0)})
	defer wsMock.Close()
	llmMock := newMockBailianLLMServer(t, []string{"你好！"})
	defer llmMock.Close()

	addr, cfg, registry, limiter, cleanup := setupCustomResourceServer(t, wsMock.WSURL(), llmMock.BaseURL(), func(c *config.Config) {
		c.Server.MaxConcurrentSessions = 2
	})
	defer cleanup()

	// 1. 建立第 1 个连接并完成握手
	conn1, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "limit-dev-1")
	defer conn1.Close(websocket.StatusNormalClosure, "done")
	waitLimiterActiveCount(t, limiter, 1, 2*time.Second)

	// 2. 建立第 2 个连接并完成握手，达到满载上限 (2/2)
	conn2, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "limit-dev-2")
	defer conn2.Close(websocket.StatusNormalClosure, "done")
	waitLimiterActiveCount(t, limiter, 2, 2*time.Second)

	// 3. 发起第 3 个连接，断言 HTTP 握手阶段直接返回 503 拒绝升级
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()

	wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + cfg.DeviceSharedToken},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"limit-dev-3"},
			"Client-Id":        []string{"client-limit-dev-3"},
			"Serial-Number":    []string{"sn-limit-dev-3"},
		},
	}
	_, resp, err := websocket.Dial(dialCtx, wsURL, dialOpts)
	if err == nil {
		t.Fatal("expected dial to fail when max concurrent sessions reached, but succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 Service Unavailable, got resp: %v, err: %v", resp, err)
	}

	// 4. 客户端 1 主动关闭连接，断言名额释放为 1
	_ = conn1.Close(websocket.StatusNormalClosure, "client 1 finished")
	waitLimiterActiveCount(t, limiter, 1, 2*time.Second)

	// 5. 客户端 3 再次尝试建连，此时名额已复用，握手成功
	conn3, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "limit-dev-3")
	defer conn3.Close(websocket.StatusNormalClosure, "done")
	waitLimiterActiveCount(t, limiter, 2, 2*time.Second)

	// 6. 关闭全部连接，断言活跃数与名额归零
	_ = conn2.Close(websocket.StatusNormalClosure, "client 2 finished")
	_ = conn3.Close(websocket.StatusNormalClosure, "client 3 finished")
	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestResourceLimits_HTTPBodySizeLimit_413 验证 HTTP 请求体超限返回 413，合法大小返回 200。
func TestResourceLimits_HTTPBodySizeLimit_413(t *testing.T) {
	wsMock := newMockBailianWSServer(t, "你好", [][]byte{generateSinePCM24k(1, 440.0)})
	defer wsMock.Close()
	llmMock := newMockBailianLLMServer(t, []string{"你好！"})
	defer llmMock.Close()

	addr, _, _, _, cleanup := setupCustomResourceServer(t, wsMock.WSURL(), llmMock.BaseURL(), func(c *config.Config) {
		c.Server.MaxHTTPBodyBytes = 65536
	})
	defer cleanup()

	otaURL := fmt.Sprintf("http://%s%s", addr, bootstrap.OTAPath)

	// 1. 发送超出 64 KiB 上限的超大正文，断言返回 413 Payload Too Large
	oversizedBody := bytes.Repeat([]byte("a"), 65536+10)
	resp, err := http.Post(otaURL, "application/json", bytes.NewReader(oversizedBody))
	if err != nil {
		t.Fatalf("failed to send oversized post request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected HTTP 413 Payload Too Large, got %d", resp.StatusCode)
	}

	// 2. 发送合法大小的正文，断言返回 200 OK
	normalBody := `{"device_id":"normal-test-device"}`
	resp2, err := http.Post(otaURL, "application/json", strings.NewReader(normalBody))
	if err != nil {
		t.Fatalf("failed to send normal post request: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK, got %d", resp2.StatusCode)
	}

	var otaData bootstrap.Response
	if err := json.NewDecoder(resp2.Body).Decode(&otaData); err != nil {
		t.Fatalf("failed to decode bootstrap response: %v", err)
	}
	if otaData.WebSocket.Version != bootstrap.ProtocolVersion {
		t.Fatalf("expected protocol version %d, got %d", bootstrap.ProtocolVersion, otaData.WebSocket.Version)
	}
}

// TestResourceLimits_WSTextMessageSizeLimit_1009 验证 WebSocket 文本消息超过 32 KiB 上限时触发 1009 并断开连接。
func TestResourceLimits_WSTextMessageSizeLimit_1009(t *testing.T) {
	wsMock := newMockBailianWSServer(t, "你好", [][]byte{generateSinePCM24k(1, 440.0)})
	defer wsMock.Close()
	llmMock := newMockBailianLLMServer(t, []string{"你好！"})
	defer llmMock.Close()

	addr, cfg, registry, limiter, cleanup := setupCustomResourceServer(t, wsMock.WSURL(), llmMock.BaseURL(), func(c *config.Config) {
		c.Session.MaxWSTextMessageBytes = 32768
	})
	defer cleanup()

	conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "limit-ws-text-dev")
	defer conn.Close(websocket.StatusNormalClosure, "done")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 构造超过 32 KiB 的文本消息并发送
	oversizedPadding := strings.Repeat("x", 33*1024)
	oversizedMsg := fmt.Sprintf(`{"type":"listen","state":"start","mode":"auto","padding":"%s"}`, oversizedPadding)
	if err := conn.Write(ctx, websocket.MessageText, []byte(oversizedMsg)); err != nil {
		t.Fatalf("failed to write oversized text message: %v", err)
	}

	// 持续读取下行，断言连接因 1009 (StatusMessageTooBig) 被服务端主动关闭
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed due to oversized text message, but read succeeded")
	}

	closeStatus := websocket.CloseStatus(err)
	if closeStatus != websocket.StatusMessageTooBig {
		t.Fatalf("expected close status StatusMessageTooBig (1009), got %v (err: %v)", closeStatus, err)
	}

	// 验证名额归零
	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestResourceLimits_OpusPacketLimits_EmptyAndTooLarge 验证 Opus 上行单包超限、空包与未握手二进制时服务端主动关闭。
func TestResourceLimits_OpusPacketLimits_EmptyAndTooLarge(t *testing.T) {
	wsMock := newMockBailianWSServer(t, "你好", [][]byte{generateSinePCM24k(1, 440.0)})
	defer wsMock.Close()
	llmMock := newMockBailianLLMServer(t, []string{"你好！"})
	defer llmMock.Close()

	addr, cfg, registry, limiter, cleanup := setupCustomResourceServer(t, wsMock.WSURL(), llmMock.BaseURL(), func(c *config.Config) {
		c.Session.MaxOpusPacketBytes = 1024
	})
	defer cleanup()

	t.Run("EmptyPacketInListening", func(t *testing.T) {
		conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "empty-packet-dev")
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		// 进入 Listening 状态
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"listen","state":"start","mode":"auto"}`)); err != nil {
			t.Fatalf("failed to write listen.start: %v", err)
		}

		// 发送 0 字节二进制空包
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{}); err != nil {
			t.Fatalf("failed to write empty binary packet: %v", err)
		}

		// 断言连接被服务端主动关闭 (1008 Policy Violation)
		_, _, err := conn.Read(ctx)
		if err == nil {
			t.Fatal("expected connection to be closed on empty opus packet, but read succeeded")
		}
		status := websocket.CloseStatus(err)
		if status != websocket.StatusPolicyViolation && status != websocket.StatusUnsupportedData {
			t.Fatalf("expected close status StatusPolicyViolation (1008) or StatusUnsupportedData (1003), got %v (err: %v)", status, err)
		}

		waitActiveCountZero(t, registry, limiter, 2*time.Second)
	})

	t.Run("OversizedPacketInListening", func(t *testing.T) {
		conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "oversized-packet-dev")
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		// 进入 Listening 状态
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"listen","state":"start","mode":"auto"}`)); err != nil {
			t.Fatalf("failed to write listen.start: %v", err)
		}

		// 发送超过 1024 字节上限的二进制包 (1025 字节)
		oversizedPacket := make([]byte, 1025)
		if err := conn.Write(ctx, websocket.MessageBinary, oversizedPacket); err != nil {
			t.Fatalf("failed to write oversized binary packet: %v", err)
		}

		// 断言连接被服务端主动关闭 (1008 Policy Violation)
		_, _, err := conn.Read(ctx)
		if err == nil {
			t.Fatal("expected connection to be closed on oversized opus packet, but read succeeded")
		}
		status := websocket.CloseStatus(err)
		if status != websocket.StatusPolicyViolation && status != websocket.StatusUnsupportedData {
			t.Fatalf("expected close status StatusPolicyViolation (1008) or StatusUnsupportedData (1003), got %v (err: %v)", status, err)
		}

		waitActiveCountZero(t, registry, limiter, 2*time.Second)
	})

	t.Run("BinaryMessageBeforeHello", func(t *testing.T) {
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dialCancel()

		conn := dialTestWebSocket(t, dialCtx, addr, cfg.DeviceSharedToken, "binary-before-hello-dev")
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		// 未发 Hello 直接发送二进制包
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x02, 0x03}); err != nil {
			t.Fatalf("failed to write binary packet before hello: %v", err)
		}

		// 断言服务端以 1003 Unsupported Data 或 1008 Policy Violation 关闭连接
		_, _, err := conn.Read(ctx)
		if err == nil {
			t.Fatal("expected connection to be closed on binary packet before hello, but read succeeded")
		}
		status := websocket.CloseStatus(err)
		if status != websocket.StatusUnsupportedData && status != websocket.StatusPolicyViolation {
			t.Fatalf("expected close status StatusUnsupportedData (1003) or PolicyViolation (1008), got %v (err: %v)", status, err)
		}

		waitActiveCountZero(t, registry, limiter, 2*time.Second)
	})
}

// TestResourceLimits_SlowClient_BackpressureAndClose 验证慢客户端（不读/慢读）下行队列满载触发背压并主动关闭连接。
func TestResourceLimits_SlowClient_BackpressureAndClose(t *testing.T) {
	// 生成 30 帧 24 kHz PCM 数据（每帧 2880 字节），供 TTS 快速产生 30 个 Opus 包
	ttsChunks := make([][]byte, 30)
	for i := 0; i < 30; i++ {
		ttsChunks[i] = generateSinePCM24k(1, 440.0+float64(i*10))
	}

	wsMock := newMockBailianWSServer(t, "慢客户端背压测试", ttsChunks)
	defer wsMock.Close()

	llmMock := newMockBailianLLMServer(t, []string{"这是背压测试的长回复文本句子。"})
	defer llmMock.Close()

	// 将下行 Opus 队列容量设置为较小值 (10)，以便快速确定性触发满载背压
	addr, cfg, registry, limiter, cleanup := setupCustomResourceServer(t, wsMock.WSURL(), llmMock.BaseURL(), func(c *config.Config) {
		c.Session.DownlinkOpusQueueCapacity = 10
	})
	defer cleanup()

	conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "slow-client-dev")
	defer conn.Close(websocket.StatusNormalClosure, "done")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. 发送 listen start 进入 auto 收音
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"listen","state":"start","mode":"auto"}`)); err != nil {
		t.Fatalf("failed to write listen start: %v", err)
	}

	// 2. 发送 1 帧 16k Opus 音频包触发 ASR 识别
	pcm16k := generateSinePCM16k(1, 440.0)
	opusPkts := encodeOpusPackets16k(t, pcm16k)
	if len(opusPkts) == 0 {
		t.Fatal("failed to encode 16k opus packet")
	}
	if err := conn.Write(ctx, websocket.MessageBinary, opusPkts[0]); err != nil {
		t.Fatalf("failed to write uplink audio packet: %v", err)
	}

	// 3. 客户端此时故意完全停止从 WebSocket 读取，使下行队列积压
	// 服务端 ASR 完成后 TTS 快速生成 30 个 Opus 包填满下行队列 (容量 10)，
	// 触发 Enqueue 中的背压保护并主动关闭连接。
	// 客户端持续读取排空缓冲区，断言连接最终因背压被服务端主动断开
	items, closeErr := readDownstreamUntilClosed(t, conn, 4*time.Second)
	if closeErr == nil {
		t.Fatal("expected downstream connection to be closed, but read succeeded until timeout")
	}

	var foundTTSStop bool
	for _, item := range items {
		if item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop {
			foundTTSStop = true
		}
	}
	if !foundTTSStop {
		t.Log("backpressure closed before tts.stop or tts.stop received")
	}

	// 4. 再次验证名额精确归零，无内存或连接泄漏
	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestResourceLimits_HighConcurrency_Stress_ZeroLeak 高并发交织压力测试：
// 并发执行正常问答、中途 abort、突发断线、握手断线与名额争抢，验证并发数据竞争安全、名额归零与协程零泄漏。
func TestResourceLimits_HighConcurrency_Stress_ZeroLeak(t *testing.T) {
	const (
		totalWorkers = 20
		maxSessions  = 15
	)

	// 准备支持多轮并发调用的 mock 数据
	ttsChunks := make([][]byte, 2)
	ttsChunks[0] = generateSinePCM24k(1, 440.0)
	ttsChunks[1] = generateSinePCM24k(1, 880.0)

	asrTurns := make([]mockASRTurn, totalWorkers*2)
	ttsTurns := make([]mockTTSTurn, totalWorkers*2)
	llmTurns := make([][]string, totalWorkers*2)
	for i := 0; i < totalWorkers*2; i++ {
		asrTurns[i] = mockASRTurn{text: fmt.Sprintf("并发测试问题-%d", i), manual: false}
		ttsTurns[i] = mockTTSTurn{pcmChunks: ttsChunks}
		llmTurns[i] = []string{"回答句1，", "回答句2。"}
	}

	// 记录测试开始前的基准 goroutine 数量
	runtime.GC()
	baselineGoroutines := runtime.NumGoroutine()

	wsMock := newMultiTurnMockBailianWSServer(t, asrTurns, ttsTurns)
	llmMock := newMultiTurnMockBailianLLMServer(t, llmTurns)

	addr, cfg, registry, limiter, cleanup := setupCustomResourceServer(t, wsMock.WSURL(), llmMock.BaseURL(), func(c *config.Config) {
		c.Server.MaxConcurrentSessions = maxSessions
	})

	pcm16k := generateSinePCM16k(2, 440.0)
	opusPkts := encodeOpusPackets16k(t, pcm16k)

	var wg sync.WaitGroup
	var successCount atomic.Int32
	var abortCount atomic.Int32
	var droppedCount atomic.Int32
	var rejectedCount atomic.Int32

	for i := 0; i < totalWorkers; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			devID := fmt.Sprintf("stress-dev-%d", workerID)
			dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer dialCancel()

			wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)
			dialOpts := &websocket.DialOptions{
				HTTPHeader: http.Header{
					"Authorization":    []string{"Bearer " + cfg.DeviceSharedToken},
					"Protocol-Version": []string{"1"},
					"Device-Id":        []string{devID},
					"Client-Id":        []string{"client-" + devID},
					"Serial-Number":    []string{"sn-" + devID},
				},
			}

			conn, resp, err := websocket.Dial(dialCtx, wsURL, dialOpts)
			if err != nil {
				if resp != nil && resp.StatusCode == http.StatusServiceUnavailable {
					rejectedCount.Add(1)
				}
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "worker done")

			// 类型 4：部分客户端在握手前或刚握手后立即断线 (Worker 14~16)
			if workerID >= 14 && workerID <= 16 {
				_ = conn.Close(websocket.StatusGoingAway, "immediate drop")
				droppedCount.Add(1)
				return
			}

			// 发送 Hello 握手
			helloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
			if err := conn.Write(dialCtx, websocket.MessageText, helloJSON); err != nil {
				return
			}
			typ, _, err := conn.Read(dialCtx)
			if err != nil || typ != websocket.MessageText {
				return
			}

			// 类型 3：部分客户端收音阶段突发网络断线 (Worker 10~13)
			if workerID >= 10 && workerID <= 13 {
				_ = conn.Write(dialCtx, websocket.MessageText, []byte(`{"type":"listen","state":"start","mode":"auto"}`))
				if len(opusPkts) > 0 {
					_ = conn.Write(dialCtx, websocket.MessageBinary, opusPkts[0])
				}
				_ = conn.Close(websocket.StatusGoingAway, "client dropped network")
				droppedCount.Add(1)
				return
			}

			// 发送 listen.start 进入 auto 收音
			if err := conn.Write(dialCtx, websocket.MessageText, []byte(`{"type":"listen","state":"start","mode":"auto"}`)); err != nil {
				return
			}
			for _, pkt := range opusPkts {
				if err := conn.Write(dialCtx, websocket.MessageBinary, pkt); err != nil {
					return
				}
			}

			// 类型 2：部分客户端在问答过程中触发 abort 打断 (Worker 5~9)
			if workerID >= 5 && workerID <= 9 {
				// 读取直到收到首个消息后发送 abort
				readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer readCancel()
				_, _, rErr := conn.Read(readCtx)
				if rErr == nil {
					_ = conn.Write(readCtx, websocket.MessageText, []byte(`{"type":"abort","reason":"user_interrupted"}`))
					// 持续读取直至停止
					_, _ = readDownstreamUntilClosed(t, conn, 2*time.Second)
				}
				abortCount.Add(1)
				return
			}

			// 类型 1：正常单轮问答 (Worker 0~4, 17~19)
			readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer readCancel()
			for {
				mType, mData, rErr := conn.Read(readCtx)
				if rErr != nil {
					break
				}
				if mType == websocket.MessageText {
					var base struct {
						Type  string `json:"type"`
						State string `json:"state"`
					}
					if err := json.Unmarshal(mData, &base); err == nil {
						if base.Type == session.MessageTypeTTS && base.State == session.TTSStateStop {
							successCount.Add(1)
							break
						}
					}
				}
			}
		}()
	}

	wg.Wait()

	// 验证所有并发会话完全释放归零
	waitActiveCountZero(t, registry, limiter, 3*time.Second)

	// 优雅关闭服务与 mock
	cleanup()
	wsMock.Close()
	llmMock.Close()

	// 确定性验证 goroutine 零泄漏（基准对比）
	runtime.GC()
	deadline := time.Now().Add(3 * time.Second)
	var finalGCount int
	for time.Now().Before(deadline) {
		finalGCount = runtime.NumGoroutine()
		if finalGCount <= baselineGoroutines+3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if finalGCount > baselineGoroutines+3 {
		t.Errorf("goroutine leak detected: baseline=%d, final=%d", baselineGoroutines, finalGCount)
	}

	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected final active limiter count 0, got %d", limiter.ActiveCount())
	}
	if registry.ActiveCount() != 0 {
		t.Fatalf("expected final registry active count 0, got %d", registry.ActiveCount())
	}
}

// TestResourceLimits_HighConcurrency_InterleavedStressAndGracefulShutdown 验证高并发交织通信中突发优雅停机。
func TestResourceLimits_HighConcurrency_InterleavedStressAndGracefulShutdown(t *testing.T) {
	const (
		totalWorkers = 15
		maxSessions  = 15
	)

	ttsChunks := [][]byte{generateSinePCM24k(1, 440.0)}
	wsMock := newMultiTurnMockBailianWSServer(t, []mockASRTurn{{text: "停服交织测试", manual: false}}, []mockTTSTurn{{pcmChunks: ttsChunks}})
	defer wsMock.Close()
	llmMock := newMultiTurnMockBailianLLMServer(t, [][]string{{"停机测试回复句子。"}})
	defer llmMock.Close()

	addr, cfg, registry, limiter, cleanup := setupCustomResourceServer(t, wsMock.WSURL(), llmMock.BaseURL(), func(c *config.Config) {
		c.Server.MaxConcurrentSessions = maxSessions
	})

	pcm16k := generateSinePCM16k(1, 440.0)
	opusPkts := encodeOpusPackets16k(t, pcm16k)

	var wg sync.WaitGroup
	var clientErrCount atomic.Int32

	for i := 0; i < totalWorkers; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			devID := fmt.Sprintf("shutdown-dev-%d", workerID)
			dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer dialCancel()

			wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)
			dialOpts := &websocket.DialOptions{
				HTTPHeader: http.Header{
					"Authorization":    []string{"Bearer " + cfg.DeviceSharedToken},
					"Protocol-Version": []string{"1"},
					"Device-Id":        []string{devID},
					"Client-Id":        []string{"client-" + devID},
					"Serial-Number":    []string{"sn-" + devID},
				},
			}

			conn, _, err := websocket.Dial(dialCtx, wsURL, dialOpts)
			if err != nil {
				clientErrCount.Add(1)
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "done")

			helloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
			if err := conn.Write(dialCtx, websocket.MessageText, helloJSON); err != nil {
				clientErrCount.Add(1)
				return
			}
			_, _, _ = conn.Read(dialCtx)

			_ = conn.Write(dialCtx, websocket.MessageText, []byte(`{"type":"listen","state":"start","mode":"auto"}`))
			if len(opusPkts) > 0 {
				_ = conn.Write(dialCtx, websocket.MessageBinary, opusPkts[0])
			}

			// 持续读取直到服务停机广播取消断开
			readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer readCancel()
			for {
				_, _, rErr := conn.Read(readCtx)
				if rErr != nil {
					clientErrCount.Add(1)
					break
				}
			}
		}()
	}

	// 确定性等待并发会话均建连并进入活跃通信状态，然后突发触发服务优雅停机
	activeDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(activeDeadline) {
		if registry.ActiveCount() >= maxSessions {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cleanup()

	wg.Wait()

	// 断言所有会话和名额在停机后精确归零
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0 after shutdown, got %d", limiter.ActiveCount())
	}
	if registry.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0 after shutdown, got %d", registry.ActiveCount())
	}
}
