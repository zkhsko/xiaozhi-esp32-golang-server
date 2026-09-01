package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

type dummySender struct {
	mu     sync.Mutex
	sent   [][]byte
	onSend func(ctx context.Context, payload json.RawMessage) error
}

func (d *dummySender) SendText(ctx context.Context, payload []byte) error {
	d.mu.Lock()
	d.sent = append(d.sent, append([]byte(nil), payload...))
	onSend := d.onSend
	d.mu.Unlock()

	var m DownlinkMCPMessage
	if err := json.Unmarshal(payload, &m); err == nil && onSend != nil {
		return onSend(ctx, m.Payload)
	}
	return nil
}

func (d *dummySender) SendBinary(ctx context.Context, payload []byte) error {
	return nil
}

func TestToolProvider_BuildSnapshot_BuiltinToolsOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provider := NewToolProvider(nil, slog.Default())
	effects := &TurnEffects{}
	tools := provider.BuildSnapshot(ctx, 1, "sess-tools-test", effects)

	// 预期仅包含两个内置工具：server.get_current_time, server.close_session
	if len(tools) != 2 {
		t.Fatalf("expected 2 available tools, got %d", len(tools))
	}

	var hasTime, hasClose bool
	for _, tool := range tools {
		switch tool.Name {
		case agentkit.ToolGetCurrentTime:
			hasTime = true
		case agentkit.ToolCloseSession:
			hasClose = true
		default:
			t.Fatalf("unexpected tool name %s", tool.Name)
		}
	}

	if !hasTime || !hasClose {
		t.Fatalf("missing expected tools: hasTime=%v, hasClose=%v", hasTime, hasClose)
	}
}

func TestToolProvider_BuildSnapshot_ExecuteServerToolClosure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provider := NewToolProvider(nil, slog.Default())
	effects := &TurnEffects{}
	tools := provider.BuildSnapshot(ctx, 1, "sess-tool-closure-test", effects)

	var closeTool *ai.Tool
	for i := range tools {
		if tools[i].Name == agentkit.ToolCloseSession {
			closeTool = &tools[i]
			break
		}
	}

	if closeTool == nil {
		t.Fatal("closeTool not found")
	}

	if effects.CloseSession {
		t.Fatal("effects.CloseSession should initially be false")
	}

	res, err := closeTool.Run(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("closeTool.Run failed: %v", err)
	}
	out, ok := res.(*agentkit.CloseSessionOutput)
	if !ok || out == nil || out.Status != "success" {
		t.Fatalf("expected *CloseSessionOutput with status 'success', got %T (%+v)", res, res)
	}

	if !effects.CloseSession {
		t.Fatal("expected effects.CloseSession to be true after executing close_session")
	}
}

func TestToolProvider_ExecuteSnapshotTool_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	provider := NewToolProvider(nil, slog.Default())
	effects := &TurnEffects{}
	tools := provider.BuildSnapshot(context.Background(), 1, "sess-cancel-test", effects)
	if len(tools) == 0 {
		t.Fatal("expected tools in snapshot")
	}

	_, err := tools[0].Run(ctx, nil)
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func TestToolProvider_DeviceToolCallLimit_8PerGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := &dummySender{}
	bridge := NewMCPBridge(slog.Default(), nil)

	sender.onSend = func(c context.Context, payload json.RawMessage) error {
		var req struct {
			Id     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(payload, &req)
		switch req.Method {
		case "initialize":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`, req.Id)
			go bridge.HandleInbound("sess-limit-test", &ClientMessage{Kind: KindMCP, SessionId: "sess-limit-test", Payload: json.RawMessage(resp)})
		case "tools/list":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"self.device.action","description":"action","inputSchema":{"type":"object"}}]}}`, req.Id)
			go bridge.HandleInbound("sess-limit-test", &ClientMessage{Kind: KindMCP, SessionId: "sess-limit-test", Payload: json.RawMessage(resp)})
		case "tools/call":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}]}}`, req.Id)
			go bridge.HandleInbound("sess-limit-test", &ClientMessage{Kind: KindMCP, SessionId: "sess-limit-test", Payload: json.RawMessage(resp)})
		}
		return nil
	}

	bridge.Enable("sess-limit-test", sender)
	_ = bridge.WaitReady(ctx)

	provider := NewToolProvider(bridge, slog.Default())
	effects := &TurnEffects{}
	tools := provider.BuildSnapshot(ctx, 1, "sess-limit-test", effects)

	var deviceTool *ai.Tool
	for i := range tools {
		if tools[i].Name == "self.device.action" {
			deviceTool = &tools[i]
			break
		}
	}
	if deviceTool == nil {
		t.Fatal("device tool not found in snapshot")
	}

	// 连续调用 8 次，应该都成功
	for i := 0; i < MaxGenerationDeviceToolCalls; i++ {
		res, err := deviceTool.Run(ctx, map[string]any{})
		if err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}
		m, ok := res.(map[string]any)
		if !ok || m["isError"] == true {
			t.Fatalf("call %d returned error: %+v", i+1, res)
		}
	}

	// 第 9 次调用应被拦截并返回预算超限错误
	res9, err := deviceTool.Run(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("call 9 unexpected go error: %v", err)
	}
	m9, ok := res9.(map[string]any)
	if !ok || m9["isError"] != true {
		t.Fatalf("call 9 expected isError=true, got %+v", res9)
	}
	contentList, _ := m9["content"].([]any)
	if len(contentList) == 0 {
		t.Fatalf("call 9 expected content error message, got %+v", m9)
	}
	firstContent, _ := contentList[0].(map[string]any)
	msgText, _ := firstContent["text"].(string)
	if !strings.Contains(msgText, "limit exceeded") {
		t.Fatalf("expected limit exceeded error message, got %s", msgText)
	}
}
