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
	"xiaozhi-esp32-golang-server/internal/database"
)

// 每轮次设备工具调用的最大执行次数。
const (
	MaxGenerationDeviceToolCalls = 8
)

// TurnEffects 记录单轮问答中工具执行所产生的副作用。
type TurnEffects struct {
	CloseSession bool
}

// AgentKitStore 定义 ToolProvider 加载内建工具配置所需的窄接口。
type AgentKitStore interface {
	ListEnabledAgentKitConfigs(ctx context.Context) ([]*database.AgentKitConfig, error)
}

// ToolProvider 负责为单轮问答聚合服务端内置工具与设备 MCP 动态工具，并绑定代次隔离与副作用记录。
type ToolProvider struct {
	agentKitStore AgentKitStore
	mcpBridge     *MCPBridge
	logger        *slog.Logger
}

// NewToolProvider 创建配置就绪的工具提供器。
func NewToolProvider(bridge *MCPBridge, store AgentKitStore, l *slog.Logger) *ToolProvider {
	if l == nil {
		l = slog.Default()
	}
	return &ToolProvider{
		agentKitStore: store,
		mcpBridge:     bridge,
		logger:        l,
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

	builtinTools := p.loadBuiltinTools(ctx)
	mergedTools := agentkit.AggregateTools(builtinTools, deviceTools)

	builtinMap := make(map[string]bool, len(builtinTools))
	for _, t := range builtinTools {
		builtinMap[t.Name] = true
	}

	var deviceCallCount atomic.Int32
	tools := make([]ai.Tool, 0, len(mergedTools))

	for _, tool := range mergedTools {
		rawTool := tool
		isDevice := !builtinMap[rawTool.Name]

		tools = append(tools, ai.Tool{
			Name:        rawTool.Name,
			Description: rawTool.Description,
			Parameters:  rawTool.Parameters,
			Run:         p.wrapToolRun(sessionId, turnId, rawTool, isDevice, &deviceCallCount, effects),
		})
	}

	return tools
}

// wrapToolRun 包装底层工具的唯一 Run 实现，负责代次上下文隔离、设备预算限制、日志打点与副作用提取。
func (p *ToolProvider) wrapToolRun(
	sessionId string,
	turnId uint64,
	rawTool ai.Tool,
	isDevice bool,
	deviceCallCount *atomic.Int32,
	effects *TurnEffects,
) ai.ToolFunc {
	return func(ctx context.Context, input any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if rawTool.Run == nil {
			return nil, fmt.Errorf("tool %s has no run handler", rawTool.Name)
		}

		// 限制每轮大模型调用设备工具的最大次数，防止死循环冲垮 MCU
		if isDevice && deviceCallCount != nil && deviceCallCount.Add(1) > MaxGenerationDeviceToolCalls {
			p.logger.Warn("device tool call limit exceeded in current generation",
				"session_id", sessionId,
				"turn_id", turnId,
				"tool_name", rawTool.Name,
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

		toolType := "server"
		if isDevice {
			toolType = "device"
		}

		p.logger.Info("executing "+toolType+" tool call",
			"session_id", sessionId,
			"turn_id", turnId,
			"tool_name", rawTool.Name,
		)

		result, err := rawTool.Run(ctx, input)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return nil, err
			}

			p.logger.Warn(toolType+" tool call failed",
				"session_id", sessionId,
				"turn_id", turnId,
				"tool_name", rawTool.Name,
				"error", err,
			)

			if isDevice {
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

			return map[string]any{
				"status":  "error",
				"message": err.Error(),
			}, nil
		}

		// 检查工具返回值是否声明了会话关闭意图
		if closer, ok := result.(agentkit.SessionCloser); ok && closer.ShouldCloseSession() {
			if effects != nil {
				effects.CloseSession = true
			}
		}

		p.logger.Info(toolType+" tool call executed successfully",
			"session_id", sessionId,
			"turn_id", turnId,
			"tool_name", rawTool.Name,
		)

		return result, nil
	}
}

// loadBuiltinTools 从 AgentKitStore 加载启用的内建工具配置并组装内置工具列表。
func (p *ToolProvider) loadBuiltinTools(ctx context.Context) []ai.Tool {
	if p.agentKitStore == nil {
		return agentkit.DefaultTools()
	}

	configs, err := p.agentKitStore.ListEnabledAgentKitConfigs(ctx)
	if err != nil || len(configs) == 0 {
		if err != nil {
			p.logger.Warn("failed to load enabled agentkit configs from store", "error", err)
		}
		return agentkit.DefaultTools()
	}

	items := make([]agentkit.ToolConfigItem, 0, len(configs))
	for _, cfg := range configs {
		if cfg != nil {
			items = append(items, agentkit.ToolConfigItem{
				ToolName:   cfg.ToolName,
				ToolConfig: cfg.ToolConfig,
				Enabled:    cfg.Enabled,
			})
		}
	}

	return agentkit.BuildTools(items)
}
