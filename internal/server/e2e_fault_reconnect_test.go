package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hraban/opus"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/ai/bailian"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/router"
	"xiaozhi-esp32-golang-server/internal/server"
	"xiaozhi-esp32-golang-server/internal/session"
)

// waitActiveCountZero 确定性轮询会话注册表与准入限流器，等待活跃会话数与名额归零。
func waitActiveCountZero(t *testing.T, registry *session.Registry, limiter *session.SessionLimiter, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		regZero := (registry == nil || registry.ActiveCount() == 0)
		limZero := (limiter == nil || limiter.ActiveCount() == 0)
		if regZero && limZero {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	activeReg := 0
	if registry != nil {
		activeReg = registry.ActiveCount()
	}
	activeLim := 0
	if limiter != nil {
		activeLim = limiter.ActiveCount()
	}
	t.Fatalf("timed out waiting for active count to become 0, registry=%d, limiter=%d", activeReg, activeLim)
}

// setupE2ETestServer 构造并启动端到端集成测试服务环境，返回服务地址、配置、注册表、限流器与清理函数。
func setupE2ETestServer(t *testing.T, wsMockURL, llmMockURL string) (string, *config.Config, *session.Registry, *session.SessionLimiter, func()) {
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

	routerHandler := router.NewHandler(cfg, wsHandler, nil)
	httpRouter := router.NewRouter(routerHandler)

	srv := server.New(cfg.Server, httpRouter)
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

// dialAndHandshakeClient 连接服务端并完成 Hello 握手，返回 WebSocket 连接与服务端分配的 session_id。
func dialAndHandshakeClient(t *testing.T, addr, token, deviceID string) (*websocket.Conn, string) {
	t.Helper()
	wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{deviceID},
			"Client-Id":        []string{"client-" + deviceID},
			"Serial-Number":    []string{"sn-" + deviceID},
		},
	}

	conn, _, err := websocket.Dial(dialCtx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}

	clientHelloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	if err := conn.Write(context.Background(), websocket.MessageText, clientHelloJSON); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "failed to send hello")
		t.Fatalf("failed to send client hello: %v", err)
	}

	readHelloCtx, readHelloCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readHelloCancel()

	mType, mData, err := conn.Read(readHelloCtx)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "failed to read hello")
		t.Fatalf("failed to read server hello: %v", err)
	}
	if mType != websocket.MessageText {
		_ = conn.Close(websocket.StatusUnsupportedData, "unexpected message type")
		t.Fatalf("expected text message for server hello, got %v", mType)
	}

	var srvHello session.ServerHelloMessage
	if err := json.Unmarshal(mData, &srvHello); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid hello json")
		t.Fatalf("failed to unmarshal server hello: %v", err)
	}
	if srvHello.Type != session.MessageTypeHello || srvHello.SessionID == "" {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid server hello content")
		t.Fatalf("invalid server hello response: %+v", srvHello)
	}

	return conn, srvHello.SessionID
}

// readDownstreamUntilClosed 持续读取下行消息直至底层连接断开，返回收集到的所有消息与最终关闭错误。
func readDownstreamUntilClosed(t *testing.T, conn *websocket.Conn, timeout time.Duration) ([]e2eReceivedItem, error) {
	t.Helper()
	var received []e2eReceivedItem
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		mType, mData, err := conn.Read(ctx)
		if err != nil {
			return received, err
		}

		item := e2eReceivedItem{
			msgType: mType,
			payload: mData,
		}

		if mType == websocket.MessageText {
			var base struct {
				Type      string `json:"type"`
				State     string `json:"state"`
				Text      string `json:"text"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(mData, &base); err == nil {
				item.textType = base.Type
				item.state = base.State
				item.text = base.Text
				item.sessionID = base.SessionID
			}
		}

		received = append(received, item)
	}
}

// TestE2E_Fault_Abort_DuringPlayback_AndRecoverNewTurn 验证播放中收到 abort 中断信号时的完整行为：
// 收到部分音频时客户端发送 abort，断言补发一次且仅一次 tts.stop，未播音频停止下发，状态回到 READY，未写入历史，且同一连接可立即开启新一轮完整对话。
func TestE2E_Fault_Abort_DuringPlayback_AndRecoverNewTurn(t *testing.T) {
	const (
		round1ASRText       = "请给我讲一个长故事"
		round1Sentence1     = "从前有座山，"
		round1Sentence2     = "山上有座庙。"
		round2ASRText       = "现在几点了"
		round2Sentence1     = "现在是，"
		round2Sentence2     = "上午十点。"
		round2AssistantText = round2Sentence1 + round2Sentence2
	)

	// 1. 构造百炼假服务：Round 1 产生较多 PCM 音频块，Round 2 正常
	round1PCMChunks := make([][]byte, 8)
	for i := 0; i < 8; i++ {
		round1PCMChunks[i] = generateSinePCM24k(1, 440.0+float64(i*50))
	}
	round2PCMChunks := [][]byte{
		generateSinePCM24k(1, 500.0),
		generateSinePCM24k(1, 600.0),
	}

	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{
			{text: round1ASRText, manual: false},
			{text: round2ASRText, manual: false},
		},
		[]mockTTSTurn{
			{pcmChunks: round1PCMChunks},
			{pcmChunks: round2PCMChunks},
		},
	)
	defer wsMock.Close()

	llmMock := newMultiTurnMockBailianLLMServer(t, [][]string{
		{round1Sentence1, round1Sentence2},
		{round2Sentence1, round2Sentence2},
	})
	defer llmMock.Close()

	addr, cfg, registry, limiter, serverCleanup := setupE2ETestServer(t, wsMock.WSURL(), llmMock.BaseURL())
	defer serverCleanup()

	// 2. 建立 WebSocket 客户端连接并完成握手
	conn, sessionID := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-abort-test")
	defer conn.Close(websocket.StatusNormalClosure, "test finished")

	// 3. 发起 Round 1: 客户端进入 auto 模式并发送音频
	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send round 1 listen start: %v", err)
	}

	pcm16k := generateSinePCM16k(2, 440.0)
	opusPackets16k := encodeOpusPackets16k(t, pcm16k)
	for _, pkt := range opusPackets16k {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet: %v", err)
		}
	}

	// 4. 客户端读取下行消息并在收到首个 Opus 音频包时立即触发 abort
	var (
		round1Received   []e2eReceivedItem
		foundTTSStart    bool
		abortSent        bool
		audioCountBefore int
		audioCountAfter  int
		ttsStopCount     int
		lastAudioIdx     = -1
		ttsStopIdx       = -1
	)

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()

	for {
		mType, mData, err := conn.Read(readCtx)
		if err != nil {
			t.Fatalf("failed to read downstream during round 1: %v", err)
		}

		item := e2eReceivedItem{
			msgType: mType,
			payload: mData,
		}

		if mType == websocket.MessageText {
			var base struct {
				Type      string `json:"type"`
				State     string `json:"state"`
				Text      string `json:"text"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(mData, &base); err == nil {
				item.textType = base.Type
				item.state = base.State
				item.text = base.Text
				item.sessionID = base.SessionID
			}

			if item.textType == session.MessageTypeTTS && item.state == session.TTSStateStart {
				foundTTSStart = true
			}
			if item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop {
				ttsStopCount++
				ttsStopIdx = len(round1Received)
			}
		} else if mType == websocket.MessageBinary {
			lastAudioIdx = len(round1Received)
			if !abortSent {
				audioCountBefore++
			} else {
				audioCountAfter++
			}
		}

		round1Received = append(round1Received, item)

		// 收到 tts.start 以及至少 1 个下行音频包时，立即发送 abort 打断
		if foundTTSStart && mType == websocket.MessageBinary && !abortSent {
			abortJSON := []byte(`{"type":"abort","reason":"user_interrupted"}`)
			if err := conn.Write(context.Background(), websocket.MessageText, abortJSON); err != nil {
				t.Fatalf("failed to send abort message: %v", err)
			}
			abortSent = true
		}

		// 收到补发的 tts.stop 后退出 Round 1 读取
		if ttsStopCount > 0 {
			break
		}
	}

	if !abortSent {
		t.Fatal("abort was never sent")
	}
	if ttsStopCount != 1 {
		t.Fatalf("expected exactly 1 tts.stop message on abort, got %d", ttsStopCount)
	}

	totalAudioReceived := audioCountBefore + audioCountAfter
	if totalAudioReceived >= len(round1PCMChunks) {
		t.Errorf("expected downstream audio to be truncated upon abort, but received all %d packets", totalAudioReceived)
	}

	if lastAudioIdx > ttsStopIdx {
		t.Errorf("audio packet (idx %d) received after tts.stop (idx %d)", lastAudioIdx, ttsStopIdx)
	}

	// 5. 校验服务端状态回到 READY，且被打断的 Round 1 对话未写入会话历史
	var sess *session.Session
	for _, s := range registry.Sessions() {
		if s.SessionID() == sessionID {
			sess = s
			break
		}
	}
	if sess == nil {
		t.Fatal("session not found in registry")
	}

	waitSessionState(t, sess, session.StateReady, 2*time.Second)

	history := sess.History()
	if len(history) != 0 {
		t.Fatalf("expected session history to be empty after abort, got %d messages: %+v", len(history), history)
	}

	// 6. 在同一 WebSocket 连接上发起 Round 2 全新对话
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send round 2 listen start: %v", err)
	}

	for _, pkt := range opusPackets16k {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet in round 2: %v", err)
		}
	}

	round2Received := readDownstreamUntilTTSStop(t, conn, 5*time.Second)

	dec24k, err := opus.NewDecoder(24000, 1)
	if err != nil {
		t.Fatalf("failed to create 24k opus decoder: %v", err)
	}

	validateTurnItems(t, round2Received, sessionID, round2ASRText, []string{round2Sentence1, round2Sentence2}, dec24k)

	waitSessionState(t, sess, session.StateReady, 2*time.Second)

	// 7. 严格断言 LLM 收到的上下文隔离：Round 2 请求仅包含系统提示词 + Round 2 User 输入
	requests := llmMock.ReceivedRequests()
	if len(requests) != 2 {
		t.Fatalf("expected 2 LLM requests, got %d", len(requests))
	}

	round2Req := requests[1]
	if len(round2Req.Messages) != 2 {
		t.Fatalf("expected Round 2 LLM request to have 2 messages (system + user), got %d: %+v", len(round2Req.Messages), round2Req.Messages)
	}
	if round2Req.Messages[0].Role != "system" {
		t.Errorf("expected Round 2 messages[0] role system, got %q", round2Req.Messages[0].Role)
	}
	if round2Req.Messages[1].Role != "user" || round2Req.Messages[1].Content != round2ASRText {
		t.Errorf("expected Round 2 messages[1] to be user query %q, got %+v", round2ASRText, round2Req.Messages[1])
	}

	// 8. 断言服务端历史仅记录 Round 2 的 1 问 1 答
	historyAfterRound2 := sess.History()
	if len(historyAfterRound2) != 2 {
		t.Fatalf("expected 2 history messages after round 2, got %d: %+v", len(historyAfterRound2), historyAfterRound2)
	}
	if historyAfterRound2[0].Role != ai.RoleUser || historyAfterRound2[0].Content != round2ASRText {
		t.Errorf("expected history[0] to be user %q, got %+v", round2ASRText, historyAfterRound2[0])
	}
	if historyAfterRound2[1].Role != ai.RoleAssistant || historyAfterRound2[1].Content != round2AssistantText {
		t.Errorf("expected history[1] to be assistant %q, got %+v", round2AssistantText, historyAfterRound2[1])
	}

	_ = conn.Close(websocket.StatusNormalClosure, "test finished")
	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestE2E_Fault_ASRFailure_ClosesConnection 验证百炼 ASR 供应商发生故障时直接关闭设备 WebSocket 连接。
func TestE2E_Fault_ASRFailure_ClosesConnection(t *testing.T) {
	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{
			{fail: true, errorCode: "ASR_SERVICE_UNAVAILABLE", errorMsg: "asr mock service error"},
		},
		[]mockTTSTurn{},
	)
	defer wsMock.Close()

	llmMock := newMockBailianLLMServer(t, []string{"无用回答"})
	defer llmMock.Close()

	addr, cfg, registry, limiter, serverCleanup := setupE2ETestServer(t, wsMock.WSURL(), llmMock.BaseURL())
	defer serverCleanup()

	conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-asr-fault")
	defer conn.Close(websocket.StatusInternalError, "cleanup")

	// 发起 auto 模式语音识别
	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send listen start: %v", err)
	}

	pcm16k := generateSinePCM16k(2, 440.0)
	opusPackets16k := encodeOpusPackets16k(t, pcm16k)
	for _, pkt := range opusPackets16k {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet: %v", err)
		}
	}

	// 持续读取直至服务端关闭连接
	received, closeErr := readDownstreamUntilClosed(t, conn, 3*time.Second)
	if closeErr == nil {
		t.Fatal("expected connection to be closed by server on ASR failure, but read succeeded")
	}

	// 验证未下发任何 TTS 播报消息
	for _, item := range received {
		if item.textType == session.MessageTypeTTS {
			t.Errorf("unexpected TTS message received during ASR failure: %+v", item)
		}
	}

	// 验证关闭状态码为 StatusInternalError (1011) 或返回关闭错误
	var wsCloseErr websocket.CloseError
	if errors.As(closeErr, &wsCloseErr) {
		if wsCloseErr.Code != websocket.StatusInternalError {
			t.Errorf("expected close code %v, got %v", websocket.StatusInternalError, wsCloseErr.Code)
		}
	}

	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestE2E_Fault_LLMFailure_ClosesConnection 验证百炼 LLM 供应商发生故障时直接关闭设备 WebSocket 连接。
func TestE2E_Fault_LLMFailure_ClosesConnection(t *testing.T) {
	const asrText = "请回答我的问题"

	wsMock := newMockBailianWSServer(t, asrText, [][]byte{})
	defer wsMock.Close()

	llmMock := newMultiTurnMockBailianLLMServerWithFailures(t,
		[][]string{{"回答"}},
		map[int]int{0: http.StatusInternalServerError},
	)
	defer llmMock.Close()

	addr, cfg, registry, limiter, serverCleanup := setupE2ETestServer(t, wsMock.WSURL(), llmMock.BaseURL())
	defer serverCleanup()

	conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-llm-fault")
	defer conn.Close(websocket.StatusInternalError, "cleanup")

	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send listen start: %v", err)
	}

	pcm16k := generateSinePCM16k(2, 440.0)
	opusPackets16k := encodeOpusPackets16k(t, pcm16k)
	for _, pkt := range opusPackets16k {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet: %v", err)
		}
	}

	// 持续读取直至服务端关闭连接
	received, closeErr := readDownstreamUntilClosed(t, conn, 3*time.Second)
	if closeErr == nil {
		t.Fatal("expected connection to be closed by server on LLM failure, but read succeeded")
	}

	var foundSTT bool
	for _, item := range received {
		if item.textType == session.MessageTypeSTT {
			foundSTT = true
			if item.text != asrText {
				t.Errorf("expected STT text %q, got %q", asrText, item.text)
			}
		}
		if item.textType == session.MessageTypeTTS {
			t.Errorf("unexpected TTS message received during LLM failure: %+v", item)
		}
	}

	if !foundSTT {
		t.Fatal("expected STT message before LLM failure")
	}

	var wsCloseErr websocket.CloseError
	if errors.As(closeErr, &wsCloseErr) {
		if wsCloseErr.Code != websocket.StatusInternalError {
			t.Errorf("expected close code %v, got %v", websocket.StatusInternalError, wsCloseErr.Code)
		}
	}

	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestE2E_Fault_TTSFailure_DuringSpeaking_SendsStopAndCloses 验证百炼 TTS 在播放中突发故障时，服务端尽力补发一次 tts.stop 并关闭连接。
func TestE2E_Fault_TTSFailure_DuringSpeaking_SendsStopAndCloses(t *testing.T) {
	const (
		asrText   = "给我读一段诗"
		sentence1 = "床前明月光，"
		sentence2 = "疑是地上霜。"
	)

	ttsChunks := [][]byte{
		generateSinePCM24k(1, 440.0),
		generateSinePCM24k(1, 880.0),
	}

	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{
			{text: asrText, manual: false},
		},
		[]mockTTSTurn{
			{
				pcmChunks:       ttsChunks,
				fail:            true,
				failAfterChunks: 1,
				errorCode:       "TTS_STREAM_ABORTED",
				errorMsg:        "mock tts mid-stream failure",
			},
		},
	)
	defer wsMock.Close()

	llmMock := newMockBailianLLMServer(t, []string{sentence1, sentence2})
	defer llmMock.Close()

	addr, cfg, registry, limiter, serverCleanup := setupE2ETestServer(t, wsMock.WSURL(), llmMock.BaseURL())
	defer serverCleanup()

	conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-tts-fault")
	defer conn.Close(websocket.StatusInternalError, "cleanup")

	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send listen start: %v", err)
	}

	pcm16k := generateSinePCM16k(2, 440.0)
	opusPackets16k := encodeOpusPackets16k(t, pcm16k)
	for _, pkt := range opusPackets16k {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet: %v", err)
		}
	}

	// 持续读取直至服务端关闭连接
	received, closeErr := readDownstreamUntilClosed(t, conn, 4*time.Second)
	if closeErr == nil {
		t.Fatal("expected connection to be closed by server on TTS failure, but read succeeded")
	}

	var (
		foundSTT         bool
		foundTTSStart    bool
		ttsStopCount     int
		audioPacketCount int
	)

	for _, item := range received {
		if item.msgType == websocket.MessageText {
			switch {
			case item.textType == session.MessageTypeSTT:
				foundSTT = true
			case item.textType == session.MessageTypeTTS && item.state == session.TTSStateStart:
				foundTTSStart = true
			case item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop:
				ttsStopCount++
			}
		} else if item.msgType == websocket.MessageBinary {
			audioPacketCount++
		}
	}

	if !foundSTT {
		t.Fatal("missing STT message")
	}
	if !foundTTSStart {
		t.Fatal("missing tts.start message before TTS failure")
	}
	if ttsStopCount != 1 {
		t.Fatalf("expected exactly 1 best-effort tts.stop message on TTS failure, got %d", ttsStopCount)
	}
	if audioPacketCount == 0 {
		t.Fatal("expected at least 1 audio packet before TTS failure")
	}

	var wsCloseErr websocket.CloseError
	if errors.As(closeErr, &wsCloseErr) {
		if wsCloseErr.Code != websocket.StatusInternalError {
			t.Errorf("expected close code %v, got %v", websocket.StatusInternalError, wsCloseErr.Code)
		}
	}

	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestE2E_Fault_ClientDisconnect_DuringListening 验证客户端在收音阶段主动断开连接，服务端所有流程平稳退出且准入名额归零。
func TestE2E_Fault_ClientDisconnect_DuringListening(t *testing.T) {
	wsMock := newMockBailianWSServer(t, "未完成收音文本", [][]byte{})
	defer wsMock.Close()

	llmMock := newMockBailianLLMServer(t, []string{"无用回答"})
	defer llmMock.Close()

	addr, cfg, registry, limiter, serverCleanup := setupE2ETestServer(t, wsMock.WSURL(), llmMock.BaseURL())
	defer serverCleanup()

	conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-disconnect-listening")

	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send listen start: %v", err)
	}

	pcm16k := generateSinePCM16k(1, 440.0)
	opusPackets16k := encodeOpusPackets16k(t, pcm16k)
	if len(opusPackets16k) > 0 {
		_ = conn.Write(context.Background(), websocket.MessageBinary, opusPackets16k[0])
	}

	// 客户端在收音中途主动挂断
	_ = conn.Close(websocket.StatusNormalClosure, "client hang up during listening")

	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestE2E_Fault_ClientDisconnect_DuringSpeaking 验证客户端在播报阶段主动断开连接，服务端所有流程平稳退出且准入名额归零。
func TestE2E_Fault_ClientDisconnect_DuringSpeaking(t *testing.T) {
	const (
		asrText   = "给我讲个故事"
		sentence1 = "很久很久以前，"
		sentence2 = "森林里住着一只小松鼠。"
	)

	ttsChunks := make([][]byte, 6)
	for i := 0; i < 6; i++ {
		ttsChunks[i] = generateSinePCM24k(1, 440.0+float64(i*50))
	}

	wsMock := newMockBailianWSServer(t, asrText, ttsChunks)
	defer wsMock.Close()

	llmMock := newMockBailianLLMServer(t, []string{sentence1, sentence2})
	defer llmMock.Close()

	addr, cfg, registry, limiter, serverCleanup := setupE2ETestServer(t, wsMock.WSURL(), llmMock.BaseURL())
	defer serverCleanup()

	conn, _ := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-disconnect-speaking")

	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send listen start: %v", err)
	}

	pcm16k := generateSinePCM16k(2, 440.0)
	opusPackets16k := encodeOpusPackets16k(t, pcm16k)
	for _, pkt := range opusPackets16k {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet: %v", err)
		}
	}

	// 持续读取消息，直到收到 tts.start 以及首个音频包时，客户端立即挂断
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()

	var foundTTSStart bool
	for {
		mType, mData, err := conn.Read(readCtx)
		if err != nil {
			break
		}

		if mType == websocket.MessageText {
			var base struct {
				Type  string `json:"type"`
				State string `json:"state"`
			}
			if err := json.Unmarshal(mData, &base); err == nil {
				if base.Type == session.MessageTypeTTS && base.State == session.TTSStateStart {
					foundTTSStart = true
				}
			}
		} else if mType == websocket.MessageBinary && foundTTSStart {
			// 收到播报中的音频包，客户端立即断开
			_ = conn.Close(websocket.StatusNormalClosure, "client hang up during speaking")
			break
		}
	}

	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}

// TestE2E_Fault_Reconnect_IndependentNewSession 验证断线后新连接接入时生成全新的 session_id，历史为空且独立完成全新问答闭环。
func TestE2E_Fault_Reconnect_IndependentNewSession(t *testing.T) {
	const (
		turn1ASRText       = "我是第一个会话"
		turn1Sentence1     = "你好，"
		turn1Sentence2     = "第一个会话已建立。"
		turn1AssistantText = turn1Sentence1 + turn1Sentence2

		turn2ASRText       = "我是第二个重连会话"
		turn2Sentence1     = "你好，"
		turn2Sentence2     = "第二个会话已独立建立。"
		turn2AssistantText = turn2Sentence1 + turn2Sentence2
	)

	ttsChunk1 := generateSinePCM24k(1, 440.0)
	ttsChunk2 := generateSinePCM24k(1, 880.0)

	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{
			{text: turn1ASRText, manual: false},
			{text: turn2ASRText, manual: false},
		},
		[]mockTTSTurn{
			{pcmChunks: [][]byte{ttsChunk1, ttsChunk2}},
			{pcmChunks: [][]byte{ttsChunk1, ttsChunk2}},
		},
	)
	defer wsMock.Close()

	llmMock := newMultiTurnMockBailianLLMServer(t, [][]string{
		{turn1Sentence1, turn1Sentence2},
		{turn2Sentence1, turn2Sentence2},
	})
	defer llmMock.Close()

	addr, cfg, registry, limiter, serverCleanup := setupE2ETestServer(t, wsMock.WSURL(), llmMock.BaseURL())
	defer serverCleanup()

	// 1. Client 1 建连握手并完成 Turn 1 问答闭环
	conn1, sessionID1 := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-reconnect-001")

	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn1.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send turn 1 listen start: %v", err)
	}

	pcm16k := generateSinePCM16k(2, 440.0)
	opusPackets16k := encodeOpusPackets16k(t, pcm16k)
	for _, pkt := range opusPackets16k {
		if err := conn1.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet in turn 1: %v", err)
		}
	}

	turn1Received := readDownstreamUntilTTSStop(t, conn1, 5*time.Second)

	dec24k, err := opus.NewDecoder(24000, 1)
	if err != nil {
		t.Fatalf("failed to create 24k opus decoder: %v", err)
	}

	validateTurnItems(t, turn1Received, sessionID1, turn1ASRText, []string{turn1Sentence1, turn1Sentence2}, dec24k)

	// 校验 Session 1 历史记录
	var sess1 *session.Session
	for _, s := range registry.Sessions() {
		if s.SessionID() == sessionID1 {
			sess1 = s
			break
		}
	}
	if sess1 == nil {
		t.Fatal("session 1 not found in registry")
	}

	waitSessionState(t, sess1, session.StateReady, 2*time.Second)
	hist1 := sess1.History()
	if len(hist1) != 2 {
		t.Fatalf("expected session 1 history to have 2 messages, got %d: %+v", len(hist1), hist1)
	}
	if hist1[0].Content != turn1ASRText || hist1[1].Content != turn1AssistantText {
		t.Errorf("unexpected session 1 history: %+v", hist1)
	}

	// 2. Client 1 主动断开连接，验证会话清理与名额归零
	_ = conn1.Close(websocket.StatusNormalClosure, "client 1 finished")
	waitActiveCountZero(t, registry, limiter, 2*time.Second)

	// 3. Client 2（重连客户端）重新建连并握手
	conn2, sessionID2 := dialAndHandshakeClient(t, addr, cfg.DeviceSharedToken, "device-reconnect-001")
	defer conn2.Close(websocket.StatusNormalClosure, "client 2 finished")

	// 严格断言重连生成全新的 session_id
	if sessionID2 == "" {
		t.Fatal("expected non-empty sessionID2")
	}
	if sessionID2 == sessionID1 {
		t.Fatalf("expected new unique sessionID2, got identical to sessionID1: %q", sessionID2)
	}

	// 严格断言 Client 2 服务端会话历史为空（完全隔离）
	var sess2 *session.Session
	for _, s := range registry.Sessions() {
		if s.SessionID() == sessionID2 {
			sess2 = s
			break
		}
	}
	if sess2 == nil {
		t.Fatal("session 2 not found in registry")
	}

	hist2Initial := sess2.History()
	if len(hist2Initial) != 0 {
		t.Fatalf("expected session 2 initial history to be empty, got %d messages: %+v", len(hist2Initial), hist2Initial)
	}

	// 4. Client 2 独立完成 Turn 2 全新问答闭环
	if err := conn2.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send turn 2 listen start: %v", err)
	}

	for _, pkt := range opusPackets16k {
		if err := conn2.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet in turn 2: %v", err)
		}
	}

	turn2Received := readDownstreamUntilTTSStop(t, conn2, 5*time.Second)
	validateTurnItems(t, turn2Received, sessionID2, turn2ASRText, []string{turn2Sentence1, turn2Sentence2}, dec24k)

	waitSessionState(t, sess2, session.StateReady, 2*time.Second)

	// 5. 严格断言 LLM 收到的请求完全隔离：Client 2 请求中未混入 Client 1 的任何历史
	requests := llmMock.ReceivedRequests()
	if len(requests) != 2 {
		t.Fatalf("expected 2 LLM requests, got %d", len(requests))
	}

	req2 := requests[1]
	if len(req2.Messages) != 2 {
		t.Fatalf("expected Client 2 LLM request to have exactly 2 messages (system + user), got %d: %+v", len(req2.Messages), req2.Messages)
	}
	if req2.Messages[0].Role != "system" {
		t.Errorf("expected Client 2 message[0] role system, got %q", req2.Messages[0].Role)
	}
	if req2.Messages[1].Role != "user" || req2.Messages[1].Content != turn2ASRText {
		t.Errorf("expected Client 2 message[1] to be %q, got %+v", turn2ASRText, req2.Messages[1])
	}

	// 6. 断言 Session 2 历史中仅记录自身的 1 问 1 答
	hist2Final := sess2.History()
	if len(hist2Final) != 2 {
		t.Fatalf("expected session 2 final history to have 2 messages, got %d: %+v", len(hist2Final), hist2Final)
	}
	if hist2Final[0].Content != turn2ASRText || hist2Final[1].Content != turn2AssistantText {
		t.Errorf("unexpected session 2 final history: %+v", hist2Final)
	}

	_ = conn2.Close(websocket.StatusNormalClosure, "test finished")
	waitActiveCountZero(t, registry, limiter, 2*time.Second)
}
