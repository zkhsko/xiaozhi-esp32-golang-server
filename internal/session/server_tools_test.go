package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func TestDefaultServerTools(t *testing.T) {
	tools := DefaultServerTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 default server tools, got %d", len(tools))
	}

	toolMap := make(map[string]ai.Tool)
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	timeTool, exists := toolMap[ServerToolGetCurrentTime]
	if !exists {
		t.Fatalf("expected tool %s to exist", ServerToolGetCurrentTime)
	}
	if timeTool.Description == "" {
		t.Fatal("expected non-empty description for get_current_time tool")
	}
	if timeTool.Parameters == nil {
		t.Fatal("expected non-nil parameters for get_current_time tool")
	}

	closeTool, exists := toolMap[ServerToolCloseSession]
	if !exists {
		t.Fatalf("expected tool %s to exist", ServerToolCloseSession)
	}
	if closeTool.Description == "" {
		t.Fatal("expected non-empty description for close_session tool")
	}
	if closeTool.Parameters == nil {
		t.Fatal("expected non-nil parameters for close_session tool")
	}
}

func TestExecuteServerTool_GetCurrentTime(t *testing.T) {
	ctx := context.Background()
	result, err := executeServerTool(ctx, ServerToolGetCurrentTime, nil)
	if err != nil {
		t.Fatalf("executeServerTool failed: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result), &data); err != nil {
		t.Fatalf("failed to unmarshal get_current_time result: %v", err)
	}

	expectedKeys := []string{"datetime", "date", "time", "weekday", "timezone", "utc_offset"}
	for _, key := range expectedKeys {
		if val, ok := data[key]; !ok || val == "" {
			t.Fatalf("expected non-empty field %q in get_current_time result", key)
		}
	}
}

func TestExecuteServerTool_CloseSession(t *testing.T) {
	ctx := context.Background()
	result, err := executeServerTool(ctx, ServerToolCloseSession, nil)
	if err != nil {
		t.Fatalf("executeServerTool failed: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(result), &data); err != nil {
		t.Fatalf("failed to unmarshal close_session result: %v", err)
	}

	if data["status"] != "success" {
		t.Fatalf("expected status 'success', got %v", data["status"])
	}
}

func TestExecuteServerTool_NotFound(t *testing.T) {
	ctx := context.Background()
	_, err := executeServerTool(ctx, "non_existent_tool", nil)
	if err == nil {
		t.Fatal("expected error for non existent tool, got nil")
	}
}

func TestSession_AvailableTools_ServerPriorityAndDedup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-tools-test",
		generation: 1,
		mcpTools: []ai.Tool{
			{
				Name:        ServerToolCloseSession, // 与服务端工具重名，应被服务端工具遮蔽
				Description: "设备端伪造的 close_session",
			},
			{
				Name:        "device.set_volume",
				Description: "调节设备音量",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"volume": map[string]any{"type": "integer"},
					},
				},
			},
		},
	}

	tools := sess.availableTools(1)

	// 预期包含：server.get_current_time, server.close_session, device.set_volume
	if len(tools) != 3 {
		t.Fatalf("expected 3 available tools, got %d", len(tools))
	}

	var hasTime, hasClose, hasVolume bool
	for _, tool := range tools {
		switch tool.Name {
		case ServerToolGetCurrentTime:
			hasTime = true
		case ServerToolCloseSession:
			hasClose = true
			if tool.Description == "设备端伪造的 close_session" {
				t.Fatal("device tool must not override server tool definition")
			}
		case "device.set_volume":
			hasVolume = true
		default:
			t.Fatalf("unexpected tool name %s", tool.Name)
		}
	}

	if !hasTime || !hasClose || !hasVolume {
		t.Fatalf("missing expected tools: hasTime=%v, hasClose=%v, hasVolume=%v", hasTime, hasClose, hasVolume)
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
		if tools[i].Name == ServerToolCloseSession {
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
	if res == nil {
		t.Fatal("expected non-nil result from closeTool.Run")
	}

	sess.mu.RLock()
	closeMarked := sess.closeAfterTurn
	sess.mu.RUnlock()

	if !closeMarked {
		t.Fatal("expected closeAfterTurn to be set to true after executing close_session")
	}
}

func TestSession_ExecuteToolClosure_StaleGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-stale-test",
		generation: 2, // 当前代次是 2
	}

	// 传入代次 1 的请求
	_, err := sess.executeToolClosure(ctx, 1, ServerToolGetCurrentTime, nil)
	if err == nil {
		t.Fatal("expected error on generation mismatch, got nil")
	}
}

func TestSession_ExecuteToolClosure_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-cancel-test",
		generation: 1,
	}

	_, err := sess.executeToolClosure(ctx, 1, ServerToolGetCurrentTime, nil)
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func TestSession_ExecuteToolClosure_UnauthorizedTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-unauth-test",
		generation: 1,
	}

	res, err := sess.executeToolClosure(ctx, 1, "unauthorized.custom_tool", nil)
	if err != nil {
		t.Fatalf("expected nil error (graceful rejection map), got %v", err)
	}

	resMap, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any response, got %T", res)
	}
	if resMap["status"] != "error" {
		t.Fatalf("expected status 'error', got %v", resMap["status"])
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
		mcpTools: []ai.Tool{
			{
				Name:        "device.set_light",
				Description: "控制设备灯光开关与颜色",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"state": map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	// 1. 验证 buildLLMMessages 生成的消息中，系统消息内容未被任何工具定义污染
	messages := sess.buildLLMMessages("现在几点啦")
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(messages))
	}
	if messages[0].Role != ai.RoleSystem || messages[0].Content != cleanSysPrompt {
		t.Fatalf("system message content modified: %q", messages[0].Content)
	}
	if strings.Contains(messages[0].Content, "get_current_time") || strings.Contains(messages[0].Content, "properties") || strings.Contains(messages[0].Content, "device.set_light") {
		t.Fatal("messages system role must NOT contain any tool schemas")
	}

	// 2. 验证工具描述唯一通过 availableTools 独立通道传递
	tools := sess.availableTools(1)
	if len(tools) != 3 { // 2 server tools + 1 mcp tool
		t.Fatalf("expected 3 tools in availableTools, got %d", len(tools))
	}
}
