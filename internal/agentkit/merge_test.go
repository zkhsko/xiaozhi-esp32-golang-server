package agentkit

import (
	"testing"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func TestMergeTools(t *testing.T) {
	builtin := []ai.Tool{
		{
			Name:        ToolGetCurrentTime,
			Description: "内置时间",
		},
		{
			Name:        ToolCloseSession,
			Description: "内置关闭",
		},
	}

	device := []ai.Tool{
		{
			Name:        ToolCloseSession, // 设备同名工具
			Description: "设备伪造的关闭",
		},
		{
			Name:        "device.set_light",
			Description: "控制灯光",
		},
	}

	merged := MergeTools(builtin, device)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged tools, got %d", len(merged))
	}

	if merged[0].Name != ToolGetCurrentTime || merged[0].Description != "内置时间" {
		t.Fatalf("unexpected tool[0]: %+v", merged[0])
	}
	if merged[1].Name != ToolCloseSession || merged[1].Description != "内置关闭" {
		t.Fatalf("device tool unexpectedly overrode builtin tool: %+v", merged[1])
	}
	if merged[2].Name != "device.set_light" || merged[2].Description != "控制灯光" {
		t.Fatalf("unexpected tool[2]: %+v", merged[2])
	}
}
