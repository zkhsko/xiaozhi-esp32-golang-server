package agentkit

import (
	"testing"
)

func TestGetCurrentTimeTool(t *testing.T) {
	tool := GetCurrentTimeTool()
	if tool.Name != ToolGetCurrentTime {
		t.Fatalf("expected tool name %s, got %s", ToolGetCurrentTime, tool.Name)
	}
	if tool.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if tool.Parameters == nil {
		t.Fatal("expected non-nil parameters")
	}
}

func TestExecuteGetCurrentTime(t *testing.T) {
	out, err := ExecuteGetCurrentTime()
	if err != nil {
		t.Fatalf("ExecuteGetCurrentTime failed: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.DateTime == "" || out.Date == "" || out.Time == "" || out.Weekday == "" || out.Timezone == "" || out.UTCOffset == "" {
		t.Fatalf("expected non-empty fields in GetCurrentTimeOutput: %+v", out)
	}
}
