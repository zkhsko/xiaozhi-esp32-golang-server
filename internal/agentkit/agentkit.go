package agentkit

import (
	"context"
	"errors"
	"fmt"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// 哨兵错误。
var (
	ErrToolNotFound = errors.New("agent tool not found")
)

// IsBuiltinTool 检查指定名称是否为启用的内置 Agent 工具。
func IsBuiltinTool(name string) bool {
	return name == ToolGetCurrentTime || name == ToolCloseSession
}

// DefaultTools 返回当前服务端支持并启用的默认内置 Agent 工具列表副本。
func DefaultTools() []ai.Tool {
	return []ai.Tool{
		GetCurrentTimeTool(),
		GetCloseSessionTool(),
	}
}

// AggregateTools 聚合服务端内置工具与设备 MCP 动态工具快照：
// 1. 优先保留所有服务端内置工具（内置工具同名优先，防止设备端重定义覆盖关键系统行为）；
// 2. 追加设备端通过 MCP 发现上报的非重复工具；
// 3. 返回合并后的纯净工具定义列表，供上层会话进一步包装代次隔离与预算控制。
func AggregateTools(builtinTools []ai.Tool, deviceTools []ai.Tool) []ai.Tool {
	result := make([]ai.Tool, 0, len(builtinTools)+len(deviceTools))
	seenNames := make(map[string]bool, len(builtinTools)+len(deviceTools))

	// 1. 优先放入服务端内置工具
	for _, tool := range builtinTools {
		if tool.Name != "" && !seenNames[tool.Name] {
			seenNames[tool.Name] = true
			result = append(result, tool)
		}
	}

	// 2. 追加设备动态工具（若与内置工具同名则被 seenNames 过滤）
	for _, tool := range deviceTools {
		if tool.Name != "" && !seenNames[tool.Name] {
			seenNames[tool.Name] = true
			result = append(result, tool)
		}
	}

	return result
}

// Execute 执行指定名称的内置 Agent 工具，返回结构化结果。
func Execute(ctx context.Context, name string, input any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	switch name {
	case ToolGetCurrentTime:
		return ExecuteGetCurrentTime()
	case ToolCloseSession:
		return ExecuteCloseSession(ParseCloseSessionInput(input))
	default:
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}
}
