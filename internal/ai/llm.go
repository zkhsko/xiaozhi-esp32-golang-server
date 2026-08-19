package ai

import "context"

// MessageRole 表示对话消息的角色类型。
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

// Message 表示一条对话历史消息。
type Message struct {
	Role    MessageRole
	Content string
}

// LLMStream 表示单个大语言模型流式输出会话。
// 客户端通过该接口逐段接收文本增量。
type LLMStream interface {
	// Recv 接收下一个非空文本增量片段。
	// 当流正常结束时返回 ("", io.EOF)。
	// 当流中发生错误、超时或被取消时返回 ("", err)。
	Recv() (string, error)

	// Close 关闭并释放流式会话的所有网络与内存资源。
	Close() error
}

// LLMClient 定义流式大语言模型客户端接口。
type LLMClient interface {
	// CreateStream 基于上下文和消息列表创建流式大语言模型回答会话。
	CreateStream(ctx context.Context, messages []Message) (LLMStream, error)
}
