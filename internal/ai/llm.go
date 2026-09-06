package ai

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultLLMProvider 是未显式配置供应商时使用的默认 LLM 供应商。
	DefaultLLMProvider = "dashscope"
	// DefaultLLMFirstTokenTimeout 是等待单次模型调用首个有效输出的默认超时。
	DefaultLLMFirstTokenTimeout = 5 * time.Second
	// DefaultLLMOverallTimeout 是单次完整生成与工具调用循环的默认总超时。
	DefaultLLMOverallTimeout = 30 * time.Second
	// DefaultLLMMaxTurns 是单次生成允许的默认模型调用轮数。
	DefaultLLMMaxTurns = 8
)

// 哨兵错误定义。
var (
	ErrMaxTurnsExceeded          = errors.New("llm max turns exceeded")
	ErrFirstTokenTimeout         = errors.New("llm first token timeout")
	ErrOverallTimeout            = errors.New("llm overall timeout")
	ErrLLMProviderNotImplemented = errors.New("llm provider not implemented")
)

// LLMOptions 封装创建 LLM 客户端所需的运行时配置，不依赖持久化模型。
type LLMOptions struct {
	Provider          string
	Endpoint          string
	APIKey            string
	Model             string
	ProxyURL          string
	FirstTokenTimeout time.Duration
	OverallTimeout    time.Duration
}

// NormalizeLLMProvider 规范化供应商标识，空值按默认供应商处理。
func NormalizeLLMProvider(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	if normalized == "" {
		return DefaultLLMProvider
	}
	return normalized
}

// Normalized 返回字段已去除首尾空白并补齐默认值的运行时配置副本。
func (o LLMOptions) Normalized() LLMOptions {
	o.Provider = NormalizeLLMProvider(o.Provider)
	o.Endpoint = strings.TrimSpace(o.Endpoint)
	o.APIKey = strings.TrimSpace(o.APIKey)
	o.Model = strings.TrimSpace(o.Model)
	o.ProxyURL = strings.TrimSpace(o.ProxyURL)
	if o.FirstTokenTimeout == 0 {
		o.FirstTokenTimeout = DefaultLLMFirstTokenTimeout
	}
	if o.OverallTimeout == 0 {
		o.OverallTimeout = DefaultLLMOverallTimeout
	}
	return o
}

// Validate 校验供应商无关的 LLM 运行时配置约束。
func (o LLMOptions) Validate() error {
	o = o.Normalized()
	if err := validateLLMURL("endpoint", o.Endpoint, map[string]bool{"http": true, "https": true}); err != nil {
		return err
	}
	if o.Model == "" {
		return errors.New("llm model is required")
	}
	if o.ProxyURL != "" {
		if err := validateLLMURL("proxy url", o.ProxyURL, map[string]bool{
			"http": true, "https": true, "socks5": true, "socks5h": true,
		}); err != nil {
			return err
		}
	}
	if o.FirstTokenTimeout < 0 {
		return errors.New("llm first token timeout cannot be negative")
	}
	if o.OverallTimeout < 0 {
		return errors.New("llm overall timeout cannot be negative")
	}
	if o.OverallTimeout <= o.FirstTokenTimeout {
		return fmt.Errorf(
			"llm overall timeout (%v) must be greater than first token timeout (%v)",
			o.OverallTimeout,
			o.FirstTokenTimeout,
		)
	}
	return nil
}

func validateLLMURL(name, rawURL string, allowedSchemes map[string]bool) error {
	if rawURL == "" {
		return fmt.Errorf("llm %s is required", name)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || !allowedSchemes[strings.ToLower(parsed.Scheme)] {
		return fmt.Errorf("invalid llm %s", name)
	}
	return nil
}

// MessageRole 表示对话消息的角色类型。
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// ToolCall 表示大语言模型发起的单次工具调用请求。
type ToolCall struct {
	Id        string `json:"id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// Message 表示一条对话历史消息（支持普通文本、助手工具调用与工具执行结果）。
type Message struct {
	Role       MessageRole `json:"role"`
	Content    string      `json:"content,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallId string      `json:"tool_call_id,omitempty"`
	ToolName   string      `json:"tool_name,omitempty"`
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

// LLMChunk 表示大语言模型流式输出的文本增量。
type LLMChunk struct {
	Text string
	// Step 标识同一次 Generate 内的模型调用步骤。同一步骤内保持不变，工具调用后再次请求模型时发生变化；具体起始值没有业务语义。
	Step int
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
	Messages  []Message
}

// LLMClient 定义流式大语言模型客户端接口。
type LLMClient interface {
	// Generate 执行完整的模型生成与工具调用循环。
	// chunks 可以为 nil；非 nil 时实现按调用方消费速度执行背压发送。
	// Generate 不关闭 chunks，调用方只能在 Generate 返回后关闭该通道。
	Generate(
		ctx context.Context,
		req LLMRequest,
		chunks chan<- LLMChunk,
	) (LLMResult, error)
}

// ManagedLLMClient 表示拥有可释放运行时资源的 LLM 客户端，由创建方负责调用 Close。
type ManagedLLMClient interface {
	LLMClient
	Close() error
}
