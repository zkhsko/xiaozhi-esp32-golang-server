package ai

import "context"

// MessageRole 表示对话消息的角色类型。
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// ToolCall 表示大语言模型输出的单次工具调用指令。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool 表示大语言模型可调用的工具定义。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Message 表示一条对话历史消息。
type Message struct {
	Role       MessageRole `json:"role"`
	Content    string      `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// LLMStream 表示单个大语言模型流式输出会话。
// 客户端通过该接口逐段接收文本增量。
type LLMStream interface {
	// Recv 接收下一个非空文本增量片段。
	// 当流正常结束时返回 ("", io.EOF)。
	// 当流中发生错误、超时或被取消时返回 ("", err)。
	Recv() (string, error)

	// ToolCalls 返回流式会话中模型生成的完整工具调用列表（若无则返回 nil）。
	ToolCalls() []ToolCall

	// Close 关闭并释放流式会话的所有网络与内存资源。
	Close() error
}

// LLMClient 定义流式大语言模型客户端接口。
type LLMClient interface {
	// CreateStream 基于上下文、消息列表与可选工具列表创建流式大语言模型回答会话。
	CreateStream(ctx context.Context, messages []Message, tools []Tool) (LLMStream, error)
}
