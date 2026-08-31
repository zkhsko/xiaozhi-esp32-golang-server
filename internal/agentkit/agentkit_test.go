package agentkit

import (
	"context"
	"testing"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func TestDefaultTools(t *testing.T) {
	tools := DefaultTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 default tools, got %d", len(tools))
	}

	toolMap := make(map[string]ai.Tool)
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	timeTool, exists := toolMap[ToolGetCurrentTime]
	if !exists {
		t.Fatalf("expected tool %s to exist", ToolGetCurrentTime)
	}
	if timeTool.Description == "" || timeTool.Parameters == nil {
		t.Fatal("expected valid description and parameters for get_current_time tool")
	}

	closeTool, exists := toolMap[ToolCloseSession]
	if !exists {
		t.Fatalf("expected tool %s to exist", ToolCloseSession)
	}
	if closeTool.Description == "" || closeTool.Parameters == nil {
		t.Fatal("expected valid description and parameters for close_session tool")
	}
}

func TestIsBuiltinTool(t *testing.T) {
	if !IsBuiltinTool(ToolGetCurrentTime) {
		t.Fatalf("expected %s to be builtin tool", ToolGetCurrentTime)
	}
	if !IsBuiltinTool(ToolCloseSession) {
		t.Fatalf("expected %s to be builtin tool", ToolCloseSession)
	}
	if IsBuiltinTool("server.unknown_tool") {
		t.Fatal("expected unknown tool not to be builtin tool")
	}
}

func TestExecute(t *testing.T) {
	ctx := context.Background()

	// 1. 测试 ToolGetCurrentTime
	timeRes, err := Execute(ctx, ToolGetCurrentTime, nil)
	if err != nil {
		t.Fatalf("Execute get_current_time failed: %v", err)
	}
	timeOut, ok := timeRes.(*GetCurrentTimeOutput)
	if !ok || timeOut.DateTime == "" {
		t.Fatalf("expected valid *GetCurrentTimeOutput, got %+v", timeRes)
	}

	// 2. 测试 ToolCloseSession
	closeRes, err := Execute(ctx, ToolCloseSession, map[string]any{"reason": "退出"})
	if err != nil {
		t.Fatalf("Execute close_session failed: %v", err)
	}
	closeOut, ok := closeRes.(*CloseSessionOutput)
	if !ok || closeOut.Status != "success" {
		t.Fatalf("expected valid *CloseSessionOutput, got %+v", closeRes)
	}

	// 3. 测试未定义工具
	_, err = Execute(ctx, "non_existent_tool", nil)
	if err == nil {
		t.Fatal("expected error for non existent tool, got nil")
	}

	// 4. 测试 Context Canceled
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Execute(canceledCtx, ToolGetCurrentTime, nil)
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
}
