package dashscope

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestASRClient_Recognize_Auto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}

		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)
		taskId := runMsg.Header.TaskId

		// 1. 发送 task-started
		startedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": taskId,
				"event":   "task-started",
			},
		}
		b, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		// 2. 读取一帧 PCM 音频
		_, _, _ = conn.Read(r.Context())

		// 3. 发送 result-generated (SentenceEnd=true)
		resultResp := asrResponseMessage{
			Header: struct {
				Action       string `json:"action"`
				TaskId       string `json:"task_id"`
				Event        string `json:"event"`
				ErrorCode    string `json:"error_code"`
				ErrorMessage string `json:"error_message"`
			}{
				Action: "result-generated",
				TaskId: taskId,
				Event:  "result-generated",
			},
			Payload: asrResponsePayload{
				Output: struct {
					Sentence *asrSentenceOutput `json:"sentence,omitempty"`
					Text     string             `json:"text,omitempty"`
				}{
					Sentence: &asrSentenceOutput{
						SentenceId:  1,
						SentenceEnd: true,
						Text:        "你好世界",
					},
					Text: "你好世界",
				},
			},
		}
		b, _ = json.Marshal(resultResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, err := NewASRClient(&database.ASRConfig{
		Provider:         "dashscope",
		APIKey:           "test-key",
		Endpoint:         wsURL,
		Model:            "paraformer-realtime-v1",
		ConnectTimeoutMS: 5000,
	})
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	pcmCh := make(chan []byte, 5)
	pcmCh <- make([]byte, 1920)

	text, err := client.Recognize(context.Background(), ai.ASRRequest{
		Mode:       ai.ASRModeAuto,
		SampleRate: 16000,
	}, pcmCh)
	if err != nil {
		t.Fatalf("Recognize failed: %v", err)
	}
	if text != "你好世界" {
		t.Fatalf("expected '你好世界', got %q", text)
	}
}

func TestASRClient_Recognize_Manual(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}

		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)
		taskId := runMsg.Header.TaskId

		startedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": taskId,
				"event":   "task-started",
			},
		}
		b, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		// 循环读取直到 finish-task
		for {
			mType, mData, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if mType == websocket.MessageText {
				var finishMsg asrFinishTaskMessage
				if err := json.Unmarshal(mData, &finishMsg); err == nil && finishMsg.Header.Action == "finish-task" {
					break
				}
			}
		}

		// 发送 task-finished
		finishedResp := asrResponseMessage{
			Header: struct {
				Action       string `json:"action"`
				TaskId       string `json:"task_id"`
				Event        string `json:"event"`
				ErrorCode    string `json:"error_code"`
				ErrorMessage string `json:"error_message"`
			}{
				Action: "task-finished",
				TaskId: taskId,
				Event:  "task-finished",
			},
			Payload: asrResponsePayload{
				Output: struct {
					Sentence *asrSentenceOutput `json:"sentence,omitempty"`
					Text     string             `json:"text,omitempty"`
				}{
					Text: "手工录音结束",
				},
			},
		}
		b, _ = json.Marshal(finishedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, err := NewASRClient(&database.ASRConfig{
		Provider:         "dashscope",
		APIKey:           "test-key",
		Endpoint:         wsURL,
		Model:            "paraformer-realtime-v1",
		ConnectTimeoutMS: 5000,
	})
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	pcmCh := make(chan []byte, 5)
	pcmCh <- make([]byte, 1920)
	close(pcmCh) // 模拟 manual stop

	text, err := client.Recognize(context.Background(), ai.ASRRequest{
		Mode:       ai.ASRModeManual,
		SampleRate: 16000,
	}, pcmCh)
	if err != nil {
		t.Fatalf("Recognize manual failed: %v", err)
	}
	if text != "手工录音结束" {
		t.Fatalf("expected '手工录音结束', got %q", text)
	}
}

func TestASRClient_Recognize_AutoEmptyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		_, data, _ := conn.Read(r.Context())
		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-started",
				"task_id": runMsg.Header.TaskId,
				"event":   "task-started",
			},
		}
		b, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		finishedResp := map[string]any{
			"header": map[string]any{
				"action":  "task-finished",
				"task_id": runMsg.Header.TaskId,
				"event":   "task-finished",
			},
		}
		b, _ = json.Marshal(finishedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, err := NewASRClient(&database.ASRConfig{
		Provider:         "dashscope",
		APIKey:           "test-key",
		Endpoint:         wsURL,
		Model:            "paraformer-realtime-v1",
		ConnectTimeoutMS: 5000,
	})
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	pcmCh := make(chan []byte)
	close(pcmCh)

	_, err = client.Recognize(context.Background(), ai.ASRRequest{
		Mode:       ai.ASRModeAuto,
		SampleRate: 16000,
	}, pcmCh)
	if err == nil {
		t.Fatal("expected error on empty asr text in auto mode, got nil")
	}
}
