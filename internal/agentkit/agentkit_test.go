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
	if timeTool.Description == "" || timeTool.Parameters == nil || timeTool.Run == nil {
		t.Fatal("expected valid description, parameters and Run handler for get_current_time tool")
	}

	closeTool, exists := toolMap[ToolCloseSession]
	if !exists {
		t.Fatalf("expected tool %s to exist", ToolCloseSession)
	}
	if closeTool.Description == "" || closeTool.Parameters == nil || closeTool.Run == nil {
		t.Fatal("expected valid description, parameters and Run handler for close_session tool")
	}
}

func TestAggregateTools(t *testing.T) {
	builtin := []ai.Tool{
		{Name: "tool.same", Description: "builtin version"},
		{Name: "tool.builtin_only", Description: "builtin"},
	}
	device := []ai.Tool{
		{Name: "tool.same", Description: "device duplicate"},
		{Name: "tool.device_only", Description: "device"},
	}

	merged := AggregateTools(builtin, device)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged tools, got %d", len(merged))
	}

	if merged[0].Name != "tool.same" || merged[0].Description != "builtin version" {
		t.Fatalf("expected builtin same tool to take precedence, got %+v", merged[0])
	}
	if merged[1].Name != "tool.builtin_only" {
		t.Fatalf("expected tool.builtin_only at idx 1, got %+v", merged[1])
	}
	if merged[2].Name != "tool.device_only" {
		t.Fatalf("expected tool.device_only at idx 2, got %+v", merged[2])
	}
}

func TestCloseSessionOutput_SessionCloser(t *testing.T) {
	out := &CloseSessionOutput{Status: "success"}
	var closer SessionCloser = out
	if !closer.ShouldCloseSession() {
		t.Fatal("expected ShouldCloseSession to return true for success status")
	}

	failedOut := &CloseSessionOutput{Status: "failed"}
	if failedOut.ShouldCloseSession() {
		t.Fatal("expected ShouldCloseSession to return false for failed status")
	}

	var nilOut *CloseSessionOutput
	if nilOut.ShouldCloseSession() {
		t.Fatal("expected ShouldCloseSession to return false for nil pointer")
	}
}

func TestToolRun_DirectExecution(t *testing.T) {
	ctx := context.Background()

	// 1. 测试 GetCurrentTimeTool 的 Run
	timeTool := GetCurrentTimeTool()
	timeRes, err := timeTool.Run(ctx, nil)
	if err != nil {
		t.Fatalf("timeTool.Run failed: %v", err)
	}
	timeOut, ok := timeRes.(*GetCurrentTimeOutput)
	if !ok || timeOut.DateTime == "" {
		t.Fatalf("expected valid *GetCurrentTimeOutput, got %+v", timeRes)
	}

	// 2. 测试 GetCloseSessionTool 的 Run
	closeTool := GetCloseSessionTool()
	closeRes, err := closeTool.Run(ctx, map[string]any{"reason": "退出"})
	if err != nil {
		t.Fatalf("closeTool.Run failed: %v", err)
	}
	closeOut, ok := closeRes.(*CloseSessionOutput)
	if !ok || closeOut.Status != "success" {
		t.Fatalf("expected valid *CloseSessionOutput, got %+v", closeRes)
	}

	// 3. 测试 Context 取消
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = timeTool.Run(canceledCtx, nil)
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
}
