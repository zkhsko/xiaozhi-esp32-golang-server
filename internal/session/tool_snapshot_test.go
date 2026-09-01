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

func (d *dummySender) SendMCPPayload(ctx context.Context, payload json.RawMessage) error {
	d.mu.Lock()
	d.sent = append(d.sent, append([]byte(nil), payload...))
	onSend := d.onSend
	d.mu.Unlock()

	if onSend != nil {
		return onSend(ctx, payload)
	}
	return nil
}

func TestSession_AvailableTools_BuiltinToolsOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-tools-test",
		generation: 1,
	}

	tools := sess.availableTools(1)

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

func TestSession_AvailableTools_ExecuteServerToolClosure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-tool-closure-test",
		generation: 1,
	}

	tools := sess.availableTools(1)
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

	if sess.closeAfterTurn {
		t.Fatal("closeAfterTurn should initially be false")
	}

	res, err := closeTool.Run(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("closeTool.Run failed: %v", err)
	}
	out, ok := res.(*agentkit.CloseSessionOutput)
	if !ok || out == nil || out.Status != "success" {
		t.Fatalf("expected *CloseSessionOutput with status 'success', got %T (%+v)", res, res)
	}

	sess.mu.RLock()
	closeMarked := sess.closeAfterTurn
	sess.mu.RUnlock()

	if !closeMarked {
		t.Fatal("expected closeAfterTurn to be set to true after executing close_session")
	}
}

func TestSession_ExecuteSnapshotTool_StaleGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-stale-test",
		generation: 2, // 当前代次是 2
	}

	// 传入代次 1 的快照工具
	tools := sess.buildToolSnapshot(ctx, 1)
	if len(tools) == 0 {
		t.Fatal("expected tools in snapshot")
	}

	_, err := tools[0].Run(ctx, nil)
	if err == nil {
		t.Fatal("expected error on generation mismatch, got nil")
	}
}

func TestSession_ExecuteSnapshotTool_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-cancel-test",
		generation: 1,
	}

	tools := sess.buildToolSnapshot(context.Background(), 1)
	if len(tools) == 0 {
		t.Fatal("expected tools in snapshot")
	}

	_, err := tools[0].Run(ctx, nil)
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func TestSession_DeviceToolCallLimit_8PerGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := &dummySender{}
	mcpClient := agentkit.NewDeviceMCPClient(sender)

	sender.onSend = func(c context.Context, payload json.RawMessage) error {
		var req struct {
			Id     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(payload, &req)
		switch req.Method {
		case "initialize":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`, req.Id)
			go mcpClient.HandlePayload(json.RawMessage(resp))
		case "tools/list":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"self.device.action","description":"action","inputSchema":{"type":"object"}}]}}`, req.Id)
			go mcpClient.HandlePayload(json.RawMessage(resp))
		case "tools/call":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}]}}`, req.Id)
			go mcpClient.HandlePayload(json.RawMessage(resp))
		}
		return nil
	}

	_ = mcpClient.Discover(ctx)

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-limit-test",
		generation: 1,
		mcpClient:  mcpClient,
	}

	snapshot := sess.buildToolSnapshot(ctx, 1)
	var deviceTool *ai.Tool
	for i := range snapshot {
		if snapshot[i].Name == "self.device.action" {
			deviceTool = &snapshot[i]
			break
		}
	}
	if deviceTool == nil {
		t.Fatal("expected self.device.action in snapshot")
	}

	// 连续调用 8 次，应该都成功
	for i := 0; i < 8; i++ {
		res, err := deviceTool.Run(ctx, nil)
		if err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}
		resMap, ok := res.(map[string]any)
		if !ok || resMap["isError"] == true {
			t.Fatalf("call %d returned error: %+v", i+1, res)
		}
	}

	// 第 9 次调用，应当返回限额结构化错误
	res9, err := deviceTool.Run(ctx, nil)
	if err != nil {
		t.Fatalf("call 9 returned unexpected Go error: %v", err)
	}
	res9Map, ok := res9.(map[string]any)
	if !ok || res9Map["isError"] != true {
		t.Fatalf("expected call 9 to return isError=true, got %+v", res9)
	}
}

func TestWithTools_AsSoleToolChannel_NoToolsInSystemPromptOrMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleanSysPrompt := "你是由智子科技研发的人工智能小助手，性格温和亲切。"
	sess := &Session{
		ctx:          ctx,
		cancel:       cancel,
		logger:       slog.Default(),
		sessionId:    "sess-channel-test",
		generation:   1,
		systemPrompt: cleanSysPrompt,
	}

	messages := sess.buildLLMMessages("现在几点啦")
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(messages))
	}
	if messages[0].Role != ai.RoleSystem || messages[0].Content != cleanSysPrompt {
		t.Fatalf("system message content modified: %q", messages[0].Content)
	}
	if strings.Contains(messages[0].Content, "get_current_time") || strings.Contains(messages[0].Content, "properties") {
		t.Fatal("messages system role must NOT contain any tool schemas")
	}

	tools := sess.availableTools(1)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools in availableTools, got %d", len(tools))
	}
}
