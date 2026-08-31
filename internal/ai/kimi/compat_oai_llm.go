package kimi

import (
	"fmt"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/database"
)

// NewLLMClient 构造 Kimi LLM 客户端（当前未实现）。
func NewLLMClient(cfg *database.LLMConfig) (ai.LLMClient, error) {
	return nil, fmt.Errorf("%w: kimi", ai.ErrLLMProviderNotImplemented)
}
