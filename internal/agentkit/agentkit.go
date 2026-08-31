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
