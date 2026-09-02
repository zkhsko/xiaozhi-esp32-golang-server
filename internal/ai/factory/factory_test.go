package factory

import (
	"errors"
	"strings"
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
	if _, err := CreateTTSClient(nil, "voice"); err == nil {
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
	if _, err := CreateTTSClient(unsupportedTTS, "voice"); err == nil {
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

	// 4. ASR / DashScope LLM configs (with and without proxy)
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
}

func TestFactory_CreateTTSClient(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		client, err := CreateTTSClient(nil, "longxiaochun")
		if err == nil {
			t.Fatal("expected error for nil tts config, got nil")
		}
		if client != nil {
			t.Fatalf("expected nil client, got: %v", client)
		}
		if !strings.Contains(err.Error(), "tts config is nil") {
			t.Fatalf("expected error message to contain 'tts config is nil', got: %v", err)
		}
	})

	t.Run("dashscope success and normalization", func(t *testing.T) {
		validProviders := []string{
			"dashscope",
			"DashScope",
			"DASHSCOPE",
			" dashscope ",
			"  DashScope  ",
		}
		for _, p := range validProviders {
			cfg := &database.TTSConfig{
				Provider:            p,
				Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
				APIKey:              "test-api-key",
				Model:               "qwen-audio-3.0-tts-flash",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
				ProxyURL:            "http://127.0.0.1:8080",
			}
			client, err := CreateTTSClient(cfg, "longxiaochun")
			if err != nil {
				t.Fatalf("CreateTTSClient with provider %q failed: %v", p, err)
			}
			if client == nil {
				t.Fatalf("expected non-nil tts client for provider %q", p)
			}
		}
	})

	t.Run("unsupported and empty providers", func(t *testing.T) {
		unsupportedProviders := []string{
			"",
			"   ",
			"\t",
			"unsupported",
			"openai",
			"volcengine",
			"cosyvoice",
			"azure",
		}
		for _, p := range unsupportedProviders {
			cfg := &database.TTSConfig{
				Provider: p,
				Endpoint: "wss://dashscope.aliyuncs.com/api-v1/ws",
				APIKey:   "test-api-key",
				Model:    "qwen-audio-3.0-tts-flash",
			}
			client, err := CreateTTSClient(cfg, "longxiaochun")
			if err == nil {
				t.Fatalf("expected error for unsupported provider %q, got client: %v", p, client)
			}
			if client != nil {
				t.Fatalf("expected nil client for unsupported provider %q", p)
			}
			if !strings.Contains(err.Error(), "unsupported tts provider") {
				t.Fatalf("expected 'unsupported tts provider' in error, got: %v", err)
			}
		}
	})

	t.Run("empty models", func(t *testing.T) {
		emptyModels := []string{
			"",
			"   ",
			"\t\n",
		}
		for _, m := range emptyModels {
			cfg := &database.TTSConfig{
				Provider: "dashscope",
				Endpoint: "wss://dashscope.aliyuncs.com/api-v1/ws",
				APIKey:   "test-api-key",
				Model:    m,
			}
			client, err := CreateTTSClient(cfg, "longxiaochun")
			if err == nil {
				t.Fatalf("expected error for empty model %q, got client: %v", m, client)
			}
			if client != nil {
				t.Fatalf("expected nil client for empty model %q", m)
			}
			if !strings.Contains(err.Error(), "dashscope tts model is required") {
				t.Fatalf("expected 'dashscope tts model is required' in error, got: %v", err)
			}
		}
	})

	t.Run("valid diverse models", func(t *testing.T) {
		models := []string{
			"qwen-audio-3.0-tts-flash",
			"cosyvoice-v1",
			"sambert-zhichu-v1",
			"custom-tts-model",
		}
		for _, m := range models {
			cfg := &database.TTSConfig{
				Provider: "dashscope",
				Endpoint: "wss://dashscope.aliyuncs.com/api-v1/ws",
				APIKey:   "test-api-key",
				Model:    m,
			}
			client, err := CreateTTSClient(cfg, "longxiaochun")
			if err != nil {
				t.Fatalf("CreateTTSClient with model %q failed: %v", m, err)
			}
			if client == nil {
				t.Fatalf("expected non-nil tts client for model %q", m)
			}
		}
	})

	t.Run("empty voice", func(t *testing.T) {
		emptyVoices := []string{
			"",
			"   ",
			"\t\n",
		}
		cfg := &database.TTSConfig{
			Provider: "dashscope",
			Endpoint: "wss://dashscope.aliyuncs.com/api-v1/ws",
			APIKey:   "test-api-key",
			Model:    "qwen-audio-3.0-tts-flash",
		}
		for _, v := range emptyVoices {
			client, err := CreateTTSClient(cfg, v)
			if err == nil {
				t.Fatalf("expected error for empty voice %q, got client: %v", v, client)
			}
			if client != nil {
				t.Fatalf("expected nil client for empty voice %q", v)
			}
			if !strings.Contains(err.Error(), "tts voice is required") {
				t.Fatalf("expected 'tts voice is required' in error, got: %v", err)
			}
		}
	})

	t.Run("invalid dashscope config parameters", func(t *testing.T) {
		baseCfg := func() *database.TTSConfig {
			return &database.TTSConfig{
				Provider: "dashscope",
				Endpoint: "wss://dashscope.aliyuncs.com/api-v1/ws",
				APIKey:   "test-api-key",
				Model:    "qwen-audio-3.0-tts-flash",
			}
		}

		// Empty endpoint
		cfgNoEndpoint := baseCfg()
		cfgNoEndpoint.Endpoint = ""
		if _, err := CreateTTSClient(cfgNoEndpoint, "longxiaochun"); err == nil {
			t.Fatal("expected error for empty endpoint, got nil")
		}

		// Empty API key
		cfgNoAPIKey := baseCfg()
		cfgNoAPIKey.APIKey = ""
		if _, err := CreateTTSClient(cfgNoAPIKey, "longxiaochun"); err == nil {
			t.Fatal("expected error for empty api key, got nil")
		}

		// Invalid proxy URL
		cfgBadProxy := baseCfg()
		cfgBadProxy.ProxyURL = "://invalid-url"
		if _, err := CreateTTSClient(cfgBadProxy, "longxiaochun"); err == nil {
			t.Fatal("expected error for invalid proxy url, got nil")
		}
	})
}
