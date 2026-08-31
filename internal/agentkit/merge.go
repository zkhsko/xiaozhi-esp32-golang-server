package agentkit

import "xiaozhi-esp32-golang-server/internal/ai"

// MergeTools 将内置工具与设备 MCP 工具合并。
// 合并规则：
// 1. 内置工具优先。
// 2. 设备同名工具不得覆盖内置工具。
// 3. 每个名称只暴露一次。
func MergeTools(builtinTools []ai.Tool, deviceTools []ai.Tool) []ai.Tool {
	totalLen := len(builtinTools) + len(deviceTools)
	seen := make(map[string]struct{}, totalLen)
	merged := make([]ai.Tool, 0, totalLen)

	// 1. 内置工具优先
	for _, tool := range builtinTools {
		if _, exists := seen[tool.Name]; !exists {
			seen[tool.Name] = struct{}{}
			merged = append(merged, tool)
		}
	}

	// 2. 设备工具追加，同名忽略
	for _, tool := range deviceTools {
		if _, exists := seen[tool.Name]; !exists {
			seen[tool.Name] = struct{}{}
			merged = append(merged, tool)
		}
	}

	return merged
}
