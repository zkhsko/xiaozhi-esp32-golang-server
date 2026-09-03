package factory

import (
	"errors"
	"testing"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestFactory_CreateClients(t *testing.T) {
	// 1. Nil configs
	if _, err := CreateASRClient(nil); err == nil {
		t.Error("expected error for nil asr config, got nil")
	}
	if _, err := CreateLLMClient(nil); err == nil {
		t.Error("expected error for nil llm config, got nil")
	}
	if _, err := CreateTTSClient(nil, "voice", 100); err == nil {
		t.Error("expected error for nil tts config, got nil")
	}

	// 2. Unsupported providers
	unsupportedASR := &database.ASRConfig{Provider: "unsupported", Endpoint: "ws://example.com", APIKey: "key", Model: "m"}
	if _, err := CreateASRClient(unsupportedASR); err == nil {
		t.Error("expected error for unsupported asr provider")
	}
	unsupportedLLM := &database.LLMConfig{Provider: "unknown_provider", Endpoint: "http://example.com", APIKey: "key", Model: "m", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000}
	if _, err := CreateLLMClient(unsupportedLLM); err == nil {
		t.Error("expected error for unsupported llm provider")
	}
	unsupportedTTS := &database.TTSConfig{Provider: "unsupported", Endpoint: "ws://example.com", APIKey: "key", Model: "m"}
	if _, err := CreateTTSClient(unsupportedTTS, "voice", 100); err == nil {
		t.Error("expected error for unsupported tts provider")
	}

	// 3. Placeholder providers return ErrLLMProviderNotImplemented
	placeholderProviders := []string{"deepseek", "kimi", "zai", "openrouter", "xai", "anthropic"}
	for _, p := range placeholderProviders {
		cfg := &database.LLMConfig{
			Provider: p,
			Endpoint: "https://example.com",
			APIKey:   "key",
			Model:    "m",
		}
		_, err := CreateLLMClient(cfg)
		if err == nil {
			t.Errorf("expected error for placeholder provider %s, got nil", p)
		} else if !errors.Is(err, ai.ErrLLMProviderNotImplemented) {
			t.Errorf("expected ErrLLMProviderNotImplemented for provider %s, got: %v", p, err)
		}
	}

	// 4. ASR / DashScope LLM / TTS configs (with and without proxy)
	for _, p := range []string{"dashscope", ""} {
		asrCfg := &database.ASRConfig{
			Provider:         p,
			Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
			APIKey:           "test-key",
			Model:            "qwen-asr",
			ConnectTimeoutMS: 5000,
			ProxyURL:         "http://127.0.0.1:8080",
		}
		asrClient, err := CreateASRClient(asrCfg)
		if err != nil {
			t.Fatalf("CreateASRClient(%q) failed: %v", p, err)
		}
		if asrClient == nil {
			t.Fatalf("expected non-nil asr client for provider %q", p)
		}
	}

	for _, p := range []string{"dashscope", ""} {
		llmCfg := &database.LLMConfig{
			Provider:            p,
			Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
			APIKey:              "test-key",
			Model:               "qwen-plus",
			FirstTokenTimeoutMS: 5000,
			OverallTimeoutMS:    30000,
			ProxyURL:            "http://127.0.0.1:8080",
		}
		llmClient, err := CreateLLMClient(llmCfg)
		if err != nil {
			t.Fatalf("CreateLLMClient(%q) failed: %v", p, err)
		}
		if llmClient == nil {
			t.Fatalf("expected non-nil llm client for provider %q", p)
		}
	}

	for _, p := range []string{"dashscope", ""} {
		ttsCfg := &database.TTSConfig{
			Provider:         p,
			Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
			APIKey:           "test-key",
			Model:            "cosyvoice-v1",
			Voices:           `["voice1"]`,
			ConnectTimeoutMS: 5000,
			ProxyURL:         "http://127.0.0.1:8080",
		}
		ttsClient, err := CreateTTSClient(ttsCfg, "voice1", 100)
		if err != nil {
			t.Fatalf("CreateTTSClient(%q) failed: %v", p, err)
		}
		if ttsClient == nil {
			t.Fatalf("expected non-nil tts client for provider %q", p)
		}
	}

	testTTSCfg := &database.TTSConfig{
		Provider:         "dashscope",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "test-key",
		Model:            "cosyvoice-v1",
		Voices:           `["voice1"]`,
		ConnectTimeoutMS: 5000,
	}
	// 5. TTS Empty voice validation
	if _, err := CreateTTSClient(testTTSCfg, "  ", 100); err == nil {
		t.Error("expected error for empty voice, got nil")
	}
}
