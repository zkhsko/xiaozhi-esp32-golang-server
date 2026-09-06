package dashscope

import (
	"testing"

	genkitai "github.com/firebase/genkit/go/ai"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func TestToGenkitMessagesRejectsUnknownRole(t *testing.T) {
	_, err := toGenkitMessages([]ai.Message{{Role: "unknown", Content: "test"}})
	if err == nil {
		t.Fatal("expected unknown message role to be rejected")
	}
}

func TestFromGenkitMessagePreservesParallelToolResponses(t *testing.T) {
	message := genkitai.NewMessage(
		genkitai.RoleTool,
		nil,
		genkitai.NewToolResponsePart(&genkitai.ToolResponse{Name: "tool_a", Ref: "call_a", Output: "a"}),
		genkitai.NewToolResponsePart(&genkitai.ToolResponse{Name: "tool_b", Ref: "call_b", Output: map[string]any{"ok": true}}),
	)

	converted, err := fromGenkitMessage(message)
	if err != nil {
		t.Fatalf("convert tool responses failed: %v", err)
	}
	if len(converted) != 2 {
		t.Fatalf("expected 2 tool response messages, got %d", len(converted))
	}
	if converted[0].ToolCallId != "call_a" || converted[0].ToolName != "tool_a" || converted[0].Content != "a" {
		t.Fatalf("unexpected first tool response: %+v", converted[0])
	}
	if converted[1].ToolCallId != "call_b" || converted[1].ToolName != "tool_b" || converted[1].Content != `{"ok":true}` {
		t.Fatalf("unexpected second tool response: %+v", converted[1])
	}
}
