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

	"xiaozhi-esp32-golang-server/internal/agentkit"
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
	chunksCh := make(chan ai.LLMChunk, 100)
	res, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: "You are a helpful assistant."},
				{Role: ai.RoleUser, Content: "Hello"},
			},
		},
		chunksCh,
	)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	for len(chunksCh) > 0 {
		chunk := <-chunksCh
		chunkCount++
		streamedText.WriteString(chunk.Text)
	}

	expectedText := "你好，我是小智。"
	if res.FinalText != expectedText {
		t.Fatalf("expected finalText %q, got %q", expectedText, res.FinalText)
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
	var reqBodiesMu sync.Mutex
	var reqBodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			var parsed map[string]any
			if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
				reqBodiesMu.Lock()
				reqBodies = append(reqBodies, parsed)
				reqBodiesMu.Unlock()
			}
		}

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
			"properties": map[string]any{
				"format": map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, input any) (any, error) {
			toolExecuted.Store(true)
			return map[string]any{"time": "10:00"}, nil
		},
	}

	var receivedChunks []ai.LLMChunk
	chunksCh := make(chan ai.LLMChunk, 100)
	res, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: "你是一个时间查询助手。"},
				{Role: ai.RoleUser, Content: "几点了？"},
			},
			Tools:    []ai.Tool{timeTool},
			MaxTurns: 8,
		},
		chunksCh,
	)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	for len(chunksCh) > 0 {
		receivedChunks = append(receivedChunks, <-chunksCh)
	}

	if !toolExecuted.Load() {
		t.Fatal("expected tool to be executed")
	}
	if requestCount.Load() != 2 {
		t.Fatalf("expected 2 requests, got %d", requestCount.Load())
	}
	if res.FinalText != "当前时间是 10:00。" {
		t.Fatalf("expected '当前时间是 10:00。', got %q", res.FinalText)
	}
	if len(receivedChunks) != 1 || receivedChunks[0].Text != "当前时间是 10:00。" {
		t.Fatalf("unexpected received chunks: %v", receivedChunks)
	}

	// 验证请求体：WithTools 是唯一工具描述通道
	reqBodiesMu.Lock()
	defer reqBodiesMu.Unlock()

	if len(reqBodies) != 2 {
		t.Fatalf("expected 2 request bodies captured, got %d", len(reqBodies))
	}

	// 1. 验证首轮请求：tools 字段包含原生 tool schema，且 messages[0] 纯净无工具 JSON
	req1 := reqBodies[0]
	rawTools, ok := req1["tools"].([]any)
	if !ok || len(rawTools) != 1 {
		t.Fatalf("expected 1 tool in req1 tools field, got %v", req1["tools"])
	}
	tool0, ok := rawTools[0].(map[string]any)
	if !ok || tool0["type"] != "function" {
		t.Fatalf("expected tool type 'function', got %v", tool0["type"])
	}
	fnMap, ok := tool0["function"].(map[string]any)
	if !ok || fnMap["name"] != "get_time" || fnMap["description"] != "获取当前时间" {
		t.Fatalf("unexpected tool function definition: %v", fnMap)
	}

	req1Messages, ok := req1["messages"].([]any)
	if !ok || len(req1Messages) != 2 {
		t.Fatalf("expected 2 messages in req1, got %v", req1["messages"])
	}
	sysMsg := req1Messages[0].(map[string]any)
	if sysMsg["role"] != "system" || sysMsg["content"] != "你是一个时间查询助手。" {
		t.Fatalf("expected pure system message, got %v", sysMsg)
	}

	// 2. 验证第二轮请求：包含工具调用结果回填（role=tool / tool message）
	req2 := reqBodies[1]
	req2Messages, ok := req2["messages"].([]any)
	if !ok || len(req2Messages) < 3 {
		t.Fatalf("expected >= 3 messages in req2, got %v", req2["messages"])
	}

	var foundToolResult bool
	for _, m := range req2Messages {
		msgMap, ok := m.(map[string]any)
		if ok && msgMap["role"] == "tool" {
			foundToolResult = true
			if msgMap["tool_call_id"] != "call_time_1" {
				t.Fatalf("expected tool_call_id 'call_time_1', got %v", msgMap["tool_call_id"])
			}
			contentStr, _ := msgMap["content"].(string)
			if !strings.Contains(contentStr, "10:00") {
				t.Fatalf("expected tool result content to contain '10:00', got %q", contentStr)
			}
		}
	}
	if !foundToolResult {
		t.Fatalf("expected tool result message in req2 messages, got: %v", req2Messages)
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

	res, err := client.Generate(
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
	if res.FinalText != "双工具执行完毕。" {
		t.Fatalf("expected '双工具执行完毕。', got %q", res.FinalText)
	}
	if !tool1Executed.Load() || !tool2Executed.Load() {
		t.Fatal("expected both tools to be executed")
	}
}

func TestLLMClient_Generate_WithTools_SchemaPayloadVerification(t *testing.T) {
	var capturedBody map[string]any
	var bodyMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			var parsed map[string]any
			if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
				bodyMu.Lock()
				capturedBody = parsed
				bodyMu.Unlock()
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"测试成功。"},"index":0}]}`+"\n\n")
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

	// 1. 无工具调用测试
	_, err = client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: "系统角色"},
				{Role: ai.RoleUser, Content: "你好"},
			},
			Tools: nil,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	bodyMu.Lock()
	if toolsVal, exists := capturedBody["tools"]; exists && toolsVal != nil {
		if toolsList, ok := toolsVal.([]any); ok && len(toolsList) > 0 {
			t.Fatalf("expected no tools when Tools is nil, got %v", toolsVal)
		}
	}
	bodyMu.Unlock()

	// 2. 带多工具 Schema 测试
	toolA := ai.Tool{
		Name:        "custom.search",
		Description: "搜索相关信息",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "搜索关键词"},
			},
			"required": []string{"query"},
		},
	}
	toolB := ai.Tool{
		Name:        "custom.mute",
		Description: "静音控制",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}

	_, err = client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleSystem, Content: "你是一个纯净的助手，没有工具被硬编码进此提示词。"},
				{Role: ai.RoleUser, Content: "请帮我查一下天气"},
			},
			Tools: []ai.Tool{toolA, toolB},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Generate with multiple tools failed: %v", err)
	}

	bodyMu.Lock()
	defer bodyMu.Unlock()

	rawTools, ok := capturedBody["tools"].([]any)
	if !ok || len(rawTools) != 2 {
		t.Fatalf("expected 2 tools sent via WithTools, got %v", capturedBody["tools"])
	}

	toolNames := make(map[string]map[string]any)
	for _, rt := range rawTools {
		if tm, ok := rt.(map[string]any); ok {
			if fn, ok := tm["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					toolNames[name] = fn
				}
			}
		}
	}

	if _, ok := toolNames["custom.search"]; !ok {
		t.Fatal("expected custom.search in tools payload")
	}
	if _, ok := toolNames["custom.mute"]; !ok {
		t.Fatal("expected custom.mute in tools payload")
	}

	// 验证 messages 中无工具 schema 注入
	msgs, ok := capturedBody["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %v", capturedBody["messages"])
	}
	sysMsg := msgs[0].(map[string]any)
	if strings.Contains(sysMsg["content"].(string), "custom.search") || strings.Contains(sysMsg["content"].(string), "custom.mute") {
		t.Fatal("system prompt must NOT contain injected tool schemas")
	}
}

func TestLLMClient_Generate_TypedStructToolResult(t *testing.T) {
	type TypedToolOutput struct {
		Status   string `json:"status"`
		Datetime string `json:"datetime"`
		Timezone string `json:"timezone"`
	}

	var requestCount atomic.Int32
	var reqBodiesMu sync.Mutex
	var reqBodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			var parsed map[string]any
			if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
				reqBodiesMu.Lock()
				reqBodies = append(reqBodies, parsed)
				reqBodiesMu.Unlock()
			}
		}

		reqNum := requestCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		if reqNum == 1 {
			// 第一轮返回工具调用
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_struct_1","type":"function","function":{"name":"server.get_time","arguments":"{}"}}]},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		// 第二轮根据结构化结果返回答复
		_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-2","choices":[{"delta":{"content":"当前系统时间获取完毕。"},"index":0}]}`+"\n\n")
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

	structTool := ai.Tool{
		Name:        "server.get_time",
		Description: "获取结构化时间",
		Run: func(ctx context.Context, input any) (any, error) {
			// 返回强类型 struct 结构体结果
			return &TypedToolOutput{
				Status:   "success",
				Datetime: "2026-08-31 18:00:00",
				Timezone: "CST",
			}, nil
		},
	}

	res, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleUser, Content: "查时间"},
			},
			Tools:    []ai.Tool{structTool},
			MaxTurns: 4,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Generate with struct tool result failed: %v", err)
	}
	if res.FinalText != "当前系统时间获取完毕。" {
		t.Fatalf("unexpected finalText %q", res.FinalText)
	}

	// 验证第二轮请求中，typed struct 被正确序列化并填入 tool message
	reqBodiesMu.Lock()
	defer reqBodiesMu.Unlock()

	if len(reqBodies) != 2 {
		t.Fatalf("expected 2 request bodies, got %d", len(reqBodies))
	}

	req2Messages, ok := reqBodies[1]["messages"].([]any)
	if !ok || len(req2Messages) < 2 {
		t.Fatalf("expected messages in req2, got %v", reqBodies[1]["messages"])
	}

	var foundToolResult bool
	for _, m := range req2Messages {
		msgMap, ok := m.(map[string]any)
		if ok && msgMap["role"] == "tool" {
			foundToolResult = true
			if msgMap["tool_call_id"] != "call_struct_1" {
				t.Fatalf("expected tool_call_id 'call_struct_1', got %v", msgMap["tool_call_id"])
			}
			contentStr, _ := msgMap["content"].(string)
			if !strings.Contains(contentStr, "2026-08-31 18:00:00") || !strings.Contains(contentStr, "CST") {
				t.Fatalf("expected tool result content to contain structured output fields, got %q", contentStr)
			}
		}
	}
	if !foundToolResult {
		t.Fatalf("expected tool message with role=tool in req2, got: %v", req2Messages)
	}
}

type mockDeviceSender struct {
	mu     sync.Mutex
	client *agentkit.DeviceMCPClient
	calls  []map[string]any
}

func (m *mockDeviceSender) SendMCPPayload(ctx context.Context, payload json.RawMessage) error {
	var req struct {
		Id     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return err
	}

	go func() {
		switch req.Method {
		case "initialize":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`, req.Id)
			m.client.HandlePayload(json.RawMessage(resp))
		case "tools/list":
			resp := fmt.Sprintf(`{
				"jsonrpc": "2.0",
				"id": %d,
				"result": {
					"tools": [
						{
							"name": "self.audio_speaker.set_volume",
							"description": "Set the volume of the audio speaker",
							"inputSchema": {
								"type": "object",
								"properties": {
									"volume": {
										"type": "integer",
										"minimum": 0,
										"maximum": 100
									}
								},
								"required": ["volume"]
							}
						}
					]
				}
			}`, req.Id)
			m.client.HandlePayload(json.RawMessage(resp))
		case "tools/call":
			var p map[string]any
			_ = json.Unmarshal(req.Params, &p)
			m.mu.Lock()
			m.calls = append(m.calls, p)
			m.mu.Unlock()

			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"true"}],"isError":false}}`, req.Id)
			m.client.HandlePayload(json.RawMessage(resp))
		}
	}()

	return nil
}

func TestLLMClient_Generate_DynamicDeviceMCPTool_EndToEnd(t *testing.T) {
	// 1. 模拟设备工具发现
	sender := &mockDeviceSender{}
	mcpClient := agentkit.NewDeviceMCPClient(sender)
	sender.client = mcpClient

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mcpClient.Discover(ctx); err != nil {
		t.Fatalf("DeviceMCPClient Discover failed: %v", err)
	}

	tools := mcpClient.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 device tool discovered, got %d", len(tools))
	}
	if tools[0].Name != "self.audio_speaker.set_volume" {
		t.Fatalf("unexpected tool name %s", tools[0].Name)
	}

	// 2. 模拟 LLM 服务端（OpenAI SSE 协议）
	var turn int32
	var reqBodies []map[string]any
	var reqBodiesMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			var parsed map[string]any
			if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
				reqBodiesMu.Lock()
				reqBodies = append(reqBodies, parsed)
				reqBodiesMu.Unlock()
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		currentTurn := atomic.AddInt32(&turn, 1)

		if currentTurn == 1 {
			// 第一轮：模型返回 tool call
			chunks := []string{
				`data: {"id":"chatcmpl-mcp-1","choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_vol_1","type":"function","function":{"name":"self.audio_speaker.set_volume","arguments":"{\"volume\":80}"}}]},"index":0}]}` + "\n\n",
				"data: [DONE]\n\n",
			}
			for _, c := range chunks {
				_, _ = fmt.Fprint(w, c)
				flusher.Flush()
			}
		} else if currentTurn == 2 {
			// 第二轮：接收工具执行结果并返回最终回答
			chunks := []string{
				`data: {"id":"chatcmpl-mcp-2","choices":[{"delta":{"role":"assistant","content":"音量已调节为 80。"},"index":0}]}` + "\n\n",
				"data: [DONE]\n\n",
			}
			for _, c := range chunks {
				_, _ = fmt.Fprint(w, c)
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	cfg := &database.LLMConfig{
		APIKey:              "test-key",
		Endpoint:            server.URL,
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    10000,
	}
	llmClient, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient failed: %v", err)
	}

	// 3. 执行流式 Generate
	res, err := llmClient.Generate(
		ctx,
		ai.LLMRequest{
			Messages: []ai.Message{
				{Role: ai.RoleUser, Content: "请把音量调到 80"},
			},
			Tools:    tools,
			MaxTurns: 8,
		},
		nil,
	)

	if err != nil {
		t.Fatalf("llmClient.Generate failed: %v", err)
	}

	// 4. 校验最终生成的文本
	if res.FinalText != "音量已调节为 80。" {
		t.Fatalf("expected final text '音量已调节为 80。', got %q", res.FinalText)
	}

	// 5. 校验设备是否收到 tools/call 且参数为 volume: 80
	sender.mu.Lock()
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 call to device, got %d", len(sender.calls))
	}
	call := sender.calls[0]
	sender.mu.Unlock()

	if call["name"] != "self.audio_speaker.set_volume" {
		t.Fatalf("expected device call tool name self.audio_speaker.set_volume, got %v", call["name"])
	}
	args, ok := call["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("expected map arguments, got %T (%+v)", call["arguments"], call["arguments"])
	}
	if args["volume"].(float64) != 80 {
		t.Fatalf("expected volume 80, got %v", args["volume"])
	}

	// 6. 校验模型第一轮请求中 tools Schema 与设备上报一致
	reqBodiesMu.Lock()
	defer reqBodiesMu.Unlock()

	if len(reqBodies) != 2 {
		t.Fatalf("expected 2 turns of model requests, got %d", len(reqBodies))
	}

	req1Tools, ok := reqBodies[0]["tools"].([]any)
	if !ok || len(req1Tools) != 1 {
		t.Fatalf("expected 1 tool in req1 tools, got %v", reqBodies[0]["tools"])
	}
	tool0 := req1Tools[0].(map[string]any)
	fn := tool0["function"].(map[string]any)
	if fn["name"] != "self.audio_speaker.set_volume" {
		t.Fatalf("expected tool name in schema self.audio_speaker.set_volume, got %v", fn["name"])
	}

	// 7. 校验模型第二轮请求中 messages 包含 role=tool 及其内容
	req2Msgs, ok := reqBodies[1]["messages"].([]any)
	if !ok || len(req2Msgs) < 2 {
		t.Fatalf("expected messages in req2, got %v", reqBodies[1]["messages"])
	}

	var foundToolMessage bool
	for _, m := range req2Msgs {
		msgMap, ok := m.(map[string]any)
		if ok && msgMap["role"] == "tool" {
			foundToolMessage = true
			if msgMap["tool_call_id"] != "call_vol_1" {
				t.Fatalf("expected tool_call_id call_vol_1, got %v", msgMap["tool_call_id"])
			}
			contentStr, _ := msgMap["content"].(string)
			if !strings.Contains(contentStr, "true") && !strings.Contains(contentStr, "content") {
				t.Fatalf("expected tool response content in tool message, got %q", contentStr)
			}
		}
	}
	if !foundToolMessage {
		t.Fatalf("expected role=tool message in second turn request, got: %v", req2Msgs)
	}
}

func TestLLMClient_Generate_MultiTurn_PreserveToolCallHistory(t *testing.T) {
	var requestCount atomic.Int32
	var reqBodiesMu sync.Mutex
	var reqBodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			var parsed map[string]any
			if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
				reqBodiesMu.Lock()
				reqBodies = append(reqBodies, parsed)
				reqBodiesMu.Unlock()
			}
		}

		reqNum := requestCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		switch reqNum {
		case 1:
			// Turn 1 第一步：返回 set_volume 工具调用
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-t1-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_vol_1","type":"function","function":{"name":"self.audio_speaker.set_volume","arguments":"{\"volume\":40}"}}]},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		case 2:
			// Turn 1 第二步：工具执行后返回最终回复
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-t1-2","choices":[{"delta":{"content":"已将音量设为40%。"},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		case 3:
			// Turn 2 第一步：基于历史继续返回 set_volume 工具调用
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-t2-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_vol_2","type":"function","function":{"name":"self.audio_speaker.set_volume","arguments":"{\"volume\":50}"}}]},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		case 4:
			// Turn 2 第二步：返回最终回复
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-t2-2","choices":[{"delta":{"content":"已将音量设为50%。"},"index":0}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
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

	var volHistory []int
	var volMu sync.Mutex
	volumeTool := ai.Tool{
		Name:        "self.audio_speaker.set_volume",
		Description: "设置音量",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"volume": map[string]any{"type": "integer"},
			},
		},
		Run: func(ctx context.Context, input any) (any, error) {
			volMu.Lock()
			defer volMu.Unlock()
			if m, ok := input.(map[string]any); ok {
				if v, ok := m["volume"].(float64); ok {
					volHistory = append(volHistory, int(v))
				}
			}
			return map[string]any{"status": "ok"}, nil
		},
	}

	// ====== Turn 1 ======
	t1UserText := "调大音量"
	t1Messages := []ai.Message{
		{Role: ai.RoleSystem, Content: "你是一个设备管家。"},
		{Role: ai.RoleUser, Content: t1UserText},
	}
	res1, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: t1Messages,
			Tools:    []ai.Tool{volumeTool},
			MaxTurns: 8,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Turn 1 Generate failed: %v", err)
	}
	if res1.FinalText != "已将音量设为40%。" {
		t.Fatalf("expected '已将音量设为40%%。', got %q", res1.FinalText)
	}
	if len(res1.Messages) != 3 {
		t.Fatalf("expected 3 messages generated in Turn 1 (AssistantToolCall, ToolResp, AssistantText), got %d", len(res1.Messages))
	}

	// ====== Turn 2 ======
	t2UserText := "再大一点"
	t2Messages := []ai.Message{
		{Role: ai.RoleSystem, Content: "你是一个设备管家。"},
		{Role: ai.RoleUser, Content: t1UserText},
	}
	t2Messages = append(t2Messages, res1.Messages...)
	t2Messages = append(t2Messages, ai.Message{Role: ai.RoleUser, Content: t2UserText})

	res2, err := client.Generate(
		context.Background(),
		ai.LLMRequest{
			Messages: t2Messages,
			Tools:    []ai.Tool{volumeTool},
			MaxTurns: 8,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Turn 2 Generate failed: %v", err)
	}
	if res2.FinalText != "已将音量设为50%。" {
		t.Fatalf("expected '已将音量设为50%%。', got %q", res2.FinalText)
	}

	// 验证 HTTP 捕获的请求报文
	reqBodiesMu.Lock()
	defer reqBodiesMu.Unlock()

	if len(reqBodies) != 4 {
		t.Fatalf("expected 4 HTTP requests across 2 turns, got %d", len(reqBodies))
	}

	// Turn 2 的首个请求 (reqBodies[2]) 必须完整包含 Turn 1 的 Tool Call 轨迹
	t2FirstReqMsgs, ok := reqBodies[2]["messages"].([]any)
	if !ok || len(t2FirstReqMsgs) < 6 {
		t.Fatalf("expected >= 6 messages in Turn 2 initial request, got %v", reqBodies[2]["messages"])
	}

	// 验证顺序与角色
	// 0: System
	// 1: User (Turn 1: 调大音量)
	// 2: Assistant (ToolCalls: call_vol_1)
	// 3: Tool (tool_call_id: call_vol_1)
	// 4: Assistant (已将音量设为40%。)
	// 5: User (Turn 2: 再大一点)
	msg0 := t2FirstReqMsgs[0].(map[string]any)
	if msg0["role"] != "system" {
		t.Errorf("msg0 expected system, got %v", msg0["role"])
	}

	msg1 := t2FirstReqMsgs[1].(map[string]any)
	if msg1["role"] != "user" || !strings.Contains(fmt.Sprint(msg1["content"]), "调大音量") {
		t.Errorf("msg1 expected user '调大音量', got %+v", msg1)
	}

	msg2 := t2FirstReqMsgs[2].(map[string]any)
	if msg2["role"] != "assistant" || msg2["tool_calls"] == nil {
		t.Errorf("msg2 expected assistant with tool_calls, got %+v", msg2)
	}

	msg3 := t2FirstReqMsgs[3].(map[string]any)
	if msg3["role"] != "tool" || msg3["tool_call_id"] != "call_vol_1" {
		t.Errorf("msg3 expected tool with call_vol_1, got %+v", msg3)
	}

	msg4 := t2FirstReqMsgs[4].(map[string]any)
	if msg4["role"] != "assistant" || !strings.Contains(fmt.Sprint(msg4["content"]), "已将音量设为40") {
		t.Errorf("msg4 expected assistant '已将音量设为40%%。', got %+v", msg4)
	}

	msg5 := t2FirstReqMsgs[5].(map[string]any)
	if msg5["role"] != "user" || !strings.Contains(fmt.Sprint(msg5["content"]), "再大一点") {
		t.Errorf("msg5 expected user '再大一点', got %+v", msg5)
	}
}
