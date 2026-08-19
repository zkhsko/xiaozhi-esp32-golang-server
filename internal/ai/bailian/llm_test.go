package bailian

import (
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

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
)

// chatCompletionRequest 结构用于解析假服务接收到的 Chat Completions JSON 请求体。
type chatCompletionRequest struct {
	Model          string `json:"model"`
	Stream         bool   `json:"stream"`
	EnableThinking *bool  `json:"enable_thinking"`
	Messages       []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools     []any `json:"tools,omitempty"`
	Functions []any `json:"functions,omitempty"`
}

func TestNewLLMClient_Validation(t *testing.T) {
	validCfg := func() *config.Config {
		return &config.Config{
			DashScopeAPIKey: "test-api-key",
			AI: config.AIConfig{
				Bailian: config.BailianConfig{
					LLMEndpoint:          "https://example.com/compatible-mode/v1",
					LLMModel:             "qwen3.7-flash",
					LLMFirstTokenTimeout: 15 * time.Second,
					LLMOverallTimeout:    60 * time.Second,
				},
			},
		}
	}

	t.Run("nil config", func(t *testing.T) {
		_, err := NewLLMClient(nil)
		if err == nil || !strings.Contains(err.Error(), "config cannot be nil") {
			t.Fatalf("expected nil config error, got: %v", err)
		}
	})

	t.Run("missing api key", func(t *testing.T) {
		cfg := validCfg()
		cfg.DashScopeAPIKey = ""
		_, err := NewLLMClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "api key is required") {
			t.Fatalf("expected missing api key error, got: %v", err)
		}
	})

	t.Run("missing endpoint", func(t *testing.T) {
		cfg := validCfg()
		cfg.AI.Bailian.LLMEndpoint = ""
		_, err := NewLLMClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "endpoint is required") {
			t.Fatalf("expected missing endpoint error, got: %v", err)
		}
	})

	t.Run("missing model", func(t *testing.T) {
		cfg := validCfg()
		cfg.AI.Bailian.LLMModel = ""
		_, err := NewLLMClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "model is required") {
			t.Fatalf("expected missing model error, got: %v", err)
		}
	})

	t.Run("overall timeout less than or equal first token timeout", func(t *testing.T) {
		cfg := validCfg()
		cfg.AI.Bailian.LLMFirstTokenTimeout = 30 * time.Second
		cfg.AI.Bailian.LLMOverallTimeout = 10 * time.Second
		_, err := NewLLMClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "must be greater than") {
			t.Fatalf("expected invalid timeout error, got: %v", err)
		}
	})

	t.Run("valid config with proxy", func(t *testing.T) {
		cfg := validCfg()
		cfg.Proxy.Enabled = true
		cfg.Proxy.URL = "http://127.0.0.1:8080"
		client, err := NewLLMClient(cfg)
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
		_, err := NewLLMClient(cfg)
		if err == nil || !strings.Contains(err.Error(), "parse proxy url") {
			t.Fatalf("expected proxy url parse error, got: %v", err)
		}
	})
}

func TestLLMClient_NormalFlow(t *testing.T) {
	const (
		expectedAPIKey = "sk-test-dashscope-secret"
		expectedModel  = "qwen3.7-flash"
	)

	var (
		mu             sync.Mutex
		receivedMethod string
		receivedPath   string
		receivedAuth   string
		receivedBody   chatCompletionRequest
		requestCount   int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		var body chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		receivedBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("expected ResponseWriter to be http.Flusher")
			return
		}

		chunks := []string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":1700000001,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{"content":"，我是小智"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl-3","object":"chat.completion.chunk","created":1700000002,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{"content":"。很高兴为你服务。"},"finish_reason":"stop"}]}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}

		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: expectedAPIKey,
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             expectedModel,
				LLMFirstTokenTimeout: 5 * time.Second,
				LLMOverallTimeout:    10 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create llm client: %v", err)
	}

	messages := []ai.Message{
		{Role: ai.RoleSystem, Content: "你是小智助手。"},
		{Role: ai.RoleUser, Content: "你好！"},
	}

	stream, err := client.CreateStream(context.Background(), messages)
	if err != nil {
		t.Fatalf("CreateStream returned error: %v", err)
	}
	defer stream.Close()

	var receivedDeltas []string
	for {
		delta, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("unexpected error during Recv: %v", recvErr)
		}
		receivedDeltas = append(receivedDeltas, delta)
	}

	expectedDeltas := []string{"你好", "，我是小智", "。很高兴为你服务。"}
	if len(receivedDeltas) != len(expectedDeltas) {
		t.Fatalf("expected %d deltas, got %d: %v", len(expectedDeltas), len(receivedDeltas), receivedDeltas)
	}
	for i, exp := range expectedDeltas {
		if receivedDeltas[i] != exp {
			t.Errorf("delta[%d] expected %q, got %q", i, exp, receivedDeltas[i])
		}
	}

	// 严格校验请求参数
	mu.Lock()
	defer mu.Unlock()

	if requestCount != 1 {
		t.Errorf("expected exactly 1 request, got %d", requestCount)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("expected POST method, got %s", receivedMethod)
	}
	if !strings.HasSuffix(receivedPath, "/chat/completions") {
		t.Errorf("expected path to end with /chat/completions, got %s", receivedPath)
	}
	if receivedAuth != "Bearer "+expectedAPIKey {
		t.Errorf("expected Authorization Bearer %s, got %s", expectedAPIKey, receivedAuth)
	}
	if receivedBody.Model != expectedModel {
		t.Errorf("expected model %s, got %s", expectedModel, receivedBody.Model)
	}
	if !receivedBody.Stream {
		t.Errorf("expected stream to be true")
	}
	if receivedBody.EnableThinking == nil || *receivedBody.EnableThinking != false {
		t.Errorf("expected enable_thinking to be explicitly false")
	}
	if len(receivedBody.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(receivedBody.Messages))
	}
	if receivedBody.Messages[0].Role != "system" || receivedBody.Messages[0].Content != "你是小智助手。" {
		t.Errorf("unexpected system message: %+v", receivedBody.Messages[0])
	}
	if receivedBody.Messages[1].Role != "user" || receivedBody.Messages[1].Content != "你好！" {
		t.Errorf("unexpected user message: %+v", receivedBody.Messages[1])
	}
	if len(receivedBody.Tools) > 0 || len(receivedBody.Functions) > 0 {
		t.Errorf("expected no tools/functions in request")
	}
}

func TestLLMClient_EmptyDeltasSkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			// 1. delta 仅含 role，content 为空
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
			// 2. choices 为空切片
			`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":1700000001,"model":"qwen3.7-flash","choices":[]}` + "\n\n",
			// 3. delta 显式 content 为空字符串
			`data: {"id":"chatcmpl-3","object":"chat.completion.chunk","created":1700000002,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}` + "\n\n",
			// 4. 有效增量
			`data: {"id":"chatcmpl-4","object":"chat.completion.chunk","created":1700000003,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{"content":"唯一有效文本"},"finish_reason":null}]}` + "\n\n",
			// 5. 结束 chunk 只有 finish_reason
			`data: {"id":"chatcmpl-5","object":"chat.completion.chunk","created":1700000004,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}

		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 5 * time.Second,
				LLMOverallTimeout:    10 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	delta, err := stream.Recv()
	if err != nil {
		t.Fatalf("first Recv failed: %v", err)
	}
	if delta != "唯一有效文本" {
		t.Fatalf("expected %q, got %q", "唯一有效文本", delta)
	}

	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF on second Recv, got: %v", err)
	}
}

func TestLLMClient_HTTPErrorOnConnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Invalid API Key","type":"invalid_request_error"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "invalid-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 5 * time.Second,
				LLMOverallTimeout:    10 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err == nil {
		if stream != nil {
			_, recvErr := stream.Recv()
			if recvErr == nil {
				t.Fatal("expected error on Recv after 401 response, got nil")
			}
			stream.Close()
		} else {
			t.Fatal("expected error or non-nil stream that fails on Recv")
		}
	}
}

func TestLLMClient_SSEErrorInStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"qwen3.7-flash","choices":[{"index":0,"delta":{"content":"开始部分"},"finish_reason":null}]}` + "\n\n",
			`data: {"error":{"message":"model overloaded during generation","type":"server_error"}}` + "\n\n",
		}

		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 5 * time.Second,
				LLMOverallTimeout:    10 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	delta, err := stream.Recv()
	if err != nil {
		t.Fatalf("first Recv failed: %v", err)
	}
	if delta != "开始部分" {
		t.Fatalf("expected %q, got %q", "开始部分", delta)
	}

	_, err = stream.Recv()
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected stream error on second Recv, got: %v", err)
	}
	if !strings.Contains(err.Error(), "overloaded") && !strings.Contains(err.Error(), "error while streaming") {
		t.Logf("received error as expected: %v", err)
	}
}

func TestLLMClient_MalformedJSONInStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"delta":{"content":"part1"}}]}` + "\n\n",
			`data: {malformed json content here...` + "\n\n",
		}

		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 5 * time.Second,
				LLMOverallTimeout:    10 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	d1, err := stream.Recv()
	if err != nil || d1 != "part1" {
		t.Fatalf("expected part1, got %q, err: %v", d1, err)
	}

	_, err = stream.Recv()
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected json unmarshal error, got: %v", err)
	}
}

func TestLLMClient_FirstTokenTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// 发送一个空增量 chunk，然后挂起阻塞
		emptyChunk := `data: {"id":"1","object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"
		_, _ = w.Write([]byte(emptyChunk))
		flusher.Flush()

		// 挂起等待客户端取消
		<-r.Context().Done()
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 50 * time.Millisecond,
				LLMOverallTimeout:    2 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	start := time.Now()
	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	_, err = stream.Recv()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected first token timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}
	if !strings.Contains(err.Error(), "first token timeout") {
		t.Fatalf("expected 'first token timeout' in error message, got: %v", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 1*time.Second {
		t.Errorf("timeout took unexpected duration: %v", elapsed)
	}
}

func TestLLMClient_OverallTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// 立即发送首个 token
		chunk1 := `data: {"id":"1","object":"chat.completion.chunk","choices":[{"delta":{"content":"token1"}}]}` + "\n\n"
		_, _ = w.Write([]byte(chunk1))
		flusher.Flush()

		// 挂起，直到 overall timeout 到期
		<-r.Context().Done()
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 50 * time.Millisecond,
				LLMOverallTimeout:    100 * time.Millisecond,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	// 首个 token 正常接收
	delta, err := stream.Recv()
	if err != nil {
		t.Fatalf("first Recv failed: %v", err)
	}
	if delta != "token1" {
		t.Fatalf("expected token1, got %q", delta)
	}

	// 第二次 Recv 等待直到 overall timeout
	start := time.Now()
	_, err = stream.Recv()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected overall timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}
	if !strings.Contains(err.Error(), "overall timeout") {
		t.Fatalf("expected 'overall timeout' in error message, got: %v", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 1*time.Second {
		t.Errorf("overall timeout took unexpected duration: %v", elapsed)
	}
}

func TestLLMClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunk := `data: {"id":"1","object":"chat.completion.chunk","choices":[{"delta":{"content":"first"}}]}` + "\n\n"
		_, _ = w.Write([]byte(chunk))
		flusher.Flush()

		<-r.Context().Done()
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 5 * time.Second,
				LLMOverallTimeout:    10 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.CreateStream(ctx, []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err != nil {
		cancel()
		t.Fatalf("CreateStream failed: %v", err)
	}
	defer stream.Close()

	delta, err := stream.Recv()
	if err != nil || delta != "first" {
		cancel()
		t.Fatalf("expected first token, got %q, err: %v", delta, err)
	}

	// 主动取消 context
	cancel()

	_, err = stream.Recv()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestLLMClient_NoRetriesOnFailure(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.Error(w, `{"error":{"message":"Internal server error","type":"internal_error"}}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 1 * time.Second,
				LLMOverallTimeout:    2 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err == nil && stream != nil {
		_, _ = stream.Recv()
		stream.Close()
	}

	if count := requestCount.Load(); count != 1 {
		t.Fatalf("expected exactly 1 request without retries, got %d", count)
	}
}

func TestLLMClient_ConcurrentSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		for i := 0; i < 5; i++ {
			chunk := fmt.Sprintf(`data: {"id":"%d","object":"chat.completion.chunk","choices":[{"delta":{"content":"chunk%d"}}]}`+"\n\n", i, i)
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 2 * time.Second,
				LLMOverallTimeout:    5 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()

			stream, sErr := client.CreateStream(context.Background(), []ai.Message{
				{Role: ai.RoleUser, Content: fmt.Sprintf("message from %d", id)},
			})
			if sErr != nil {
				t.Errorf("goroutine %d CreateStream failed: %v", id, sErr)
				return
			}
			defer stream.Close()

			var readCount int
			for {
				_, rErr := stream.Recv()
				if errors.Is(rErr, io.EOF) {
					break
				}
				if rErr != nil {
					t.Errorf("goroutine %d Recv failed: %v", id, rErr)
					break
				}
				readCount++
			}
			if readCount != 5 {
				t.Errorf("goroutine %d expected 5 chunks, got %d", id, readCount)
			}
		}(g)
	}

	wg.Wait()
}

func TestLLMClient_ConcurrentRecvAndClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		for i := 0; i < 50; i++ {
			chunk := fmt.Sprintf(`data: {"id":"%d","object":"chat.completion.chunk","choices":[{"delta":{"content":"c"}}]}`+"\n\n", i)
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	cfg := &config.Config{
		DashScopeAPIKey: "test-key",
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				LLMEndpoint:          server.URL,
				LLMModel:             "qwen3.7-flash",
				LLMFirstTokenTimeout: 2 * time.Second,
				LLMOverallTimeout:    5 * time.Second,
			},
		},
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	stream, err := client.CreateStream(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: 持续读取
	go func() {
		defer wg.Done()
		for {
			_, rErr := stream.Recv()
			if rErr != nil {
				break
			}
		}
	}()

	// Goroutine 2: 延迟并发 Close
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		_ = stream.Close()
	}()

	wg.Wait()
}
