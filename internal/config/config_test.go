package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
)

const validYAML = `
server:
  listen_addr: ":8080"
  websocket_url: "wss://example.com/xiaozhi/v1/"
  max_concurrent_sessions: 10
  shutdown_timeout: 10s
  http_read_timeout: 15s
  http_write_timeout: 30s
  http_idle_timeout: 60s
  max_http_body_bytes: 65536
  max_http_header_bytes: 1024

session:
  hello_timeout: 10s
  max_ws_text_message_bytes: 32768
  max_opus_packet_bytes: 1024
  max_listening_duration: 30s
  asr_pcm_queue_capacity: 100
  tts_pcm_queue_capacity: 100
  downlink_opus_queue_capacity: 100
  max_history_turns: 6
  system_prompt: "你是小智，一个智能语音助手。请用简明、友好的中文回答，回答适合直接语音朗读。"

ai:
  bailian:
    ws_endpoint: "wss://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/api-ws/v1/inference"
    llm_endpoint: "https://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
    asr_model: "qwen-audio-3.0-asr-flash-streaming"
    llm_model: "qwen3.7-flash"
    tts_model: "qwen-audio-3.0-tts-flash"
    tts_voice: "longanlingxi"
    asr_connect_timeout: 10s
    tts_connect_timeout: 10s
    llm_first_token_timeout: 15s
    llm_overall_timeout: 60s
    tts_first_audio_timeout: 10s
    tts_sentence_timeout: 15s

proxy:
  enabled: false
  url: ""
`

func TestLoadFromReader_Valid(t *testing.T) {
	cfg, err := config.LoadFromReader(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("unexpected error loading valid yaml: %v", err)
	}

	if cfg.Server.ListenAddr != ":8080" {
		t.Errorf("expected ListenAddr :8080, got %s", cfg.Server.ListenAddr)
	}
	if cfg.Server.WebSocketURL != "wss://example.com/xiaozhi/v1/" {
		t.Errorf("expected WebSocketURL wss://example.com/xiaozhi/v1/, got %s", cfg.Server.WebSocketURL)
	}
	if cfg.Server.MaxConcurrentSessions != 10 {
		t.Errorf("expected MaxConcurrentSessions 10, got %d", cfg.Server.MaxConcurrentSessions)
	}
	if cfg.Server.ShutdownTimeout != 10*time.Second {
		t.Errorf("expected ShutdownTimeout 10s, got %v", cfg.Server.ShutdownTimeout)
	}
	if cfg.Server.HTTPReadTimeout != 15*time.Second {
		t.Errorf("expected HTTPReadTimeout 15s, got %v", cfg.Server.HTTPReadTimeout)
	}
	if cfg.Server.HTTPWriteTimeout != 30*time.Second {
		t.Errorf("expected HTTPWriteTimeout 30s, got %v", cfg.Server.HTTPWriteTimeout)
	}
	if cfg.Server.HTTPIdleTimeout != 60*time.Second {
		t.Errorf("expected HTTPIdleTimeout 60s, got %v", cfg.Server.HTTPIdleTimeout)
	}
	if cfg.Server.MaxHTTPBodyBytes != 65536 {
		t.Errorf("expected MaxHTTPBodyBytes 65536, got %d", cfg.Server.MaxHTTPBodyBytes)
	}
	if cfg.Server.MaxHTTPHeaderBytes != 1024 {
		t.Errorf("expected MaxHTTPHeaderBytes 1024, got %d", cfg.Server.MaxHTTPHeaderBytes)
	}

	if cfg.Session.HelloTimeout != 10*time.Second {
		t.Errorf("expected HelloTimeout 10s, got %v", cfg.Session.HelloTimeout)
	}
	if cfg.Session.MaxWSTextMessageBytes != 32768 {
		t.Errorf("expected MaxWSTextMessageBytes 32768, got %d", cfg.Session.MaxWSTextMessageBytes)
	}
	if cfg.Session.MaxOpusPacketBytes != 1024 {
		t.Errorf("expected MaxOpusPacketBytes 1024, got %d", cfg.Session.MaxOpusPacketBytes)
	}
	if cfg.Session.MaxListeningDuration != 30*time.Second {
		t.Errorf("expected MaxListeningDuration 30s, got %v", cfg.Session.MaxListeningDuration)
	}
	if cfg.Session.ASRPCMQueueCapacity != 100 {
		t.Errorf("expected ASRPCMQueueCapacity 100, got %d", cfg.Session.ASRPCMQueueCapacity)
	}
	if cfg.Session.TTSPCMQueueCapacity != 100 {
		t.Errorf("expected TTSPCMQueueCapacity 100, got %d", cfg.Session.TTSPCMQueueCapacity)
	}
	if cfg.Session.DownlinkOpusQueueCapacity != 100 {
		t.Errorf("expected DownlinkOpusQueueCapacity 100, got %d", cfg.Session.DownlinkOpusQueueCapacity)
	}
	if cfg.Session.MaxHistoryTurns != 6 {
		t.Errorf("expected MaxHistoryTurns 6, got %d", cfg.Session.MaxHistoryTurns)
	}
	if !strings.Contains(cfg.Session.SystemPrompt, "小智") {
		t.Errorf("expected SystemPrompt containing 小智, got %s", cfg.Session.SystemPrompt)
	}

	if cfg.AI.Bailian.WSEndpoint != "wss://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/api-ws/v1/inference" {
		t.Errorf("expected Bailian WSEndpoint match, got %s", cfg.AI.Bailian.WSEndpoint)
	}
	if cfg.AI.Bailian.LLMEndpoint != "https://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/compatible-mode/v1" {
		t.Errorf("expected Bailian LLMEndpoint match, got %s", cfg.AI.Bailian.LLMEndpoint)
	}
	if cfg.AI.Bailian.ASRModel != "qwen-audio-3.0-asr-flash-streaming" {
		t.Errorf("expected ASRModel match, got %s", cfg.AI.Bailian.ASRModel)
	}
	if cfg.AI.Bailian.LLMModel != "qwen3.7-flash" {
		t.Errorf("expected LLMModel match, got %s", cfg.AI.Bailian.LLMModel)
	}
	if cfg.AI.Bailian.TTSModel != "qwen-audio-3.0-tts-flash" {
		t.Errorf("expected TTSModel match, got %s", cfg.AI.Bailian.TTSModel)
	}
	if cfg.AI.Bailian.TTSVoice != "longanlingxi" {
		t.Errorf("expected TTSVoice match, got %s", cfg.AI.Bailian.TTSVoice)
	}
	if cfg.AI.Bailian.ASRConnectTimeout != 10*time.Second {
		t.Errorf("expected ASRConnectTimeout 10s, got %v", cfg.AI.Bailian.ASRConnectTimeout)
	}
	if cfg.AI.Bailian.TTSConnectTimeout != 10*time.Second {
		t.Errorf("expected TTSConnectTimeout 10s, got %v", cfg.AI.Bailian.TTSConnectTimeout)
	}
	if cfg.AI.Bailian.LLMFirstTokenTimeout != 15*time.Second {
		t.Errorf("expected LLMFirstTokenTimeout 15s, got %v", cfg.AI.Bailian.LLMFirstTokenTimeout)
	}
	if cfg.AI.Bailian.LLMOverallTimeout != 60*time.Second {
		t.Errorf("expected LLMOverallTimeout 60s, got %v", cfg.AI.Bailian.LLMOverallTimeout)
	}
	if cfg.AI.Bailian.TTSFirstAudioTimeout != 10*time.Second {
		t.Errorf("expected TTSFirstAudioTimeout 10s, got %v", cfg.AI.Bailian.TTSFirstAudioTimeout)
	}
	if cfg.AI.Bailian.TTSSentenceTimeout != 15*time.Second {
		t.Errorf("expected TTSSentenceTimeout 15s, got %v", cfg.AI.Bailian.TTSSentenceTimeout)
	}

	if cfg.Proxy.Enabled {
		t.Errorf("expected Proxy.Enabled to be false")
	}
	if cfg.Proxy.URL != "" {
		t.Errorf("expected Proxy.URL to be empty, got %s", cfg.Proxy.URL)
	}
}

func TestLoad_ExampleConfigFile(t *testing.T) {
	examplePath := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := config.Load(examplePath)
	if err != nil {
		t.Fatalf("unexpected error loading example config file: %v", err)
	}

	if cfg.Server.ListenAddr != ":8080" {
		t.Errorf("expected ListenAddr :8080, got %s", cfg.Server.ListenAddr)
	}
	if cfg.Server.MaxConcurrentSessions != 10 {
		t.Errorf("expected MaxConcurrentSessions 10, got %d", cfg.Server.MaxConcurrentSessions)
	}
	if cfg.AI.Bailian.ASRModel != "qwen-audio-3.0-asr-flash-streaming" {
		t.Errorf("expected ASRModel match, got %s", cfg.AI.Bailian.ASRModel)
	}
}

func TestLoadFromReader_UnknownField(t *testing.T) {
	unknownYAML := `
server:
  listen_addr: ":8080"
  unknown_extra_field: "disallowed"
`
	_, err := config.LoadFromReader(strings.NewReader(unknownYAML))
	if err == nil {
		t.Fatalf("expected error for unknown yaml field, got nil")
	}
}

func TestLoadFromReader_EnvironmentVariables(t *testing.T) {
	const (
		mockAPIKey = "mock-dashscope-key-12345"
		mockToken  = "mock-device-token-67890"
	)

	t.Setenv(config.EnvDashScopeAPIKey, mockAPIKey)
	t.Setenv(config.EnvDeviceSharedToken, mockToken)

	cfg, err := config.LoadFromReader(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.DashScopeAPIKey != mockAPIKey {
		t.Errorf("expected DashScopeAPIKey %s, got %s", mockAPIKey, cfg.DashScopeAPIKey)
	}
	if cfg.DeviceSharedToken != mockToken {
		t.Errorf("expected DeviceSharedToken %s, got %s", mockToken, cfg.DeviceSharedToken)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	nonExistentPath := filepath.Join(os.TempDir(), "non_existent_config_file_xyz.yaml")
	_, err := config.Load(nonExistentPath)
	if err == nil {
		t.Fatalf("expected error loading non existent file, got nil")
	}
}
