package bailian

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/config"
)

func TestASRClient_NormalFlow(t *testing.T) {
	const (
		expectedAPIKey = "test-dashscope-key"
		expectedModel  = "qwen-audio-3.0-asr-flash-streaming"
		finalSpeech    = "你好小智，今天天气怎么样"
	)

	var (
		receivedAuthHeader string
		receivedRunTask    asrRunTaskMessage
		receivedFinishTask asrFinishTaskMessage
		receivedPCMChunks  [][]byte
		mu                 sync.Mutex
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedAuthHeader = r.Header.Get("Authorization")
		mu.Unlock()

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("mock server accept error: %v", err)
			return
		}
		defer conn.Close(websocket.StatusInternalError, "mock finished")

		// 1. Read run-task
		msgType, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("mock server read run-task error: %v", err)
			return
		}
		if msgType != websocket.MessageText {
			t.Errorf("expected text message for run-task, got %v", msgType)
			return
		}

		var runMsg asrRunTaskMessage
		if err := json.Unmarshal(data, &runMsg); err != nil {
			t.Errorf("unmarshal run-task error: %v", err)
			return
		}
		mu.Lock()
		receivedRunTask = runMsg
		mu.Unlock()

		// 2. Respond task-started
		startedResp := asrResponseMessage{}
		startedResp.Header.Action = "task-started"
		startedResp.Header.TaskID = runMsg.Header.TaskID
		startedResp.Header.Event = "task-started"
		startedBytes, _ := json.Marshal(startedResp)
		if err := conn.Write(r.Context(), websocket.MessageText, startedBytes); err != nil {
			t.Errorf("write task-started error: %v", err)
			return
		}

		// 3. Read binary PCM chunks and finish-task message
		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageBinary {
				mu.Lock()
				chunkCopy := make([]byte, len(mData))
				copy(chunkCopy, mData)
				receivedPCMChunks = append(receivedPCMChunks, chunkCopy)
				mu.Unlock()
			} else if mType == websocket.MessageText {
				var finishMsg asrFinishTaskMessage
				if err := json.Unmarshal(mData, &finishMsg); err == nil && finishMsg.Header.Action == "finish-task" {
					mu.Lock()
					receivedFinishTask = finishMsg
					mu.Unlock()

					// Send intermediate result-generated
					interResp := asrResponseMessage{}
					interResp.Header.Action = "result-generated"
					interResp.Header.TaskID = runMsg.Header.TaskID
					interResp.Header.Event = "result-generated"
					interResp.Payload.Output.Sentence = &asrSentenceOutput{
						SentenceID:    1,
						SentenceBegin: true,
						SentenceEnd:   false,
						Text:          "你好小智",
					}
					interBytes, _ := json.Marshal(interResp)
					_ = conn.Write(r.Context(), websocket.MessageText, interBytes)

					// Send final task-finished
					finalResp := asrResponseMessage{}
					finalResp.Header.Action = "task-finished"
					finalResp.Header.TaskID = runMsg.Header.TaskID
					finalResp.Header.Event = "task-finished"
					finalResp.Payload.Output.Sentence = &asrSentenceOutput{
						SentenceID:    1,
						SentenceBegin: true,
						SentenceEnd:   true,
						Text:          finalSpeech,
					}
					finalBytes, _ := json.Marshal(finalResp)
					_ = conn.Write(r.Context(), websocket.MessageText, finalBytes)
					break
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
				ASRModel:          expectedModel,
				ASRConnectTimeout: 5 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// Verify Authorization header
	mu.Lock()
	if receivedAuthHeader != "Bearer "+expectedAPIKey {
		t.Errorf("expected auth header 'Bearer %s', got '%s'", expectedAPIKey, receivedAuthHeader)
	}

	// Verify run-task request structure
	if receivedRunTask.Header.Action != "run-task" {
		t.Errorf("expected action 'run-task', got '%s'", receivedRunTask.Header.Action)
	}
	if receivedRunTask.Header.Streaming != "duplex" {
		t.Errorf("expected streaming 'duplex', got '%s'", receivedRunTask.Header.Streaming)
	}
	if receivedRunTask.Header.TaskID == "" {
		t.Errorf("expected non-empty task_id")
	}
	if receivedRunTask.Payload.TaskGroup != "audio" || receivedRunTask.Payload.Task != "asr" || receivedRunTask.Payload.Function != "recognition" {
		t.Errorf("unexpected task payload: %+v", receivedRunTask.Payload)
	}
	if receivedRunTask.Payload.Model != expectedModel {
		t.Errorf("expected model '%s', got '%s'", expectedModel, receivedRunTask.Payload.Model)
	}
	if receivedRunTask.Payload.Parameters.Format != "pcm" || receivedRunTask.Payload.Parameters.SampleRate != 16000 {
		t.Errorf("unexpected parameters: %+v", receivedRunTask.Payload.Parameters)
	}
	mu.Unlock()

	// Write 3 binary PCM chunks
	chunks := [][]byte{
		{0x01, 0x02, 0x03, 0x04},
		{0x05, 0x06, 0x07, 0x08},
		{0x09, 0x0A, 0x0B, 0x0C},
	}
	for i, chunk := range chunks {
		if err := stream.WritePCM(ctx, chunk); err != nil {
			t.Fatalf("WritePCM chunk %d failed: %v", i, err)
		}
	}

	// Finish task
	if err := stream.Finish(ctx); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	// Get result
	resultText, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	if resultText != finalSpeech {
		t.Errorf("expected result text '%s', got '%s'", finalSpeech, resultText)
	}

	// Verify binary chunks received on mock server
	mu.Lock()
	if len(receivedPCMChunks) != len(chunks) {
		t.Errorf("expected %d PCM chunks, got %d", len(chunks), len(receivedPCMChunks))
	} else {
		for i := range chunks {
			if !bytes.Equal(receivedPCMChunks[i], chunks[i]) {
				t.Errorf("chunk %d mismatch: expected %v, got %v", i, chunks[i], receivedPCMChunks[i])
			}
		}
	}

	// Verify finish-task message
	if receivedFinishTask.Header.Action != "finish-task" {
		t.Errorf("expected finish action 'finish-task', got '%s'", receivedFinishTask.Header.Action)
	}
	if receivedFinishTask.Header.TaskID != receivedRunTask.Header.TaskID {
		t.Errorf("finish task_id '%s' does not match run task_id '%s'", receivedFinishTask.Header.TaskID, receivedRunTask.Header.TaskID)
	}
	mu.Unlock()
}

func TestASRClient_AuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = websocket.Accept(w, r, nil)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cfg := &config.Config{
		DashScopeAPIKey: "wrong-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:        wsURL,
				ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
				ASRConnectTimeout: 2 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = client.CreateStream(ctx)
	if err == nil {
		t.Fatal("expected CreateStream to fail with 401 Unauthorized, got nil error")
	}
}

func TestASRClient_TaskFailedOnStart(t *testing.T) {
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

		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		failedResp := asrResponseMessage{}
		failedResp.Header.Action = "task-failed"
		failedResp.Header.TaskID = runMsg.Header.TaskID
		failedResp.Header.Event = "task-failed"
		failedResp.Header.ErrorCode = "InvalidParameter"
		failedResp.Header.ErrorMessage = "model not found"
		failedBytes, _ := json.Marshal(failedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, failedBytes)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:        wsURL,
				ASRModel:          "invalid-model",
				ASRConnectTimeout: 2 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = client.CreateStream(ctx)
	if err == nil {
		t.Fatal("expected CreateStream to fail when task-failed received, got nil error")
	}
	if !strings.Contains(err.Error(), "InvalidParameter") || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("expected error to contain InvalidParameter and 'model not found', got: %v", err)
	}
}

func TestASRClient_TaskFailedDuringStream(t *testing.T) {
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

		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		// 1. Send task-started
		startedResp := asrResponseMessage{}
		startedResp.Header.Action = "task-started"
		startedResp.Header.TaskID = runMsg.Header.TaskID
		startedResp.Header.Event = "task-started"
		startedBytes, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

		// 2. Read one binary chunk then send task-failed
		_, _, _ = conn.Read(r.Context())

		failedResp := asrResponseMessage{}
		failedResp.Header.Action = "task-failed"
		failedResp.Header.TaskID = runMsg.Header.TaskID
		failedResp.Header.Event = "task-failed"
		failedResp.Header.ErrorCode = "QuotaExhausted"
		failedResp.Header.ErrorMessage = "daily quota exceeded"
		failedBytes, _ := json.Marshal(failedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, failedBytes)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:        wsURL,
				ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
				ASRConnectTimeout: 2 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	_ = stream.WritePCM(ctx, []byte{1, 2, 3})

	_, err = stream.Result(ctx)
	if err == nil {
		t.Fatal("expected Result to fail on task-failed event, got nil error")
	}
	if !strings.Contains(err.Error(), "QuotaExhausted") || !strings.Contains(err.Error(), "daily quota exceeded") {
		t.Errorf("expected error to contain QuotaExhausted, got: %v", err)
	}
}

func TestASRClient_ContextCancel(t *testing.T) {
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
		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startedResp := asrResponseMessage{}
		startedResp.Header.Action = "task-started"
		startedResp.Header.TaskID = runMsg.Header.TaskID
		startedResp.Header.Event = "task-started"
		startedBytes, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

		// Block indefinitely until connection closes
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:        wsURL,
				ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
				ASRConnectTimeout: 2 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())

	stream, err := client.CreateStream(streamCtx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	resultCtx, resultCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer resultCancel()

	_, err = stream.Result(resultCtx)
	if err == nil {
		t.Fatal("expected Result to fail on timeout, got nil error")
	}

	streamCancel()
	_ = stream.Close()
}

func TestASRClient_NewClientValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr string
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: "config cannot be nil",
		},
		{
			name: "missing api key",
			cfg: &config.Config{
				AI: config.AIConfig{
					Bailian: config.BailianConfig{
						WSEndpoint: "ws://example.com",
						ASRModel:   "model",
					},
				},
			},
			wantErr: "dashscope api key is required",
		},
		{
			name: "missing ws endpoint",
			cfg: &config.Config{
				DashScopeAPIKey: "key",
				AI: config.AIConfig{
					Bailian: config.BailianConfig{
						ASRModel: "model",
					},
				},
			},
			wantErr: "bailian ws endpoint is required",
		},
		{
			name: "missing asr model",
			cfg: &config.Config{
				DashScopeAPIKey: "key",
				AI: config.AIConfig{
					Bailian: config.BailianConfig{
						WSEndpoint: "ws://example.com",
					},
				},
			},
			wantErr: "bailian asr model is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewASRClient(tt.cfg)
			if err == nil {
				t.Fatalf("expected error containing '%s', got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing '%s', got '%v'", tt.wantErr, err)
			}
			if client != nil {
				t.Errorf("expected nil client on error")
			}
		})
	}
}

func TestASRClient_ProxyConfiguration(t *testing.T) {
	cfgDisabled := &config.Config{
		DashScopeAPIKey: "key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: "ws://example.com",
				ASRModel:   "model",
			},
		},
		Proxy: config.ProxyConfig{
			Enabled: false,
			URL:     "http://127.0.0.1:8080",
		},
	}
	c1, err := NewASRClient(cfgDisabled)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c1.httpClient != nil {
		t.Errorf("expected nil httpClient when proxy is disabled")
	}

	cfgEnabled := &config.Config{
		DashScopeAPIKey: "key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: "ws://example.com",
				ASRModel:   "model",
			},
		},
		Proxy: config.ProxyConfig{
			Enabled: true,
			URL:     "http://127.0.0.1:8080",
		},
	}
	c2, err := NewASRClient(cfgEnabled)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c2.httpClient == nil {
		t.Errorf("expected non-nil httpClient when proxy is enabled")
	}

	cfgInvalidProxy := &config.Config{
		DashScopeAPIKey: "key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint: "ws://example.com",
				ASRModel:   "model",
			},
		},
		Proxy: config.ProxyConfig{
			Enabled: true,
			URL:     "://invalid-url",
		},
	}
	_, err = NewASRClient(cfgInvalidProxy)
	if err == nil {
		t.Fatal("expected error on invalid proxy URL, got nil")
	}
}

func TestASRStream_PartialAndFinalResultsInterleaved(t *testing.T) {
	const (
		expectedModel = "qwen-audio-3.0-asr-flash-streaming"
		finalSpeech   = "今天天气真好。我想去公园散步。"
	)

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
		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		// 1. task-started
		startedResp := asrResponseMessage{}
		startedResp.Header.Action = "task-started"
		startedResp.Header.TaskID = runMsg.Header.TaskID
		startedResp.Header.Event = "task-started"
		startedBytes, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

		// 2. Read first PCM chunk, send intermediate partial result
		_, _, _ = conn.Read(r.Context())
		interResp1 := asrResponseMessage{}
		interResp1.Header.Action = "result-generated"
		interResp1.Header.TaskID = runMsg.Header.TaskID
		interResp1.Header.Event = "result-generated"
		interResp1.Payload.Output.Sentence = &asrSentenceOutput{
			SentenceID:    1,
			SentenceBegin: true,
			SentenceEnd:   false,
			Text:          "今天",
		}
		b1, _ := json.Marshal(interResp1)
		_ = conn.Write(r.Context(), websocket.MessageText, b1)

		// 3. Read second PCM chunk, send second partial result
		_, _, _ = conn.Read(r.Context())
		interResp2 := asrResponseMessage{}
		interResp2.Header.Action = "result-generated"
		interResp2.Header.TaskID = runMsg.Header.TaskID
		interResp2.Header.Event = "result-generated"
		interResp2.Payload.Output.Sentence = &asrSentenceOutput{
			SentenceID:    1,
			SentenceBegin: true,
			SentenceEnd:   false,
			Text:          "今天天气",
		}
		b2, _ := json.Marshal(interResp2)
		_ = conn.Write(r.Context(), websocket.MessageText, b2)

		// 4. Send first sentence final result (sentence_end: true)
		interResp3 := asrResponseMessage{}
		interResp3.Header.Action = "result-generated"
		interResp3.Header.TaskID = runMsg.Header.TaskID
		interResp3.Header.Event = "result-generated"
		interResp3.Payload.Output.Sentence = &asrSentenceOutput{
			SentenceID:    1,
			SentenceBegin: true,
			SentenceEnd:   true,
			Text:          "今天天气真好。",
		}
		b3, _ := json.Marshal(interResp3)
		_ = conn.Write(r.Context(), websocket.MessageText, b3)

		// 5. Read third PCM chunk, send second sentence partial result
		_, _, _ = conn.Read(r.Context())
		interResp4 := asrResponseMessage{}
		interResp4.Header.Action = "result-generated"
		interResp4.Header.TaskID = runMsg.Header.TaskID
		interResp4.Header.Event = "result-generated"
		interResp4.Payload.Output.Sentence = &asrSentenceOutput{
			SentenceID:    2,
			SentenceBegin: true,
			SentenceEnd:   false,
			Text:          "我想去",
		}
		b4, _ := json.Marshal(interResp4)
		_ = conn.Write(r.Context(), websocket.MessageText, b4)

		// 6. Read finish-task message and send task-finished
		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageText {
				var finishMsg asrFinishTaskMessage
				if err := json.Unmarshal(mData, &finishMsg); err == nil && finishMsg.Header.Action == "finish-task" {
					finalResp := asrResponseMessage{}
					finalResp.Header.Action = "task-finished"
					finalResp.Header.TaskID = runMsg.Header.TaskID
					finalResp.Header.Event = "task-finished"
					finalResp.Payload.Output.Text = finalSpeech
					finalResp.Payload.Output.Sentence = &asrSentenceOutput{
						SentenceID:    2,
						SentenceBegin: true,
						SentenceEnd:   true,
						Text:          "我想去公园散步。",
					}
					finalBytes, _ := json.Marshal(finalResp)
					_ = conn.Write(r.Context(), websocket.MessageText, finalBytes)
					break
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
				ASRModel:          expectedModel,
				ASRConnectTimeout: 2 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	_ = stream.WritePCM(ctx, []byte{1, 2})
	_ = stream.WritePCM(ctx, []byte{3, 4})
	_ = stream.WritePCM(ctx, []byte{5, 6})

	if err := stream.Finish(ctx); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	resultText, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	if resultText != finalSpeech {
		t.Errorf("expected final speech '%s', got '%s'", finalSpeech, resultText)
	}
}

func TestASRStream_PartialFallbackOnTaskFinished(t *testing.T) {
	const partialText = "仅有部分中间识别内容"

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
		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		startedResp := asrResponseMessage{}
		startedResp.Header.Action = "task-started"
		startedResp.Header.TaskID = runMsg.Header.TaskID
		startedResp.Header.Event = "task-started"
		startedBytes, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

		// Send intermediate result with sentence_end = false
		_, _, _ = conn.Read(r.Context())
		interResp := asrResponseMessage{}
		interResp.Header.Action = "result-generated"
		interResp.Header.TaskID = runMsg.Header.TaskID
		interResp.Header.Event = "result-generated"
		interResp.Payload.Output.Sentence = &asrSentenceOutput{
			SentenceID:    1,
			SentenceBegin: true,
			SentenceEnd:   false,
			Text:          partialText,
		}
		b, _ := json.Marshal(interResp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		// Wait finish-task and respond task-finished without output text
		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageText {
				var finishMsg asrFinishTaskMessage
				if err := json.Unmarshal(mData, &finishMsg); err == nil && finishMsg.Header.Action == "finish-task" {
					finalResp := asrResponseMessage{}
					finalResp.Header.Action = "task-finished"
					finalResp.Header.TaskID = runMsg.Header.TaskID
					finalResp.Header.Event = "task-finished"
					// output is empty
					finalBytes, _ := json.Marshal(finalResp)
					_ = conn.Write(r.Context(), websocket.MessageText, finalBytes)
					break
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
				ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
				ASRConnectTimeout: 2 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	_ = stream.WritePCM(ctx, []byte{1, 2, 3})
	_ = stream.Finish(ctx)

	resultText, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	if resultText != partialText {
		t.Errorf("expected fallback text '%s', got '%s'", partialText, resultText)
	}
}

func TestASRStream_UnknownServerEvents(t *testing.T) {
	const expectedFinal = "未知事件容错测试成功"

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
		var runMsg asrRunTaskMessage
		_ = json.Unmarshal(data, &runMsg)

		// 1. task-started
		startedResp := asrResponseMessage{}
		startedResp.Header.Action = "task-started"
		startedResp.Header.TaskID = runMsg.Header.TaskID
		startedResp.Header.Event = "task-started"
		startedBytes, _ := json.Marshal(startedResp)
		_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

		// 2. Send unknown action/event
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"header":{"action":"custom-ext","event":"custom-ext","task_id":"`+runMsg.Header.TaskID+`"},"payload":{"ext":123}}`))

		// 3. Send heartbeat event
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"header":{"event":"heartbeat","task_id":"`+runMsg.Header.TaskID+`"}}`))

		// 4. Send corrupted JSON text
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"corrupted json string`))

		// 5. Send raw binary message
		_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{0xDE, 0xAD, 0xBE, 0xEF})

		// 6. Send intermediate partial result
		interResp := asrResponseMessage{}
		interResp.Header.Action = "result-generated"
		interResp.Header.TaskID = runMsg.Header.TaskID
		interResp.Header.Event = "result-generated"
		interResp.Payload.Output.Sentence = &asrSentenceOutput{
			SentenceID:    1,
			SentenceBegin: true,
			SentenceEnd:   false,
			Text:          "正在识别中",
		}
		interBytes, _ := json.Marshal(interResp)
		_ = conn.Write(r.Context(), websocket.MessageText, interBytes)

		// 7. Send another unknown event
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"header":{"event":"future-speech-event-v2"}}`))

		// 8. Read finish-task and send task-finished
		for {
			mType, mData, rErr := conn.Read(r.Context())
			if rErr != nil {
				break
			}
			if mType == websocket.MessageText {
				var finishMsg asrFinishTaskMessage
				if err := json.Unmarshal(mData, &finishMsg); err == nil && finishMsg.Header.Action == "finish-task" {
					finalResp := asrResponseMessage{}
					finalResp.Header.Action = "task-finished"
					finalResp.Header.TaskID = runMsg.Header.TaskID
					finalResp.Header.Event = "task-finished"
					finalResp.Payload.Output.Sentence = &asrSentenceOutput{
						SentenceID:    1,
						SentenceBegin: true,
						SentenceEnd:   true,
						Text:          expectedFinal,
					}
					finalBytes, _ := json.Marshal(finalResp)
					_ = conn.Write(r.Context(), websocket.MessageText, finalBytes)
					break
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
				ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
				ASRConnectTimeout: 2 * time.Second,
			},
		},
	}

	client, err := NewASRClient(cfg)
	if err != nil {
		t.Fatalf("NewASRClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	_ = stream.WritePCM(ctx, []byte{1, 2, 3})
	_ = stream.Finish(ctx)

	resultText, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	if resultText != expectedFinal {
		t.Errorf("expected final text '%s', got '%s'", expectedFinal, resultText)
	}
}

func TestASRStream_TaskFailedErrorsTable(t *testing.T) {
	tests := []struct {
		name         string
		failOnStart  bool
		errorCode    string
		errorMessage string
		wantErrSub   []string
	}{
		{
			name:         "start_failure_with_details",
			failOnStart:  true,
			errorCode:    "InvalidModel",
			errorMessage: "asr model not found in catalog",
			wantErrSub:   []string{"InvalidModel", "asr model not found in catalog"},
		},
		{
			name:         "start_failure_empty_details",
			failOnStart:  true,
			errorCode:    "",
			errorMessage: "",
			wantErrSub:   []string{"UNKNOWN_ERROR", "task start failed"},
		},
		{
			name:         "stream_failure_with_details",
			failOnStart:  false,
			errorCode:    "AudioInspectionFailed",
			errorMessage: "audio content violates security policy",
			wantErrSub:   []string{"AudioInspectionFailed", "audio content violates security policy"},
		},
		{
			name:         "stream_failure_empty_details",
			failOnStart:  false,
			errorCode:    "",
			errorMessage: "",
			wantErrSub:   []string{"UNKNOWN_ERROR", "asr task failed on server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				var runMsg asrRunTaskMessage
				_ = json.Unmarshal(data, &runMsg)

				if tt.failOnStart {
					failedResp := asrResponseMessage{}
					failedResp.Header.Action = "task-failed"
					failedResp.Header.TaskID = runMsg.Header.TaskID
					failedResp.Header.Event = "task-failed"
					failedResp.Header.ErrorCode = tt.errorCode
					failedResp.Header.ErrorMessage = tt.errorMessage
					failedBytes, _ := json.Marshal(failedResp)
					_ = conn.Write(r.Context(), websocket.MessageText, failedBytes)
					return
				}

				// If stream failure, first send task-started
				startedResp := asrResponseMessage{}
				startedResp.Header.Action = "task-started"
				startedResp.Header.TaskID = runMsg.Header.TaskID
				startedResp.Header.Event = "task-started"
				startedBytes, _ := json.Marshal(startedResp)
				_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

				// Read one binary packet then send task-failed
				_, _, _ = conn.Read(r.Context())
				failedResp := asrResponseMessage{}
				failedResp.Header.Action = "task-failed"
				failedResp.Header.TaskID = runMsg.Header.TaskID
				failedResp.Header.Event = "task-failed"
				failedResp.Header.ErrorCode = tt.errorCode
				failedResp.Header.ErrorMessage = tt.errorMessage
				failedBytes, _ := json.Marshal(failedResp)
				_ = conn.Write(r.Context(), websocket.MessageText, failedBytes)
			}))
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			cfg := &config.Config{
				DashScopeAPIKey: "test-key",
				AI: config.AIConfig{
					Bailian: config.BailianConfig{
						WSEndpoint:        wsURL,
						ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
						ASRConnectTimeout: 2 * time.Second,
					},
				},
			}

			client, err := NewASRClient(cfg)
			if err != nil {
				t.Fatalf("NewASRClient failed: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if tt.failOnStart {
				_, err = client.CreateStream(ctx)
				if err == nil {
					t.Fatalf("expected CreateStream to fail, got nil")
				}
				for _, sub := range tt.wantErrSub {
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

				_ = stream.WritePCM(ctx, []byte{1, 2, 3})
				_, err = stream.Result(ctx)
				if err == nil {
					t.Fatalf("expected Result to fail, got nil")
				}
				for _, sub := range tt.wantErrSub {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("expected error to contain '%s', got: %v", sub, err)
					}
				}
			}
		})
	}
}

func TestASRStream_ConnectionAbruptlyClosed(t *testing.T) {
	t.Run("server_closes_connection_during_streaming", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			// Read run-task
			_, data, err := conn.Read(r.Context())
			if err != nil {
				_ = conn.Close(websocket.StatusInternalError, "closed")
				return
			}
			var runMsg asrRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startedResp := asrResponseMessage{}
			startedResp.Header.Action = "task-started"
			startedResp.Header.TaskID = runMsg.Header.TaskID
			startedResp.Header.Event = "task-started"
			startedBytes, _ := json.Marshal(startedResp)
			_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

			// Read one chunk and abruptly close connection
			_, _, _ = conn.Read(r.Context())
			_ = conn.Close(websocket.StatusGoingAway, "server going down")
		}))
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
		cfg := &config.Config{
			DashScopeAPIKey: "test-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint:        wsURL,
					ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
					ASRConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewASRClient(cfg)
		if err != nil {
			t.Fatalf("NewASRClient failed: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		stream, err := client.CreateStream(ctx)
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}
		defer stream.Close()

		// Write PCM to trigger server close
		_ = stream.WritePCM(ctx, []byte{1, 2, 3})

		// Result should unblock immediately with an error
		_, err = stream.Result(ctx)
		if err == nil {
			t.Fatalf("expected Result to fail on abrupt connection close, got nil")
		}

		// Subsequent WritePCM should fail
		if err := stream.WritePCM(ctx, []byte{4, 5, 6}); err == nil {
			t.Errorf("expected WritePCM to fail after connection closed, got nil")
		}

		// Subsequent Finish should fail
		if err := stream.Finish(ctx); err == nil {
			t.Errorf("expected Finish to fail after connection closed, got nil")
		}
	})

	t.Run("server_closes_connection_after_finish_before_result", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			_, data, err := conn.Read(r.Context())
			if err != nil {
				_ = conn.Close(websocket.StatusInternalError, "closed")
				return
			}
			var runMsg asrRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startedResp := asrResponseMessage{}
			startedResp.Header.Action = "task-started"
			startedResp.Header.TaskID = runMsg.Header.TaskID
			startedResp.Header.Event = "task-started"
			startedBytes, _ := json.Marshal(startedResp)
			_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

			for {
				mType, mData, rErr := conn.Read(r.Context())
				if rErr != nil {
					break
				}
				if mType == websocket.MessageText {
					var finishMsg asrFinishTaskMessage
					if err := json.Unmarshal(mData, &finishMsg); err == nil && finishMsg.Header.Action == "finish-task" {
						// Abruptly close connection instead of sending task-finished
						_ = conn.Close(websocket.StatusInternalError, "abrupt crash")
						break
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
					ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
					ASRConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewASRClient(cfg)
		if err != nil {
			t.Fatalf("NewASRClient failed: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		stream, err := client.CreateStream(ctx)
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}
		defer stream.Close()

		_ = stream.WritePCM(ctx, []byte{1, 2, 3})
		_ = stream.Finish(ctx)

		_, err = stream.Result(ctx)
		if err == nil {
			t.Fatalf("expected Result to fail on abrupt crash after finish, got nil")
		}
	})
}

func TestASRStream_ContextCancelAndConcurrentClose(t *testing.T) {
	t.Run("caller_context_cancel_unblocks_result", func(t *testing.T) {
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
			var runMsg asrRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startedResp := asrResponseMessage{}
			startedResp.Header.Action = "task-started"
			startedResp.Header.TaskID = runMsg.Header.TaskID
			startedResp.Header.Event = "task-started"
			startedBytes, _ := json.Marshal(startedResp)
			_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

			// Hang connection
			_, _, _ = conn.Read(r.Context())
		}))
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
		cfg := &config.Config{
			DashScopeAPIKey: "test-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint:        wsURL,
					ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
					ASRConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewASRClient(cfg)
		if err != nil {
			t.Fatalf("NewASRClient failed: %v", err)
		}

		streamCtx, streamCancel := context.WithCancel(context.Background())
		defer streamCancel()

		stream, err := client.CreateStream(streamCtx)
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}
		defer stream.Close()

		resultCtx, resultCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer resultCancel()

		_, err = stream.Result(resultCtx)
		if err == nil {
			t.Fatalf("expected Result to fail on timeout, got nil")
		}
	})

	t.Run("external_stream_ctx_cancel_closes_conn_and_unblocks_all", func(t *testing.T) {
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
			var runMsg asrRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startedResp := asrResponseMessage{}
			startedResp.Header.Action = "task-started"
			startedResp.Header.TaskID = runMsg.Header.TaskID
			startedResp.Header.Event = "task-started"
			startedBytes, _ := json.Marshal(startedResp)
			_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

			_, _, _ = conn.Read(r.Context())
		}))
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
		cfg := &config.Config{
			DashScopeAPIKey: "test-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					WSEndpoint:        wsURL,
					ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
					ASRConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewASRClient(cfg)
		if err != nil {
			t.Fatalf("NewASRClient failed: %v", err)
		}

		streamCtx, streamCancel := context.WithCancel(context.Background())

		stream, err := client.CreateStream(streamCtx)
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}

		// Cancel external context
		streamCancel()

		// Wait briefly for read loop to detect cancellation
		time.Sleep(20 * time.Millisecond)

		// Result should return context.Canceled
		_, err = stream.Result(context.Background())
		if err == nil {
			t.Fatalf("expected Result to fail on cancelled context, got nil")
		}

		// Close should be safe
		_ = stream.Close()
	})

	t.Run("concurrent_close_50_goroutines", func(t *testing.T) {
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
			var runMsg asrRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startedResp := asrResponseMessage{}
			startedResp.Header.Action = "task-started"
			startedResp.Header.TaskID = runMsg.Header.TaskID
			startedResp.Header.Event = "task-started"
			startedBytes, _ := json.Marshal(startedResp)
			_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

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
					WSEndpoint:        wsURL,
					ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
					ASRConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewASRClient(cfg)
		if err != nil {
			t.Fatalf("NewASRClient failed: %v", err)
		}

		stream, err := client.CreateStream(context.Background())
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}

		const numGoroutines = 50
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer wg.Done()
				_ = stream.Close()
			}()
		}

		wg.Wait()
	})

	t.Run("concurrent_read_write_finish_close_race_free", func(t *testing.T) {
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
			var runMsg asrRunTaskMessage
			_ = json.Unmarshal(data, &runMsg)

			startedResp := asrResponseMessage{}
			startedResp.Header.Action = "task-started"
			startedResp.Header.TaskID = runMsg.Header.TaskID
			startedResp.Header.Event = "task-started"
			startedBytes, _ := json.Marshal(startedResp)
			_ = conn.Write(r.Context(), websocket.MessageText, startedBytes)

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
					WSEndpoint:        wsURL,
					ASRModel:          "qwen-audio-3.0-asr-flash-streaming",
					ASRConnectTimeout: 2 * time.Second,
				},
			},
		}

		client, err := NewASRClient(cfg)
		if err != nil {
			t.Fatalf("NewASRClient failed: %v", err)
		}

		stream, err := client.CreateStream(context.Background())
		if err != nil {
			t.Fatalf("CreateStream failed: %v", err)
		}

		var wg sync.WaitGroup
		// 5 goroutines writing PCM
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					_ = stream.WritePCM(context.Background(), []byte{1, 2, 3, 4})
					time.Sleep(1 * time.Millisecond)
				}
			}()
		}

		// 1 goroutine calling Finish
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond)
			_ = stream.Finish(context.Background())
		}()

		// 1 goroutine calling Result
		wg.Add(1)
		go func() {
			defer wg.Done()
			resultCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, _ = stream.Result(resultCtx)
		}()

		// 1 goroutine calling Close
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(15 * time.Millisecond)
			_ = stream.Close()
		}()

		wg.Wait()
	})
}
