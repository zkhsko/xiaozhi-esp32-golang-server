package session

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

func TestSession_AvailableTools_BuiltinTools(t *testing.T) {
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
	_, err := sess.executeToolClosure(ctx, 1, agentkit.ToolGetCurrentTime, nil)
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

	_, err := sess.executeToolClosure(ctx, 1, agentkit.ToolGetCurrentTime, nil)
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
	}

	// 1. 验证 buildLLMMessages 生成的消息中，系统消息内容未被任何工具定义污染
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

	// 2. 验证工具描述唯一通过 availableTools 独立通道传递
	tools := sess.availableTools(1)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools in availableTools, got %d", len(tools))
	}
}
