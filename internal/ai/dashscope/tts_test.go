package dashscope

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/database"
)

func TestNewTTSClient_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *database.TTSConfig
		voice   string
		wantErr string
	}{
		{
			name:    "nil config",
			cfg:     nil,
			voice:   "longxiaochun",
			wantErr: "tts config cannot be nil",
		},
		{
			name: "empty endpoint",
			cfg: &database.TTSConfig{
				Endpoint: "  ",
				APIKey:   "sk-test",
				Model:    TargetTTSModel,
			},
			voice:   "longxiaochun",
			wantErr: "dashscope ws endpoint is required",
		},
		{
			name: "empty api key",
			cfg: &database.TTSConfig{
				Endpoint: "wss://test.maas.aliyuncs.com/api-ws/v1/inference",
				APIKey:   "   ",
				Model:    TargetTTSModel,
			},
			voice:   "longxiaochun",
			wantErr: "dashscope api key is required",
		},
		{
			name: "empty model",
			cfg: &database.TTSConfig{
				Endpoint: "wss://test.maas.aliyuncs.com/api-ws/v1/inference",
				APIKey:   "sk-test",
				Model:    "   ",
			},
			voice:   "longxiaochun",
			wantErr: "dashscope tts model is required",
		},
		{
			name: "unsupported model",
			cfg: &database.TTSConfig{
				Endpoint: "wss://test.maas.aliyuncs.com/api-ws/v1/inference",
				APIKey:   "sk-test",
				Model:    "cosyvoice-v1",
			},
			voice:   "longxiaochun",
			wantErr: "unsupported dashscope tts model: cosyvoice-v1",
		},
		{
			name: "empty voice",
			cfg: &database.TTSConfig{
				Endpoint: "wss://test.maas.aliyuncs.com/api-ws/v1/inference",
				APIKey:   "sk-test",
				Model:    TargetTTSModel,
			},
			voice:   "   ",
			wantErr: "tts voice is required",
		},
		{
			name: "invalid proxy url",
			cfg: &database.TTSConfig{
				Endpoint: "wss://test.maas.aliyuncs.com/api-ws/v1/inference",
				APIKey:   "sk-test",
				Model:    TargetTTSModel,
				ProxyURL: "://invalid-url",
			},
			voice:   "longxiaochun",
			wantErr: "parse proxy url",
		},
		{
			name: "valid config with default timeouts",
			cfg: &database.TTSConfig{
				Endpoint: "wss://test.maas.aliyuncs.com/api-ws/v1/inference",
				APIKey:   "sk-test",
				Model:    TargetTTSModel,
			},
			voice: "longxiaochun",
		},
		{
			name: "valid config with custom values and proxy",
			cfg: &database.TTSConfig{
				Endpoint:            "wss://test.maas.aliyuncs.com/api-ws/v1/inference",
				APIKey:              "sk-test",
				Model:               TargetTTSModel,
				ProxyURL:            "http://127.0.0.1:8080",
				ConnectTimeoutMS:    3000,
				FirstAudioTimeoutMS: 4000,
				SentenceTimeoutMS:   8000,
			},
			voice: "longxiaochun",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewTTSClient(tc.cfg, tc.voice)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if client == nil {
				t.Fatal("expected non-nil client")
			}
			if client.model != TargetTTSModel {
				t.Errorf("expected model %q, got %q", TargetTTSModel, client.model)
			}
			if client.voice != tc.voice {
				t.Errorf("expected voice %q, got %q", tc.voice, client.voice)
			}
			if client.connectTimeout <= 0 {
				t.Errorf("expected positive connect timeout, got %v", client.connectTimeout)
			}
			if client.firstAudioTimeout <= 0 {
				t.Errorf("expected positive first audio timeout, got %v", client.firstAudioTimeout)
			}
			if client.sentenceTimeout <= 0 {
				t.Errorf("expected positive sentence timeout, got %v", client.sentenceTimeout)
			}
		})
	}
}

func TestTTSClient_CreateStream_HeadersAndDial(t *testing.T) {
	var (
		gotAuthHeader           string
		gotInspectionHeader     string
		serverReceivedConnected bool
		mu                      sync.Mutex
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuthHeader = r.Header.Get("Authorization")
		gotInspectionHeader = r.Header.Get("X-DashScope-DataInspection")
		serverReceivedConnected = true
		mu.Unlock()

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "server close")

		// 保持连接直到客户端断开
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cfg := &database.TTSConfig{
		Endpoint:         wsURL,
		APIKey:           "test-api-key-12345",
		Model:            TargetTTSModel,
		ConnectTimeoutMS: 2000,
	}

	client, err := NewTTSClient(cfg, "longxiaochun")
	if err != nil {
		t.Fatalf("NewTTSClient error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	mu.Lock()
	defer mu.Unlock()
	if !serverReceivedConnected {
		t.Fatal("server did not receive connection")
	}
	expectedAuth := "Bearer test-api-key-12345"
	if gotAuthHeader != expectedAuth {
		t.Errorf("expected Authorization %q, got %q", expectedAuth, gotAuthHeader)
	}
	expectedInspection := "enable"
	if gotInspectionHeader != expectedInspection {
		t.Errorf("expected X-DashScope-DataInspection %q, got %q", expectedInspection, gotInspectionHeader)
	}
}

func TestTTSClient_CreateStream_DialFailure(t *testing.T) {
	cfg := &database.TTSConfig{
		Endpoint:         "ws://127.0.0.1:1", // 不可达地址
		APIKey:           "test-api-key",
		Model:            TargetTTSModel,
		ConnectTimeoutMS: 500,
	}

	client, err := NewTTSClient(cfg, "longxiaochun")
	if err != nil {
		t.Fatalf("NewTTSClient error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err == nil {
		if stream != nil {
			_ = stream.Close()
		}
		t.Fatal("expected error on dialing unreachable endpoint, got nil")
	}
	if !strings.Contains(err.Error(), "dial dashscope tts websocket") {
		t.Errorf("expected dial error message, got: %v", err)
	}
}

func TestTTSStream_SynthesizeSentence_NormalProtocolFlow(t *testing.T) {
	var (
		receivedRunTask      ttsRunTaskMessage
		receivedContinueTask ttsContinueTaskMessage
		receivedFinishTask   ttsFinishTaskMessage
		runTaskId            string
		continueTaskId       string
		finishTaskId         string
		serverErr            error
		serverDone           = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverErr = fmt.Errorf("accept websocket: %w", err)
			close(serverDone)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		defer close(serverDone)

		ctx := r.Context()

		// 1. 读取 run-task
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			serverErr = fmt.Errorf("read run-task: %w", err)
			return
		}
		if msgType != websocket.MessageText {
			serverErr = fmt.Errorf("expected text message for run-task, got %v", msgType)
			return
		}
		if err := json.Unmarshal(data, &receivedRunTask); err != nil {
			serverErr = fmt.Errorf("unmarshal run-task: %w", err)
			return
		}
		runTaskId = receivedRunTask.Header.TaskId

		// 2. 发送 task-started 事件
		startedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": runTaskId,
				"event":   "task-started",
			},
			"payload": map[string]any{},
		}
		startedBytes, _ := json.Marshal(startedResp)
		if err := conn.Write(ctx, websocket.MessageText, startedBytes); err != nil {
			serverErr = fmt.Errorf("write task-started: %w", err)
			return
		}

		// 3. 读取 continue-task
		msgType, data, err = conn.Read(ctx)
		if err != nil {
			serverErr = fmt.Errorf("read continue-task: %w", err)
			return
		}
		if msgType != websocket.MessageText {
			serverErr = fmt.Errorf("expected text message for continue-task, got %v", msgType)
			return
		}
		if err := json.Unmarshal(data, &receivedContinueTask); err != nil {
			serverErr = fmt.Errorf("unmarshal continue-task: %w", err)
			return
		}
		continueTaskId = receivedContinueTask.Header.TaskId

		// 4. 读取 finish-task
		msgType, data, err = conn.Read(ctx)
		if err != nil {
			serverErr = fmt.Errorf("read finish-task: %w", err)
			return
		}
		if msgType != websocket.MessageText {
			serverErr = fmt.Errorf("expected text message for finish-task, got %v", msgType)
			return
		}
		if err := json.Unmarshal(data, &receivedFinishTask); err != nil {
			serverErr = fmt.Errorf("unmarshal finish-task: %w", err)
			return
		}
		finishTaskId = receivedFinishTask.Header.TaskId

		// 5. 下发 PCM 二进制分片 1
		pcmChunk1 := []byte{0x01, 0x02, 0x03, 0x04}
		if err := conn.Write(ctx, websocket.MessageBinary, pcmChunk1); err != nil {
			serverErr = fmt.Errorf("write pcm chunk 1: %w", err)
			return
		}

		// 6. 下发合法中间事件 result-generated
		intermediateResp := map[string]any{
			"header": map[string]any{
				"action":  "result-generated",
				"task_id": runTaskId,
				"event":   "result-generated",
			},
			"payload": map[string]any{},
		}
		interBytes, _ := json.Marshal(intermediateResp)
		if err := conn.Write(ctx, websocket.MessageText, interBytes); err != nil {
			serverErr = fmt.Errorf("write result-generated: %w", err)
			return
		}

		// 7. 下发 PCM 二进制分片 2
		pcmChunk2 := []byte{0x05, 0x06, 0x07, 0x08}
		if err := conn.Write(ctx, websocket.MessageBinary, pcmChunk2); err != nil {
			serverErr = fmt.Errorf("write pcm chunk 2: %w", err)
			return
		}

		// 8. 下发 task-finished 终态事件
		finishedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-finished",
				"task_id": runTaskId,
				"event":   "task-finished",
			},
			"payload": map[string]any{},
		}
		finishedBytes, _ := json.Marshal(finishedResp)
		if err := conn.Write(ctx, websocket.MessageText, finishedBytes); err != nil {
			serverErr = fmt.Errorf("write task-finished: %w", err)
			return
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &database.TTSConfig{
		Endpoint: wsURL,
		APIKey:   "sk-test-auth",
		Model:    TargetTTSModel,
	}

	client, err := NewTTSClient(cfg, "longxiaochun")
	if err != nil {
		t.Fatalf("NewTTSClient error: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	var (
		collectedChunks [][]byte
		chunkMu         sync.Mutex
	)

	onPCM := func(ctx context.Context, data []byte) error {
		chunkMu.Lock()
		defer chunkMu.Unlock()
		cp := make([]byte, len(data))
		copy(cp, data)
		collectedChunks = append(collectedChunks, cp)
		return nil
	}

	sentenceText := "你好，小智！今天天气真好。"
	err = stream.SynthesizeSentence(context.Background(), sentenceText, onPCM)
	if err != nil {
		t.Fatalf("SynthesizeSentence failed: %v", err)
	}

	// 等待服务端协程完成
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server handler")
	}

	if serverErr != nil {
		t.Fatalf("server handler error: %v", serverErr)
	}

	// 验证请求参数
	if receivedRunTask.Header.Action != "run-task" {
		t.Errorf("expected run-task action, got %s", receivedRunTask.Header.Action)
	}
	if receivedRunTask.Header.Streaming != "duplex" {
		t.Errorf("expected duplex streaming, got %s", receivedRunTask.Header.Streaming)
	}
	if receivedRunTask.Header.TaskId == "" {
		t.Error("expected non-empty task_id in run-task")
	}
	if receivedRunTask.Payload.TaskGroup != "audio" {
		t.Errorf("expected task_group 'audio', got %s", receivedRunTask.Payload.TaskGroup)
	}
	if receivedRunTask.Payload.Task != "tts" {
		t.Errorf("expected task 'tts', got %s", receivedRunTask.Payload.Task)
	}
	if receivedRunTask.Payload.Function != "SpeechSynthesizer" {
		t.Errorf("expected function 'SpeechSynthesizer', got %s", receivedRunTask.Payload.Function)
	}
	if receivedRunTask.Payload.Model != TargetTTSModel {
		t.Errorf("expected model %s, got %s", TargetTTSModel, receivedRunTask.Payload.Model)
	}
	params := receivedRunTask.Payload.Parameters
	if params.TextType != "PlainText" {
		t.Errorf("expected text_type 'PlainText', got %s", params.TextType)
	}
	if params.Voice != "longxiaochun" {
		t.Errorf("expected voice 'longxiaochun', got %s", params.Voice)
	}
	if params.Format != "pcm" {
		t.Errorf("expected format 'pcm', got %s", params.Format)
	}
	if params.SampleRate != 24000 {
		t.Errorf("expected sample_rate 24000, got %d", params.SampleRate)
	}
	if params.Volume != 50 {
		t.Errorf("expected volume 50, got %d", params.Volume)
	}
	if params.Rate != 1.0 {
		t.Errorf("expected rate 1.0, got %f", params.Rate)
	}
	if params.Pitch != 1.0 {
		t.Errorf("expected pitch 1.0, got %f", params.Pitch)
	}
	if params.EnableSSML != false {
		t.Errorf("expected enable_ssml false, got %v", params.EnableSSML)
	}

	// 验证相同 TaskId
	if continueTaskId != runTaskId {
		t.Errorf("continue-task task_id %q does not match run-task task_id %q", continueTaskId, runTaskId)
	}
	if finishTaskId != runTaskId {
		t.Errorf("finish-task task_id %q does not match run-task task_id %q", finishTaskId, runTaskId)
	}

	// 验证 continue-task 文本内容与 finish-task 指令
	if receivedContinueTask.Payload.Input.Text != sentenceText {
		t.Errorf("expected continue-task text %q, got %q", sentenceText, receivedContinueTask.Payload.Input.Text)
	}
	if receivedFinishTask.Payload.Input.Directive != "" {
		t.Errorf("expected empty directive for normal finish-task, got %q", receivedFinishTask.Payload.Input.Directive)
	}

	// 验证 PCM 回调顺序及完整性
	chunkMu.Lock()
	defer chunkMu.Unlock()
	if len(collectedChunks) != 2 {
		t.Fatalf("expected 2 pcm chunks, got %d", len(collectedChunks))
	}
	if string(collectedChunks[0]) != string([]byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("unexpected first pcm chunk: %v", collectedChunks[0])
	}
	if string(collectedChunks[1]) != string([]byte{0x05, 0x06, 0x07, 0x08}) {
		t.Errorf("unexpected second pcm chunk: %v", collectedChunks[1])
	}
}

func TestTTSStream_SynthesizeSentence_Validation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &database.TTSConfig{
		Endpoint: wsURL,
		APIKey:   "sk-test",
		Model:    TargetTTSModel,
	}

	client, err := NewTTSClient(cfg, "longxiaochun")
	if err != nil {
		t.Fatalf("NewTTSClient error: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}
	defer stream.Close()

	// 1. 空文本
	err = stream.SynthesizeSentence(context.Background(), "", func(ctx context.Context, b []byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "synthesis text cannot be empty") {
		t.Errorf("expected empty text error, got: %v", err)
	}

	// 2. 纯空白文本
	err = stream.SynthesizeSentence(context.Background(), "   \n\t  ", func(ctx context.Context, b []byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "synthesis text cannot be empty") {
		t.Errorf("expected whitespace text error, got: %v", err)
	}

	// 3. nil 回调
	err = stream.SynthesizeSentence(context.Background(), "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "onPCM callback cannot be nil") {
		t.Errorf("expected nil onPCM error, got: %v", err)
	}

	// 4. 关闭后调用
	_ = stream.Close()
	err = stream.SynthesizeSentence(context.Background(), "hello", func(ctx context.Context, b []byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "tts stream is closed") {
		t.Errorf("expected closed stream error, got: %v", err)
	}
}

func TestTTSStream_CancelAndClose_Idempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &database.TTSConfig{
		Endpoint: wsURL,
		APIKey:   "sk-test",
		Model:    TargetTTSModel,
	}

	client, err := NewTTSClient(cfg, "longxiaochun")
	if err != nil {
		t.Fatalf("NewTTSClient error: %v", err)
	}

	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatalf("CreateStream error: %v", err)
	}

	// 无活跃任务时 Cancel 幂等返回 nil
	if err := stream.Cancel(context.Background()); err != nil {
		t.Errorf("expected Cancel on idle stream to return nil, got: %v", err)
	}

	// 连续多次 Close 幂等
	if err := stream.Close(); err != nil {
		t.Errorf("expected first Close to succeed, got: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("expected second Close to succeed, got: %v", err)
	}

	// 关闭后 Cancel 也幂等返回 nil
	if err := stream.Cancel(context.Background()); err != nil {
		t.Errorf("expected Cancel on closed stream to return nil, got: %v", err)
	}
}
