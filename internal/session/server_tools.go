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
			Description: "获取服务端当前的日期、时间、星期和时区信息",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        ServerToolCloseSession,
			Description: "关闭当前会话并断开连接。当用户表示想要结束对话、退下、去睡觉、断开连接、再见或不再需要交互时调用此工具",
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

// isServerTool 检查指定名称是否为启用的服务端工具。
func isServerTool(name string) bool {
	return name == ServerToolGetCurrentTime ||
		name == ServerToolCloseSession
}

// executeServerTool 执行指定名称的服务端工具。
func executeServerTool(ctx context.Context, name string, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	switch name {
	case ServerToolGetCurrentTime:
		return executeGetCurrentTime()
	case ServerToolCloseSession:
		return executeCloseSession()
	default:
		return "", fmt.Errorf("%w: %s", ErrServerToolNotFound, name)
	}
}

// executeCloseSession 执行会话关闭工具逻辑，返回格式化确认 JSON。
func executeCloseSession() (string, error) {
	data := map[string]any{
		"status":  "success",
		"message": "session will be closed after this turn",
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshal close session result: %w", err)
	}
	return string(bytes), nil
}

// executeGetCurrentTime 获取服务端系统当前日期、时间、星期、时区及 UTC 偏移。
func executeGetCurrentTime() (string, error) {
	now := time.Now()
	zoneName, _ := now.Zone()
	data := map[string]any{
		"datetime":   now.Format("2006-01-02 15:04:05"),
		"date":       now.Format("2006-01-02"),
		"time":       now.Format("15:04:05"),
		"weekday":    now.Weekday().String(),
		"timezone":   zoneName,
		"utc_offset": now.Format("-07:00"),
	}

	bytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshal current time: %w", err)
	}
	return string(bytes), nil
}

// availableTools 返回向大语言模型提供的完整工具列表（服务端工具 + 设备 MCP 工具）。
// 合并规则：
// 1. 服务端工具优先。
// 2. 同名工具只向大模型提供一次。
// 3. 设备不得覆盖同名服务端工具。
func (s *Session) availableTools() []ai.Tool {
	serverTools := DefaultServerTools()

	s.mu.RLock()
	deviceTools := s.mcpTools
	s.mu.RUnlock()

	if len(deviceTools) == 0 {
		return serverTools
	}

	seen := make(map[string]struct{}, len(serverTools)+len(deviceTools))
	merged := make([]ai.Tool, 0, len(serverTools)+len(deviceTools))

	// 1. 服务端工具优先
	for _, t := range serverTools {
		seen[t.Name] = struct{}{}
		merged = append(merged, t)
	}

	// 2. 设备 MCP 工具追加，若与服务端工具同名则忽略
	for _, t := range deviceTools {
		if _, exists := seen[t.Name]; !exists {
			seen[t.Name] = struct{}{}
			merged = append(merged, t)
		}
	}

	return merged
}

// executeTool 统一调度并执行大模型调用的工具（优先服务端工具，次选已授权的设备 MCP 工具）。
func (s *Session) executeTool(ctx context.Context, gen uint64, tc ai.ToolCall) string {
	sessionId := s.SessionId()

	// 1. 服务端工具直接在服务端执行
	if isServerTool(tc.Name) {
		s.logger.Info("executing server tool call",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", tc.Name,
		)

		if tc.Name == ServerToolCloseSession {
			s.mu.Lock()
			s.closeAfterTurn = true
			s.mu.Unlock()
		}

		resultText, err := executeServerTool(ctx, tc.Name, tc.Arguments)
		if err != nil {
			s.logger.Warn("server tool call failed",
				"session_id", sessionId,
				"generation", gen,
				"tool_name", tc.Name,
				"error", err,
			)
			return fmt.Sprintf("Error: %v", err)
		}

		s.logger.Info("server tool call executed successfully",
			"session_id", sessionId,
			"generation", gen,
			"tool_name", tc.Name,
		)
		return resultText
	}

	// 2. 设备 MCP 工具通过 JSON-RPC 下发给设备执行
	if s.isMCPToolAllowed(tc.Name) {
		resultText, err := s.callMCPTool(ctx, tc.Name, tc.Arguments)
		if err != nil {
			s.logger.Warn("mcp tool call failed during turn",
				"session_id", sessionId,
				"generation", gen,
				"tool_name", tc.Name,
				"error", err,
			)
			return fmt.Sprintf("Error: %v", err)
		}
		return resultText
	}

	// 3. 未启用或未授权的工具
	s.logger.Warn("tool call rejected: tool not authorized in session",
		"session_id", sessionId,
		"generation", gen,
		"tool_name", tc.Name,
	)
	return fmt.Sprintf("Error: tool %q is not authorized in current session", tc.Name)
}
