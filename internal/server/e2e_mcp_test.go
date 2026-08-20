package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai/bailian"
	"xiaozhi-esp32-golang-server/internal/bootstrap"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/server"
	"xiaozhi-esp32-golang-server/internal/session"
)

// TestE2E_MCP_ToolCallFullCycle 验证从“建立连接 -> MCP初始化与工具发现 -> 用户语音指令 -> LLM工具调用 -> MCP指令下发与设备执行 -> LLM总结 -> TTS播报”的端到端完整闭环。
func TestE2E_MCP_ToolCallFullCycle(t *testing.T) {
	// 1. 启动百炼 WebSocket 模拟服务（ASR 与 TTS）
	pcmData := generateSinePCM24k(4, 440)
	wsMock := newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{{text: "把音量调到80", manual: false}},
		[]mockTTSTurn{{pcmChunks: [][]byte{pcmData}}},
	)
	defer wsMock.Close()

	// 2. 启动百炼 LLM HTTP 模拟服务（第 1 次返回 tool_calls，第 2 次返回最终文本）
	var llmMu sync.Mutex
	llmCallCount := 0
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmMu.Lock()
		llmCallCount++
		currentCall := llmCallCount
		llmMu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "flusher unsupported", http.StatusInternalServerError)
			return
		}

		if currentCall == 1 {
			// 第 1 次调用：输出工具调用指令
			chunks := []string{
				`data: {"id":"call-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_vol_123","type":"function","function":{"name":"self.audio_speaker.set_volume","arguments":"{\"volume\": 80}"}}]}}]}` + "\n\n",
				`data: {"id":"call-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
				"data: [DONE]\n\n",
			}
			for _, c := range chunks {
				_, _ = w.Write([]byte(c))
				flusher.Flush()
			}
		} else {
			// 第 2 次调用：输出最终确认文本
			chunks := []string{
				`data: {"id":"call-2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"好的，音量已调至80。"},"finish_reason":null}]}` + "\n\n",
				`data: {"id":"call-2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
				"data: [DONE]\n\n",
			}
			for _, c := range chunks {
				_, _ = w.Write([]byte(c))
				flusher.Flush()
			}
		}
	}))
	defer llmServer.Close()

	// 3. 构建配置并装配 Server 实例
	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            "127.0.0.1:0",
			WebSocketURL:          "ws://127.0.0.1:0" + session.WebSocketPath,
			MaxConcurrentSessions: 5,
			ShutdownTimeout:       5 * time.Second,
			HTTPReadTimeout:       10 * time.Second,
			HTTPWriteTimeout:      10 * time.Second,
			HTTPIdleTimeout:       30 * time.Second,
			MaxHTTPBodyBytes:      65536,
			MaxHTTPHeaderBytes:    1024,
		},
		Session: config.SessionConfig{
			HelloTimeout:              5 * time.Second,
			MaxWSTextMessageBytes:     32768,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      10 * time.Second,
			ASRPCMQueueCapacity:       100,
			TTSPCMQueueCapacity:       100,
			DownlinkOpusQueueCapacity: 100,
			MaxHistoryTurns:           6,
			SystemPrompt:              "你是小智助手。",
		},
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:           wsMock.WSURL(),
				LLMEndpoint:          llmServer.URL,
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
		DashScopeAPIKey:   "test-e2e-dashscope-secret",
		DeviceSharedToken: "test-mcp-token",
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
	defer srvCancel()

	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Run(srvCtx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)
	wsURL := fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer test-mcp-token"},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"10:52:1c:7a:5e:30"},
			"Client-Id":        []string{"test-mcp-client"},
		},
	}

	conn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// 4. 发送客户端 Hello 声明 features.mcp = true
	helloJSON := `{
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
	if err := conn.Write(ctx, websocket.MessageText, []byte(helloJSON)); err != nil {
		t.Fatalf("failed to write client hello: %v", err)
	}

	// 5. 接收服务端 Hello
	_, respData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}
	var serverHello session.ServerHelloMessage
	if err := json.Unmarshal(respData, &serverHello); err != nil {
		t.Fatalf("failed to unmarshal server hello: %v", err)
	}

	// 6. 响应服务端发起的 MCP initialize
	_, initReqData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read mcp initialize: %v", err)
	}
	var initReq struct {
		Type    string `json:"type"`
		Payload struct {
			ID int64 `json:"id"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(initReqData, &initReq)
	initResp := fmt.Sprintf(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":%d,"result":{"serverInfo":{"name":"esp32-s3","version":"1.0"}}}}`, initReq.Payload.ID)
	if err := conn.Write(ctx, websocket.MessageText, []byte(initResp)); err != nil {
		t.Fatalf("failed to write mcp init resp: %v", err)
	}

	// 7. 响应服务端发起的 MCP tools/list
	_, listReqData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read mcp tools/list: %v", err)
	}
	var listReq struct {
		Type    string `json:"type"`
		Payload struct {
			ID int64 `json:"id"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(listReqData, &listReq)
	listResp := fmt.Sprintf(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"self.audio_speaker.set_volume","description":"Set speaker volume","inputSchema":{"type":"object","properties":{"volume":{"type":"integer"}}}}]}}}`, listReq.Payload.ID)
	if err := conn.Write(ctx, websocket.MessageText, []byte(listResp)); err != nil {
		t.Fatalf("failed to write mcp tools/list resp: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// 8. 触发用户收音模式
	listenStart := `{"type":"listen","state":"start","mode":"auto"}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(listenStart)); err != nil {
		t.Fatalf("failed to send listen.start: %v", err)
	}

	// 发送 2 包上行 Opus 音频帧
	pcm16k := generateSinePCM16k(2, 440)
	opusPackets := encodeOpusPackets16k(t, pcm16k)
	for _, pkt := range opusPackets {
		if err := conn.Write(ctx, websocket.MessageBinary, pkt); err != nil {
			t.Fatalf("failed to send opus audio packet: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 9. 接收消息流：处理下发的 MCP tools/call 指令并回复结果
	gotToolCall := false
	gotSentenceStart := false
	gotTTSAudio := false
	gotTTSStop := false

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("unexpected read error: %v", err)
		}

		if msgType == websocket.MessageBinary {
			gotTTSAudio = true
			continue
		}

		var generic struct {
			Type    string `json:"type"`
			State   string `json:"state"`
			Payload struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &generic); err == nil {
			if generic.Type == "mcp" && generic.Payload.Method == "tools/call" {
				gotToolCall = true
				toolName, _ := generic.Payload.Params["name"].(string)
				if toolName != "self.audio_speaker.set_volume" {
					t.Errorf("expected tool call self.audio_speaker.set_volume, got %s", toolName)
				}
				// 模拟硬件执行完毕并返回结果
				toolResp := fmt.Sprintf(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"true"}],"isError":false}}}`, generic.Payload.ID)
				if err := conn.Write(ctx, websocket.MessageText, []byte(toolResp)); err != nil {
					t.Fatalf("failed to write tool call response: %v", err)
				}
			} else if generic.Type == "tts" && generic.State == "sentence_start" {
				gotSentenceStart = true
			} else if generic.Type == "tts" && generic.State == "stop" {
				gotTTSStop = true
				break
			}
		}
	}

	if !gotToolCall {
		t.Errorf("expected MCP tools/call message received by client")
	}
	if !gotSentenceStart {
		t.Errorf("expected TTS sentence_start message received by client")
	}
	if !gotTTSAudio {
		t.Errorf("expected TTS binary opus audio received by client")
	}
	if !gotTTSStop {
		t.Errorf("expected TTS stop message received by client")
	}
}
