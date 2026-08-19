package bailian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/config"
)

func TestNewTTSClient_Validation(t *testing.T) {
	validCfg := func() *config.Config {
		return &config.Config{
			DashScopeAPIKey: "test-api-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint:        "wss://example.com/api-ws/v1/inference",
					TTSModel:          "qwen-audio-3.0-tts-flash",
					TTSVoice:          "longanlingxi",
					TTSConnectTimeout: 10 * time.Second,
				},
			},
			Session: config.SessionConfig{
				TTSPCMQueueCapacity: 100,
			},
		}
	}

	t.Run("nil config", func(t *testing.T) {
		_, err := NewTTSClient(nil)
		if err == nil || !strings.Contains(err.Error(), "config cannot be nil") {
			t.Fatalf("expected nil config error, got: %v", err)
		}
	})

	t.Run("missing api key", func(t *testing.T) {
		cfg := validCfg()
		cfg.DashScopeAPIKey = ""
		_, err := NewTTSClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "dashscope api key is required") {
			t.Fatalf("expected missing api key error, got: %v", err)
		}
	})

	t.Run("missing ws endpoint", func(t *testing.T) {
		cfg := validCfg()
		cfg.AI.Bailian.WSEndpoint = ""
		_, err := NewTTSClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "bailian ws endpoint is required") {
			t.Fatalf("expected missing ws endpoint error, got: %v", err)
		}
	})

	t.Run("missing tts model", func(t *testing.T) {
		cfg := validCfg()
		cfg.AI.Bailian.TTSModel = ""
		_, err := NewTTSClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "bailian tts model is required") {
			t.Fatalf("expected missing tts model error, got: %v", err)
		}
	})

	t.Run("missing tts voice", func(t *testing.T) {
		cfg := validCfg()
		cfg.AI.Bailian.TTSVoice = ""
		_, err := NewTTSClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "bailian tts voice is required") {
			t.Fatalf("expected missing tts voice error, got: %v", err)
		}
	})

	t.Run("valid config with proxy", func(t *testing.T) {
		cfg := validCfg()
		cfg.Proxy.Enabled = true
		cfg.Proxy.URL = "http://127.0.0.1:8080"
		client, err := NewTTSClient(cfg)
		if err != nil {
			t.Fatalf("unexpected error with proxy config: %v", err)
		}
		if client == nil {
			t.Fatal("expected non-nil client")
		}
	})

	t.Run("invalid proxy url", func(t *testing.T) {
		cfg := validCfg()
		cfg.Proxy.Enabled = true
		cfg.Proxy.URL = "::invalid-url::"
		_, err := NewTTSClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "parse proxy url") {
			t.Fatalf("expected proxy url parse error, got: %v", err)
		}
	})
}

func TestTTSClient_NormalFlow(t *testing.T) {
	const (
		expectedAPIKey = "sk-test-dashscope-secret"
		expectedModel  = "qwen-audio-3.0-tts-flash"
		expectedVoice  = "longanlingxi"
	)

	var (
		mu                      sync.Mutex
		receivedAuthHeader      string
		receivedRunTask         ttsRunTaskMessage
		receivedContinueTasks   []ttsContinueTaskMessage
		receivedFinishTaskCount int
	)

	pcmChunksToSend := [][]byte{
		{0x01, 0x02, 0x03, 0x04},
		{0x11, 0x12, 0x13, 0x14},
		{0x21, 0x22, 0x23, 0x24},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedAuthHeader = r.Header.Get("Authorization")
		mu.Unlock()

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("mock server accept error: %v", err)
			return
		}
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		// 1. Read run-task message
		msgType, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("mock server read run-task error: %v", err)
			return
		}
		if msgType != websocket.MessageText {
			t.Errorf("expected text message for run-task, got %v", msgType)
			return
		}

		var runMsg ttsRunTaskMessage
		if err := json.Unmarshal(data, &runMsg); err != nil {
			t.Errorf("unmarshal run-task error: %v", err)
			return
		}
		mu.Lock()
		receivedRunTask = runMsg
		mu.Unlock()

		// 2. Respond task-started
		startedResp := ttsResponseMessage{}
		startedResp.Header.Action = "task-started"
		startedResp.Header.TaskID = runMsg.Header.TaskID
		startedResp.Header.Event = "task-started"
		startedBytes, _ := json.Marshal(startedResp)
		if err := conn.Write(r.Context(), websocket.MessageText, startedBytes); err != nil {
			t.Errorf("write task-started error: %v", err)
			return
		}

		// 3. Read continue-task and finish-task messages
		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageText {
				var genericMsg struct {
					Header struct {
						Action string `json:"action"`
					} `json:"header"`
				}
				if err := json.Unmarshal(mData, &genericMsg); err != nil {
					continue
				}

				if genericMsg.Header.Action == "continue-task" {
					var contMsg ttsContinueTaskMessage
					_ = json.Unmarshal(mData, &contMsg)
					mu.Lock()
					receivedContinueTasks = append(receivedContinueTasks, contMsg)
					idx := len(receivedContinueTasks) - 1
					mu.Unlock()

					// 第一句下发 chunk[0]，第二句下发 chunk[1]
					if idx < 2 {
						_ = conn.Write(r.Context(), websocket.MessageBinary, pcmChunksToSend[idx])
					}
				} else if genericMsg.Header.Action == "finish-task" {
					mu.Lock()
					receivedFinishTaskCount++
					mu.Unlock()

					// 下发最后一块 PCM 音频数据
					_ = conn.Write(r.Context(), websocket.MessageBinary, pcmChunksToSend[2])

					// 下发一个 result-generated 中间事件
					interResp := ttsResponseMessage{}
					interResp.Header.Action = "result-generated"
					interResp.Header.TaskID = runMsg.Header.TaskID
					interResp.Header.Event = "result-generated"
					interBytes, _ := json.Marshal(interResp)
					_ = conn.Write(r.Context(), websocket.MessageText, interBytes)

					// 下发 task-finished 结束事件
					finishResp := ttsResponseMessage{}
					finishResp.Header.Action = "task-finished"
					finishResp.Header.TaskID = runMsg.Header.TaskID
					finishResp.Header.Event = "task-finished"
					finishBytes, _ := json.Marshal(finishResp)
					_ = conn.Write(r.Context(), websocket.MessageText, finishBytes)
					return
				}
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cfg := &config.Config{
		DashScopeAPIKey: expectedAPIKey,
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:        wsURL,
				TTSModel:          expectedModel,
				TTSVoice:          expectedVoice,
				TTSConnectTimeout: 5 * time.Second,
			},
		},
		Session: config.SessionConfig{
			TTSPCMQueueCapacity: 100,
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create tts client: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 发送两个句子
	if err := stream.SendSentence(context.Background(), "第一句话。"); err != nil {
		t.Fatalf("SendSentence 1 failed: %v", err)
	}
	if err := stream.SendSentence(context.Background(), "第二句话。"); err != nil {
		t.Fatalf("SendSentence 2 failed: %v", err)
	}

	// 结束输入
	if err := stream.Finish(context.Background()); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	// 接收 PCM 数据流
	var receivedChunks [][]byte
	for {
		chunk, err := stream.NextPCM(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextPCM failed unexpectedly: %v", err)
		}
		receivedChunks = append(receivedChunks, chunk)
	}

	// 断言接收到的 PCM 数据块
	if len(receivedChunks) != 3 {
		t.Fatalf("expected 3 pcm chunks, got %d", len(receivedChunks))
	}
	for i := 0; i < 3; i++ {
		if !bytes.Equal(receivedChunks[i], pcmChunksToSend[i]) {
			t.Errorf("chunk %d mismatch: expected %v, got %v", i, pcmChunksToSend[i], receivedChunks[i])
		}
	}

	// 严格断言发送给假服务的请求协议参数
	mu.Lock()
	defer mu.Unlock()

	if receivedAuthHeader != "Bearer "+expectedAPIKey {
		t.Errorf("expected Authorization Header 'Bearer %s', got %q", expectedAPIKey, receivedAuthHeader)
	}
	if receivedRunTask.Header.Action != "run-task" {
		t.Errorf("expected action run-task, got %q", receivedRunTask.Header.Action)
	}
	if receivedRunTask.Header.Streaming != "duplex" {
		t.Errorf("expected streaming duplex, got %q", receivedRunTask.Header.Streaming)
	}
	if receivedRunTask.Payload.TaskGroup != "audio" {
		t.Errorf("expected task_group audio, got %q", receivedRunTask.Payload.TaskGroup)
	}
	if receivedRunTask.Payload.Task != "tts" {
		t.Errorf("expected task tts, got %q", receivedRunTask.Payload.Task)
	}
	if receivedRunTask.Payload.Function != "SpeechSynthesizer" {
		t.Errorf("expected function SpeechSynthesizer, got %q", receivedRunTask.Payload.Function)
	}
	if receivedRunTask.Payload.Model != expectedModel {
		t.Errorf("expected model %s, got %q", expectedModel, receivedRunTask.Payload.Model)
	}
	if receivedRunTask.Payload.Parameters.Voice != expectedVoice {
		t.Errorf("expected voice %s, got %q", expectedVoice, receivedRunTask.Payload.Parameters.Voice)
	}
	if receivedRunTask.Payload.Parameters.Format != "pcm" {
		t.Errorf("expected format pcm, got %q", receivedRunTask.Payload.Parameters.Format)
	}
	if receivedRunTask.Payload.Parameters.SampleRate != 24000 {
		t.Errorf("expected sample_rate 24000, got %d", receivedRunTask.Payload.Parameters.SampleRate)
	}
	if receivedRunTask.Payload.Parameters.TextType != "PlainText" {
		t.Errorf("expected text_type PlainText, got %q", receivedRunTask.Payload.Parameters.TextType)
	}

	// 验证 continue-task 顺序
	if len(receivedContinueTasks) != 2 {
		t.Fatalf("expected 2 continue-task messages, got %d", len(receivedContinueTasks))
	}
	if receivedContinueTasks[0].Payload.Input.Text != "第一句话。" {
		t.Errorf("expected continue-task 1 text '第一句话。', got %q", receivedContinueTasks[0].Payload.Input.Text)
	}
	if receivedContinueTasks[1].Payload.Input.Text != "第二句话。" {
		t.Errorf("expected continue-task 2 text '第二句话。', got %q", receivedContinueTasks[1].Payload.Input.Text)
	}

	// 验证 finish-task
	if receivedFinishTaskCount != 1 {
		t.Errorf("expected exactly 1 finish-task message, got %d", receivedFinishTaskCount)
	}
}

func TestTTSClient_EmptySentenceSkipped(t *testing.T) {
	var continueCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, data, _ := conn.Read(r.Context())
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		resp := ttsResponseMessage{}
		resp.Header.Event = "task-started"
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		for {
			mType, mData, err := conn.Read(r.Context())
			if err != nil {
				break
			}
			if mType == websocket.MessageText && strings.Contains(string(mData), "continue-task") {
				continueCount.Add(1)
			}
			if mType == websocket.MessageText && strings.Contains(string(mData), "finish-task") {
				fResp := ttsResponseMessage{}
				fResp.Header.Event = "task-finished"
				fb, _ := json.Marshal(fResp)
				_ = conn.Write(r.Context(), websocket.MessageText, fb)
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "longanlingxi",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 发送空字符串，不应向服务端发送 continue-task
	if err := stream.SendSentence(context.Background(), ""); err != nil {
		t.Fatalf("SendSentence with empty text returned error: %v", err)
	}

	_ = stream.Finish(context.Background())
	_, _ = stream.NextPCM(context.Background())

	if count := continueCount.Load(); count != 0 {
		t.Fatalf("expected 0 continue-task messages for empty text, got %d", count)
	}
}

func TestTTSClient_OperationsAfterFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, _, _ = conn.Read(r.Context())
		resp := ttsResponseMessage{}
		resp.Header.Event = "task-started"
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		for {
			mType, mData, err := conn.Read(r.Context())
			if err != nil {
				break
			}
			if mType == websocket.MessageText && strings.Contains(string(mData), "finish-task") {
				fResp := ttsResponseMessage{}
				fResp.Header.Event = "task-finished"
				fb, _ := json.Marshal(fResp)
				_ = conn.Write(r.Context(), websocket.MessageText, fb)
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "longanlingxi",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 第一次 Finish 正常返回
	if err := stream.Finish(context.Background()); err != nil {
		t.Fatalf("first Finish failed: %v", err)
	}

	// 第二次 Finish 幂等返回 nil
	if err := stream.Finish(context.Background()); err != nil {
		t.Fatalf("second Finish failed: %v", err)
	}

	// 在 Finish 之后 SendSentence 应返回错误
	err = stream.SendSentence(context.Background(), "新句子")
	if err == nil || !strings.Contains(err.Error(), "finished") {
		t.Fatalf("expected finished stream error on SendSentence, got: %v", err)
	}
}

func TestTTSClient_TaskFailedOnInit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, _, _ = conn.Read(r.Context())

		failResp := ttsResponseMessage{}
		failResp.Header.Event = "task-failed"
		failResp.Header.ErrorCode = "InvalidVoice"
		failResp.Header.ErrorMessage = "Voice 'invalid_voice' not found"
		b, _ := json.Marshal(failResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "invalid_voice",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	_, err = client.CreateStream(context.Background())
	if err == nil {
		t.Fatal("expected error on CreateStream when task-failed returned, got nil")
	}
	if !strings.Contains(err.Error(), "InvalidVoice") || !strings.Contains(err.Error(), "Voice 'invalid_voice' not found") {
		t.Fatalf("expected error message to contain error code and message, got: %v", err)
	}
}

func TestTTSClient_UnexpectedInitEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, _, _ = conn.Read(r.Context())

		resp := ttsResponseMessage{}
		resp.Header.Event = "result-generated"
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "longanlingxi",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	_, err = client.CreateStream(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected initial event") {
		t.Fatalf("expected unexpected initial event error, got: %v", err)
	}
}

func TestTTSClient_TaskFailedInStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, _, _ = conn.Read(r.Context())

		resp := ttsResponseMessage{}
		resp.Header.Event = "task-started"
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		// 收到 continue-task 后返回 task-failed
		_, _, _ = conn.Read(r.Context())
		failResp := ttsResponseMessage{}
		failResp.Header.Event = "task-failed"
		failResp.Header.ErrorCode = "SynthesisError"
		failResp.Header.ErrorMessage = "TTS backend error"
		fb, _ := json.Marshal(failResp)
		_ = conn.Write(r.Context(), websocket.MessageText, fb)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "longanlingxi",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	_ = stream.SendSentence(context.Background(), "测试句子")

	_, err = stream.NextPCM(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SynthesisError") {
		t.Fatalf("expected SynthesisError in NextPCM, got: %v", err)
	}
}

func TestTTSClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, _, _ = conn.Read(r.Context())
		resp := ttsResponseMessage{}
		resp.Header.Event = "task-started"
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		// 循环等待读取直到连接断开或 context 取消
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				break
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "longanlingxi",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.CreateStream(ctx)
	if err != nil {
		cancel()
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 外部主动取消
	cancel()

	_, err = stream.NextPCM(context.Background())
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected context.Canceled or closed error, got: %v", err)
	}
}

func TestTTSClient_CloseDuringStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, _, _ = conn.Read(r.Context())
		resp := ttsResponseMessage{}
		resp.Header.Event = "task-started"
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		for i := 0; i < 100; i++ {
			_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{0x01, 0x02})
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "longanlingxi",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}

	// 接收 1 个块后主动 Close
	chunk, err := stream.NextPCM(context.Background())
	if err != nil || len(chunk) == 0 {
		t.Fatalf("failed to read initial chunk: %v", err)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// 再次 Close 幂等安全
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}

	// 已关闭状态下调用 SendSentence 和 Finish 必须报错
	if err := stream.SendSentence(context.Background(), "abc"); err == nil {
		t.Fatal("expected error on SendSentence after Close, got nil")
	}
	if err := stream.Finish(context.Background()); err == nil {
		t.Fatal("expected error on Finish after Close, got nil")
	}
}

func TestTTSClient_ConcurrentSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		_, _, _ = conn.Read(r.Context())
		resp := ttsResponseMessage{}
		resp.Header.Event = "task-started"
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		go func() {
			for {
				mType, mData, err := conn.Read(r.Context())
				if err != nil {
					break
				}
				if mType == websocket.MessageText && strings.Contains(string(mData), "finish-task") {
					fResp := ttsResponseMessage{}
					fResp.Header.Event = "task-finished"
					fb, _ := json.Marshal(fResp)
					_ = conn.Write(r.Context(), websocket.MessageText, fb)
					return
				}
			}
		}()

		for i := 0; i < 10; i++ {
			_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{byte(i)})
			time.Sleep(2 * time.Millisecond)
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: wsURL,
				TTSModel:   "qwen-audio-3.0-tts-flash",
				TTSVoice:   "longanlingxi",
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()

			stream, sErr := client.CreateStream(context.Background())
			if sErr != nil {
				t.Errorf("goroutine %d CreateStream failed: %v", id, sErr)
				return
			}
			defer stream.Close()

			_ = stream.SendSentence(context.Background(), fmt.Sprintf("句子来自 %d", id))
			_ = stream.Finish(context.Background())

			var readCount int
			for {
				_, rErr := stream.NextPCM(context.Background())
				if errors.Is(rErr, io.EOF) {
					break
				}
				if rErr != nil {
					break
				}
				readCount++
			}
		}(g)
	}

	wg.Wait()
}
