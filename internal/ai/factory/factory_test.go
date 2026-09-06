package factory

import (
	"errors"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestValidateLLMProvider(t *testing.T) {
	for _, provider := range []string{"", "dashscope", " DASHSCOPE ", "deepseek", "kimi", "zai", "openrouter", "xai", "anthropic"} {
		if err := ValidateLLMProvider(provider); err != nil {
			t.Fatalf("expected provider %q to be recognized, got %v", provider, err)
		}
	}
	if err := ValidateLLMProvider("openai"); err == nil {
		t.Fatal("expected unknown provider to be rejected")
	}
	if err := ValidateAvailableLLMProvider(""); err != nil {
		t.Fatalf("expected default provider to be available, got %v", err)
	}
	if err := ValidateAvailableLLMProvider("deepseek"); !errors.Is(err, ai.ErrLLMProviderNotImplemented) {
		t.Fatalf("expected placeholder provider to be unavailable, got %v", err)
	}
}

func TestFactory_CreateClients(t *testing.T) {
	// 1. Invalid configs
	if _, err := CreateASRClient(nil); err == nil {
		t.Error("expected error for nil asr config, got nil")
	}
	if _, err := CreateLLMClient(ai.LLMOptions{}); err == nil {
		t.Error("expected error for empty llm options, got nil")
	}
	unsupportedTTS := ai.TTSOptions{Provider: "unsupported", Endpoint: "ws://example.com", APIKey: "key", Model: "m", Voice: "voice"}
	if _, err := CreateTTSClient(unsupportedTTS); err == nil {
		t.Error("expected error for unsupported tts provider")
	}

	// 3. Placeholder providers return ErrLLMProviderNotImplemented
	placeholderProviders := []string{"deepseek", "kimi", "zai", "openrouter", "xai", "anthropic"}
	for _, p := range placeholderProviders {
		opts := ai.LLMOptions{
			Provider: p,
			Endpoint: "https://example.com",
			APIKey:   "key",
			Model:    "m",
		}
		_, err := CreateLLMClient(opts)
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
		llmOpts := ai.LLMOptions{
			Provider:          p,
			Endpoint:          "https://dashscope.aliyuncs.com/compatible-mode/v1",
			APIKey:            "test-key",
			Model:             "qwen-plus",
			FirstTokenTimeout: 5 * time.Second,
			OverallTimeout:    30 * time.Second,
			ProxyURL:          "http://127.0.0.1:8080",
		}
		llmClient, err := CreateLLMClient(llmOpts)
		if err != nil {
			t.Fatalf("CreateLLMClient(%q) failed: %v", p, err)
		}
		if llmClient == nil {
			t.Fatalf("expected non-nil llm client for provider %q", p)
		}
		if err := llmClient.Close(); err != nil {
			t.Fatalf("CloseLLMClient(%q) failed: %v", p, err)
		}
	}

	for _, p := range []string{"dashscope", ""} {
		ttsOpts := ai.TTSOptions{
			Provider:       p,
			Endpoint:       "wss://dashscope.aliyuncs.com/api-v1/ws",
			APIKey:         "test-key",
			Model:          "cosyvoice-v1",
			Voice:          "voice1",
			ConnectTimeout: 5000,
			ProxyURL:       "http://127.0.0.1:8080",
		}
		ttsClient, err := CreateTTSClient(ttsOpts)
		if err != nil {
			t.Fatalf("CreateTTSClient(%q) failed: %v", p, err)
		}
		if ttsClient == nil {
			t.Fatalf("expected non-nil tts client for provider %q", p)
		}
	}

	testTTSOpts := ai.TTSOptions{
		Provider:       "dashscope",
		Endpoint:       "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:         "test-key",
		Model:          "cosyvoice-v1",
		Voice:          "  ",
		ConnectTimeout: 5000,
	}
	// 5. TTS Empty voice validation
	if _, err := CreateTTSClient(testTTSOpts); err == nil {
		t.Error("expected error for empty voice, got nil")
	}
}
