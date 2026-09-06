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
	case "dashscope":
		return dashscope.NewLLMClient(opts)
	case "deepseek":
		return deepseek.NewLLMClient(opts)
	case "kimi":
		return kimi.NewLLMClient(opts)
	case "zai":
		return zai.NewLLMClient(opts)
	case "openrouter":
		return openrouter.NewLLMClient(opts)
	case "xai":
		return xai.NewLLMClient(opts)
	case "anthropic":
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
