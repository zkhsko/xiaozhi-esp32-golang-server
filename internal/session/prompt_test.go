package session

import (
	"testing"
)

func TestRenderPrompt(t *testing.T) {
	values := map[string]string{
		"name": "张三",
		"city": "上海",
		"tools": `[
  {
    "name": "self.lamp.turn_on",
    "description": "打开台灯",
    "inputSchema": {}
  }
]`,
	}

	tests := []struct {
		name     string
		template string
		values   map[string]string
		expected string
	}{
		{
			name:     "tools placeholder",
			template: "你是一个智能管家。\n【可用工具】\n${tools}\n请根据指令调用。",
			values:   values,
			expected: "你是一个智能管家。\n【可用工具】\n" + values["tools"] + "\n请根据指令调用。",
		},
		{
			name:     "multiple variables",
			template: "你好，${name}！欢迎来到 $city。",
			values:   values,
			expected: "你好，张三！欢迎来到 上海。",
		},
		{
			name:     "no placeholder in prompt",
			template: "你是一个普通助手，没有占位符",
			values:   values,
			expected: "你是一个普通助手，没有占位符",
		},
		{
			name:     "undefined variable expands to empty string",
			template: "姓名: ${name}, 未知: [${unknown_var}]",
			values:   values,
			expected: "姓名: 张三, 未知: []",
		},
		{
			name:     "empty template",
			template: "",
			values:   values,
			expected: "",
		},
		{
			name:     "nil map",
			template: "你好 ${name}",
			values:   nil,
			expected: "你好 ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := RenderPrompt(tc.template, tc.values)
			if result != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}
