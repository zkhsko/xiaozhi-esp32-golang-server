package agentkit

import (
	"fmt"
	"strings"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// SessionCloser 是可选的工具结果接口，用于向会话层表明在当前轮次结束后应关闭会话。
type SessionCloser interface {
	ShouldCloseSession() bool
}

// DefaultTools 返回当前服务端支持并启用的默认内置 Agent 工具列表副本。
func DefaultTools() []ai.Tool {
	return []ai.Tool{
		GetCurrentTimeTool(),
		GetCloseSessionTool(),
	}
}

// ToolConfigItem 抽象单个内建工具的名称、配置与启用状态。
type ToolConfigItem struct {
	ToolName   string
	ToolConfig string
	Enabled    bool
}

// BuildTools 根据工具配置项列表构建所有启用的 AgentKit 工具，并与默认基础工具进行聚合。
func BuildTools(items []ToolConfigItem) []ai.Tool {
	baseTools := DefaultTools()
	if len(items) == 0 {
		return baseTools
	}

	customTools := make([]ai.Tool, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		tool, err := BuildTool(item.ToolName, item.ToolConfig)
		if err == nil {
			customTools = append(customTools, tool)
		}
	}

	return AggregateTools(baseTools, customTools)
}

// BuildTool 根据工具名称与 JSON 配置字符串构造对应的 AgentKit 内置工具。
func BuildTool(toolName, toolConfigJSON string) (ai.Tool, error) {
	switch strings.TrimSpace(toolName) {
	case ToolGetCurrentWeather:
		return NewWeatherToolFromConfig(toolConfigJSON)
	case ToolGetCurrentTime:
		return GetCurrentTimeTool(), nil
	case ToolCloseSession:
		return GetCloseSessionTool(), nil
	default:
		return ai.Tool{}, fmt.Errorf("unsupported agentkit tool: %s", toolName)
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
