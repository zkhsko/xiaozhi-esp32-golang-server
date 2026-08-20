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
		t.Errorf("expected first continue text '第一句话。', got %q", receivedContinueTasks[0].Payload.Input.Text)
	}
	if receivedContinueTasks[1].Payload.Input.Text != "第二句话。" {
		t.Errorf("expected second continue text '第二句话。', got %q", receivedContinueTasks[1].Payload.Input.Text)
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
		defer conn.Close(websocket.StatusNormalClosure, "mock closed")

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

func TestTTSStream_ArbitraryPCMChunksAndUnknownEvents(t *testing.T) {
	// 定义各种任意大小的 PCM 分块：1 字节、3 字节、5 字节（奇数）、7 字节、0 字节空包、17 字节、1024 字节、4096 字节
	largeChunk1 := make([]byte, 1024)
	for i := range largeChunk1 {
		largeChunk1[i] = byte(i % 256)
	}
	largeChunk2 := make([]byte, 4096)
	for i := range largeChunk2 {
		largeChunk2[i] = byte((i * 3) % 256)
	}

	chunksToSend := [][]byte{
		{0x42},                         // 1 字节
		{0x01, 0x02, 0x03},             // 3 字节（奇数）
		{0x10, 0x20, 0x30, 0x40, 0x50}, // 5 字节（奇数）
		{0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x11}, // 7 字节（奇数）
		{}, // 0 字节空包，应安全忽略
		{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}, // 17 字节
		largeChunk1,        // 1024 字节大块
		largeChunk2,        // 4096 字节大块
		{0xFE, 0xFF, 0x00}, // 3 字节
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "mock closed")

		// 1. 读取 run-task
		_, data, rErr := conn.Read(r.Context())
		if rErr != nil {
			return
		}
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		// 2. 发送 task-started
		startResp := ttsResponseMessage{}
		startResp.Header.Action = "task-started"
		startResp.Header.TaskID = runMsg.Header.TaskID
		startResp.Header.Event = "task-started"
		sBytes, _ := json.Marshal(startResp)
		_ = conn.Write(r.Context(), websocket.MessageText, sBytes)

		// 3. 读取 continue-task 与 finish-task
		for {
			mType, mData, readErr := conn.Read(r.Context())
			if readErr != nil {
				break
			}
			if mType == websocket.MessageText {
				var msg struct {
					Header struct {
						Action string `json:"action"`
					} `json:"header"`
				}
				_ = json.Unmarshal(mData, &msg)

				if msg.Header.Action == "continue-task" {
					// 下发前 4 个分块，并穿插未知/中间事件
					for i := 0; i < 4; i++ {
						_ = conn.Write(r.Context(), websocket.MessageBinary, chunksToSend[i])
					}

					// 穿插发送 result-generated 事件
					interResp1 := ttsResponseMessage{}
					interResp1.Header.Event = "result-generated"
					b1, _ := json.Marshal(interResp1)
					_ = conn.Write(r.Context(), websocket.MessageText, b1)

					// 穿插发送 task-progress 事件
					interResp2 := ttsResponseMessage{}
					interResp2.Header.Event = "task-progress"
					b2, _ := json.Marshal(interResp2)
					_ = conn.Write(r.Context(), websocket.MessageText, b2)

					// 穿插未知供应商扩展事件与非 JSON 文本
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"header":{"event":"unknown-custom-event"}}`))
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`not-a-valid-json-string`))

				} else if msg.Header.Action == "finish-task" {
					// 下发剩余分块（包含 0 字节空包、17 字节、1024 字节、4096 字节、3 字节）
					for i := 4; i < len(chunksToSend); i++ {
						_ = conn.Write(r.Context(), websocket.MessageBinary, chunksToSend[i])
					}

					// 发送 task-finished
					finishResp := ttsResponseMessage{}
					finishResp.Header.Action = "task-finished"
					finishResp.Header.TaskID = runMsg.Header.TaskID
					finishResp.Header.Event = "task-finished"
					fBytes, _ := json.Marshal(finishResp)
					_ = conn.Write(r.Context(), websocket.MessageText, fBytes)
					return
				}
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
		Session: config.SessionConfig{
			TTSPCMQueueCapacity: 100,
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	if err := stream.SendSentence(ctx, "测试变长 PCM 分块。"); err != nil {
		t.Fatalf("SendSentence failed: %v", err)
	}

	if err := stream.Finish(ctx); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	// 收集并校验所有 PCM 分块
	var receivedChunks [][]byte
	for {
		chunk, rErr := stream.NextPCM(ctx)
		if errors.Is(rErr, io.EOF) {
			break
		}
		if rErr != nil {
			t.Fatalf("NextPCM failed: %v", rErr)
		}
		receivedChunks = append(receivedChunks, chunk)
	}

	// 过滤掉预期中的 0 字节空包
	var expectedNonEmptyChunks [][]byte
	var expectedTotalBytes int
	for _, c := range chunksToSend {
		if len(c) > 0 {
			expectedNonEmptyChunks = append(expectedNonEmptyChunks, c)
			expectedTotalBytes += len(c)
		}
	}

	if len(receivedChunks) != len(expectedNonEmptyChunks) {
		t.Fatalf("expected %d chunks, got %d", len(expectedNonEmptyChunks), len(receivedChunks))
	}

	var receivedTotalBytes int
	for i, expected := range expectedNonEmptyChunks {
		actual := receivedChunks[i]
		receivedTotalBytes += len(actual)
		if !bytes.Equal(actual, expected) {
			t.Errorf("chunk %d mismatch: expected len %d, got len %d", i, len(expected), len(actual))
		}
	}

	if receivedTotalBytes != expectedTotalBytes {
		t.Errorf("expected total %d bytes, got %d bytes", expectedTotalBytes, receivedTotalBytes)
	}
}

func TestTTSStream_TaskFailedErrors(t *testing.T) {
	tests := []struct {
		name         string
		failOnInit   bool
		failOnFinish bool
		errorCode    string
		errorMessage string
		wantErrSubs  []string
	}{
		{
			name:         "init_task_failed_standard",
			failOnInit:   true,
			errorCode:    "InvalidVoice",
			errorMessage: "Voice 'non_existent_voice' not found",
			wantErrSubs:  []string{"InvalidVoice", "Voice 'non_existent_voice' not found"},
		},
		{
			name:         "init_task_failed_empty_fields",
			failOnInit:   true,
			errorCode:    "",
			errorMessage: "",
			wantErrSubs:  []string{"UNKNOWN_ERROR", "task start failed"},
		},
		{
			name:         "midstream_task_failed_quota_exceeded",
			failOnInit:   false,
			failOnFinish: false,
			errorCode:    "QuotaExhausted",
			errorMessage: "Realtime TTS quota exceeded",
			wantErrSubs:  []string{"QuotaExhausted", "Realtime TTS quota exceeded"},
		},
		{
			name:         "midstream_task_failed_empty_fields",
			failOnInit:   false,
			failOnFinish: false,
			errorCode:    "",
			errorMessage: "",
			wantErrSubs:  []string{"UNKNOWN_ERROR", "tts task failed on server"},
		},
		{
			name:         "finish_task_failed",
			failOnInit:   false,
			failOnFinish: true,
			errorCode:    "BackendSynthesisTimeout",
			errorMessage: "Audio generation backend timed out",
			wantErrSubs:  []string{"BackendSynthesisTimeout", "Audio generation backend timed out"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close(websocket.StatusInternalError, "mock closed")

				_, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				var runMsg ttsRunTaskMessage
				_ = json.Unmarshal(data, &runMsg)

				if tt.failOnInit {
					failedResp := ttsResponseMessage{}
					failedResp.Header.Action = "task-failed"
					failedResp.Header.TaskID = runMsg.Header.TaskID
					failedResp.Header.Event = "task-failed"
					failedResp.Header.ErrorCode = tt.errorCode
					failedResp.Header.ErrorMessage = tt.errorMessage
					b, _ := json.Marshal(failedResp)
					_ = conn.Write(r.Context(), websocket.MessageText, b)
					return
				}

				// 发送 task-started
				startResp := ttsResponseMessage{}
				startResp.Header.Action = "task-started"
				startResp.Header.TaskID = runMsg.Header.TaskID
				startResp.Header.Event = "task-started"
				sb, _ := json.Marshal(startResp)
				_ = conn.Write(r.Context(), websocket.MessageText, sb)

				for {
					mType, mData, rErr := conn.Read(r.Context())
					if rErr != nil {
						break
					}
					if mType == websocket.MessageText {
						var msg struct {
							Header struct {
								Action string `json:"action"`
							} `json:"header"`
						}
						_ = json.Unmarshal(mData, &msg)

						if msg.Header.Action == "continue-task" && !tt.failOnFinish {
							failedResp := ttsResponseMessage{}
							failedResp.Header.Action = "task-failed"
							failedResp.Header.TaskID = runMsg.Header.TaskID
							failedResp.Header.Event = "task-failed"
							failedResp.Header.ErrorCode = tt.errorCode
							failedResp.Header.ErrorMessage = tt.errorMessage
							fb, _ := json.Marshal(failedResp)
							_ = conn.Write(r.Context(), websocket.MessageText, fb)
							return
						} else if msg.Header.Action == "finish-task" && tt.failOnFinish {
							failedResp := ttsResponseMessage{}
							failedResp.Header.Action = "task-failed"
							failedResp.Header.TaskID = runMsg.Header.TaskID
							failedResp.Header.Event = "task-failed"
							failedResp.Header.ErrorCode = tt.errorCode
							failedResp.Header.ErrorMessage = tt.errorMessage
							fb, _ := json.Marshal(failedResp)
							_ = conn.Write(r.Context(), websocket.MessageText, fb)
							return
						}
					}
				}
			}))
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			cfg := &config.Config{
				DashScopeAPIKey: "test-key",
				AI: config.AIConfig{
					Bailian: config.BailianConfig{
						WSEndpoint:        wsURL,
						TTSModel:          "qwen-audio-3.0-tts-flash",
						TTSVoice:          "longanlingxi",
						TTSConnectTimeout: 2 * time.Second,
					},
				},
			}

			client, err := NewTTSClient(cfg)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if tt.failOnInit {
				_, err = client.CreateStream(ctx)
				if err == nil {
					t.Fatalf("expected CreateStream to fail on task-failed, got nil")
				}
				for _, sub := range tt.wantErrSubs {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("expected error to contain '%s', got: %v", sub, err)
					}
				}
			} else {
				stream, err := client.CreateStream(ctx)
				if err != nil {
					t.Fatalf("CreateStream failed: %v", err)
				}
				defer stream.Close()

				_ = stream.SendSentence(ctx, "测试异常分支")
				if tt.failOnFinish {
					_ = stream.Finish(ctx)
				}

				_, err = stream.NextPCM(ctx)
				if err == nil {
					t.Fatalf("expected NextPCM to fail, got nil")
				}
				for _, sub := range tt.wantErrSubs {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("expected error to contain '%s', got: %v", sub, err)
					}
				}

				// 发生错误后，再次调用 SendSentence 必须直接返回错误
				errSend := stream.SendSentence(ctx, "新句子")
				if errSend == nil {
					t.Fatalf("expected SendSentence to fail after task-failed, got nil")
				}
			}
		})
	}
}

func TestTTSStream_AbruptDisconnect(t *testing.T) {
	t.Run("server_closes_during_reading", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}

			// 读取 run-task
			_, data, rErr := conn.Read(r.Context())
			if rErr != nil {
				return
			}
			var runMsg ttsRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			// 发送 task-started
			startResp := ttsResponseMessage{}
			startResp.Header.Event = "task-started"
			startResp.Header.TaskID = runMsg.Header.TaskID
			sb, _ := json.Marshal(startResp)
			_ = conn.Write(r.Context(), websocket.MessageText, sb)

			// 下发 1 个 PCM 块后突发异常断开连接
			_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{0x01, 0x02})
			_ = conn.Close(websocket.StatusInternalError, "server crashed abruptly")
		}))
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
		cfg := &config.Config{
			DashScopeAPIKey: "test-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint:        wsURL,
					TTSModel:          "qwen-audio-3.0-tts-flash",
					TTSVoice:          "longanlingxi",
					TTSConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewTTSClient(cfg)
		if err != nil {
			t.Fatalf("NewTTSClient failed: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		stream, err := client.CreateStream(ctx)
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}
		defer stream.Close()

		// 接收第一个已缓冲的数据块
		chunk, err := stream.NextPCM(ctx)
		if err != nil {
			t.Fatalf("expected initial chunk, got error: %v", err)
		}
		if !bytes.Equal(chunk, []byte{0x01, 0x02}) {
			t.Fatalf("unexpected chunk content: %v", chunk)
		}

		// 第二次读取应立即返回异常断开错误（而非 io.EOF）
		_, err = stream.NextPCM(ctx)
		if err == nil {
			t.Fatalf("expected error on abrupt disconnect, got nil")
		}
		if errors.Is(err, io.EOF) {
			t.Fatalf("expected non-EOF error on abnormal closure, got io.EOF")
		}
	})

	t.Run("blocked_NextPCM_unblocks_on_abrupt_disconnect", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}

			_, data, _ := conn.Read(r.Context())
			var runMsg ttsRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startResp := ttsResponseMessage{}
			startResp.Header.Event = "task-started"
			startResp.Header.TaskID = runMsg.Header.TaskID
			sb, _ := json.Marshal(startResp)
			_ = conn.Write(r.Context(), websocket.MessageText, sb)

			// 等待片刻后直接关闭连接
			time.Sleep(50 * time.Millisecond)
			_ = conn.Close(websocket.StatusInternalError, "server disconnect")
		}))
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
		cfg := &config.Config{
			DashScopeAPIKey: "test-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint:        wsURL,
					TTSModel:          "qwen-audio-3.0-tts-flash",
					TTSVoice:          "longanlingxi",
					TTSConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewTTSClient(cfg)
		if err != nil {
			t.Fatalf("NewTTSClient failed: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		stream, err := client.CreateStream(ctx)
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}
		defer stream.Close()

		doneCh := make(chan error, 1)
		go func() {
			_, rErr := stream.NextPCM(ctx)
			doneCh <- rErr
		}()

		select {
		case rErr := <-doneCh:
			if rErr == nil || errors.Is(rErr, io.EOF) {
				t.Fatalf("expected error on abrupt disconnect, got: %v", rErr)
			}
		case <-time.After(1 * time.Second):
			t.Fatal("NextPCM did not unblock in time on abrupt disconnect")
		}
	})
}

func TestTTSStream_ContextCancellationAndUnblock(t *testing.T) {
	t.Run("caller_context_canceled_on_NextPCM", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, _ := websocket.Accept(w, r, nil)
			defer conn.Close(websocket.StatusInternalError, "closed")

			_, data, _ := conn.Read(r.Context())
			var runMsg ttsRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startResp := ttsResponseMessage{}
			startResp.Header.Event = "task-started"
			startResp.Header.TaskID = runMsg.Header.TaskID
			sb, _ := json.Marshal(startResp)
			_ = conn.Write(r.Context(), websocket.MessageText, sb)

			// 持续阻塞连接
			_, _, _ = conn.Read(r.Context())
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
			t.Fatalf("NewTTSClient failed: %v", err)
		}

		stream, err := client.CreateStream(context.Background())
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}
		defer stream.Close()

		callCtx, callCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer callCancel()

		_, err = stream.NextPCM(callCtx)
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context deadline exceeded, got: %v", err)
		}
	})

	t.Run("caller_context_canceled_on_SendSentence_and_Finish", func(t *testing.T) {
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel() // 提前取消

		stream := &TTSStream{
			ctx:    context.Background(),
			pcmCh:  make(chan []byte, 10),
			taskID: "test-task",
		}

		if err := stream.SendSentence(canceledCtx, "测试"); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled on SendSentence, got: %v", err)
		}

		if err := stream.Finish(canceledCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled on Finish, got: %v", err)
		}
	})

	t.Run("stream_context_cancel_unblocks_NextPCM", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, _ := websocket.Accept(w, r, nil)
			defer conn.Close(websocket.StatusInternalError, "closed")

			_, data, _ := conn.Read(r.Context())
			var runMsg ttsRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startResp := ttsResponseMessage{}
			startResp.Header.Event = "task-started"
			startResp.Header.TaskID = runMsg.Header.TaskID
			sb, _ := json.Marshal(startResp)
			_ = conn.Write(r.Context(), websocket.MessageText, sb)

			// 持续阻塞
			_, _, _ = conn.Read(r.Context())
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
			t.Fatalf("NewTTSClient failed: %v", err)
		}

		streamCtx, streamCancel := context.WithCancel(context.Background())
		stream, err := client.CreateStream(streamCtx)
		if err != nil {
			streamCancel()
			t.Fatalf("CreateStream failed: %v", err)
		}
		defer stream.Close()

		doneCh := make(chan error, 1)
		go func() {
			_, rErr := stream.NextPCM(context.Background())
			doneCh <- rErr
		}()

		// 主动取消外部 context
		time.Sleep(20 * time.Millisecond)
		streamCancel()

		select {
		case rErr := <-doneCh:
			if !errors.Is(rErr, context.Canceled) && !strings.Contains(rErr.Error(), "closed") {
				t.Fatalf("expected context.Canceled or closed error, got: %v", rErr)
			}
		case <-time.After(1 * time.Second):
			t.Fatal("NextPCM did not unblock after stream context was canceled")
		}
	})

	t.Run("stream_close_unblocks_NextPCM", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, _ := websocket.Accept(w, r, nil)
			defer conn.Close(websocket.StatusInternalError, "closed")

			_, data, _ := conn.Read(r.Context())
			var runMsg ttsRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startResp := ttsResponseMessage{}
			startResp.Header.Event = "task-started"
			startResp.Header.TaskID = runMsg.Header.TaskID
			sb, _ := json.Marshal(startResp)
			_ = conn.Write(r.Context(), websocket.MessageText, sb)

			_, _, _ = conn.Read(r.Context())
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
			t.Fatalf("NewTTSClient failed: %v", err)
		}

		stream, err := client.CreateStream(context.Background())
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}

		doneCh := make(chan error, 1)
		go func() {
			_, rErr := stream.NextPCM(context.Background())
			doneCh <- rErr
		}()

		// 主动 Close
		time.Sleep(20 * time.Millisecond)
		if cErr := stream.Close(); cErr != nil {
			t.Fatalf("Close failed: %v", cErr)
		}

		select {
		case rErr := <-doneCh:
			if rErr == nil || (!strings.Contains(rErr.Error(), "closed") && !errors.Is(rErr, context.Canceled)) {
				t.Fatalf("expected closed or canceled error on NextPCM, got: %v", rErr)
			}
		case <-time.After(1 * time.Second):
			t.Fatal("NextPCM did not unblock after stream.Close()")
		}
	})
}

func TestTTSStream_ConcurrentClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "closed")

		_, data, _ := conn.Read(r.Context())
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startResp := ttsResponseMessage{}
		startResp.Header.Event = "task-started"
		startResp.Header.TaskID = runMsg.Header.TaskID
		sb, _ := json.Marshal(startResp)
		_ = conn.Write(r.Context(), websocket.MessageText, sb)

		for {
			_, _, err := conn.Read(r.Context())
			if err != nil {
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
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()
			if id%3 == 0 {
				_ = stream.SendSentence(context.Background(), "并发发送")
			} else if id%3 == 1 {
				_, _ = stream.NextPCM(context.Background())
			}
			_ = stream.Close()
		}(i)
	}

	wg.Wait()

	// 再次验证 Close 后的状态
	if err := stream.SendSentence(context.Background(), "after close"); err == nil {
		t.Fatal("expected SendSentence to fail after Close, got nil")
	}
	if err := stream.Finish(context.Background()); err == nil {
		t.Fatal("expected Finish to fail after Close, got nil")
	}
}

func TestTTSStream_CancelMethod(t *testing.T) {
	var (
		mu                 sync.Mutex
		receivedCancelTask bool
		cancelTaskID       string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, nil)
		defer conn.Close(websocket.StatusInternalError, "closed")

		_, data, _ := conn.Read(r.Context())
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startResp := ttsResponseMessage{}
		startResp.Header.Event = "task-started"
		startResp.Header.TaskID = runMsg.Header.TaskID
		sb, _ := json.Marshal(startResp)
		_ = conn.Write(r.Context(), websocket.MessageText, sb)

		for {
			mType, mData, err := conn.Read(r.Context())
			if err != nil {
				break
			}
			if mType == websocket.MessageText {
				var msg struct {
					Header struct {
						Action string `json:"action"`
						TaskID string `json:"task_id"`
					} `json:"header"`
				}
				if err := json.Unmarshal(mData, &msg); err == nil && msg.Header.Action == "cancel-task" {
					mu.Lock()
					receivedCancelTask = true
					cancelTaskID = msg.Header.TaskID
					mu.Unlock()
					_ = conn.Close(websocket.StatusNormalClosure, "task-canceled")
					return
				}
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
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	rawStream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	stream := rawStream.(*TTSStream)

	// 调用 Cancel 发送 cancel-task
	if err := stream.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	// 等待服务端收到消息
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	gotCancel := receivedCancelTask
	taskID := cancelTaskID
	mu.Unlock()

	if !gotCancel {
		t.Fatal("expected mock server to receive cancel-task message")
	}
	if taskID != stream.taskID {
		t.Fatalf("expected cancel-task task_id %s, got %s", stream.taskID, taskID)
	}

	// Cancel 后会话处于关闭状态
	if err := stream.SendSentence(context.Background(), "新文本"); err == nil {
		t.Fatal("expected SendSentence to fail after Cancel, got nil")
	}
}

func TestTTSClient_TimeoutConfiguration(t *testing.T) {
	t.Run("custom_timeouts_preserved", func(t *testing.T) {
		cfg := &config.Config{
			DashScopeAPIKey: "test-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint:           "ws://127.0.0.1:8080/ws",
					TTSModel:             "qwen-audio-3.0-tts-flash",
					TTSVoice:             "longanlingxi",
					TTSFirstAudioTimeout: 8 * time.Second,
					TTSSentenceTimeout:   25 * time.Second,
				},
			},
		}

		client, err := NewTTSClient(cfg)
		if err != nil {
			t.Fatalf("NewTTSClient failed: %v", err)
		}
		if client.firstAudioTimeout != 8*time.Second {
			t.Fatalf("expected firstAudioTimeout 8s, got %v", client.firstAudioTimeout)
		}
		if client.sentenceTimeout != 25*time.Second {
			t.Fatalf("expected sentenceTimeout 25s, got %v", client.sentenceTimeout)
		}
	})

	t.Run("default_timeouts_fallback", func(t *testing.T) {
		cfg := &config.Config{
			DashScopeAPIKey: "test-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint: "ws://127.0.0.1:8080/ws",
					TTSModel:   "qwen-audio-3.0-tts-flash",
					TTSVoice:   "longanlingxi",
				},
			},
		}

		client, err := NewTTSClient(cfg)
		if err != nil {
			t.Fatalf("NewTTSClient failed: %v", err)
		}
		if client.firstAudioTimeout != 5*time.Second {
			t.Fatalf("expected default firstAudioTimeout 5s, got %v", client.firstAudioTimeout)
		}
		if client.sentenceTimeout != 20*time.Second {
			t.Fatalf("expected default sentenceTimeout 20s, got %v", client.sentenceTimeout)
		}
	})
}

func TestTTSStream_FirstAudioTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "closed")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startResp := ttsResponseMessage{}
		startResp.Header.Event = "task-started"
		startResp.Header.TaskID = runMsg.Header.TaskID
		sb, _ := json.Marshal(startResp)
		_ = conn.Write(r.Context(), websocket.MessageText, sb)

		// 持续读取客户端请求，但不返回任何 PCM 音频数据，模拟首音频假死
		for {
			_, _, rErr := conn.Read(r.Context())
			if rErr != nil {
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
				WSEndpoint:           wsURL,
				TTSModel:             "qwen-audio-3.0-tts-flash",
				TTSVoice:             "longanlingxi",
				TTSFirstAudioTimeout: 80 * time.Millisecond,
				TTSSentenceTimeout:   300 * time.Millisecond,
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 发送首句文本
	if err := stream.SendSentence(context.Background(), "你好，请回答。"); err != nil {
		t.Fatalf("SendSentence failed: %v", err)
	}

	doneCh := make(chan error, 1)
	go func() {
		_, rErr := stream.NextPCM(context.Background())
		doneCh <- rErr
	}()

	select {
	case rErr := <-doneCh:
		if rErr == nil {
			t.Fatal("expected error on first audio timeout, got nil")
		}
		if !errors.Is(rErr, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded in error chain, got: %v", rErr)
		}
		if !strings.Contains(rErr.Error(), "first audio timeout") {
			t.Fatalf("expected error message to contain 'first audio timeout', got: %v", rErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("NextPCM did not unblock in time on first audio timeout")
	}

	// 验证超时后后续调用均安全报错
	if err := stream.SendSentence(context.Background(), "后续句子"); err == nil {
		t.Fatal("expected SendSentence to fail after timeout, got nil")
	}
}

func TestTTSStream_SentenceTimeout_MidStreamHang(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "closed")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startResp := ttsResponseMessage{}
		startResp.Header.Event = "task-started"
		startResp.Header.TaskID = runMsg.Header.TaskID
		sb, _ := json.Marshal(startResp)
		_ = conn.Write(r.Context(), websocket.MessageText, sb)

		var sentenceIndex int
		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageText && strings.Contains(string(mData), "continue-task") {
				sentenceIndex++
				if sentenceIndex == 1 {
					// 第一句正常返回 PCM 数据块
					_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{0x01, 0x02, 0x03, 0x04})
				}
				// 第二句收到后假死挂起，不再返回任何数据
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:           wsURL,
				TTSModel:             "qwen-audio-3.0-tts-flash",
				TTSVoice:             "longanlingxi",
				TTSFirstAudioTimeout: 300 * time.Millisecond,
				TTSSentenceTimeout:   80 * time.Millisecond,
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 发送第一句
	if err := stream.SendSentence(context.Background(), "第一句话。"); err != nil {
		t.Fatalf("SendSentence 1 failed: %v", err)
	}

	// 接收第一句的 PCM 音频
	chunk, err := stream.NextPCM(context.Background())
	if err != nil {
		t.Fatalf("expected initial pcm chunk, got error: %v", err)
	}
	if !bytes.Equal(chunk, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("unexpected chunk content: %v", chunk)
	}

	// 发送第二句（服务端将假死不回音频）
	if err := stream.SendSentence(context.Background(), "第二句话。"); err != nil {
		t.Fatalf("SendSentence 2 failed: %v", err)
	}

	doneCh := make(chan error, 1)
	go func() {
		_, rErr := stream.NextPCM(context.Background())
		doneCh <- rErr
	}()

	select {
	case rErr := <-doneCh:
		if rErr == nil {
			t.Fatal("expected error on sentence timeout, got nil")
		}
		if !errors.Is(rErr, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded in error chain, got: %v", rErr)
		}
		if !strings.Contains(rErr.Error(), "sentence timeout") {
			t.Fatalf("expected error message to contain 'sentence timeout', got: %v", rErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("NextPCM did not unblock in time on sentence timeout")
	}
}

func TestTTSStream_SentenceTimeout_FinishHang(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "closed")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startResp := ttsResponseMessage{}
		startResp.Header.Event = "task-started"
		startResp.Header.TaskID = runMsg.Header.TaskID
		sb, _ := json.Marshal(startResp)
		_ = conn.Write(r.Context(), websocket.MessageText, sb)

		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageText {
				if strings.Contains(string(mData), "continue-task") {
					// 响应 1 个音频块
					_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{0xaa, 0xbb})
				}
				// 收到 finish-task 后故意挂起，不发 task-finished 也不发剩余音频
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:           wsURL,
				TTSModel:             "qwen-audio-3.0-tts-flash",
				TTSVoice:             "longanlingxi",
				TTSFirstAudioTimeout: 300 * time.Millisecond,
				TTSSentenceTimeout:   80 * time.Millisecond,
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	if err := stream.SendSentence(context.Background(), "唯一的一句话。"); err != nil {
		t.Fatalf("SendSentence failed: %v", err)
	}

	chunk, err := stream.NextPCM(context.Background())
	if err != nil {
		t.Fatalf("expected pcm chunk, got error: %v", err)
	}
	if !bytes.Equal(chunk, []byte{0xaa, 0xbb}) {
		t.Fatalf("unexpected chunk content: %v", chunk)
	}

	// 调用 Finish 发送结束，随后等待流完成
	if err := stream.Finish(context.Background()); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	doneCh := make(chan error, 1)
	go func() {
		_, rErr := stream.NextPCM(context.Background())
		doneCh <- rErr
	}()

	select {
	case rErr := <-doneCh:
		if rErr == nil {
			t.Fatal("expected error on finish sentence timeout, got nil")
		}
		if !errors.Is(rErr, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded in error chain, got: %v", rErr)
		}
		if !strings.Contains(rErr.Error(), "sentence timeout") {
			t.Fatalf("expected error message to contain 'sentence timeout', got: %v", rErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("NextPCM did not unblock in time on finish sentence timeout")
	}
}

func TestTTSStream_NormalFlowWithSentenceReset(t *testing.T) {
	// 验证在正常多次交互且单句间隔内持续产出 PCM 时，定时器不断重置，正常完成不会被误杀
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "closed")

		_, data, _ := conn.Read(r.Context())
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startResp := ttsResponseMessage{}
		startResp.Header.Event = "task-started"
		startResp.Header.TaskID = runMsg.Header.TaskID
		sb, _ := json.Marshal(startResp)
		_ = conn.Write(r.Context(), websocket.MessageText, sb)

		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageText {
				if strings.Contains(string(mData), "continue-task") {
					// 持续下发 3 个小音频包，每个包间隔 20ms
					for i := 0; i < 3; i++ {
						time.Sleep(20 * time.Millisecond)
						_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{byte(i + 1)})
					}
				} else if strings.Contains(string(mData), "finish-task") {
					time.Sleep(20 * time.Millisecond)
					_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{0x99})
					finishResp := ttsResponseMessage{}
					finishResp.Header.Event = "task-finished"
					fb, _ := json.Marshal(finishResp)
					_ = conn.Write(r.Context(), websocket.MessageText, fb)
					return
				}
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:           wsURL,
				TTSModel:             "qwen-audio-3.0-tts-flash",
				TTSVoice:             "longanlingxi",
				TTSFirstAudioTimeout: 100 * time.Millisecond,
				TTSSentenceTimeout:   100 * time.Millisecond,
			},
		},
	}

	client, err := NewTTSClient(cfg)
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 发送两个句子，每个句子生成耗时约 60ms（总耗时约 140ms > 100ms 超时），但因中间持续有 PCM 重置定时器，应顺利完成
	if err := stream.SendSentence(context.Background(), "句子一"); err != nil {
		t.Fatalf("SendSentence 1 failed: %v", err)
	}
	if err := stream.SendSentence(context.Background(), "句子二"); err != nil {
		t.Fatalf("SendSentence 2 failed: %v", err)
	}
	if err := stream.Finish(context.Background()); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	var totalChunks int
	for {
		_, rErr := stream.NextPCM(context.Background())
		if errors.Is(rErr, io.EOF) {
			break
		}
		if rErr != nil {
			t.Fatalf("unexpected error during streaming: %v", rErr)
		}
		totalChunks++
	}

	if totalChunks != 7 { // 3 + 3 + 1
		t.Fatalf("expected 7 chunks, got %d", totalChunks)
	}
}
