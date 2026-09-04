package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockSender struct {
	mu       sync.Mutex
	sent     [][]byte
	onSend   func(ctx context.Context, payload json.RawMessage) error
	senderFn func(client *DeviceMCPClient, req jsonRPCRequest)
	client   *DeviceMCPClient
}

func (m *mockSender) SendMCPPayload(ctx context.Context, payload json.RawMessage) error {
	m.mu.Lock()
	m.sent = append(m.sent, append([]byte(nil), payload...))
	onSend := m.onSend
	senderFn := m.senderFn
	client := m.client
	m.mu.Unlock()

	if onSend != nil {
		if err := onSend(ctx, payload); err != nil {
			return err
		}
	}

	if senderFn != nil && client != nil {
		var req jsonRPCRequest
		if err := json.Unmarshal(payload, &req); err == nil {
			go senderFn(client, req)
		}
	}

	return nil
}

// 1. 测试 initialize 成功以及协议版本不匹配/错误响应。
func TestDeviceMCPClient_Initialize(t *testing.T) {
	// 成功场景
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test-board","version":"1.0.0"}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"tools":[{"name":"self.audio_speaker.set_volume","description":"set vol","inputSchema":{"type":"object","properties":{"volume":{"type":"integer","minimum":0,"maximum":100}}}}]}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Discover(ctx)
	if err != nil {
		t.Fatalf("expected discovery to succeed, got %v", err)
	}

	tools := client.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "self.audio_speaker.set_volume" {
		t.Fatalf("unexpected tool name %s", tools[0].Name)
	}

	// 协议版本不匹配场景
	senderBad := &mockSender{}
	clientBad := NewDeviceMCPClient(senderBad)
	senderBad.client = clientBad
	senderBad.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		if req.Method == "initialize" {
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-01-01","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		}
	}

	err = clientBad.Discover(ctx)
	if err == nil {
		t.Fatal("expected error on version mismatch, got nil")
	}
	if !errors.Is(err, ErrMCPVersionMismatch) {
		t.Fatalf("expected ErrMCPVersionMismatch, got %v", err)
	}
	if len(clientBad.Tools()) != 0 {
		t.Fatalf("expected 0 tools on discovery failure, got %d", len(clientBad.Tools()))
	}
}

// 2. 测试数字请求 Id 及多并发/乱序响应关联。
func TestDeviceMCPClient_OutOfOrderResponses(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	var requests []jsonRPCRequest
	var reqMu sync.Mutex

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		reqMu.Lock()
		requests = append(requests, req)
		reqMu.Unlock()
	}

	// 手动并发调用 callRPC
	ctx := context.Background()
	var wg sync.WaitGroup
	var res1, res2 map[string]any

	wg.Add(2)
	go func() {
		defer wg.Done()
		var dst map[string]any
		_ = client.callRPC(ctx, "method1", map[string]any{}, &dst)
		res1 = dst
	}()
	go func() {
		defer wg.Done()
		var dst map[string]any
		_ = client.callRPC(ctx, "method2", map[string]any{}, &dst)
		res2 = dst
	}()

	// 等待两个请求都发出
	for {
		reqMu.Lock()
		count := len(requests)
		reqMu.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 乱序响应：先响应 method2，再响应 method1
	var req1, req2 jsonRPCRequest
	if requests[0].Method == "method1" {
		req1 = requests[0]
		req2 = requests[1]
	} else {
		req1 = requests[1]
		req2 = requests[0]
	}

	// 先响应 method2
	resp2 := jsonRPCResponse{
		JSONRPC: "2.0",
		Id:      &req2.Id,
		Result:  json.RawMessage(`{"val":"B"}`),
	}
	data2, _ := json.Marshal(resp2)
	client.HandlePayload(data2)

	// 再响应 method1
	resp1 := jsonRPCResponse{
		JSONRPC: "2.0",
		Id:      &req1.Id,
		Result:  json.RawMessage(`{"val":"A"}`),
	}
	data1, _ := json.Marshal(resp1)
	client.HandlePayload(data1)

	wg.Wait()

	if res1["val"] != "A" {
		t.Fatalf("expected res1 to be A, got %v", res1)
	}
	if res2["val"] != "B" {
		t.Fatalf("expected res2 to be B, got %v", res2)
	}
}

// 3. 测试 tools/list 多页拉取及游标翻页。
func TestDeviceMCPClient_Pagination(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/list":
			params, _ := json.Marshal(req.Params)
			var p mcpToolsListParams
			_ = json.Unmarshal(params, &p)

			if p.Cursor == "" {
				// 第一页
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					Id:      &req.Id,
					Result:  json.RawMessage(`{"tools":[{"name":"tool1","description":"tool 1","inputSchema":{"type":"object"}}],"nextCursor":"cursor-2"}`),
				}
				data, _ := json.Marshal(resp)
				c.HandlePayload(data)
			} else if p.Cursor == "cursor-2" {
				// 第二页
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					Id:      &req.Id,
					Result:  json.RawMessage(`{"tools":[{"name":"tool2","description":"tool 2","inputSchema":{"type":"object"}}],"nextCursor":""}`),
				}
				data, _ := json.Marshal(resp)
				c.HandlePayload(data)
			}
		}
	}

	err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	tools := client.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools from 2 pages, got %d", len(tools))
	}
	if tools[0].Name != "tool1" || tools[1].Name != "tool2" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}

// 4. 测试分页上限 16 页、循环 cursor、64 工具上限。
func TestDeviceMCPClient_DiscoveryLimits(t *testing.T) {
	// 4.1 循环 cursor
	senderLoop := &mockSender{}
	clientLoop := NewDeviceMCPClient(senderLoop)
	senderLoop.client = clientLoop
	senderLoop.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		if req.Method == "initialize" {
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		} else if req.Method == "tools/list" {
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"tools":[{"name":"tool_loop","description":"d","inputSchema":{"type":"object"}}],"nextCursor":"same-cursor"}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		}
	}

	err := clientLoop.Discover(context.Background())
	if err == nil {
		t.Fatal("expected discovery to fail on loop cursor, got nil")
	}
	if len(clientLoop.Tools()) != 0 {
		t.Fatalf("expected 0 tools on failed discovery, got %d", len(clientLoop.Tools()))
	}

	// 4.2 超过 64 工具上限
	senderMax := &mockSender{}
	clientMax := NewDeviceMCPClient(senderMax)
	senderMax.client = clientMax
	senderMax.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		if req.Method == "initialize" {
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		} else if req.Method == "tools/list" {
			// 返回 65 个工具
			toolsJson := `[`
			for i := 0; i < 65; i++ {
				if i > 0 {
					toolsJson += `,`
				}
				toolsJson += fmt.Sprintf(`{"name":"tool_%d","description":"d","inputSchema":{"type":"object"}}`, i)
			}
			toolsJson += `]`

			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(fmt.Sprintf(`{"tools":%s}`, toolsJson)),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		}
	}

	err = clientMax.Discover(context.Background())
	if err == nil {
		t.Fatal("expected discovery to fail when tool count > 64, got nil")
	}
	if len(clientMax.Tools()) != 0 {
		t.Fatalf("expected 0 tools when tool limit exceeded, got %d", len(clientMax.Tools()))
	}
}

// 5. 测试单个非法工具过滤、user-only 工具过滤，以及同名工具第一页优先。
func TestDeviceMCPClient_ToolFilteringAndPrecedence(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		if req.Method == "initialize" {
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		} else if req.Method == "tools/list" {
			params, _ := json.Marshal(req.Params)
			var p mcpToolsListParams
			_ = json.Unmarshal(params, &p)

			if p.Cursor == "" {
				// Page 1
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					Id:      &req.Id,
					Result: json.RawMessage(`{
						"tools": [
							{"name":"valid_tool","description":"desc 1","inputSchema":{"type":"object","properties":{"p1":{"type":"string"}}}},
							{"name":"user_tool","description":"u","inputSchema":{"type":"object"},"annotations":{"audience":["user"]}},
							{"name":"bad_schema_tool","description":"b","inputSchema":{"type":"string"}},
							{"name":"","description":"empty name","inputSchema":{"type":"object"}},
							{"name":"self.camera.take_photo","description":"camera tool","inputSchema":{"type":"object","properties":{"question":{"type":"string"}}}}
						],
						"nextCursor": "p2"
					}`),
				}
				data, _ := json.Marshal(resp)
				c.HandlePayload(data)
			} else {
				// Page 2: 包含同名 valid_tool（desc 2）
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					Id:      &req.Id,
					Result: json.RawMessage(`{
						"tools": [
							{"name":"valid_tool","description":"desc 2 duplicate","inputSchema":{"type":"object"}}
						]
					}`),
				}
				data, _ := json.Marshal(resp)
				c.HandlePayload(data)
			}
		}
	}

	err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	tools := client.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 valid tools, got %d", len(tools))
	}

	toolMap := make(map[string]ai.Tool)
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	validTool, ok := toolMap["valid_tool"]
	if !ok {
		t.Fatal("expected valid_tool to exist")
	}
	if validTool.Description != "desc 1" {
		t.Fatalf("expected first page description 'desc 1', got %q", validTool.Description)
	}

	camTool, ok := toolMap["self.camera.take_photo"]
	if !ok {
		t.Fatal("expected self.camera.take_photo to exist")
	}
	if camTool.Description != "camera tool" {
		t.Fatalf("unexpected camTool desc: %s", camTool.Description)
	}
}

// 6. 测试 tools/call 执行、参数转换、单并发串行、普通成功与错误。
func TestDeviceMCPClient_CallTool(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	var activeCalls atomic.Int32
	var maxConcurrency atomic.Int32

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result: json.RawMessage(`{
					"tools": [
						{"name":"test.echo","description":"echo tool","inputSchema":{"type":"object","properties":{"msg":{"type":"string"}}}},
						{"name":"test.error","description":"error tool","inputSchema":{"type":"object"}}
					]
				}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/call":
			cur := activeCalls.Add(1)
			for {
				oldMax := maxConcurrency.Load()
				if cur <= oldMax || maxConcurrency.CompareAndSwap(oldMax, cur) {
					break
				}
			}

			time.Sleep(30 * time.Millisecond) // 模拟执行耗时
			activeCalls.Add(-1)

			paramsBytes, _ := json.Marshal(req.Params)
			var p mcpToolsCallParams
			_ = json.Unmarshal(paramsBytes, &p)

			if p.Name == "test.echo" {
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					Id:      &req.Id,
					Result:  json.RawMessage(`{"content":[{"type":"text","text":"hello echo"}],"isError":false}`),
				}
				data, _ := json.Marshal(resp)
				c.HandlePayload(data)
			} else if p.Name == "test.error" {
				// 设备返回无 code 的 JSON-RPC error
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					Id:      &req.Id,
					Error: &jsonRPCError{
						Message: "device error: sensor failed",
					},
				}
				data, _ := json.Marshal(resp)
				c.HandlePayload(data)
			}
		}
	}

	err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// 6.1 正常调用
	res, err := client.CallTool(context.Background(), "test.echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatalf("CallTool test.echo failed: %v", err)
	}
	resMap, ok := res.(map[string]any)
	if !ok || resMap["isError"] != false {
		t.Fatalf("unexpected res: %+v", res)
	}

	// 6.2 设备报错调用（返回结构化 isError: true）
	errRes, err := client.CallTool(context.Background(), "test.error", nil)
	if err != nil {
		t.Fatalf("expected nil Go error for structured device error, got %v", err)
	}
	errResMap, ok := errRes.(map[string]any)
	if !ok || errResMap["isError"] != true {
		t.Fatalf("expected isError=true, got %+v", errRes)
	}

	// 6.3 未授权工具调用
	unauthRes, err := client.CallTool(context.Background(), "unauthorized.tool", nil)
	if err != nil {
		t.Fatalf("expected nil Go error for unauthorized tool, got %v", err)
	}
	unauthMap, ok := unauthRes.(map[string]any)
	if !ok || unauthMap["isError"] != true {
		t.Fatalf("expected isError=true for unauth tool, got %+v", unauthRes)
	}

	// 6.4 并发调用验证单并发串行
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.CallTool(context.Background(), "test.echo", nil)
		}()
	}
	wg.Wait()

	if max := maxConcurrency.Load(); max > 1 {
		t.Fatalf("expected max concurrency 1, got %d", max)
	}
}

// 7. 测试调用超时及永久停用机制。
func TestDeviceMCPClient_TimeoutAndDisable(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client
	client.toolCallTimeout = 50 * time.Millisecond

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"tools":[{"name":"slow_tool","description":"slow","inputSchema":{"type":"object"}}]}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/call":
			// 故意不响应，触发超时
		}
	}

	err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	// 此时未停用
	if client.IsDisabled() {
		t.Fatal("expected IsDisabled to be false initially")
	}

	// 执行工具调用，应当在 50ms 后超时，并返回结构化工具错误
	res, err := client.CallTool(context.Background(), "slow_tool", nil)
	if err != nil {
		t.Fatalf("expected nil Go error on tool call timeout, got %v", err)
	}
	resMap, ok := res.(map[string]any)
	if !ok || resMap["isError"] != true {
		t.Fatalf("expected isError=true on timeout, got %+v", res)
	}

	// 验证已自动永久停用
	if !client.IsDisabled() {
		t.Fatal("expected IsDisabled to be true after timeout")
	}

	// 停用后 Tools() 返回 nil
	if tools := client.Tools(); len(tools) != 0 {
		t.Fatalf("expected 0 tools when disabled, got %d", len(tools))
	}

	// 停用后再次调用直接返回 ErrMCPDisabled
	_, err = client.CallTool(context.Background(), "slow_tool", nil)
	if !errors.Is(err, ErrMCPDisabled) {
		t.Fatalf("expected ErrMCPDisabled, got %v", err)
	}
}

// 8. 测试工具 Schema 完整保留（required/default/min/max）。
func TestDeviceMCPClient_SchemaRetention(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	rawSchema := `{
		"type": "object",
		"properties": {
			"volume": {
				"type": "integer",
				"default": 50,
				"minimum": 0,
				"maximum": 100
			},
			"name": {
				"type": "string",
				"default": "guest"
			}
		},
		"required": ["volume"]
	}`

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result: json.RawMessage(fmt.Sprintf(`{
					"tools": [
						{"name":"test.vol","description":"vol tool","inputSchema":%s}
					]
				}`, rawSchema)),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		}
	}

	err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	tools := client.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	params := tools[0].Parameters
	if params["type"] != "object" {
		t.Fatalf("expected type=object, got %v", params["type"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", params["properties"])
	}
	volProp, ok := props["volume"].(map[string]any)
	if !ok {
		t.Fatalf("expected volume property map, got %T", props["volume"])
	}
	if volProp["minimum"].(float64) != 0 || volProp["maximum"].(float64) != 100 {
		t.Fatalf("expected min=0, max=100, got min=%v, max=%v", volProp["minimum"], volProp["maximum"])
	}
	reqList, ok := params["required"].([]any)
	if !ok || len(reqList) != 1 || reqList[0] != "volume" {
		t.Fatalf("expected required=['volume'], got %v", params["required"])
	}
}

// 8. 测试 Close 清理 pending 并唤醒等待者。
func TestDeviceMCPClient_CloseAndCleanup(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		// 不返回响应
	}

	var callErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var dst map[string]any
		callErr = client.callRPC(context.Background(), "test.hang", nil, &dst)
	}()

	time.Sleep(20 * time.Millisecond)
	client.Close()
	wg.Wait()

	if !errors.Is(callErr, ErrMCPClientClosed) {
		t.Fatalf("expected ErrMCPClientClosed on Close(), got %v", callErr)
	}
}

// 9. 测试 normalizeInputSchema 的各场景覆盖。
func TestDeviceMCPClient_NormalizeInputSchema(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		expectOk   bool
		expectType string
		hasProp    string
	}{
		{
			name:       "empty json raw",
			raw:        "",
			expectOk:   true,
			expectType: "object",
		},
		{
			name:       "null literal",
			raw:        "null",
			expectOk:   true,
			expectType: "object",
		},
		{
			name:       "empty object",
			raw:        "{}",
			expectOk:   true,
			expectType: "object",
		},
		{
			name:       "esp32 flat properties",
			raw:        `{"volume":{"type":"integer","minimum":0,"maximum":100}}`,
			expectOk:   true,
			expectType: "object",
			hasProp:    "volume",
		},
		{
			name:       "esp32 flat property named type",
			raw:        `{"type":{"type":"string"}}`,
			expectOk:   true,
			expectType: "object",
			hasProp:    "type",
		},
		{
			name:       "standard schema with properties",
			raw:        `{"type":"object","properties":{"p1":{"type":"string"}}}`,
			expectOk:   true,
			expectType: "object",
			hasProp:    "p1",
		},
		{
			name:       "standard schema without properties",
			raw:        `{"type":"object"}`,
			expectOk:   true,
			expectType: "object",
		},
		{
			name:     "invalid schema with string type",
			raw:      `{"type":"string"}`,
			expectOk: false,
		},
		{
			name:     "invalid json",
			raw:      `{invalid}`,
			expectOk: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeInputSchema(json.RawMessage(tc.raw))
			if ok != tc.expectOk {
				t.Fatalf("expected ok=%v, got %v", tc.expectOk, ok)
			}
			if tc.expectOk {
				if got["type"] != tc.expectType {
					t.Fatalf("expected type %q, got %v", tc.expectType, got["type"])
				}
				props, ok := got["properties"].(map[string]any)
				if !ok {
					t.Fatalf("expected properties to be map, got %T", got["properties"])
				}
				if tc.hasProp != "" {
					if _, exists := props[tc.hasProp]; !exists {
						t.Fatalf("expected property %q to exist in properties", tc.hasProp)
					}
				}
			}
		})
	}
}

// 10. 模拟真实 ESP32 固件端上报的工具列表（包含无参工具、属性字典工具）。
func TestDeviceMCPClient_ESP32FirmwareToolDiscovery(t *testing.T) {
	sender := &mockSender{}
	client := NewDeviceMCPClient(sender)
	sender.client = client

	sender.senderFn = func(c *DeviceMCPClient, req jsonRPCRequest) {
		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"esp32-s3-box","version":"1.2.0"}}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				Id:      &req.Id,
				Result: json.RawMessage(`{
					"tools": [
						{
							"name": "self.get_device_status",
							"description": "Provides real-time device status",
							"inputSchema": {}
						},
						{
							"name": "self.audio_speaker.set_volume",
							"description": "Set speaker volume",
							"inputSchema": {
								"volume": {
									"type": "integer",
									"minimum": 0,
									"maximum": 100
								}
							}
						},
						{
							"name": "self.screen.set_theme",
							"description": "Set screen theme",
							"inputSchema": {
								"theme": {
									"type": "string"
								}
							}
						}
					]
				}`),
			}
			data, _ := json.Marshal(resp)
			c.HandlePayload(data)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Discover(ctx); err != nil {
		t.Fatalf("expected Discover to succeed on ESP32 firmware tools, got %v", err)
	}

	tools := client.Tools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools discovered, got %d", len(tools))
	}

	toolMap := make(map[string]ai.Tool)
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	// 验证无参工具
	statusTool, ok := toolMap["self.get_device_status"]
	if !ok {
		t.Fatal("expected self.get_device_status to be discovered")
	}
	if statusTool.Parameters["type"] != "object" {
		t.Fatalf("expected statusTool type=object, got %v", statusTool.Parameters["type"])
	}

	// 验证带范围的整数参数工具
	volTool, ok := toolMap["self.audio_speaker.set_volume"]
	if !ok {
		t.Fatal("expected self.audio_speaker.set_volume to be discovered")
	}
	volProps := volTool.Parameters["properties"].(map[string]any)
	volDef := volProps["volume"].(map[string]any)
	if volDef["type"] != "integer" || volDef["minimum"].(float64) != 0 || volDef["maximum"].(float64) != 100 {
		t.Fatalf("unexpected volume parameter definition: %+v", volDef)
	}

	// 验证带字符串参数工具
	themeTool, ok := toolMap["self.screen.set_theme"]
	if !ok {
		t.Fatal("expected self.screen.set_theme to be discovered")
	}
	themeProps := themeTool.Parameters["properties"].(map[string]any)
	themeDef := themeProps["theme"].(map[string]any)
	if themeDef["type"] != "string" {
		t.Fatalf("unexpected theme parameter definition: %+v", themeDef)
	}
}
