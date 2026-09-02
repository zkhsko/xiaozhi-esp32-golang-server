package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// mockHandleSingleTask 在指定 WebSocket 连接上模拟服务端处理单句语音合成的完整时序。
func mockHandleSingleTask(ctx context.Context, conn *websocket.Conn, expectedText string, pcmChunks [][]byte) (string, error) {
	// 1. 读取 run-task
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("read run-task: %w", err)
	}
	if msgType != websocket.MessageText {
		return "", fmt.Errorf("expected text message for run-task, got %v", msgType)
	}
	var runMsg ttsRunTaskMessage
	if err := json.Unmarshal(data, &runMsg); err != nil {
		return "", fmt.Errorf("unmarshal run-task: %w", err)
	}
	taskId := runMsg.Header.TaskId
	if taskId == "" {
		return "", errors.New("empty task_id in run-task")
	}

	// 2. 发送 task-started
	startedResp := map[string]any{
		"header": map[string]any{
			"action":  "task-started",
			"task_id": taskId,
			"event":   "task-started",
		},
		"payload": map[string]any{},
	}
	startedBytes, _ := json.Marshal(startedResp)
	if err := conn.Write(ctx, websocket.MessageText, startedBytes); err != nil {
		return "", fmt.Errorf("write task-started: %w", err)
	}

	// 3. 读取 continue-task
	msgType, data, err = conn.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("read continue-task: %w", err)
	}
	var continueMsg ttsContinueTaskMessage
	if err := json.Unmarshal(data, &continueMsg); err != nil {
		return "", fmt.Errorf("unmarshal continue-task: %w", err)
	}
	if continueMsg.Header.TaskId != taskId {
		return "", fmt.Errorf("continue-task task_id mismatch: got %s, want %s", continueMsg.Header.TaskId, taskId)
	}
	if expectedText != "" && continueMsg.Payload.Input.Text != expectedText {
		return "", fmt.Errorf("continue-task text mismatch: got %s, want %s", continueMsg.Payload.Input.Text, expectedText)
	}

	// 4. 读取 finish-task
	msgType, data, err = conn.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("read finish-task: %w", err)
	}
	var finishMsg ttsFinishTaskMessage
	if err := json.Unmarshal(data, &finishMsg); err != nil {
		return "", fmt.Errorf("unmarshal finish-task: %w", err)
	}
	if finishMsg.Header.TaskId != taskId {
		return "", fmt.Errorf("finish-task task_id mismatch: got %s, want %s", finishMsg.Header.TaskId, taskId)
	}

	// 5. 发送 PCM 二进制数据分片
	for i, chunk := range pcmChunks {
		if err := conn.Write(ctx, websocket.MessageBinary, chunk); err != nil {
			return "", fmt.Errorf("write pcm chunk %d: %w", i, err)
		}
	}

	// 6. 发送 task-finished
	finishedResp := map[string]any{
		"header": map[string]any{
			"action":  "task-finished",
			"task_id": taskId,
			"event":   "task-finished",
		},
		"payload": map[string]any{},
	}
	finishedBytes, _ := json.Marshal(finishedResp)
	if err := conn.Write(ctx, websocket.MessageText, finishedBytes); err != nil {
		return "", fmt.Errorf("write task-finished: %w", err)
	}

	return taskId, nil
}

func TestTTSStream_SequentialReuse_SingleConnectionMultipleSentences(t *testing.T) {
	var (
		serverConnCount int32
		serverErr       error
		serverDone      = make(chan struct{})
		taskIds         []string
		mu              sync.Mutex
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverErr = fmt.Errorf("accept websocket: %w", err)
			close(serverDone)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "server close")
		defer close(serverDone)

		atomic.AddInt32(&serverConnCount, 1)
		ctx := r.Context()

		// 顺序处理第一句
		task1Id, err := mockHandleSingleTask(ctx, conn, "第一句文本", [][]byte{{0x01, 0x02}, {0x03, 0x04}})
		if err != nil {
			serverErr = fmt.Errorf("task 1 failed: %w", err)
			return
		}

		// 顺序处理第二句
		task2Id, err := mockHandleSingleTask(ctx, conn, "第二句文本", [][]byte{{0x05, 0x06}})
		if err != nil {
			serverErr = fmt.Errorf("task 2 failed: %w", err)
			return
		}

		mu.Lock()
		taskIds = append(taskIds, task1Id, task2Id)
		mu.Unlock()

		// 保持连接直到客户端主动发送关闭帧
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &database.TTSConfig{
		Endpoint: wsURL,
		APIKey:   "sk-test-reuse",
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

	// 1. 合成第一句
	var pcmSentence1 [][]byte
	onPCM1 := func(ctx context.Context, data []byte) error {
		cp := make([]byte, len(data))
		copy(cp, data)
		pcmSentence1 = append(pcmSentence1, cp)
		return nil
	}
	if err := stream.SynthesizeSentence(context.Background(), "第一句文本", onPCM1); err != nil {
		t.Fatalf("sentence 1 SynthesizeSentence failed: %v", err)
	}

	// 验证第一句 PCM 数据
	if len(pcmSentence1) != 2 {
		t.Fatalf("expected 2 pcm chunks for sentence 1, got %d", len(pcmSentence1))
	}
	if string(pcmSentence1[0]) != string([]byte{0x01, 0x02}) || string(pcmSentence1[1]) != string([]byte{0x03, 0x04}) {
		t.Errorf("unexpected sentence 1 pcm: %v", pcmSentence1)
	}

	// 验证首句 task-finished 后物理连接仍保持打开（服务端尚未断开连接）
	select {
	case <-serverDone:
		t.Fatal("server connection closed prematurely after first sentence")
	default:
	}

	// 2. 在同一 Stream 物理连接上合成第二句
	var pcmSentence2 [][]byte
	onPCM2 := func(ctx context.Context, data []byte) error {
		cp := make([]byte, len(data))
		copy(cp, data)
		pcmSentence2 = append(pcmSentence2, cp)
		return nil
	}
	if err := stream.SynthesizeSentence(context.Background(), "第二句文本", onPCM2); err != nil {
		t.Fatalf("sentence 2 SynthesizeSentence failed: %v", err)
	}

	// 验证第二句 PCM 数据
	if len(pcmSentence2) != 1 {
		t.Fatalf("expected 1 pcm chunk for sentence 2, got %d", len(pcmSentence2))
	}
	if string(pcmSentence2[0]) != string([]byte{0x05, 0x06}) {
		t.Errorf("unexpected sentence 2 pcm: %v", pcmSentence2)
	}

	// 3. 轮末主动关闭 Stream
	if err := stream.Close(); err != nil {
		t.Errorf("stream Close error: %v", err)
	}

	// 等待服务端退出
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server handler to complete")
	}

	if serverErr != nil {
		t.Fatalf("server error: %v", serverErr)
	}

	// 验证仅建立了一条物理连接
	if count := atomic.LoadInt32(&serverConnCount); count != 1 {
		t.Errorf("expected exactly 1 physical connection, got %d", count)
	}

	// 验证两句使用了不同的 task_id
	mu.Lock()
	defer mu.Unlock()
	if len(taskIds) != 2 {
		t.Fatalf("expected 2 task ids, got %d", len(taskIds))
	}
	if taskIds[0] == "" || taskIds[1] == "" {
		t.Errorf("task ids cannot be empty: %v", taskIds)
	}
	if taskIds[0] == taskIds[1] {
		t.Errorf("adjacent sentences must use different task ids, got identical: %s", taskIds[0])
	}
}

func TestTTSStream_ConcurrentSynthesize_RejectedImmediately(t *testing.T) {
	var (
		serverConnCount int32
		serverErr       error
		serverDone      = make(chan struct{})
		task1StartedCh  = make(chan struct{})
		releaseTask1Ch  = make(chan struct{})
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

		atomic.AddInt32(&serverConnCount, 1)
		ctx := r.Context()

		// 1. 读取第 1 句 run-task
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			serverErr = fmt.Errorf("read run-task: %w", err)
			return
		}
		if msgType != websocket.MessageText {
			serverErr = fmt.Errorf("expected text message, got %v", msgType)
			return
		}
		var runMsg ttsRunTaskMessage
		if err := json.Unmarshal(data, &runMsg); err != nil {
			serverErr = fmt.Errorf("unmarshal run-task: %w", err)
			return
		}
		task1Id := runMsg.Header.TaskId

		// 通知客户端测试协程：第 1 句任务已进入执行中（非空闲状态）
		close(task1StartedCh)

		// 挂起，等待并发调用验证完成
		select {
		case <-releaseTask1Ch:
		case <-ctx.Done():
			serverErr = ctx.Err()
			return
		}

		// 发送 task-started
		startedResp, _ := json.Marshal(map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": task1Id,
				"event":   "task-started",
			},
			"payload": map[string]any{},
		})
		if err := conn.Write(ctx, websocket.MessageText, startedResp); err != nil {
			serverErr = fmt.Errorf("write task-started: %w", err)
			return
		}

		// 读取 continue-task
		if _, _, err := conn.Read(ctx); err != nil {
			serverErr = fmt.Errorf("read continue-task: %w", err)
			return
		}
		// 读取 finish-task
		if _, _, err := conn.Read(ctx); err != nil {
			serverErr = fmt.Errorf("read finish-task: %w", err)
			return
		}

		// 下发 PCM
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{0xAA, 0xBB}); err != nil {
			serverErr = fmt.Errorf("write pcm: %w", err)
			return
		}

		// 下发 task-finished
		finishedResp, _ := json.Marshal(map[string]any{
			"header": map[string]any{
				"action":  "task-finished",
				"task_id": task1Id,
				"event":   "task-finished",
			},
			"payload": map[string]any{},
		})
		if err := conn.Write(ctx, websocket.MessageText, finishedResp); err != nil {
			serverErr = fmt.Errorf("write task-finished: %w", err)
			return
		}

		// 处理后续顺序执行的恢复测试任务
		_, err = mockHandleSingleTask(ctx, conn, "并发拒绝后恢复测试", [][]byte{{0xCC, 0xDD}})
		if err != nil {
			serverErr = fmt.Errorf("task after concurrent failed: %w", err)
			return
		}

		// 等待客户端主动关闭
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &database.TTSConfig{
		Endpoint: wsURL,
		APIKey:   "sk-test-concurrent",
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

	// 启动协程 1 执行第一句
	task1Done := make(chan error, 1)
	var pcmTask1 [][]byte
	var pcmTask1Mu sync.Mutex
	go func() {
		err := stream.SynthesizeSentence(context.Background(), "正在执行的第一句", func(ctx context.Context, b []byte) error {
			pcmTask1Mu.Lock()
			defer pcmTask1Mu.Unlock()
			pcmTask1 = append(pcmTask1, append([]byte(nil), b...))
			return nil
		})
		task1Done <- err
	}()

	// 等待第 1 句进入执行中
	select {
	case <-task1StartedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for task 1 to start")
	}

	// 在主协程尝试并发调用 SynthesizeSentence，必须立即被拒绝
	concurrentCallStart := time.Now()
	errConcurrent := stream.SynthesizeSentence(context.Background(), "并发调用的第二句", func(ctx context.Context, b []byte) error {
		return nil
	})
	concurrentCallDuration := time.Since(concurrentCallStart)

	if errConcurrent == nil {
		t.Fatal("expected error on concurrent SynthesizeSentence, got nil")
	}
	if !errors.Is(errConcurrent, ErrConcurrentSynthesize) {
		t.Fatalf("expected ErrConcurrentSynthesize, got: %v", errConcurrent)
	}
	if concurrentCallDuration > 100*time.Millisecond {
		t.Errorf("concurrent call was queued or blocked too long: %v", concurrentCallDuration)
	}

	// 释放服务端第 1 句处理
	close(releaseTask1Ch)

	// 等待第 1 句完成
	select {
	case err := <-task1Done:
		if err != nil {
			t.Fatalf("task 1 failed unexpectedly: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for task 1 to finish")
	}

	// 验证第 1 句的 PCM 数据完整
	pcmTask1Mu.Lock()
	if len(pcmTask1) != 1 || string(pcmTask1[0]) != string([]byte{0xAA, 0xBB}) {
		t.Errorf("unexpected task 1 pcm: %v", pcmTask1)
	}
	pcmTask1Mu.Unlock()

	// 验证第 1 句完成后，Stream 恢复空闲，可继续顺序执行后续任务
	var pcmRecovery [][]byte
	errRecovery := stream.SynthesizeSentence(context.Background(), "并发拒绝后恢复测试", func(ctx context.Context, b []byte) error {
		pcmRecovery = append(pcmRecovery, append([]byte(nil), b...))
		return nil
	})
	if errRecovery != nil {
		t.Fatalf("recovery sentence SynthesizeSentence failed: %v", errRecovery)
	}
	if len(pcmRecovery) != 1 || string(pcmRecovery[0]) != string([]byte{0xCC, 0xDD}) {
		t.Errorf("unexpected recovery pcm: %v", pcmRecovery)
	}

	// 轮末主动关闭
	if err := stream.Close(); err != nil {
		t.Errorf("stream Close error: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server handler to finish")
	}

	if serverErr != nil {
		t.Fatalf("server error: %v", serverErr)
	}
	if count := atomic.LoadInt32(&serverConnCount); count != 1 {
		t.Errorf("expected exactly 1 connection, got %d", count)
	}
}

func TestTTSStream_ConcurrentSynthesize_MultiGoroutines_RaceProtection(t *testing.T) {
	var (
		serverDone = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			close(serverDone)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		defer close(serverDone)

		ctx := r.Context()
		for {
			_, err := mockHandleSingleTask(ctx, conn, "", [][]byte{{0x01}})
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := &database.TTSConfig{
		Endpoint: wsURL,
		APIKey:   "sk-test-race",
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

	const numWorkers = 8
	var wg sync.WaitGroup
	var concurrentErrCount int32
	var successCount int32

	startCh := make(chan struct{})
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startCh
			err := stream.SynthesizeSentence(context.Background(), fmt.Sprintf("并发句子%d", idx), func(ctx context.Context, b []byte) error {
				time.Sleep(10 * time.Millisecond)
				return nil
			})
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else if errors.Is(err, ErrConcurrentSynthesize) {
				atomic.AddInt32(&concurrentErrCount, 1)
			}
		}(i)
	}

	close(startCh)
	wg.Wait()

	// 在多协程并发竞争下，必须有调用被并发保护拦截
	if atomic.LoadInt32(&concurrentErrCount) == 0 {
		t.Error("expected at least one call to be rejected with ErrConcurrentSynthesize")
	}
}

func mockReadRunTask(ctx context.Context, conn *websocket.Conn) (string, error) {
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("read run-task: %w", err)
	}
	if msgType != websocket.MessageText {
		return "", fmt.Errorf("expected text message for run-task, got %v", msgType)
	}
	var runMsg ttsRunTaskMessage
	if err := json.Unmarshal(data, &runMsg); err != nil {
		return "", fmt.Errorf("unmarshal run-task: %w", err)
	}
	if runMsg.Header.TaskId == "" {
		return "", errors.New("empty task_id in run-task")
	}
	return runMsg.Header.TaskId, nil
}

func mockReadContinueAndFinish(ctx context.Context, conn *websocket.Conn, expectedTaskId string) error {
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("read continue-task: %w", err)
	}
	if msgType != websocket.MessageText {
		return fmt.Errorf("expected text message for continue-task, got %v", msgType)
	}
	var continueMsg ttsContinueTaskMessage
	if err := json.Unmarshal(data, &continueMsg); err != nil {
		return fmt.Errorf("unmarshal continue-task: %w", err)
	}
	if expectedTaskId != "" && continueMsg.Header.TaskId != expectedTaskId {
		return fmt.Errorf("continue-task task_id mismatch: got %s, want %s", continueMsg.Header.TaskId, expectedTaskId)
	}

	msgType, data, err = conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("read finish-task: %w", err)
	}
	if msgType != websocket.MessageText {
		return fmt.Errorf("expected text message for finish-task, got %v", msgType)
	}
	var finishMsg ttsFinishTaskMessage
	if err := json.Unmarshal(data, &finishMsg); err != nil {
		return fmt.Errorf("unmarshal finish-task: %w", err)
	}
	if expectedTaskId != "" && finishMsg.Header.TaskId != expectedTaskId {
		return fmt.Errorf("finish-task task_id mismatch: got %s, want %s", finishMsg.Header.TaskId, expectedTaskId)
	}
	return nil
}

func TestTTSStream_ProtocolErrorsAndTaskFailed(t *testing.T) {
	tests := []struct {
		name              string
		serverHandler     func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error
		wantErrSubstrings []string
		checkErr          func(t *testing.T, err error, capturedTaskId string)
	}{
		{
			name: "binary_before_task_started",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				// 在 task-started 前发送二进制数据
				return conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x02})
			},
			wantErrSubstrings: []string{"received binary audio message before task-started"},
		},
		{
			name: "invalid_json_in_task_started",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				return conn.Write(ctx, websocket.MessageText, []byte("{invalid-json-response"))
			},
			wantErrSubstrings: []string{"unmarshal task-started JSON"},
		},
		{
			name: "missing_task_id_in_task_started",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				resp := `{"header":{"action":"task-started","event":"task-started"}}`
				return conn.Write(ctx, websocket.MessageText, []byte(resp))
			},
			wantErrSubstrings: []string{"missing task_id in task-started response"},
		},
		{
			name: "mismatched_task_id_in_task_started",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				resp := `{"header":{"action":"task-started","event":"task-started","task_id":"wrong-task-id-abc"}}`
				return conn.Write(ctx, websocket.MessageText, []byte(resp))
			},
			wantErrSubstrings: []string{"task_id mismatch in task-started", "wrong-task-id-abc"},
		},
		{
			name: "unexpected_event_in_task_started",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				resp := fmt.Sprintf(`{"header":{"action":"unknown-event","event":"unknown-event","task_id":"%s"}}`, taskId)
				return conn.Write(ctx, websocket.MessageText, []byte(resp))
			},
			wantErrSubstrings: []string{"unexpected event waiting for task-started: unknown-event"},
		},
		{
			name: "task_failed_in_task_started",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				resp := fmt.Sprintf(`{"header":{"action":"task-failed","event":"task-failed","task_id":"%s","error_code":"PrecheckFailed","error_message":"balance exhausted"}}`, taskId)
				return conn.Write(ctx, websocket.MessageText, []byte(resp))
			},
			wantErrSubstrings: []string{"tts task failed:", "PrecheckFailed", "balance exhausted"},
			checkErr: func(t *testing.T, err error, capturedTaskId string) {
				if capturedTaskId == "" {
					t.Fatal("expected capturedTaskId to be non-empty")
				}
				if !strings.Contains(err.Error(), capturedTaskId) {
					t.Errorf("expected error to contain task_id %q, got: %v", capturedTaskId, err)
				}
			},
		},
		{
			name: "conn_closed_before_task_started",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				return conn.Close(websocket.StatusGoingAway, "server shutdown")
			},
			wantErrSubstrings: []string{"read task-started"},
		},
		{
			name: "invalid_json_in_receiving_audio",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				startedResp := fmt.Sprintf(`{"header":{"action":"task-started","event":"task-started","task_id":"%s"}}`, taskId)
				if err := conn.Write(ctx, websocket.MessageText, []byte(startedResp)); err != nil {
					return err
				}
				if err := mockReadContinueAndFinish(ctx, conn, taskId); err != nil {
					return err
				}
				return conn.Write(ctx, websocket.MessageText, []byte("{corrupted-json-in-stream"))
			},
			wantErrSubstrings: []string{"unmarshal tts response JSON"},
		},
		{
			name: "missing_task_id_in_receiving_audio",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				startedResp := fmt.Sprintf(`{"header":{"action":"task-started","event":"task-started","task_id":"%s"}}`, taskId)
				if err := conn.Write(ctx, websocket.MessageText, []byte(startedResp)); err != nil {
					return err
				}
				if err := mockReadContinueAndFinish(ctx, conn, taskId); err != nil {
					return err
				}
				// 文本事件缺少 task_id
				missingTaskIdResp := `{"header":{"action":"result-generated","event":"result-generated"}}`
				return conn.Write(ctx, websocket.MessageText, []byte(missingTaskIdResp))
			},
			wantErrSubstrings: []string{"missing task_id in tts response"},
		},
		{
			name: "mismatched_task_id_in_receiving_audio",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				startedResp := fmt.Sprintf(`{"header":{"action":"task-started","event":"task-started","task_id":"%s"}}`, taskId)
				if err := conn.Write(ctx, websocket.MessageText, []byte(startedResp)); err != nil {
					return err
				}
				if err := mockReadContinueAndFinish(ctx, conn, taskId); err != nil {
					return err
				}
				// 文本事件携带不匹配的 task_id
				mismatchedResp := `{"header":{"action":"result-generated","event":"result-generated","task_id":"different-task-id-xyz"}}`
				return conn.Write(ctx, websocket.MessageText, []byte(mismatchedResp))
			},
			wantErrSubstrings: []string{"task_id mismatch", "different-task-id-xyz"},
		},
		{
			name: "unknown_event_in_receiving_audio",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				startedResp := fmt.Sprintf(`{"header":{"action":"task-started","event":"task-started","task_id":"%s"}}`, taskId)
				if err := conn.Write(ctx, websocket.MessageText, []byte(startedResp)); err != nil {
					return err
				}
				if err := mockReadContinueAndFinish(ctx, conn, taskId); err != nil {
					return err
				}
				unknownEventResp := fmt.Sprintf(`{"header":{"action":"unsupported-event","event":"unsupported-event","task_id":"%s"}}`, taskId)
				return conn.Write(ctx, websocket.MessageText, []byte(unknownEventResp))
			},
			wantErrSubstrings: []string{"unknown tts event: unsupported-event"},
		},
		{
			name: "task_failed_in_receiving_audio",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				startedResp := fmt.Sprintf(`{"header":{"action":"task-started","event":"task-started","task_id":"%s"}}`, taskId)
				if err := conn.Write(ctx, websocket.MessageText, []byte(startedResp)); err != nil {
					return err
				}
				if err := mockReadContinueAndFinish(ctx, conn, taskId); err != nil {
					return err
				}
				// 先下发一段 PCM
				if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x02}); err != nil {
					return err
				}
				// 下发 task-failed 事件
				failedResp := fmt.Sprintf(`{"header":{"action":"task-failed","event":"task-failed","task_id":"%s","error_code":"RateLimitExceeded","error_message":"request rate limit reached"}}`, taskId)
				return conn.Write(ctx, websocket.MessageText, []byte(failedResp))
			},
			wantErrSubstrings: []string{"tts task failed:", "RateLimitExceeded", "request rate limit reached"},
			checkErr: func(t *testing.T, err error, capturedTaskId string) {
				if capturedTaskId == "" {
					t.Fatal("expected capturedTaskId to be non-empty")
				}
				if !strings.Contains(err.Error(), capturedTaskId) {
					t.Errorf("expected error to contain task_id %q, got: %v", capturedTaskId, err)
				}
			},
		},
		{
			name: "conn_closed_in_receiving_audio",
			serverHandler: func(ctx context.Context, conn *websocket.Conn, setTaskId func(string)) error {
				taskId, err := mockReadRunTask(ctx, conn)
				if err != nil {
					return err
				}
				setTaskId(taskId)
				startedResp := fmt.Sprintf(`{"header":{"action":"task-started","event":"task-started","task_id":"%s"}}`, taskId)
				if err := conn.Write(ctx, websocket.MessageText, []byte(startedResp)); err != nil {
					return err
				}
				if err := mockReadContinueAndFinish(ctx, conn, taskId); err != nil {
					return err
				}
				if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x02}); err != nil {
					return err
				}
				// 未发 task-finished 异常关闭连接
				return conn.Close(websocket.StatusInternalError, "server crashed")
			},
			wantErrSubstrings: []string{"read tts message"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			serverDone := make(chan struct{})
			var (
				capturedTaskId string
				taskIdMu       sync.Mutex
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					close(serverDone)
					return
				}
				defer conn.Close(websocket.StatusNormalClosure, "done")
				defer close(serverDone)

				if tc.serverHandler != nil {
					_ = tc.serverHandler(r.Context(), conn, func(id string) {
						taskIdMu.Lock()
						capturedTaskId = id
						taskIdMu.Unlock()
					})
				}
			}))
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			cfg := &database.TTSConfig{
				Endpoint: wsURL,
				APIKey:   "sk-test-protocol-error",
				Model:    TargetTTSModel,
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

			ttsStream, ok := stream.(*TTSStream)
			if !ok {
				t.Fatalf("expected *TTSStream instance, got %T", stream)
			}

			onPCM := func(c context.Context, b []byte) error {
				return nil
			}

			// 执行单句语音合成，预期必须发生协议或任务错误
			err = stream.SynthesizeSentence(ctx, "测试文本", onPCM)
			if err == nil {
				t.Fatalf("expected SynthesizeSentence to fail, got nil")
			}

			for _, sub := range tc.wantErrSubstrings {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("expected error containing %q, got: %v", sub, err)
				}
			}

			taskIdMu.Lock()
			capturedId := capturedTaskId
			taskIdMu.Unlock()
			if tc.checkErr != nil {
				tc.checkErr(t, err, capturedId)
			}

			// 验证 Stream 内部状态已标记为 stateFailed
			ttsStream.mu.Lock()
			streamStateVal := ttsStream.state
			ttsStream.mu.Unlock()
			if streamStateVal != stateFailed {
				t.Errorf("expected stream state to be stateFailed (%d), got: %d", stateFailed, streamStateVal)
			}

			// 处于 stateFailed 的 Stream，后续再次调用 SynthesizeSentence 必须被直接拒绝
			nextErr := stream.SynthesizeSentence(ctx, "后续尝试执行的句子", onPCM)
			if nextErr == nil {
				t.Fatal("expected next SynthesizeSentence to be rejected, got nil")
			}
			if !errors.Is(nextErr, ErrStreamFailed) {
				t.Fatalf("expected ErrStreamFailed on subsequent call, got: %v", nextErr)
			}

			// 验证 Close 幂等安全
			if err := stream.Close(); err != nil {
				t.Errorf("stream Close error: %v", err)
			}

			// 等待服务端测试协程退出
			select {
			case <-serverDone:
			case <-time.After(2 * time.Second):
				t.Fatal("timeout waiting for server handler to complete")
			}
		})
	}
}
