package dashscope

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
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestNewLLMClient_Validation(t *testing.T) {
	// 1. Nil config
	if _, err := NewLLMClient(nil); err == nil {
		t.Error("expected error for nil config")
	}

	// 2. Empty API key
	cfg := &database.LLMConfig{Endpoint: "http://example.com", Model: "qwen-plus"}
	if _, err := NewLLMClient(cfg); err == nil {
		t.Error("expected error for empty APIKey")
	}

	// 3. Empty Endpoint
	cfg = &database.LLMConfig{APIKey: "key", Model: "qwen-plus"}
	if _, err := NewLLMClient(cfg); err == nil {
		t.Error("expected error for empty Endpoint")
	}

	// 4. Empty Model
	cfg = &database.LLMConfig{APIKey: "key", Endpoint: "http://example.com"}
	if _, err := NewLLMClient(cfg); err == nil {
		t.Error("expected error for empty Model")
	}

	// 5. Overall timeout <= First token timeout
	cfg = &database.LLMConfig{
		APIKey:              "key",
		Endpoint:            "http://example.com",
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    3000,
	}
	if _, err := NewLLMClient(cfg); err == nil {
		t.Error("expected error when overall timeout <= first token timeout")
	}

	// 6. Invalid Proxy URL
	cfg = &database.LLMConfig{
		APIKey:              "key",
		Endpoint:            "http://example.com",
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 3000,
		OverallTimeoutMS:    10000,
		ProxyURL:            "://invalid-url",
	}
	if _, err := NewLLMClient(cfg); err == nil {
		t.Error("expected error for invalid proxy url")
	}

	// 7. Valid config
	cfg = &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            "http://example.com",
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 3000,
		OverallTimeoutMS:    10000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestLLMClient_Generate_StreamingAndThinkingCheck(t *testing.T) {
	var receivedBody map[string]any
	var bodyMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			var parsed map[string]any
			if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
				bodyMu.Lock()
				receivedBody = parsed
				bodyMu.Unlock()
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		// SSE 模拟流式输出，包括 reasoning 块与文本块
		chunks := []string{
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"reasoning_content":"思考中..."},"index":0}]}` + "\n\n",
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"你好"},"index":0}]}` + "\n\n",
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"，我是小智。"},"index":0}]}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, c := range chunks {
			_, _ = fmt.Fprint(w, c)
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    15000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	var streamedText strings.Builder
	var chunkCount int
	finalText, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: "You are a helpful assistant."},
				{Role: ai.RoleUser, Content: "Hello"},
			},
		},
		func(ctx context.Context, chunk ai.LLMChunk) error {
			chunkCount++
			streamedText.WriteString(chunk.Text)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	expectedText := "你好，我是小智。"
	if finalText != expectedText {
		t.Fatalf("expected finalText %q, got %q", expectedText, finalText)
	}
	if streamedText.String() != expectedText {
		t.Fatalf("expected streamedText %q, got %q", expectedText, streamedText.String())
	}
	if chunkCount != 2 {
		t.Fatalf("expected 2 text chunks (reasoning filtered), got %d", chunkCount)
	}

	// 验证 enable_thinking 为 false 且进入了请求体
	bodyMu.Lock()
	defer bodyMu.Unlock()
	if val, ok := receivedBody["enable_thinking"]; !ok || val != false {
		t.Fatalf("expected enable_thinking=false in request body, got %v", receivedBody["enable_thinking"])
	}
}

func TestLLMClient_Generate_ToolCalls_ExecutionAndLoop(t *testing.T) {
	var requestCount atomic.Int32
	var toolExecuted atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqNum := requestCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		if reqNum == 1 {
			// 第一轮返回工具调用
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_time_1","type":"function","function":{"name":"get_time","arguments":"{}"}}]},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		// 第二轮返回最终文本
		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-2","choices":[{"delta":{"content":"当前时间是 10:00。"},"index":0}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    15000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	timeTool := ai.Tool{
		Name:        "get_time",
		Description: "获取当前时间",
		Parameters: map[string]any{
			"type": "object",
		},
		Run: func(ctx context.Context, input any) (any, error) {
			toolExecuted.Store(true)
			return map[string]any{"time": "10:00"}, nil
		},
	}

	var receivedChunks []ai.LLMChunk
	finalText, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleUser, Content: "几点了？"},
			},
			Tools:    []ai.Tool{timeTool},
			MaxTurns: 8,
		},
		func(ctx context.Context, chunk ai.LLMChunk) error {
			receivedChunks = append(receivedChunks, chunk)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if !toolExecuted.Load() {
		t.Fatal("expected tool to be executed")
	}
	if requestCount.Load() != 2 {
		t.Fatalf("expected 2 requests, got %d", requestCount.Load())
	}
	if finalText != "当前时间是 10:00。" {
		t.Fatalf("expected '当前时间是 10:00。', got %q", finalText)
	}
	if len(receivedChunks) != 1 || receivedChunks[0].Text != "当前时间是 10:00。" {
		t.Fatalf("unexpected received chunks: %v", receivedChunks)
	}
}

func TestLLMClient_Generate_FirstTokenTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 延迟 500ms 超过 100ms 首 token 超时
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"太晚了"},"index":0}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 100,
		OverallTimeoutMS:    10000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	_, err = client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{{Role: ai.RoleUser, Content: "test"}},
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ai.ErrFirstTokenTimeout) {
		t.Fatalf("expected ErrFirstTokenTimeout, got: %v", err)
	}
}

func TestLLMClient_Generate_FirstTokenTimeout_OnSecondTurn(t *testing.T) {
	var reqCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		num := reqCount.Add(1)
		if num == 1 {
			// 第一轮立即返回工具调用
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"test_tool","arguments":"{}"}}]},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		// 第二轮延迟超过首 token 超时
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-2","choices":[{"delta":{"content":"late"},"index":0}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 150,
		OverallTimeoutMS:    10000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	testTool := ai.Tool{
		Name:        "test_tool",
		Description: "test",
		Run: func(ctx context.Context, input any) (any, error) {
			return "ok", nil
		},
	}

	_, err = client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{{Role: ai.RoleUser, Content: "test"}},
			Tools:    []ai.Tool{testTool},
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error on second turn first token timeout")
	}
	if !errors.Is(err, ai.ErrFirstTokenTimeout) {
		t.Fatalf("expected ErrFirstTokenTimeout, got: %v", err)
	}
}

func TestLLMClient_Generate_OverallTimeout_DuringTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"slow_tool","arguments":"{}"}}]},"index":0}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 500,
		OverallTimeoutMS:    800,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	slowTool := ai.Tool{
		Name:        "slow_tool",
		Description: "slow",
		Run: func(ctx context.Context, input any) (any, error) {
			select {
			case <-time.After(2 * time.Second):
				return "done", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	_, err = client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{{Role: ai.RoleUser, Content: "test"}},
			Tools:    []ai.Tool{slowTool},
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error for overall timeout")
	}
	if !errors.Is(err, ai.ErrOverallTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrOverallTimeout or DeadlineExceeded, got: %v", err)
	}
}

func TestLLMClient_Generate_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    10000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 取消

	_, err = client.Generate(
		ctx,
		ai.LLMRequest{
			Messages: []ai.Message{{Role: ai.RoleUser, Content: "test"}},
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestLLMClient_Generate_MaxTurnsExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 始终返回工具调用，永不停歇
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_repeat","type":"function","function":{"name":"loop_tool","arguments":"{}"}}]},"index":0}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 2000,
		OverallTimeoutMS:    10000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	loopTool := ai.Tool{
		Name:        "loop_tool",
		Description: "loop",
		Run: func(ctx context.Context, input any) (any, error) {
			return "continue", nil
		},
	}

	_, err = client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{{Role: ai.RoleUser, Content: "test"}},
			Tools:    []ai.Tool{loopTool},
			MaxTurns: 2,
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected ErrMaxTurnsExceeded")
	}
	if !errors.Is(err, ai.ErrMaxTurnsExceeded) {
		t.Fatalf("expected ErrMaxTurnsExceeded, got: %v", err)
	}
}

func TestLLMClient_Generate_ConcurrentToolExecution(t *testing.T) {
	var reqCount atomic.Int32
	var tool1Executed, tool2Executed atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		num := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		if num == 1 {
			// 一次返回两个 tool call
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"tool_a","arguments":"{}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"tool_b","arguments":"{}"}}]},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-2","choices":[{"delta":{"content":"双工具执行完毕。"},"index":0}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    15000,
	}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	toolA := ai.Tool{
		Name:        "tool_a",
		Description: "a",
		Run: func(ctx context.Context, input any) (any, error) {
			time.Sleep(10 * time.Millisecond)
			tool1Executed.Store(true)
			return "result_a", nil
		},
	}
	toolB := ai.Tool{
		Name:        "tool_b",
		Description: "b",
		Run: func(ctx context.Context, input any) (any, error) {
			time.Sleep(10 * time.Millisecond)
			tool2Executed.Store(true)
			return "result_b", nil
		},
	}

	finalText, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{{Role: ai.RoleUser, Content: "test"}},
			Tools:    []ai.Tool{toolA, toolB},
			MaxTurns: 8,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if finalText != "双工具执行完毕。" {
		t.Fatalf("expected '双工具执行完毕。', got %q", finalText)
	}
	if !tool1Executed.Load() || !tool2Executed.Load() {
		t.Fatal("expected both tools to be executed")
	}
}
