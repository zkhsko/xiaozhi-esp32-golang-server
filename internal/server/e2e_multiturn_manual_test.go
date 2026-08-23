package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

// validateTurnItems 严格校验单轮下行消息的总顺序与字段内容。
func validateTurnItems(t *testing.T, received []e2eReceivedItem, expectedSessionID, expectedASRText string, expectedSentences []string, dec24k *opus.Decoder) {
	t.Helper()
	var (
		foundSTT          bool
		receivedSentences []string
		foundTTSStart     bool
		foundTTSStop      bool
		audioPacketCount  int
		sttIdx            = -1
		firstSentenceIdx  = -1
		ttsStartIdx       = -1
		firstAudioIdx     = -1
		lastAudioIdx      = -1
		ttsStopIdx        = -1
	)

	for idx, item := range received {
		if item.msgType == websocket.MessageText {
			if item.sessionID != expectedSessionID {
				t.Errorf("message %d session_id %q does not match expected %q", idx, item.sessionID, expectedSessionID)
			}
			switch {
			case item.textType == session.MessageTypeSTT:
				if foundSTT {
					t.Errorf("multiple STT messages received in turn")
				}
				foundSTT = true
				sttIdx = idx
				if item.text != expectedASRText {
					t.Errorf("expected STT text %q, got %q", expectedASRText, item.text)
				}

			case item.textType == session.MessageTypeTTS && item.state == session.TTSStateSentenceStart:
				if len(receivedSentences) == 0 {
					firstSentenceIdx = idx
				}
				receivedSentences = append(receivedSentences, item.text)

			case item.textType == session.MessageTypeTTS && item.state == session.TTSStateStart:
				if foundTTSStart {
					t.Errorf("multiple tts.start messages received in turn")
				}
				foundTTSStart = true
				ttsStartIdx = idx

			case item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop:
				if foundTTSStop {
					t.Errorf("multiple tts.stop messages received in turn")
				}
				foundTTSStop = true
				ttsStopIdx = idx
			}
		} else if item.msgType == websocket.MessageBinary {
			audioPacketCount++
			if firstAudioIdx == -1 {
				firstAudioIdx = idx
			}
			lastAudioIdx = idx
			pcm := decodeOpusPacket24k(t, dec24k, item.payload)
			if len(pcm) != 2880 {
				t.Errorf("expected 2880 bytes pcm from 24k opus, got %d", len(pcm))
			}
		}
	}

	if !foundSTT {
		t.Fatal("missing STT message")
	}
	if len(receivedSentences) != len(expectedSentences) {
		t.Fatalf("expected %d tts.sentence_start messages, got %d", len(expectedSentences), len(receivedSentences))
	}
	for i, expected := range expectedSentences {
		if receivedSentences[i] != expected {
			t.Errorf("expected sentence %d %q, got %q", i, expected, receivedSentences[i])
		}
	}
	if !foundTTSStart {
		t.Fatal("missing tts.start message")
	}
	if !foundTTSStop {
		t.Fatal("missing tts.stop message")
	}
	if audioPacketCount == 0 {
		t.Fatal("no downlink opus audio packets received")
	}

	// 协议总顺序校验：stt -> tts.sentence_start (首句) -> tts.start -> binary opus -> tts.stop
	if sttIdx >= firstSentenceIdx {
		t.Errorf("stt message (idx %d) must arrive before sentence_start (idx %d)", sttIdx, firstSentenceIdx)
	}
	if firstSentenceIdx >= ttsStartIdx {
		t.Errorf("sentence_start (idx %d) must arrive before tts.start (idx %d)", firstSentenceIdx, ttsStartIdx)
	}
	if ttsStartIdx >= firstAudioIdx {
		t.Errorf("tts.start (idx %d) must arrive before first audio packet (idx %d)", ttsStartIdx, firstAudioIdx)
	}
	if lastAudioIdx >= ttsStopIdx {
		t.Errorf("all audio packets (last idx %d) must arrive before tts.stop (idx %d)", lastAudioIdx, ttsStopIdx)
	}
}

// TestE2E_MultiTurn_Dialogue_Success 验证同一 WebSocket 连接下的连续多轮语音问答闭环：
// 包含 Round 1 与 Round 2 连续问答，断言第 2 轮大语言模型收到的 Messages 数组严格包含 Round 1 的完整历史上下文。
func TestE2E_MultiTurn_Dialogue_Success(t *testing.T) {
	const (
		round1ASRText       = "北京今天天气怎么样"
		round1Sentence1     = "北京今天天气晴朗，"
		round1Sentence2     = "适合出门散步。"
		round1AssistantText = round1Sentence1 + round1Sentence2

		round2ASRText       = "那明天呢"
		round2Sentence1     = "明天天气多云转阴，"
		round2Sentence2     = "气温稍微下降。"
		round2AssistantText = round2Sentence1 + round2Sentence2
	)

	// 1. 启动本地多轮回环假服务
	ttsChunk1 := generateSinePCM24k(1, 440.0)
	ttsChunk2 := generateSinePCM24k(1, 880.0)
	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{
			{text: round1ASRText, manual: false},
			{text: round2ASRText, manual: false},
		},
		[]mockTTSTurn{
			{pcmChunks: [][]byte{ttsChunk1, ttsChunk2}},
			{pcmChunks: [][]byte{ttsChunk1, ttsChunk2}},
		},
	)
	defer wsMock.Close()

	llmMock := newMultiTurnMockBailianLLMServer(t, [][]string{
		{round1Sentence1, round1Sentence2},
		{round2Sentence1, round2Sentence2},
	})
	defer llmMock.Close()

	// 2. 构造生产配置并装配 Server
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
				WSEndpoint:           wsMock.WSURL(),
				LLMEndpoint:          llmMock.BaseURL(),
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
		DashScopeAPIKey:   "test-multiturn-dashscope-secret",
		DeviceSharedToken: "test-multiturn-device-shared-token",
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
	defer srvCancel()

	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Run(srvCtx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)
	cfg.Server.WebSocketURL = fmt.Sprintf("ws://%s%s", addr, router.WebSocketPath)

	// 3. 配置发现 (OTA)
	otaURL := fmt.Sprintf("http://%s%s", addr, router.OTAPath)
	otaResp, err := http.Post(otaURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("failed to send OTA request: %v", err)
	}
	defer otaResp.Body.Close()

	if otaResp.StatusCode != http.StatusOK {
		t.Fatalf("expected OTA status 200, got %d", otaResp.StatusCode)
	}

	var otaData router.Response
	if err := json.NewDecoder(otaResp.Body).Decode(&otaData); err != nil {
		t.Fatalf("failed to decode OTA response: %v", err)
	}

	// 4. 建立 WebSocket 客户端连接并完成 Hello 协商
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + otaData.WebSocket.Token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"multiturn-device-001"},
			"Client-Id":        []string{"client-001"},
			"Serial-Number":    []string{"SN-multiturn-001"},
		},
	}

	conn, _, err := websocket.Dial(dialCtx, otaData.WebSocket.URL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test finished")

	clientHelloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	if err := conn.Write(context.Background(), websocket.MessageText, clientHelloJSON); err != nil {
		t.Fatalf("failed to send client hello: %v", err)
	}

	readHelloCtx, readHelloCancel := context.WithTimeout(context.Background(), 2*time.Second)
	mType, mData, err := conn.Read(readHelloCtx)
	readHelloCancel()
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}
	if mType != websocket.MessageText {
		t.Fatalf("expected text message for server hello, got %v", mType)
	}

	var srvHello session.ServerHelloMessage
	if err := json.Unmarshal(mData, &srvHello); err != nil {
		t.Fatalf("failed to unmarshal server hello: %v", err)
	}
	sessionID := srvHello.SessionID

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

	dec24k, err := opus.NewDecoder(24000, 1)
	if err != nil {
		t.Fatalf("failed to create 24k opus decoder: %v", err)
	}

	// 5. ===== 第一轮问答 (Round 1) =====
	listenStart1 := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStart1); err != nil {
		t.Fatalf("failed to send listen start round 1: %v", err)
	}

	pcm16kRound1 := generateSinePCM16k(2, 440.0)
	for _, pkt := range encodeOpusPackets16k(t, pcm16kRound1) {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet round 1: %v", err)
		}
	}

	round1Items := readDownstreamUntilTTSStop(t, conn, 5*time.Second)
	validateTurnItems(t, round1Items, sessionID, round1ASRText, []string{round1Sentence1, round1Sentence2}, dec24k)
	waitSessionState(t, sess, session.StateReady, 2*time.Second)

	historyRound1 := sess.History()
	if len(historyRound1) != 2 {
		t.Fatalf("expected 2 history items after round 1, got %d", len(historyRound1))
	}
	if historyRound1[0].Role != ai.RoleUser || historyRound1[0].Content != round1ASRText {
		t.Errorf("expected round 1 user text %q, got %+v", round1ASRText, historyRound1[0])
	}
	if historyRound1[1].Role != ai.RoleAssistant || historyRound1[1].Content != round1AssistantText {
		t.Errorf("expected round 1 assistant text %q, got %+v", round1AssistantText, historyRound1[1])
	}

	// 6. ===== 第二轮问答 (Round 2，同一 WebSocket 连接) =====
	listenStart2 := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStart2); err != nil {
		t.Fatalf("failed to send listen start round 2: %v", err)
	}

	pcm16kRound2 := generateSinePCM16k(2, 880.0)
	for _, pkt := range encodeOpusPackets16k(t, pcm16kRound2) {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet round 2: %v", err)
		}
	}

	round2Items := readDownstreamUntilTTSStop(t, conn, 5*time.Second)
	validateTurnItems(t, round2Items, sessionID, round2ASRText, []string{round2Sentence1, round2Sentence2}, dec24k)
	waitSessionState(t, sess, session.StateReady, 2*time.Second)

	// 7. 严格断言假 LLM 服务收到的请求与历史上下文
	requests := llmMock.ReceivedRequests()
	if len(requests) != 2 {
		t.Fatalf("expected 2 LLM requests, got %d", len(requests))
	}

	// 第 1 轮 LLM 请求消息校验：[System, Round 1 User]
	r1Msgs := requests[0].Messages
	if len(r1Msgs) != 2 {
		t.Fatalf("expected 2 messages in LLM request 1, got %d", len(r1Msgs))
	}
	if r1Msgs[0].Role != "system" || r1Msgs[0].Content != "你是小智助手。" {
		t.Errorf("unexpected LLM request 1 system message: %+v", r1Msgs[0])
	}
	if r1Msgs[1].Role != "user" || r1Msgs[1].Content != round1ASRText {
		t.Errorf("unexpected LLM request 1 user message: %+v", r1Msgs[1])
	}

	// 第 2 轮 LLM 请求消息校验：[System, Round 1 User, Round 1 Assistant, Round 2 User]
	r2Msgs := requests[1].Messages
	if len(r2Msgs) != 4 {
		t.Fatalf("expected 4 messages in LLM request 2 (with history), got %d", len(r2Msgs))
	}
	if r2Msgs[0].Role != "system" || r2Msgs[0].Content != "你是小智助手。" {
		t.Errorf("unexpected LLM request 2 system message: %+v", r2Msgs[0])
	}
	if r2Msgs[1].Role != "user" || r2Msgs[1].Content != round1ASRText {
		t.Errorf("expected LLM request 2 to contain round 1 user text %q, got %+v", round1ASRText, r2Msgs[1])
	}
	if r2Msgs[2].Role != "assistant" || r2Msgs[2].Content != round1AssistantText {
		t.Errorf("expected LLM request 2 to contain round 1 assistant text %q, got %+v", round1AssistantText, r2Msgs[2])
	}
	if r2Msgs[3].Role != "user" || r2Msgs[3].Content != round2ASRText {
		t.Errorf("expected LLM request 2 to contain round 2 user text %q, got %+v", round2ASRText, r2Msgs[3])
	}

	// 8. 验证 Session 最终历史记录包含 2 轮完整历史（4 条消息）
	historyFinal := sess.History()
	if len(historyFinal) != 4 {
		t.Fatalf("expected 4 history items after round 2, got %d", len(historyFinal))
	}
	if historyFinal[0].Role != ai.RoleUser || historyFinal[0].Content != round1ASRText {
		t.Errorf("expected history[0] to be user %q, got %+v", round1ASRText, historyFinal[0])
	}
	if historyFinal[1].Role != ai.RoleAssistant || historyFinal[1].Content != round1AssistantText {
		t.Errorf("expected history[1] to be assistant %q, got %+v", round1AssistantText, historyFinal[1])
	}
	if historyFinal[2].Role != ai.RoleUser || historyFinal[2].Content != round2ASRText {
		t.Errorf("expected history[2] to be user %q, got %+v", round2ASRText, historyFinal[2])
	}
	if historyFinal[3].Role != ai.RoleAssistant || historyFinal[3].Content != round2AssistantText {
		t.Errorf("expected history[3] to be assistant %q, got %+v", round2AssistantText, historyFinal[3])
	}

	// 9. 客户端退出与停服会话归零
	_ = conn.Close(websocket.StatusNormalClosure, "multiturn test exit")
	srvCancel()
	<-srvErrCh

	if registry.ActiveCount() != 0 {
		t.Errorf("expected active session count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Errorf("expected limiter count 0, got %d", limiter.ActiveCount())
	}
}

// TestE2E_ManualMode_FullLoop_Success 验证 manual 模式下的完整闭环流程：
// listen.start (manual) -> 16 kHz Opus 上行 -> listen.stop -> STT -> LLM -> 按句 TTS -> 60 ms Opus 下行 -> tts.stop -> READY。
func TestE2E_ManualMode_FullLoop_Success(t *testing.T) {
	const (
		expectedASRText       = "手动按键收音指令测试"
		expectedSentence1     = "已接收手动指令，"
		expectedSentence2     = "为您完成操作。"
		expectedAssistantText = expectedSentence1 + expectedSentence2
	)

	// 1. 启动本地假服务（ASR 设置为 manual 模式，必须等 finish-task 才会结束并返回识别文本）
	ttsChunk1 := generateSinePCM24k(1, 440.0)
	ttsChunk2 := generateSinePCM24k(1, 880.0)
	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{
			{text: expectedASRText, manual: true},
		},
		[]mockTTSTurn{
			{pcmChunks: [][]byte{ttsChunk1, ttsChunk2}},
		},
	)
	defer wsMock.Close()

	llmMock := newMockBailianLLMServer(t, []string{expectedSentence1, expectedSentence2})
	defer llmMock.Close()

	// 2. 构造生产配置并装配 Server
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
				WSEndpoint:           wsMock.WSURL(),
				LLMEndpoint:          llmMock.BaseURL(),
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
		DashScopeAPIKey:   "test-manual-dashscope-secret",
		DeviceSharedToken: "test-manual-device-shared-token",
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
	defer srvCancel()

	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Run(srvCtx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)
	cfg.Server.WebSocketURL = fmt.Sprintf("ws://%s%s", addr, router.WebSocketPath)

	// 3. 配置发现 (OTA)
	otaURL := fmt.Sprintf("http://%s%s", addr, router.OTAPath)
	otaResp, err := http.Post(otaURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("failed to send OTA request: %v", err)
	}
	defer otaResp.Body.Close()

	var otaData router.Response
	_ = json.NewDecoder(otaResp.Body).Decode(&otaData)

	// 4. 建立 WebSocket 客户端连接并完成 Hello 协商
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + otaData.WebSocket.Token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"manual-device-001"},
			"Client-Id":        []string{"client-001"},
			"Serial-Number":    []string{"SN-manual-001"},
		},
	}

	conn, _, err := websocket.Dial(dialCtx, otaData.WebSocket.URL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test finished")

	clientHelloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	if err := conn.Write(context.Background(), websocket.MessageText, clientHelloJSON); err != nil {
		t.Fatalf("failed to send client hello: %v", err)
	}

	readHelloCtx, readHelloCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, mData, err := conn.Read(readHelloCtx)
	readHelloCancel()
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}

	var srvHello session.ServerHelloMessage
	_ = json.Unmarshal(mData, &srvHello)
	sessionID := srvHello.SessionID

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

	dec24k, err := opus.NewDecoder(24000, 1)
	if err != nil {
		t.Fatalf("failed to create 24k opus decoder: %v", err)
	}

	// 5. 客户端发送 listen.start (manual)
	listenStartJSON := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartJSON); err != nil {
		t.Fatalf("failed to send listen.start manual: %v", err)
	}

	waitSessionState(t, sess, session.StateListening, 2*time.Second)
	if sess.Mode() != session.ListenModeManual {
		t.Fatalf("expected session mode %q, got %q", session.ListenModeManual, sess.Mode())
	}

	// 6. 发送 16 kHz Opus 上行音频包
	pcm16k := generateSinePCM16k(3, 440.0)
	for _, pkt := range encodeOpusPackets16k(t, pcm16k) {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet: %v", err)
		}
	}

	// 7. 客户端显式发送 listen.stop
	listenStopJSON := []byte(`{"type":"listen","state":"stop"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStopJSON); err != nil {
		t.Fatalf("failed to send listen.stop: %v", err)
	}

	// 8. 持续读取下行消息直至 tts.stop
	items := readDownstreamUntilTTSStop(t, conn, 5*time.Second)
	validateTurnItems(t, items, sessionID, expectedASRText, []string{expectedSentence1, expectedSentence2}, dec24k)
	waitSessionState(t, sess, session.StateReady, 2*time.Second)

	// 9. 验证会话历史
	history := sess.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(history))
	}
	if history[0].Role != ai.RoleUser || history[0].Content != expectedASRText {
		t.Errorf("expected user text %q, got %+v", expectedASRText, history[0])
	}
	if history[1].Role != ai.RoleAssistant || history[1].Content != expectedAssistantText {
		t.Errorf("expected assistant text %q, got %+v", expectedAssistantText, history[1])
	}

	// 10. 正常退出与名额释放
	_ = conn.Close(websocket.StatusNormalClosure, "manual test exit")
	srvCancel()
	<-srvErrCh

	if registry.ActiveCount() != 0 {
		t.Errorf("expected active session count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Errorf("expected limiter count 0, got %d", limiter.ActiveCount())
	}
}

// TestE2E_AudioDiscard_ReadyAndPostASR_Success 验证两类额外音频的安全静默丢弃机制：
// 1. 在 READY 状态下提前发送的 Opus 音频包被静默丢弃，不触发 ASR，不污染状态；
// 2. 在 ASR 识别产生后（PROCESSING / SPEAKING 阶段）继续发送的 Opus 音频包被静默丢弃，不串入下一轮问答。
func TestE2E_AudioDiscard_ReadyAndPostASR_Success(t *testing.T) {
	const (
		round1ASRText  = "第一轮正常识别"
		round1Sentence = "第一轮回答完毕。"
		round2ASRText  = "第二轮正常识别"
		round2Sentence = "第二轮回答完毕。"
	)

	// 1. 启动本地回环假服务
	ttsChunk1 := generateSinePCM24k(1, 440.0)
	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{
			{text: round1ASRText, manual: false},
			{text: round2ASRText, manual: false},
		},
		[]mockTTSTurn{
			{pcmChunks: [][]byte{ttsChunk1}},
			{pcmChunks: [][]byte{ttsChunk1}},
		},
	)
	defer wsMock.Close()

	llmMock := newMultiTurnMockBailianLLMServer(t, [][]string{
		{round1Sentence},
		{round2Sentence},
	})
	defer llmMock.Close()

	// 2. 构造生产配置并装配 Server
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
				WSEndpoint:           wsMock.WSURL(),
				LLMEndpoint:          llmMock.BaseURL(),
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
		DashScopeAPIKey:   "test-discard-dashscope-secret",
		DeviceSharedToken: "test-discard-device-shared-token",
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
	defer srvCancel()

	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Run(srvCtx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)
	cfg.Server.WebSocketURL = fmt.Sprintf("ws://%s%s", addr, router.WebSocketPath)

	// 3. 配置发现 (OTA)
	otaURL := fmt.Sprintf("http://%s%s", addr, router.OTAPath)
	otaResp, err := http.Post(otaURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("failed to send OTA request: %v", err)
	}
	defer otaResp.Body.Close()

	var otaData router.Response
	_ = json.NewDecoder(otaResp.Body).Decode(&otaData)

	// 4. 建立 WebSocket 客户端连接并完成 Hello 协商
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + otaData.WebSocket.Token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"discard-device-001"},
			"Client-Id":        []string{"client-001"},
			"Serial-Number":    []string{"SN-discard-001"},
		},
	}

	conn, _, err := websocket.Dial(dialCtx, otaData.WebSocket.URL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test finished")

	clientHelloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	if err := conn.Write(context.Background(), websocket.MessageText, clientHelloJSON); err != nil {
		t.Fatalf("failed to send client hello: %v", err)
	}

	readHelloCtx, readHelloCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, mData, err := conn.Read(readHelloCtx)
	readHelloCancel()
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}

	var srvHello session.ServerHelloMessage
	_ = json.Unmarshal(mData, &srvHello)
	sessionID := srvHello.SessionID

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

	dec24k, err := opus.NewDecoder(24000, 1)
	if err != nil {
		t.Fatalf("failed to create 24k opus decoder: %v", err)
	}

	// 5. 【阶段 1：READY 状态提前音频丢弃验证】
	// 在 READY 状态（尚未发送 listen.start）发送 3 个上行 Opus 音频包
	readyEarlyPCM := generateSinePCM16k(3, 440.0)
	for _, pkt := range encodeOpusPackets16k(t, readyEarlyPCM) {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send early opus packet in ready state: %v", err)
		}
	}

	// 断言状态依然保持为 READY，没有触发 ASR 或 LLM，连接正常保持
	waitSessionState(t, sess, session.StateReady, 1*time.Second)
	if len(sess.History()) != 0 {
		t.Fatalf("expected history to be empty after ready audio discard, got %d", len(sess.History()))
	}
	if len(llmMock.ReceivedRequests()) != 0 {
		t.Fatalf("expected 0 LLM requests after ready audio discard, got %d", len(llmMock.ReceivedRequests()))
	}

	// 6. 【阶段 2：启动 Round 1 并在 ASR 识别产生后发送多余音频】
	listenStart1 := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStart1); err != nil {
		t.Fatalf("failed to send listen start round 1: %v", err)
	}

	pcm16kRound1 := generateSinePCM16k(2, 440.0)
	for _, pkt := range encodeOpusPackets16k(t, pcm16kRound1) {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet round 1: %v", err)
		}
	}

	// 持续读取直到读到 STT 消息（确认 ASR 识别已完成）
	var round1Received []e2eReceivedItem
	readLoopCtx, readLoopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readLoopCancel()

	for {
		mType, data, rErr := conn.Read(readLoopCtx)
		if rErr != nil {
			t.Fatalf("failed to read STT message: %v", rErr)
		}
		item := e2eReceivedItem{msgType: mType, payload: data}
		if mType == websocket.MessageText {
			var base struct {
				Type      string `json:"type"`
				State     string `json:"state"`
				Text      string `json:"text"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &base); err == nil {
				item.textType = base.Type
				item.state = base.State
				item.text = base.Text
				item.sessionID = base.SessionID
			}
		}
		round1Received = append(round1Received, item)
		if item.textType == session.MessageTypeSTT {
			break
		}
	}

	// 此时 ASR 已结束，Session 处于 PROCESSING 或 SPEAKING 阶段
	// 发送 4 个多余的 16 kHz Opus 音频包，断言被静默安全丢弃
	extraPCM := generateSinePCM16k(4, 550.0)
	for _, pkt := range encodeOpusPackets16k(t, extraPCM) {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send extra post-ASR opus packet: %v", err)
		}
	}

	// 继续读取下行消息直至 tts.stop
	for {
		mType, data, rErr := conn.Read(readLoopCtx)
		if rErr != nil {
			t.Fatalf("failed to read downstream message after extra audio: %v", rErr)
		}
		item := e2eReceivedItem{msgType: mType, payload: data}
		if mType == websocket.MessageText {
			var base struct {
				Type      string `json:"type"`
				State     string `json:"state"`
				Text      string `json:"text"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &base); err == nil {
				item.textType = base.Type
				item.state = base.State
				item.text = base.Text
				item.sessionID = base.SessionID
			}
		}
		round1Received = append(round1Received, item)
		if item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop {
			break
		}
	}

	validateTurnItems(t, round1Received, sessionID, round1ASRText, []string{round1Sentence}, dec24k)
	waitSessionState(t, sess, session.StateReady, 2*time.Second)

	// 断言第 1 轮历史严格记录 1 轮正常问答，未受多余音频影响
	historyRound1 := sess.History()
	if len(historyRound1) != 2 {
		t.Fatalf("expected 2 history items after round 1, got %d", len(historyRound1))
	}
	if historyRound1[0].Role != ai.RoleUser || historyRound1[0].Content != round1ASRText {
		t.Errorf("expected round 1 user text %q, got %+v", round1ASRText, historyRound1[0])
	}
	if historyRound1[1].Role != ai.RoleAssistant || historyRound1[1].Content != round1Sentence {
		t.Errorf("expected round 1 assistant text %q, got %+v", round1Sentence, historyRound1[1])
	}

	// 7. 【阶段 3：在同一连接上启动 Round 2 验证多余音频未串入下一轮】
	listenStart2 := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStart2); err != nil {
		t.Fatalf("failed to send listen start round 2: %v", err)
	}

	pcm16kRound2 := generateSinePCM16k(2, 880.0)
	for _, pkt := range encodeOpusPackets16k(t, pcm16kRound2) {
		if err := conn.Write(context.Background(), websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send uplink opus packet round 2: %v", err)
		}
	}

	round2Items := readDownstreamUntilTTSStop(t, conn, 5*time.Second)
	validateTurnItems(t, round2Items, sessionID, round2ASRText, []string{round2Sentence}, dec24k)
	waitSessionState(t, sess, session.StateReady, 2*time.Second)

	// 断言第 2 轮 LLM 请求消息严格只包含 Round 1 历史与 Round 2 当前输入，无额外串入内容
	requests := llmMock.ReceivedRequests()
	if len(requests) != 2 {
		t.Fatalf("expected 2 LLM requests, got %d", len(requests))
	}
	r2Msgs := requests[1].Messages
	if len(r2Msgs) != 4 {
		t.Fatalf("expected 4 messages in LLM request 2, got %d", len(r2Msgs))
	}
	if r2Msgs[1].Role != "user" || r2Msgs[1].Content != round1ASRText {
		t.Errorf("unexpected round 1 user message in request 2: %+v", r2Msgs[1])
	}
	if r2Msgs[2].Role != "assistant" || r2Msgs[2].Content != round1Sentence {
		t.Errorf("unexpected round 1 assistant message in request 2: %+v", r2Msgs[2])
	}
	if r2Msgs[3].Role != "user" || r2Msgs[3].Content != round2ASRText {
		t.Errorf("unexpected round 2 user message in request 2: %+v", r2Msgs[3])
	}

	// 最终历史记录断言
	historyFinal := sess.History()
	if len(historyFinal) != 4 {
		t.Fatalf("expected 4 history items after round 2, got %d", len(historyFinal))
	}

	// 8. 客户端退出与停服会话归零
	_ = conn.Close(websocket.StatusNormalClosure, "discard test exit")
	srvCancel()
	<-srvErrCh

	if registry.ActiveCount() != 0 {
		t.Errorf("expected active session count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Errorf("expected limiter count 0, got %d", limiter.ActiveCount())
	}
}

// TestE2E_RealtimeMode_Rejected 验证发送 realtime 监听模式时服务端明确拒绝并以 StatusPolicyViolation 关闭连接。
func TestE2E_RealtimeMode_Rejected(t *testing.T) {
	// 1. 构造生产配置并装配 Server
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
				WSEndpoint:           "ws://127.0.0.1:9999/api-ws/v1/inference",
				LLMEndpoint:          "http://127.0.0.1:9999/compatible-mode/v1",
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
		DashScopeAPIKey:   "test-realtime-dashscope-secret",
		DeviceSharedToken: "test-realtime-device-shared-token",
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
	defer srvCancel()

	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Run(srvCtx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)
	cfg.Server.WebSocketURL = fmt.Sprintf("ws://%s%s", addr, router.WebSocketPath)

	// 2. 配置发现 (OTA)
	otaURL := fmt.Sprintf("http://%s%s", addr, router.OTAPath)
	otaResp, err := http.Post(otaURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("failed to send OTA request: %v", err)
	}
	defer otaResp.Body.Close()

	var otaData router.Response
	_ = json.NewDecoder(otaResp.Body).Decode(&otaData)

	// 3. 建立 WebSocket 客户端连接并完成 Hello 协商
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + otaData.WebSocket.Token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"realtime-device-001"},
			"Client-Id":        []string{"client-001"},
			"Serial-Number":    []string{"SN-realtime-001"},
		},
	}

	conn, _, err := websocket.Dial(dialCtx, otaData.WebSocket.URL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test finished")

	clientHelloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	if err := conn.Write(context.Background(), websocket.MessageText, clientHelloJSON); err != nil {
		t.Fatalf("failed to send client hello: %v", err)
	}

	readHelloCtx, readHelloCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _, err = conn.Read(readHelloCtx)
	readHelloCancel()
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}

	// 4. 发送 listen.start (realtime)，断言服务端明确拒绝并关闭连接
	listenStartRealtime := []byte(`{"type":"listen","state":"start","mode":"realtime"}`)
	if err := conn.Write(context.Background(), websocket.MessageText, listenStartRealtime); err != nil {
		t.Fatalf("failed to send listen start realtime: %v", err)
	}

	// 5. 读取下行消息，断言连接因 Policy Violation (1008) 关闭
	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()

	_, _, readErr := conn.Read(readCtx)
	if readErr == nil {
		t.Fatal("expected websocket read error after realtime mode request, got nil")
	}

	closeStatus := websocket.CloseStatus(readErr)
	if closeStatus != websocket.StatusPolicyViolation {
		t.Errorf("expected close status StatusPolicyViolation (1008), got %v (err: %v)", closeStatus, readErr)
	}

	// 6. 服务端活跃会话数在关闭后归零
	deadline := time.Now().Add(2 * time.Second)
	for registry.ActiveCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if registry.ActiveCount() != 0 {
		t.Errorf("expected active session count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Errorf("expected limiter count 0, got %d", limiter.ActiveCount())
	}

	srvCancel()
	<-srvErrCh
}
