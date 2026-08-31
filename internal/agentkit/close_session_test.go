package agentkit

import (
	"testing"
)

func TestGetCloseSessionTool(t *testing.T) {
	tool := GetCloseSessionTool()
	if tool.Name != ToolCloseSession {
		t.Fatalf("expected tool name %s, got %s", ToolCloseSession, tool.Name)
	}
	if tool.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if tool.Parameters == nil {
		t.Fatal("expected non-nil parameters")
	}
}

func TestParseCloseSessionInput(t *testing.T) {
	t.Run("Struct", func(t *testing.T) {
		in := ParseCloseSessionInput(CloseSessionInput{Reason: "r1"})
		if in.Reason != "r1" {
			t.Fatalf("expected 'r1', got %q", in.Reason)
		}
	})

	t.Run("StructPointer", func(t *testing.T) {
		in := ParseCloseSessionInput(&CloseSessionInput{Reason: "r2"})
		if in.Reason != "r2" {
			t.Fatalf("expected 'r2', got %q", in.Reason)
		}
	})

	t.Run("StructPointerNil", func(t *testing.T) {
		var p *CloseSessionInput
		in := ParseCloseSessionInput(p)
		if in.Reason != "" {
			t.Fatalf("expected empty reason, got %q", in.Reason)
		}
	})

	t.Run("Map", func(t *testing.T) {
		in := ParseCloseSessionInput(map[string]any{"reason": "r3"})
		if in.Reason != "r3" {
			t.Fatalf("expected 'r3', got %q", in.Reason)
		}
	})

	t.Run("JSONString", func(t *testing.T) {
		in := ParseCloseSessionInput(`{"reason":"r4"}`)
		if in.Reason != "r4" {
			t.Fatalf("expected 'r4', got %q", in.Reason)
		}
	})

	t.Run("Nil", func(t *testing.T) {
		in := ParseCloseSessionInput(nil)
		if in.Reason != "" {
			t.Fatalf("expected empty reason, got %q", in.Reason)
		}
	})
}

func TestExecuteCloseSession(t *testing.T) {
	in := CloseSessionInput{Reason: "用户想要休息"}
	out, err := ExecuteCloseSession(in)
	if err != nil {
		t.Fatalf("ExecuteCloseSession failed: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Status != "success" {
		t.Fatalf("expected status 'success', got %v", out.Status)
	}
	if out.Message == "" {
		t.Fatal("expected non-empty message in CloseSessionOutput")
	}
}
