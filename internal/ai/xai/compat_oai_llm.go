package xai

import (
	"fmt"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// NewLLMClient 构造 xAI LLM 客户端（当前未实现）。
func NewLLMClient(ai.LLMOptions) (ai.ManagedLLMClient, error) {
	return nil, fmt.Errorf("%w: xai", ai.ErrLLMProviderNotImplemented)
}
