package xai

import (
	"fmt"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/database"
)

// NewLLMClient 构造 xAI LLM 客户端（当前未实现）。
func NewLLMClient(cfg *database.LLMConfig) (ai.LLMClient, error) {
	return nil, fmt.Errorf("%w: xai", ai.ErrLLMProviderNotImplemented)
}
