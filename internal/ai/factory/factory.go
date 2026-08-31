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

// CreateLLMClient 根据数据库 LLM 配置创建对应的大语言模型客户端。
func CreateLLMClient(cfg *database.LLMConfig) (ai.LLMClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("llm config is nil")
	}

	provider := strings.TrimSpace(cfg.Provider)
	switch strings.ToLower(provider) {
	case "dashscope", "":
		return dashscope.NewLLMClient(cfg)
	case "deepseek":
		return deepseek.NewLLMClient(cfg)
	case "kimi":
		return kimi.NewLLMClient(cfg)
	case "zai":
		return zai.NewLLMClient(cfg)
	case "openrouter":
		return openrouter.NewLLMClient(cfg)
	case "xai":
		return xai.NewLLMClient(cfg)
	case "anthropic":
		return anthropic.NewLLMClient(cfg)
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", provider)
	}
}

// CreateTTSClient 根据数据库 TTS 配置和指定音色创建对应的流式语音合成客户端。
func CreateTTSClient(cfg *database.TTSConfig, voice string, queueCap int) (ai.TTSClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("tts config is nil")
	}

	provider := strings.TrimSpace(cfg.Provider)
	switch strings.ToLower(provider) {
	case "dashscope", "":
		return dashscope.NewTTSClient(cfg, voice, queueCap)
	default:
		return nil, fmt.Errorf("unsupported tts provider: %s", provider)
	}
}
