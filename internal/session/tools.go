package session

import (
	"context"
	"errors"
	"fmt"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

// availableTools 返回向大语言模型提供的服务端内置工具列表。
// 每个工具附加闭包 Run 函数供 LLM 客户端执行。
func (s *Session) availableTools(gen uint64) []ai.Tool {
	builtinDefs := agentkit.DefaultTools()
	tools := make([]ai.Tool, 0, len(builtinDefs))

	for _, def := range builtinDefs {
		toolName := def.Name
		tools = append(tools, ai.Tool{
			Name:        toolName,
			Description: def.Description,
			Parameters:  def.Parameters,
			Run: func(ctx context.Context, input any) (any, error) {
				return s.executeToolClosure(ctx, gen, toolName, input)
			},
		})
	}

	return tools
}

// executeToolClosure 统一调度并执行大模型调用的工具闭包（服务端内置工具）。
func (s *Session) executeToolClosure(ctx context.Context, gen uint64, name string, input any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Generation() > gen {
		return nil, errors.New("generation mismatch")
	}

	sessionId := s.SessionId()

	// 1. 内置 Agent 工具直接在服务端执行
	if agentkit.IsBuiltinTool(name) {
		s.logger.Info("executing server tool call",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", name,
		)

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

	// 2. 未启用或未授权的工具
	s.logger.Warn("tool call rejected: tool not authorized in session",
		"session_id", sessionId,
		"generation", gen,
		"tool_name", name,
	)
	return map[string]any{
		"status":  "error",
		"message": fmt.Sprintf("tool %q is not authorized in current session", name),
	}, nil
}
