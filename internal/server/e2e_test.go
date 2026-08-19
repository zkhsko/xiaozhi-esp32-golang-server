package server_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hraban/opus"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/ai/bailian"
	"xiaozhi-esp32-golang-server/internal/bootstrap"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/server"
	"xiaozhi-esp32-golang-server/internal/session"
)

// mockASRTurn 描述单轮 ASR 模拟配置。
type mockASRTurn struct {
	text      string
	manual    bool
	fail      bool
	errorCode string
	errorMsg  string
}

// mockTTSTurn 描述单轮 TTS 模拟配置。
type mockTTSTurn struct {
	pcmChunks       [][]byte
	fail            bool
	failAfterChunks int
	errorCode       string
	errorMsg        string
}

// mockBailianWSServer 实现本地模拟的百炼 ASR 与 TTS WebSocket 流式服务，支持多轮与 manual 模式。
type mockBailianWSServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	asrTurns []mockASRTurn
	ttsTurns []mockTTSTurn
	asrIndex int
	ttsIndex int
}

func newMockBailianWSServer(t *testing.T, asrFinalText string, ttsPCMChunks [][]byte) *mockBailianWSServer {
	return newMultiTurnMockBailianWSServer(t,
		[]mockASRTurn{{text: asrFinalText, manual: false}},
		[]mockTTSTurn{{pcmChunks: ttsPCMChunks}},
	)
}

func newMultiTurnMockBailianWSServer(t *testing.T, asrTurns []mockASRTurn, ttsTurns []mockTTSTurn) *mockBailianWSServer {
	t.Helper()
	m := &mockBailianWSServer{
		asrTurns: asrTurns,
		ttsTurns: ttsTurns,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "closed")

		// 1. 读取首条 run-task 消息
		msgType, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if msgType != websocket.MessageText {
			return
		}

		var req struct {
			Header struct {
				Action string `json:"action"`
				TaskID string `json:"task_id"`
			} `json:"header"`
			Payload struct {
				Task string `json:"task"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}

		taskID := req.Header.TaskID
		taskType := req.Payload.Task

		// 回复 task-started 响应
		startedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": taskID,
				"event":   "task-started",
			},
		}
		startedBytes, _ := json.Marshal(startedResp)
		if err := conn.Write(r.Context(), websocket.MessageText, startedBytes); err != nil {
			return
		}

		if taskType == "asr" {
			m.mu.Lock()
			idx := m.asrIndex
			if idx < len(m.asrTurns) {
				m.asrIndex++
			}
			var curTurn mockASRTurn
			if idx < len(m.asrTurns) {
				curTurn = m.asrTurns[idx]
			} else if len(m.asrTurns) > 0 {
				curTurn = m.asrTurns[len(m.asrTurns)-1]
			}
			m.mu.Unlock()

			// ASR 识别流：接收上行 PCM 帧，并在符合条件时返回最终识别文本
			for {
				mType, mData, rErr := conn.Read(r.Context())
				if rErr != nil {
					break
				}
				if curTurn.fail {
					errCode := curTurn.errorCode
					if errCode == "" {
						errCode = "ASR_FAILED"
					}
					errMsg := curTurn.errorMsg
					if errMsg == "" {
						errMsg = "mock asr failed"
					}
					failResp := map[string]any{
						"header": map[string]any{
							"action":        "task-failed",
							"task_id":       taskID,
							"event":         "task-failed",
							"error_code":    errCode,
							"error_message": errMsg,
						},
					}
					failBytes, _ := json.Marshal(failResp)
					_ = conn.Write(r.Context(), websocket.MessageText, failBytes)
					_ = conn.Close(websocket.StatusNormalClosure, "asr failed")
					return
				}
				if curTurn.manual {
					// manual 模式：持续接收 PCM 二进制，直到收到 finish-task 文本指令
					if mType == websocket.MessageText {
						var finishReq struct {
							Header struct {
								Action string `json:"action"`
							} `json:"header"`
						}
						_ = json.Unmarshal(mData, &finishReq)
						if finishReq.Header.Action == "finish-task" {
							finalResp := map[string]any{
								"header": map[string]any{
									"action":  "task-finished",
									"task_id": taskID,
									"event":   "task-finished",
								},
								"payload": map[string]any{
									"output": map[string]any{
										"sentence": map[string]any{
											"sentence_id":    1,
											"sentence_begin": true,
											"sentence_end":   true,
											"text":           curTurn.text,
										},
										"text": curTurn.text,
									},
								},
							}
							finalBytes, _ := json.Marshal(finalResp)
							_ = conn.Write(r.Context(), websocket.MessageText, finalBytes)
							_ = conn.Close(websocket.StatusNormalClosure, "asr finished")
							return
						}
					}
				} else {
					// auto 模式：收到音频数据或 finish-task 立即返回识别结果
					if mType == websocket.MessageBinary || mType == websocket.MessageText {
						finalResp := map[string]any{
							"header": map[string]any{
								"action":  "task-finished",
								"task_id": taskID,
								"event":   "task-finished",
							},
							"payload": map[string]any{
								"output": map[string]any{
									"sentence": map[string]any{
										"sentence_id":    1,
										"sentence_begin": true,
										"sentence_end":   true,
										"text":           curTurn.text,
									},
									"text": curTurn.text,
								},
							},
						}
						finalBytes, _ := json.Marshal(finalResp)
						_ = conn.Write(r.Context(), websocket.MessageText, finalBytes)
						_ = conn.Close(websocket.StatusNormalClosure, "asr finished")
						return
					}
				}
			}
		} else if taskType == "tts" {
			m.mu.Lock()
			idx := m.ttsIndex
			if idx < len(m.ttsTurns) {
				m.ttsIndex++
			}
			var curTurn mockTTSTurn
			if idx < len(m.ttsTurns) {
				curTurn = m.ttsTurns[idx]
			} else if len(m.ttsTurns) > 0 {
				curTurn = m.ttsTurns[len(m.ttsTurns)-1]
			}
			m.mu.Unlock()

			// TTS 合成流：接收 continue-task 和 finish-task 消息，并下发 24 kHz PCM 数据
			chunkIdx := 0
			for {
				mType, mData, rErr := conn.Read(r.Context())
				if rErr != nil {
					break
				}
				if mType != websocket.MessageText {
					continue
				}

				var ttsReq struct {
					Header struct {
						Action string `json:"action"`
					} `json:"header"`
				}
				if err := json.Unmarshal(mData, &ttsReq); err != nil {
					continue
				}

				if ttsReq.Header.Action == "continue-task" {
					if curTurn.fail && curTurn.failAfterChunks == 0 {
						errCode := curTurn.errorCode
						if errCode == "" {
							errCode = "TTS_FAILED"
						}
						errMsg := curTurn.errorMsg
						if errMsg == "" {
							errMsg = "mock tts failed"
						}
						failResp := map[string]any{
							"header": map[string]any{
								"action":        "task-failed",
								"task_id":       taskID,
								"event":         "task-failed",
								"error_code":    errCode,
								"error_message": errMsg,
							},
						}
						failBytes, _ := json.Marshal(failResp)
						_ = conn.Write(r.Context(), websocket.MessageText, failBytes)
						_ = conn.Close(websocket.StatusNormalClosure, "tts failed")
						return
					}

					if chunkIdx < len(curTurn.pcmChunks) {
						_ = conn.Write(r.Context(), websocket.MessageBinary, curTurn.pcmChunks[chunkIdx])
						chunkIdx++
					}

					if curTurn.fail && curTurn.failAfterChunks > 0 && chunkIdx >= curTurn.failAfterChunks {
						errCode := curTurn.errorCode
						if errCode == "" {
							errCode = "TTS_FAILED"
						}
						errMsg := curTurn.errorMsg
						if errMsg == "" {
							errMsg = "mock tts failed"
						}
						failResp := map[string]any{
							"header": map[string]any{
								"action":        "task-failed",
								"task_id":       taskID,
								"event":         "task-failed",
								"error_code":    errCode,
								"error_message": errMsg,
							},
						}
						failBytes, _ := json.Marshal(failResp)
						_ = conn.Write(r.Context(), websocket.MessageText, failBytes)
						_ = conn.Close(websocket.StatusNormalClosure, "tts failed")
						return
					}
				} else if ttsReq.Header.Action == "finish-task" {
					for chunkIdx < len(curTurn.pcmChunks) {
						_ = conn.Write(r.Context(), websocket.MessageBinary, curTurn.pcmChunks[chunkIdx])
						chunkIdx++
					}
					finishedResp := map[string]any{
						"header": map[string]any{
							"action":  "task-finished",
							"task_id": taskID,
							"event":   "task-finished",
						},
					}
					finBytes, _ := json.Marshal(finishedResp)
					_ = conn.Write(r.Context(), websocket.MessageText, finBytes)
					_ = conn.Close(websocket.StatusNormalClosure, "tts finished")
					return
				}
			}
		}
	})

	m.server = httptest.NewServer(handler)
	return m
}

func (m *mockBailianWSServer) WSURL() string {
	return strings.Replace(m.server.URL, "http://", "ws://", 1) + "/api-ws/v1/inference"
}

func (m *mockBailianWSServer) Close() {
	m.server.Close()
}

// llmMessageRecord 记录 mock LLM 收到的单条聊天消息。
type llmMessageRecord struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// llmRequestRecord 记录 mock LLM 收到的完整请求体。
type llmRequestRecord struct {
	Model    string             `json:"model"`
	Messages []llmMessageRecord `json:"messages"`
}

// mockBailianLLMServer 实现本地模拟的百炼 OpenAI 兼容 Chat Completions SSE 流式服务。
type mockBailianLLMServer struct {
	server           *httptest.Server
	mu               sync.Mutex
	turnChunks       [][]string
	turnIndex        int
	turnFailures     map[int]int
	receivedRequests []llmRequestRecord
}

func newMockBailianLLMServer(t *testing.T, chunks []string) *mockBailianLLMServer {
	return newMultiTurnMockBailianLLMServer(t, [][]string{chunks})
}

func newMultiTurnMockBailianLLMServer(t *testing.T, turnChunks [][]string) *mockBailianLLMServer {
	return newMultiTurnMockBailianLLMServerWithFailures(t, turnChunks, nil)
}

func newMultiTurnMockBailianLLMServerWithFailures(t *testing.T, turnChunks [][]string, turnFailures map[int]int) *mockBailianLLMServer {
	t.Helper()
	m := &mockBailianLLMServer{
		turnChunks:   turnChunks,
		turnFailures: turnFailures,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// 读取并记录客户端发送的 Messages 列表
		var req llmRequestRecord
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			_ = json.Unmarshal(bodyBytes, &req)
		}

		m.mu.Lock()
		m.receivedRequests = append(m.receivedRequests, req)
		curIdx := m.turnIndex
		if curIdx < len(m.turnChunks) {
			m.turnIndex++
		}
		var chunks []string
		if curIdx < len(m.turnChunks) {
			chunks = m.turnChunks[curIdx]
		} else if len(m.turnChunks) > 0 {
			chunks = m.turnChunks[len(m.turnChunks)-1]
		}
		failStatus := 0
		if m.turnFailures != nil {
			failStatus = m.turnFailures[curIdx]
		}
		m.mu.Unlock()

		if failStatus != 0 {
			http.Error(w, fmt.Sprintf("mock llm failure status %d", failStatus), failStatus)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "flusher not supported", http.StatusInternalServerError)
			return
		}

		for i, text := range chunks {
			delta := map[string]any{
				"content": text,
			}
			if i == 0 {
				delta["role"] = "assistant"
			}
			chunkData := map[string]any{
				"id":     "chatcmpl-e2e-test",
				"object": "chat.completion.chunk",
				"choices": []map[string]any{
					{
						"index": 0,
						"delta": delta,
					},
				},
			}
			jsonBytes, _ := json.Marshal(chunkData)
			fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
			flusher.Flush()
		}

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	m.server = httptest.NewServer(handler)
	return m
}

func (m *mockBailianLLMServer) BaseURL() string {
	return m.server.URL + "/compatible-mode/v1"
}

func (m *mockBailianLLMServer) Close() {
	m.server.Close()
}

func (m *mockBailianLLMServer) ReceivedRequests() []llmRequestRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]llmRequestRecord, len(m.receivedRequests))
	copy(res, m.receivedRequests)
	return res
}

// readDownstreamUntilTTSStop 持续读取下行消息直至收到 tts.stop。
func readDownstreamUntilTTSStop(t *testing.T, conn *websocket.Conn, timeout time.Duration) []e2eReceivedItem {
	t.Helper()
	var received []e2eReceivedItem
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		mType, mData, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("failed to read downstream message: %v", err)
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

		if item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop {
			break
		}
	}
	return received
}

// waitSessionState 等待指定会话流转至目标状态。
func waitSessionState(t *testing.T, sess *session.Session, expected session.State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for sess.State() != expected && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != expected {
		t.Fatalf("timed out waiting for session state %v, got %v", expected, sess.State())
	}
}

// generateSinePCM16k 在内存中生成 16 kHz 60 ms 单声道（960 采样点，1920 字节）PCM。
func generateSinePCM16k(frameCount int, freq float64) []byte {
	totalSamples := frameCount * 960
	buf := make([]byte, totalSamples*2)
	for i := 0; i < totalSamples; i++ {
		t := float64(i) / 16000.0
		sample := int16(16000.0 * math.Sin(2.0*math.Pi*freq*t))
		binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(sample))
	}
	return buf
}

// generateSinePCM24k 在内存中生成 24 kHz 60 ms 单声道（1440 采样点，2880 字节）PCM。
func generateSinePCM24k(frameCount int, freq float64) []byte {
	totalSamples := frameCount * 1440
	buf := make([]byte, totalSamples*2)
	for i := 0; i < totalSamples; i++ {
		t := float64(i) / 24000.0
		sample := int16(16000.0 * math.Sin(2.0*math.Pi*freq*t))
		binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(sample))
	}
	return buf
}

// encodeOpusPackets16k 将 16 kHz PCM 数据编码为 60 ms 帧长 Opus 音频包切片。
func encodeOpusPackets16k(t *testing.T, pcmBytes []byte) [][]byte {
	t.Helper()
	enc, err := opus.NewEncoder(16000, 1, opus.AppVoIP)
	if err != nil {
		t.Fatalf("failed to create 16k opus encoder: %v", err)
	}

	frameCount := len(pcmBytes) / 1920
	packets := make([][]byte, 0, frameCount)
	samples := make([]int16, 960)
	opusBuf := make([]byte, 1024)

	for f := 0; f < frameCount; f++ {
		frameData := pcmBytes[f*1920 : (f+1)*1920]
		for i := 0; i < 960; i++ {
			samples[i] = int16(binary.LittleEndian.Uint16(frameData[i*2 : i*2+2]))
		}
		n, err := enc.Encode(samples, opusBuf)
		if err != nil {
			t.Fatalf("failed to encode 16k opus packet: %v", err)
		}
		pkt := make([]byte, n)
		copy(pkt, opusBuf[:n])
		packets = append(packets, pkt)
	}
	return packets
}

// decodeOpusPacket24k 使用 24 kHz Opus 解码器解码 Opus 包并验证为合法 1440 采样点 / 2880 字节。
func decodeOpusPacket24k(t *testing.T, dec *opus.Decoder, packet []byte) []byte {
	t.Helper()
	pcmBuf := make([]int16, 1440)
	n, err := dec.Decode(packet, pcmBuf)
	if err != nil {
		t.Fatalf("failed to decode 24k opus packet: %v", err)
	}
	if n != 1440 {
		t.Fatalf("expected 1440 samples from 24k opus decode, got %d", n)
	}

	pcmBytes := make([]byte, n*2)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(pcmBytes[i*2:i*2+2], uint16(pcmBuf[i]))
	}
	return pcmBytes
}

type e2eReceivedItem struct {
	msgType   websocket.MessageType
	payload   []byte
	textType  string
	state     string
	text      string
	sessionID string
}

// TestE2E_AutoMode_SingleTurn_Success 验证完整的本地 auto 单轮语音闭环端到端流程：
// 包含配置发现 (OTA) -> WebSocket 握手 -> Hello 协商 -> listen.start (auto) -> 16 kHz Opus 上行 ->
// ASR 识别 -> 下发 stt -> LLM 流式回答与分句 -> 下发 tts.sentence_start -> TTS 24 kHz PCM 合成 ->
// 24 kHz Opus 编码与 60 ms 节奏下发 -> 下发 tts.start -> Opus 下行解码校验 -> 下发 tts.stop ->
// 回到 READY 状态并记录会话历史 -> 客户端正常关闭 -> 服务端优雅退出会话归零。
func TestE2E_AutoMode_SingleTurn_Success(t *testing.T) {
	const (
		expectedASRText       = "北京今天天气怎么样"
		expectedSentence1     = "北京今天天气晴朗，"
		expectedSentence2     = "适合出门散步。"
		expectedAssistantText = expectedSentence1 + expectedSentence2
	)

	// 1. 启动本地回环假服务
	ttsChunk1 := generateSinePCM24k(1, 440.0)
	ttsChunk2 := generateSinePCM24k(1, 880.0)
	wsMock := newMockBailianWSServer(t, expectedASRText, [][]byte{ttsChunk1, ttsChunk2})
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
		DashScopeAPIKey:   "test-e2e-dashscope-secret",
		DeviceSharedToken: "test-e2e-device-shared-token",
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
	cfg.Server.WebSocketURL = fmt.Sprintf("ws://%s%s", addr, session.WebSocketPath)

	// 3. 配置发现 (OTA) 请求与断言
	otaURL := fmt.Sprintf("http://%s%s", addr, bootstrap.OTAPath)
	otaResp, err := http.Post(otaURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("failed to send OTA request: %v", err)
	}
	defer otaResp.Body.Close()

	if otaResp.StatusCode != http.StatusOK {
		t.Fatalf("expected OTA status 200, got %d", otaResp.StatusCode)
	}

	var otaData bootstrap.Response
	if err := json.NewDecoder(otaResp.Body).Decode(&otaData); err != nil {
		t.Fatalf("failed to decode OTA response: %v", err)
	}

	if otaData.WebSocket.Version != 1 {
		t.Errorf("expected OTA websocket version 1, got %d", otaData.WebSocket.Version)
	}
	if otaData.WebSocket.Token != cfg.DeviceSharedToken {
		t.Errorf("expected OTA token to match configured token")
	}
	if otaData.WebSocket.URL != cfg.Server.WebSocketURL {
		t.Errorf("expected OTA URL %q, got %q", cfg.Server.WebSocketURL, otaData.WebSocket.URL)
	}

	// 4. 建立 WebSocket 客户端连接并完成 Hello 协商
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":    []string{"Bearer " + otaData.WebSocket.Token},
			"Protocol-Version": []string{"1"},
			"Device-Id":        []string{"e2e-device-001"},
			"Client-Id":        []string{"client-001"},
		},
	}

	conn, _, err := websocket.Dial(dialCtx, otaData.WebSocket.URL, dialOpts)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test finished")

	// 发送客户端 Hello
	clientHelloJSON := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	if err := conn.Write(context.Background(), websocket.MessageText, clientHelloJSON); err != nil {
		t.Fatalf("failed to send client hello: %v", err)
	}

	// 接收并验证服务端 Hello (总顺序 1)
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

	if srvHello.Type != session.MessageTypeHello {
		t.Errorf("expected type %q, got %q", session.MessageTypeHello, srvHello.Type)
	}
	if srvHello.Transport != session.TransportWebSocket {
		t.Errorf("expected transport %q, got %q", session.TransportWebSocket, srvHello.Transport)
	}
	if srvHello.SessionID == "" {
		t.Fatal("expected non-empty session_id in server hello")
	}
	if srvHello.AudioParams.Format != "opus" || srvHello.AudioParams.SampleRate != 24000 || srvHello.AudioParams.Channels != 1 || srvHello.AudioParams.FrameDuration != 60 {
		t.Errorf("unexpected audio_params: %+v", srvHello.AudioParams)
	}
	sessionID := srvHello.SessionID

	// 5. 开启 auto 模式并发送上行 16 kHz Opus 音频
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

	// 6. 持续读取下行消息直至收到 tts.stop
	var received []e2eReceivedItem
	readLoopCtx, readLoopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readLoopCancel()

	for {
		mType, mData, err := conn.Read(readLoopCtx)
		if err != nil {
			t.Fatalf("failed to read downstream message: %v", err)
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

		if item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop {
			break
		}
	}

	// 7. 严格断言下行消息顺序与字段
	var (
		foundSTT          bool
		receivedSentences []string
		foundTTSStart     bool
		foundTTSStop      bool
		audioPacketCount  int
		ttsStartIdx       = -1
		firstAudioIdx     = -1
		lastAudioIdx      = -1
		ttsStopIdx        = -1
		sttIdx            = -1
		firstSentenceIdx  = -1
	)

	dec24k, err := opus.NewDecoder(24000, 1)
	if err != nil {
		t.Fatalf("failed to create 24k opus decoder: %v", err)
	}

	for idx, item := range received {
		if item.msgType == websocket.MessageText {
			if item.sessionID != sessionID {
				t.Errorf("message %d session_id %q does not match expected %q", idx, item.sessionID, sessionID)
			}
			switch {
			case item.textType == session.MessageTypeSTT:
				if foundSTT {
					t.Errorf("multiple STT messages received")
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
					t.Errorf("multiple tts.start messages received")
				}
				foundTTSStart = true
				ttsStartIdx = idx

			case item.textType == session.MessageTypeTTS && item.state == session.TTSStateStop:
				if foundTTSStop {
					t.Errorf("multiple tts.stop messages received")
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
	if len(receivedSentences) != 2 {
		t.Fatalf("expected 2 tts.sentence_start messages, got %d", len(receivedSentences))
	}
	if receivedSentences[0] != expectedSentence1 {
		t.Errorf("expected first sentence %q, got %q", expectedSentence1, receivedSentences[0])
	}
	if receivedSentences[1] != expectedSentence2 {
		t.Errorf("expected second sentence %q, got %q", expectedSentence2, receivedSentences[1])
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

	// 验证总顺序：stt -> tts.sentence_start (首句) -> tts.start -> binary opus -> tts.stop
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

	// 8. 最终断言状态机回到 READY 状态，对话历史中已记录该完整轮次
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

	deadline := time.Now().Add(2 * time.Second)
	for sess.State() != session.StateReady && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != session.StateReady {
		t.Errorf("expected session state StateReady, got %v", sess.State())
	}

	history := sess.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages (1 turn), got %d", len(history))
	}
	if history[0].Role != ai.RoleUser || history[0].Content != expectedASRText {
		t.Errorf("expected history[0] to be user text %q, got %+v", expectedASRText, history[0])
	}
	if history[1].Role != ai.RoleAssistant || history[1].Content != expectedAssistantText {
		t.Errorf("expected history[1] to be assistant text %q, got %+v", expectedAssistantText, history[1])
	}

	// 9. 客户端正常关闭，服务端在宽限期内平稳退出，会话数归零
	_ = conn.Close(websocket.StatusNormalClosure, "e2e client normal exit")

	srvCancel()

	select {
	case runErr := <-srvErrCh:
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Fatalf("server exited with unexpected error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to shutdown within timeout")
	}

	if registry.ActiveCount() != 0 {
		t.Errorf("expected active session count 0, got %d", registry.ActiveCount())
	}
	if limiter.ActiveCount() != 0 {
		t.Errorf("expected limiter active count 0, got %d", limiter.ActiveCount())
	}
}
