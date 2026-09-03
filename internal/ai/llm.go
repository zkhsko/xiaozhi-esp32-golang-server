package ai

import (
	"context"
	"errors"
)

// 哨兵错误定义。
var (
	ErrMaxTurnsExceeded          = errors.New("llm max turns exceeded")
	ErrFirstTokenTimeout         = errors.New("llm first token timeout")
	ErrOverallTimeout            = errors.New("llm overall timeout")
	ErrLLMProviderNotImplemented = errors.New("llm provider not implemented")
)

// MessageRole 表示对话消息的角色类型。
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

// Message 表示一条对话历史消息。
type Message struct {
	Role    MessageRole `json:"role"`
	Content string      `json:"content"`
}

// ToolFunc 定义工具执行函数签名。
type ToolFunc func(ctx context.Context, input any) (any, error)

// Tool 表示大语言模型可调用的工具定义。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Run         ToolFunc       `json:"-"`
}

// LLMChunk 表示大语言模型流式输出的增量片段。
type LLMChunk struct {
	Text      string
	Iteration int
}

// LLMRequest 封装发送给大语言模型的完整请求。
type LLMRequest struct {
	Messages []Message
	Tools    []Tool
	MaxTurns int
}

// LLMResult 表示大语言模型生成的最终汇总结果。
type LLMResult struct {
	FinalText string
}

// LLMClient 定义流式大语言模型客户端接口。
type LLMClient interface {
	// Generate 基于上下文、请求与流式增量通道执行完整的模型生成与工具调用循环。
	Generate(
		ctx context.Context,
		req LLMRequest,
		chunks chan<- LLMChunk,
	) (LLMResult, error)
}
