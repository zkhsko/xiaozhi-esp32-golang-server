package factory

import (
	"fmt"
	"strings"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/ai/anthropic"
	"xiaozhi-esp32-golang-server/internal/ai/dashscope"
	"xiaozhi-esp32-golang-server/internal/ai/deepseek"
	"xiaozhi-esp32-golang-server/internal/ai/kimi"
	"xiaozhi-esp32-golang-server/internal/ai/openrouter"
	"xiaozhi-esp32-golang-server/internal/ai/xai"
	"xiaozhi-esp32-golang-server/internal/ai/zai"
	"xiaozhi-esp32-golang-server/internal/database"
)

const (
	llmProviderDeepSeek   = "deepseek"
	llmProviderKimi       = "kimi"
	llmProviderZAI        = "zai"
	llmProviderOpenRouter = "openrouter"
	llmProviderXAI        = "xai"
	llmProviderAnthropic  = "anthropic"
)

// ValidateLLMProvider 校验供应商标识是否属于当前代码识别的 LLM 供应商。
func ValidateLLMProvider(provider string) error {
	switch ai.NormalizeLLMProvider(provider) {
	case ai.DefaultLLMProvider,
		llmProviderDeepSeek,
		llmProviderKimi,
		llmProviderZAI,
		llmProviderOpenRouter,
		llmProviderXAI,
		llmProviderAnthropic:
		return nil
	default:
		return fmt.Errorf("unsupported llm provider: %s", strings.TrimSpace(provider))
	}
}

// ValidateAvailableLLMProvider 校验供应商是否已具备可进入运行时的生产实现。
func ValidateAvailableLLMProvider(provider string) error {
	normalized := ai.NormalizeLLMProvider(provider)
	if err := ValidateLLMProvider(normalized); err != nil {
		return err
	}
	if normalized != ai.DefaultLLMProvider {
		return fmt.Errorf("%w: %s", ai.ErrLLMProviderNotImplemented, normalized)
	}
	return nil
}

// CreateASRClient 根据数据库 ASR 配置创建对应的语音识别客户端。
func CreateASRClient(cfg *database.ASRConfig) (ai.ASRClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("asr config is nil")
	}

	provider := strings.TrimSpace(cfg.Provider)
	switch provider {
	case "dashscope", "":
		return dashscope.NewASRClient(cfg)
	default:
		return nil, fmt.Errorf("unsupported asr provider: %s", provider)
	}
}

// CreateLLMClient 根据 LLM 运行时配置创建对应的大语言模型客户端。
func CreateLLMClient(opts ai.LLMOptions) (ai.ManagedLLMClient, error) {
	opts = opts.Normalized()
	switch opts.Provider {
	case ai.DefaultLLMProvider:
		return dashscope.NewLLMClient(opts)
	case llmProviderDeepSeek:
		return deepseek.NewLLMClient(opts)
	case llmProviderKimi:
		return kimi.NewLLMClient(opts)
	case llmProviderZAI:
		return zai.NewLLMClient(opts)
	case llmProviderOpenRouter:
		return openrouter.NewLLMClient(opts)
	case llmProviderXAI:
		return xai.NewLLMClient(opts)
	case llmProviderAnthropic:
		return anthropic.NewLLMClient(opts)
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", opts.Provider)
	}
}

// CreateTTSClient 根据 TTS 领域配置创建对应的流式语音合成客户端。
func CreateTTSClient(opts ai.TTSOptions) (ai.TTSClient, error) {
	provider := strings.TrimSpace(opts.Provider)
	switch strings.ToLower(provider) {
	case "dashscope", "":
		return dashscope.NewTTSClient(opts)
	default:
		return nil, fmt.Errorf("unsupported tts provider: %s", provider)
	}
}
