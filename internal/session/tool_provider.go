package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

// 每轮次设备工具调用的最大执行次数。
const (
	MaxGenerationDeviceToolCalls = 8
)

// TurnEffects 记录单轮问答中工具执行所产生的副作用。
type TurnEffects struct {
	CloseSession bool
}

// ToolProvider 负责为单轮问答聚合服务端内置工具与设备 MCP 动态工具，并绑定代次隔离与副作用记录。
type ToolProvider struct {
	mcpBridge *MCPBridge
	logger    *slog.Logger
}

// NewToolProvider 创建配置就绪的工具提供器。
func NewToolProvider(bridge *MCPBridge, l *slog.Logger) *ToolProvider {
	if l == nil {
		l = slog.Default()
	}
	return &ToolProvider{
		mcpBridge: bridge,
		logger:    l,
	}
}

// BuildSnapshot 构造当前代次专属的不可变工具快照副本。
func (p *ToolProvider) BuildSnapshot(ctx context.Context, turnId uint64, sessionId string, effects *TurnEffects) []ai.Tool {
	var deviceTools []ai.Tool
	if p.mcpBridge != nil && p.mcpBridge.IsEnabled() {
		// 首轮等待发现完成，最多等待 5 秒
		waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
		_ = p.mcpBridge.WaitReady(waitCtx)
		waitCancel()

		deviceTools = p.mcpBridge.Tools()
	}

	builtinDefs := agentkit.DefaultTools()
	mergedDefs := agentkit.AggregateTools(builtinDefs, deviceTools)

	var deviceCallCount atomic.Int32
	tools := make([]ai.Tool, 0, len(mergedDefs))

	for _, def := range mergedDefs {
		toolDef := def
		toolName := toolDef.Name
		isBuiltin := agentkit.IsBuiltinTool(toolName)

		tools = append(tools, ai.Tool{
			Name:        toolName,
			Description: toolDef.Description,
			Parameters:  toolDef.Parameters,
			Run: func(toolCtx context.Context, input any) (any, error) {
				return p.executeTool(toolCtx, turnId, sessionId, toolDef, isBuiltin, input, effects, &deviceCallCount)
			},
		})
	}

	return tools
}

// executeTool 执行单次工具调用。
func (p *ToolProvider) executeTool(
	ctx context.Context,
	turnId uint64,
	sessionId string,
	toolDef ai.Tool,
	isBuiltin bool,
	input any,
	effects *TurnEffects,
	deviceCallCount *atomic.Int32,
) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if isBuiltin {
		p.logger.Info("executing server tool call",
			"session_id", sessionId,
			"turn_id", turnId,
			"tool_name", toolDef.Name,
		)

		var result any
		var err error
		if toolDef.Run != nil {
			result, err = toolDef.Run(ctx, input)
		} else {
			result, err = agentkit.Execute(ctx, toolDef.Name, input)
		}

		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return nil, err
			}
			p.logger.Warn("server tool call failed",
				"session_id", sessionId,
				"turn_id", turnId,
				"tool_name", toolDef.Name,
				"error", err,
			)
			return map[string]any{
				"status":  "error",
				"message": err.Error(),
			}, nil
		}

		// 若工具为 close_session 且执行成功，在当前轮次副作用中记录关闭意图
		if toolDef.Name == agentkit.ToolCloseSession && effects != nil {
			effects.CloseSession = true
		}

		p.logger.Info("server tool call executed successfully",
			"session_id", sessionId,
			"turn_id", turnId,
			"tool_name", toolDef.Name,
		)
		return result, nil
	}

	// 设备 MCP 工具调用
	if p.mcpBridge == nil || !p.mcpBridge.IsEnabled() {
		p.logger.Warn("device tool call rejected: mcp disabled or not configured",
			"session_id", sessionId,
			"turn_id", turnId,
			"tool_name", toolDef.Name,
		)
		return map[string]any{
			"isError": true,
			"content": []any{
				map[string]any{
					"type": "text",
					"text": fmt.Sprintf("tool %q is unavailable: device mcp disabled", toolDef.Name),
				},
			},
		}, nil
	}

	if deviceCallCount != nil && deviceCallCount.Add(1) > MaxGenerationDeviceToolCalls {
		p.logger.Warn("device tool call limit exceeded in current generation",
			"session_id", sessionId,
			"turn_id", turnId,
			"tool_name", toolDef.Name,
			"limit", MaxGenerationDeviceToolCalls,
		)
		return map[string]any{
			"isError": true,
			"content": []any{
				map[string]any{
					"type": "text",
					"text": fmt.Sprintf("device tool call limit exceeded in current turn (max %d)", MaxGenerationDeviceToolCalls),
				},
			},
		}, nil
	}

	p.logger.Info("executing device tool call",
		"session_id", sessionId,
		"turn_id", turnId,
		"tool_name", toolDef.Name,
	)

	result, err := p.mcpBridge.CallTool(ctx, toolDef.Name, input)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, err
		}
		p.logger.Warn("device tool call failed with error",
			"session_id", sessionId,
			"turn_id", turnId,
			"tool_name", toolDef.Name,
			"error", err,
		)
		return map[string]any{
			"isError": true,
			"content": []any{
				map[string]any{
					"type": "text",
					"text": err.Error(),
				},
			},
		}, nil
	}

	return result, nil
}
