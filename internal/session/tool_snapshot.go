package session

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

// 每 generation 设备工具调用的最大执行次数。
const (
	MaxGenerationDeviceToolCalls = 8
)

// buildToolSnapshot 构造当前 generation 专属的不可变工具快照副本。
// 该函数在每次触发 LLM 生成前被调用，完成以下关键聚合与防护步骤：
// 1. 若启用了设备 MCP，首轮等待设备工具发现完成（最多等待 5 秒，超时或失败则优雅降级为纯内置工具）；
// 2. 获取当前服务端支持的默认内置工具（如 server.get_current_time, server.close_session）；
// 3. 调用 agentkit.AggregateTools 执行工具去重聚合，并保持内置工具同名优先；
// 4. 遍历所有合并后的工具，为每一个工具动态绑定带有代次（generation）隔离与设备调用预算（单轮最多 8 次）的 Run 闭包；
// 5. 返回供 Genkit 统一编排与流式 Function Calling 使用的统一 []ai.Tool 快照。
func (s *Session) buildToolSnapshot(ctx context.Context, gen uint64) []ai.Tool {
	s.mu.RLock()
	client := s.mcpClient
	s.mu.RUnlock()

	// 1. 获取设备 MCP 工具快照
	var deviceTools []ai.Tool
	if client != nil && !client.IsDisabled() {
		// 首轮等待发现完成，最多等待 5 秒（若已发现则立即返回）
		waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
		_ = client.WaitReady(waitCtx)
		waitCancel()

		deviceTools = client.Tools()
	}

	// 2. 获取服务端内置工具定义
	builtinDefs := agentkit.DefaultTools()

	// 3. 执行工具列表聚合（内置工具同名优先）
	mergedDefs := agentkit.AggregateTools(builtinDefs, deviceTools)

	// 4. 为当前代次初始化设备工具调用计数器（单轮最多 8 次），并注入闭包
	var deviceCallCount atomic.Int32
	tools := make([]ai.Tool, 0, len(mergedDefs))

	for _, def := range mergedDefs {
		toolName := def.Name
		isBuiltin := agentkit.IsBuiltinTool(toolName)

		tools = append(tools, ai.Tool{
			Name:        toolName,
			Description: def.Description,
			Parameters:  def.Parameters,
			Run: func(toolCtx context.Context, input any) (any, error) {
				return s.executeSnapshotTool(toolCtx, gen, toolName, isBuiltin, input, &deviceCallCount)
			},
		})
	}

	return tools
}

// executeSnapshotTool 执行快照工具的 Run 闭包，校验代次并根据内置/设备工具分别路由：
// - 内置工具：直接在服务端当前进程执行，若为 close_session 则标记本轮结束后关闭连接；
// - 设备工具：受单代次最多 8 次调用预算约束，通过 DeviceMCPClient 经由 WebSocket 串行下发给硬件。
func (s *Session) executeSnapshotTool(
	ctx context.Context,
	gen uint64,
	name string,
	isBuiltin bool,
	input any,
	deviceCallCount *atomic.Int32,
) (any, error) {
	// 代次与生命周期防护：丢弃已取消上下文或迟到的旧代次工具调用
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Generation() > gen {
		return nil, errors.New("generation mismatch")
	}

	sessionId := s.SessionId()

	// 1. 服务端内置工具分发
	if isBuiltin {
		s.logger.Info("executing server tool call",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", name,
		)

		// 若用户意图为断开连接，标记在本次回答播报完成后关闭底层 WebSocket
		if name == agentkit.ToolCloseSession {
			s.mu.Lock()
			s.closeAfterTurn = true
			s.mu.Unlock()
		}

		result, err := agentkit.Execute(ctx, name, input)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return nil, err
			}
			s.logger.Warn("server tool call failed",
				"session_id", sessionId,
				"generation", gen,
				"tool_name", name,
				"error", err,
			)
			return map[string]any{
				"status":  "error",
				"message": err.Error(),
			}, nil
		}

		s.logger.Info("server tool call executed successfully",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", name,
		)
		return result, nil
	}

	// 2. 设备 MCP 工具分发
	s.mu.RLock()
	client := s.mcpClient
	s.mu.RUnlock()

	if client == nil || client.IsDisabled() {
		s.logger.Warn("device tool call rejected: mcp disabled or not configured",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", name,
		)
		return map[string]any{
			"isError": true,
			"content": []any{
				map[string]any{
					"type": "text",
					"text": fmt.Sprintf("tool %q is unavailable: device mcp disabled", name),
				},
			},
		}, nil
	}

	// 校验单 generation 设备工具调用预算（最多 8 次，防止大模型陷入死循环耗尽硬件资源）
	if deviceCallCount != nil && deviceCallCount.Add(1) > MaxGenerationDeviceToolCalls {
		s.logger.Warn("device tool call limit exceeded in current generation",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", name,
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

	s.logger.Info("executing device tool call",
		"session_id", sessionId,
		"generation", gen,
		"tool_name", name,
	)

	result, err := client.CallTool(ctx, name, input)
	if err != nil {
		// 会话关闭或上下文取消直接返回 Go error 中止生成循环
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, err
		}
		// 设备端业务错误以结构化错误回填给大模型，供大模型生成友好提示且不中断语音会话
		s.logger.Warn("device tool call failed with error",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", name,
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

	s.logger.Info("device tool call completed",
		"session_id", sessionId,
		"generation", gen,
		"tool_name", name,
	)

	return result, nil
}
