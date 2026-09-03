package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestTTSSession_EmptyText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
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

	sess, err := cli.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	defer sess.Close()

	stream, err := sess.Synthesize(context.Background(), "")
	if err != nil {
		t.Fatalf("Synthesize empty text failed: %v", err)
	}
	defer stream.Close()

	pkt, err := stream.NextPacket(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF for empty text, got pkt=%v, err=%v", pkt, err)
	}
}

func TestTTSSession_MockWSServer_MultiSentenceReuse(t *testing.T) {
	var connCount atomic.Int32
	var taskCount atomic.Int32

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
		connCount.Add(1)
		defer conn.Close(websocket.StatusNormalClosure, "done")

		// 在单个 WebSocket 连接内支持连续多句 Task 复用
		for {
			// 1. 读取 run-task
			_, msgData, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var runMsg ttsRunTaskMessage
			if err := json.Unmarshal(msgData, &runMsg); err != nil {
				return
			}
			taskId := runMsg.Header.TaskId
			taskCount.Add(1)

			// 2. 回复 task-started
			startedResp := map[string]any{
				"header": map[string]any{
					"action":  "task-started",
					"task_id": taskId,
					"event":   "task-started",
				},
			}
			startedBytes, _ := json.Marshal(startedResp)
			if err := conn.Write(r.Context(), websocket.MessageText, startedBytes); err != nil {
				return
			}

			// 3. 读取 continue-task
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}

			// 4. 读取 finish-task
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}

			// 5. 发送 24000Hz 60ms 完整 PCM 帧 (2880 字节)
			pcmFrame := make([]byte, 2880)
			if err := conn.Write(r.Context(), websocket.MessageBinary, pcmFrame); err != nil {
				return
			}

			// 6. 发送 task-finished
			finishedResp := map[string]any{
				"header": map[string]any{
					"action":  "task-finished",
					"task_id": taskId,
					"event":   "task-finished",
				},
			}
			finishedBytes, _ := json.Marshal(finishedResp)
			if err := conn.Write(r.Context(), websocket.MessageText, finishedBytes); err != nil {
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

	// 验证整轮建立 1 次 WebSocket 会话连接
	sess, err := cli.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	defer sess.Close()

	sentences := []string{
		"你好，这是第一句测试。",
		"你好，这是复用同个长连接的第二句测试。",
	}

	for i, sentence := range sentences {
		stream, err := sess.Synthesize(context.Background(), sentence)
		if err != nil {
			t.Fatalf("sentence %d Synthesize failed: %v", i+1, err)
		}

		packetCount := 0
		for {
			pkt, err := stream.NextPacket(context.Background())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("sentence %d error reading packet: %v", i+1, err)
			}
			if len(pkt) > 0 {
				packetCount++
			}
		}

		if packetCount == 0 {
			t.Fatalf("sentence %d: expected at least 1 Opus packet, got 0", i+1)
		}
		_ = stream.Close()
	}

	// 关键断言：整轮仅建立 1 次底层连接，执行了 2 次单句 Task
	if c := connCount.Load(); c != 1 {
		t.Fatalf("expected 1 websocket connection established, got %d", c)
	}
	if tasks := taskCount.Load(); tasks != 2 {
		t.Fatalf("expected 2 tasks executed on same connection, got %d", tasks)
	}
}

func TestTTSSession_TaskFailed(t *testing.T) {
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

	sess, err := cli.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	defer sess.Close()

	_, err = sess.Synthesize(context.Background(), "测试失败")
	if err == nil {
		t.Fatal("expected error on task-failed, got nil")
	}
	if !strings.Contains(err.Error(), "InvalidApiKey") {
		t.Fatalf("expected error message to contain InvalidApiKey, got %v", err)
	}
}

func TestTTSSession_Cancel(t *testing.T) {
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

	sess, err := cli.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	defer sess.Close()

	stream, err := sess.Synthesize(context.Background(), "测试取消")
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	if err := stream.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
}
