package factory

import (
	"testing"

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
	unsupportedLLM := &database.LLMConfig{Provider: "unsupported", Endpoint: "http://example.com", APIKey: "key", Model: "m", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000}
	if _, err := CreateLLMClient(unsupportedLLM); err == nil {
		t.Error("expected error for unsupported llm provider")
	}
	unsupportedTTS := &database.TTSConfig{Provider: "unsupported", Endpoint: "ws://example.com", APIKey: "key", Model: "m"}
	if _, err := CreateTTSClient(unsupportedTTS, "voice", 100); err == nil {
		t.Error("expected error for unsupported tts provider")
	}

	// 3. Bailian configs (with and without proxy)
	asrCfg := &database.ASRConfig{
		Provider:         "bailian",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "test-key",
		Model:            "qwen-asr",
		ConnectTimeoutMS: 5000,
		ProxyURL:         "http://127.0.0.1:8080",
	}
	asrClient, err := CreateASRClient(asrCfg)
	if err != nil {
		t.Fatalf("CreateASRClient failed: %v", err)
	}
	if asrClient == nil {
		t.Fatal("expected non-nil asr client")
	}

	llmCfg := &database.LLMConfig{
		Provider:            "bailian",
		Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:              "test-key",
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		ProxyURL:            "http://127.0.0.1:8080",
	}
	llmClient, err := CreateLLMClient(llmCfg)
	if err != nil {
		t.Fatalf("CreateLLMClient failed: %v", err)
	}
	if llmClient == nil {
		t.Fatal("expected non-nil llm client")
	}

	ttsCfg := &database.TTSConfig{
		Provider:            "bailian",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:              "test-key",
		Model:               "cosyvoice-v1",
		Voices:              `["voice1"]`,
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		ProxyURL:            "http://127.0.0.1:8080",
	}
	ttsClient, err := CreateTTSClient(ttsCfg, "voice1", 100)
	if err != nil {
		t.Fatalf("CreateTTSClient failed: %v", err)
	}
	if ttsClient == nil {
		t.Fatal("expected non-nil tts client")
	}

	// 4. TTS Empty voice validation
	if _, err := CreateTTSClient(ttsCfg, "  ", 100); err == nil {
		t.Error("expected error for empty voice, got nil")
	}
}
