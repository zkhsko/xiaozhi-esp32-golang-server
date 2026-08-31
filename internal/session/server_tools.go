package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// 服务端工具名称常量。
const (
	ServerToolGetCurrentTime = "server.get_current_time"
	ServerToolCloseSession   = "server.close_session"
)

// 哨兵错误。
var (
	ErrServerToolNotFound = errors.New("server tool not found")
)

// DefaultServerTools 返回当前服务端支持并启用的默认服务端工具列表副本。
func DefaultServerTools() []ai.Tool {
	return []ai.Tool{
		{
			Name:        ServerToolGetCurrentTime,
			Description: "获取服务端当前的日期、时间、星期和时区信息。当用户询问现在几点、今天几号、星期几、当前时间日期等问题时调用此工具",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        ServerToolCloseSession,
			Description: "关闭当前会话并断开连接。当用户表达想要结束对话、退下、退下吧、去睡觉、去睡吧、睡觉了、晚安、不聊了、再见、拜拜、断开连接、退出、闭嘴、不要再说了、结束对话或不再需要交互时必须立即调用此工具",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "关闭会话的原因（可选）",
					},
				},
			},
		},
	}
}

// GetCurrentTimeInput 定义获取当前时间工具的入参。
type GetCurrentTimeInput struct{}

// GetCurrentTimeOutput 定义获取当前时间工具的结构化返回值。
type GetCurrentTimeOutput struct {
	DateTime  string `json:"datetime"`
	Date      string `json:"date"`
	Time      string `json:"time"`
	Weekday   string `json:"weekday"`
	Timezone  string `json:"timezone"`
	UTCOffset string `json:"utc_offset"`
}

// CloseSessionInput 定义关闭会话工具的入参。
type CloseSessionInput struct {
	Reason string `json:"reason,omitempty" jsonschema:"description=关闭会话的原因（可选）"`
}

// CloseSessionOutput 定义关闭会话工具的结构化返回值。
type CloseSessionOutput struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// isServerTool 检查指定名称是否为启用的服务端工具。
func isServerTool(name string) bool {
	return name == ServerToolGetCurrentTime ||
		name == ServerToolCloseSession
}

// parseCloseSessionInput 将入参解析为 CloseSessionInput。
func parseCloseSessionInput(input any) CloseSessionInput {
	var in CloseSessionInput
	if input == nil {
		return in
	}
	switch v := input.(type) {
	case CloseSessionInput:
		return v
	case *CloseSessionInput:
		if v != nil {
			return *v
		}
	case map[string]any:
		if r, ok := v["reason"].(string); ok {
			in.Reason = r
		}
	case string:
		if v != "" {
			_ = json.Unmarshal([]byte(v), &in)
		}
	}
	return in
}

// executeServerTool 执行指定名称的服务端工具，返回结构化结果。
func executeServerTool(ctx context.Context, name string, input any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	switch name {
	case ServerToolGetCurrentTime:
		return executeGetCurrentTime()
	case ServerToolCloseSession:
		return executeCloseSession(parseCloseSessionInput(input))
	default:
		return nil, fmt.Errorf("%w: %s", ErrServerToolNotFound, name)
	}
}

// executeCloseSession 执行会话关闭工具逻辑，返回结构化确认对象。
func executeCloseSession(_ CloseSessionInput) (*CloseSessionOutput, error) {
	return &CloseSessionOutput{
		Status:  "success",
		Message: "session will be closed after this turn. You must now say a short goodbye to the user.",
	}, nil
}

// executeGetCurrentTime 获取服务端系统当前日期、时间、星期、时区及 UTC 偏移，返回结构化对象。
func executeGetCurrentTime() (*GetCurrentTimeOutput, error) {
	now := time.Now()
	zoneName, _ := now.Zone()
	return &GetCurrentTimeOutput{
		DateTime:  now.Format("2006-01-02 15:04:05"),
		Date:      now.Format("2006-01-02"),
		Time:      now.Format("15:04:05"),
		Weekday:   now.Weekday().String(),
		Timezone:  zoneName,
		UTCOffset: now.Format("-07:00"),
	}, nil
}

// availableTools 返回向大语言模型提供的完整可执行工具列表（服务端工具 + 设备 MCP 工具）。
// 合并规则：
// 1. 服务端工具优先。
// 2. 同名工具只向大模型提供一次。
// 3. 设备不得覆盖同名服务端工具。
// 4. 每个工具附加闭包 Run 函数供 Genkit 执行。
func (s *Session) availableTools(gen uint64) []ai.Tool {
	serverDefs := DefaultServerTools()

	s.mu.RLock()
	deviceTools := s.mcpTools
	s.mu.RUnlock()

	totalLen := len(serverDefs) + len(deviceTools)
	seen := make(map[string]struct{}, totalLen)
	merged := make([]ai.Tool, 0, totalLen)

	// 1. 服务端工具优先
	for _, def := range serverDefs {
		toolName := def.Name
		seen[toolName] = struct{}{}
		merged = append(merged, ai.Tool{
			Name:        toolName,
			Description: def.Description,
			Parameters:  def.Parameters,
			Run: func(ctx context.Context, input any) (any, error) {
				return s.executeToolClosure(ctx, gen, toolName, input)
			},
		})
	}

	// 2. 设备 MCP 工具追加，若与服务端工具同名则忽略
	for _, def := range deviceTools {
		toolName := def.Name
		if _, exists := seen[toolName]; !exists {
			seen[toolName] = struct{}{}
			merged = append(merged, ai.Tool{
				Name:        toolName,
				Description: def.Description,
				Parameters:  def.Parameters,
				Run: func(ctx context.Context, input any) (any, error) {
					return s.executeToolClosure(ctx, gen, toolName, input)
				},
			})
		}
	}

	return merged
}

// executeToolClosure 统一调度并执行大模型调用的工具闭包（优先服务端工具，次选已授权的设备 MCP 工具）。
func (s *Session) executeToolClosure(ctx context.Context, gen uint64, name string, input any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Generation() > gen {
		return nil, errors.New("generation mismatch")
	}

	sessionId := s.SessionId()

	// 1. 服务端工具直接在服务端执行
	if isServerTool(name) {
		s.logger.Info("executing server tool call",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", name,
		)

		if name == ServerToolCloseSession {
			s.mu.Lock()
			s.closeAfterTurn = true
			s.mu.Unlock()
		}

		result, err := executeServerTool(ctx, name, input)
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

	// 2. 设备 MCP 工具通过 JSON-RPC 下发给设备执行
	if s.isMCPToolAllowed(name) {
		resultText, err := s.callMCPToolWithInput(ctx, gen, name, input)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return nil, err
			}
			s.logger.Warn("mcp tool call failed during turn",
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
		return resultText, nil
	}

	// 3. 未启用或未授权的工具
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
