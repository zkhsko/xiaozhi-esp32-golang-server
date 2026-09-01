package agentkit

import (
	"context"
	"encoding/json"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// ToolCloseSession 关闭会话工具名称常量。
const ToolCloseSession = "server.close_session"

// CloseSessionInput 定义关闭会话工具的入参。
type CloseSessionInput struct {
	Reason string `json:"reason,omitempty" jsonschema:"description=关闭会话的原因（可选）"`
}

// CloseSessionOutput 定义关闭会话工具的结构化返回值。
type CloseSessionOutput struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ShouldCloseSession 实现 SessionCloser 接口，指示当前轮次结束后关闭会话。
func (o *CloseSessionOutput) ShouldCloseSession() bool {
	return o != nil && o.Status == "success"
}

// GetCloseSessionTool 返回关闭会话工具的描述与参数定义。
func GetCloseSessionTool() ai.Tool {
	return ai.Tool{
		Name:        ToolCloseSession,
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
		Run: func(ctx context.Context, input any) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return ExecuteCloseSession(ParseCloseSessionInput(input))
		},
	}
}

// ParseCloseSessionInput 将入参解析为 CloseSessionInput。
func ParseCloseSessionInput(input any) CloseSessionInput {
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

// ExecuteCloseSession 执行会话关闭工具逻辑，返回结构化确认对象。
func ExecuteCloseSession(_ CloseSessionInput) (*CloseSessionOutput, error) {
	return &CloseSessionOutput{
		Status:  "success",
		Message: "session will be closed after this turn. You must now say a short goodbye to the user.",
	}, nil
}
