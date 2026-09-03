package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func TestNewTTSClient_Validation(t *testing.T) {
	// 1. 空 APIKey
	if _, err := NewTTSClient(ai.TTSOptions{Endpoint: "ws://localhost", Model: "cosyvoice-v1", Voice: "v"}); err == nil {
		t.Error("expected error for empty api key")
	}

	// 2. 空 Endpoint
	if _, err := NewTTSClient(ai.TTSOptions{APIKey: "key", Model: "cosyvoice-v1", Voice: "v"}); err == nil {
		t.Error("expected error for empty endpoint")
	}

	// 3. 空 Model
	if _, err := NewTTSClient(ai.TTSOptions{APIKey: "key", Endpoint: "ws://localhost", Voice: "v"}); err == nil {
		t.Error("expected error for empty model")
	}

	// 4. 空 Voice
	if _, err := NewTTSClient(ai.TTSOptions{APIKey: "key", Endpoint: "ws://localhost", Model: "cosyvoice-v1"}); err == nil {
		t.Error("expected error for empty voice")
	}

	// 5. 合法配置
	cli, err := NewTTSClient(ai.TTSOptions{
		APIKey:         "test-key",
		Endpoint:       "ws://localhost/tts",
		Model:          "cosyvoice-v1",
		Voice:          "longxiaochun",
		ConnectTimeout: 5 * time.Second,
		QueueCapacity:  50,
	})
	if err != nil {
		t.Fatalf("expected nil error for valid config, got %v", err)
	}
	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestSynthesizeSentence_EmptyText(t *testing.T) {
	cli, err := NewTTSClient(ai.TTSOptions{
		APIKey:   "test-key",
		Endpoint: "ws://localhost/tts",
		Model:    "cosyvoice-v1",
		Voice:    "longxiaochun",
	})
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := cli.SynthesizeSentence(context.Background(), "")
	if err != nil {
		t.Fatalf("SynthesizeSentence empty text failed: %v", err)
	}
	defer stream.Close()

	pkt, err := stream.NextPacket(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF for empty text, got pkt=%v, err=%v", pkt, err)
	}
}

func TestSynthesizeSentence_MockWSServer_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		// 1. 读取 run-task
		_, msgData, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(msgData, &runMsg)
		taskId := runMsg.Header.TaskId

		// 2. 回复 task-started
		startedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": taskId,
				"event":   "task-started",
			},
		}
		startedBytes, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

		// 3. 读取 continue-task
		_, _, _ = conn.Read(r.Context())

		// 4. 读取 finish-task
		_, _, _ = conn.Read(r.Context())

		// 5. 发送 24000Hz 60ms 完整 PCM 帧 (2880 字节)
		pcmFrame := make([]byte, 2880)
		_ = conn.Write(r.Context(), websocket.MessageBinary, pcmFrame)

		// 6. 发送 task-finished
		finishedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-finished",
				"task_id": taskId,
				"event":   "task-finished",
			},
		}
		finishedBytes, _ := json.Marshal(finishedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, finishedBytes)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cli, err := NewTTSClient(ai.TTSOptions{
		APIKey:   "test-key",
		Endpoint: wsURL,
		Model:    "cosyvoice-v1",
		Voice:    "longxiaochun",
	})
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := cli.SynthesizeSentence(context.Background(), "你好，这是一句测试。")
	if err != nil {
		t.Fatalf("SynthesizeSentence failed: %v", err)
	}
	defer stream.Close()

	packetCount := 0
	for {
		pkt, err := stream.NextPacket(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error reading packet: %v", err)
		}
		if len(pkt) > 0 {
			packetCount++
		}
	}

	if packetCount == 0 {
		t.Fatal("expected at least 1 Opus packet, got 0")
	}
}

func TestSynthesizeSentence_TaskFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		_, msgData, _ := conn.Read(r.Context())
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(msgData, &runMsg)

		failedResp := map[string]any{
			"header": map[string]any{
				"action":        "task-failed",
				"task_id":       runMsg.Header.TaskId,
				"event":         "task-failed",
				"error_code":    "InvalidApiKey",
				"error_message": "api key is invalid",
			},
		}
		failedBytes, _ := json.Marshal(failedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, failedBytes)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cli, err := NewTTSClient(ai.TTSOptions{
		APIKey:   "test-key",
		Endpoint: wsURL,
		Model:    "cosyvoice-v1",
		Voice:    "longxiaochun",
	})
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	_, err = cli.SynthesizeSentence(context.Background(), "测试失败")
	if err == nil {
		t.Fatal("expected error on task-failed, got nil")
	}
	if !strings.Contains(err.Error(), "InvalidApiKey") {
		t.Fatalf("expected error message to contain InvalidApiKey, got %v", err)
	}
}

func TestSynthesizeSentence_Cancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		_, msgData, _ := conn.Read(r.Context())
		var runMsg ttsRunTaskMessage
		_ = json.Unmarshal(msgData, &runMsg)

		startedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": runMsg.Header.TaskId,
				"event":   "task-started",
			},
		}
		startedBytes, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

		// 持续读消息，直到读取到 cancel-task
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var header struct {
				Header struct {
					Action string `json:"action"`
				} `json:"header"`
			}
			_ = json.Unmarshal(data, &header)
			if header.Header.Action == "cancel-task" {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cli, err := NewTTSClient(ai.TTSOptions{
		APIKey:   "test-key",
		Endpoint: wsURL,
		Model:    "cosyvoice-v1",
		Voice:    "longxiaochun",
	})
	if err != nil {
		t.Fatalf("NewTTSClient failed: %v", err)
	}

	stream, err := cli.SynthesizeSentence(context.Background(), "测试取消")
	if err != nil {
		t.Fatalf("SynthesizeSentence failed: %v", err)
	}

	if err := stream.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
}
