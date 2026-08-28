package factory

import (
	"fmt"
	"strings"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/ai/bailian"
	"xiaozhi-esp32-golang-server/internal/database"
)

// CreateASRClient 根据数据库 ASR 配置创建对应的语音识别客户端。
func CreateASRClient(cfg *database.ASRConfig) (ai.ASRClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("asr config is nil")
	}

	provider := strings.TrimSpace(cfg.Provider)
	switch provider {
	case "bailian", "":
		return bailian.NewASRClient(cfg)
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
	switch provider {
	case "bailian", "":
		return bailian.NewLLMClient(cfg)
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
	switch provider {
	case "bailian", "":
		return bailian.NewTTSClient(cfg, voice, queueCap)
	default:
		return nil, fmt.Errorf("unsupported tts provider: %s", provider)
	}
}
