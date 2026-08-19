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

func newValidConfig() config.Config {
	return config.Config{
		Server: config.ServerConfig{
			ListenAddr:            ":8080",
			WebSocketURL:          "wss://example.com/xiaozhi/v1/",
			MaxConcurrentSessions: 10,
			ShutdownTimeout:       10 * time.Second,
			HTTPReadTimeout:       15 * time.Second,
			HTTPWriteTimeout:      30 * time.Second,
			HTTPIdleTimeout:       60 * time.Second,
			MaxHTTPBodyBytes:      65536,
			MaxHTTPHeaderBytes:    1024,
		},
		Session: config.SessionConfig{
			HelloTimeout:              10 * time.Second,
			MaxWSTextMessageBytes:     32768,
			MaxOpusPacketBytes:        1024,
			MaxListeningDuration:      30 * time.Second,
			ASRPCMQueueCapacity:       100,
			TTSPCMQueueCapacity:       100,
			DownlinkOpusQueueCapacity: 100,
			MaxHistoryTurns:           6,
			SystemPrompt:              "你是小智，一个智能语音助手。请用简明、友好的中文回答，回答适合直接语音朗读。",
		},
		AI: config.AIConfig{
			Bailian: config.BailianConfig{
				WSEndpoint:           "wss://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/api-ws/v1/inference",
				LLMEndpoint:          "https://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
				ASRModel:             "qwen-audio-3.0-asr-flash-streaming",
				LLMModel:             "qwen3.7-flash",
				TTSModel:             "qwen-audio-3.0-tts-flash",
				TTSVoice:             "longanlingxi",
				ASRConnectTimeout:    10 * time.Second,
				TTSConnectTimeout:    10 * time.Second,
				LLMFirstTokenTimeout: 15 * time.Second,
				LLMOverallTimeout:    60 * time.Second,
				TTSFirstAudioTimeout: 10 * time.Second,
				TTSSentenceTimeout:   15 * time.Second,
			},
		},
		Proxy: config.ProxyConfig{
			Enabled: false,
			URL:     "",
		},
		DashScopeAPIKey:   "test-dashscope-api-key",
		DeviceSharedToken: "test-device-shared-token",
	}
}

func TestLoadFromReader_Valid(t *testing.T) {
	t.Setenv(config.EnvDashScopeAPIKey, "test-dashscope-api-key")
	t.Setenv(config.EnvDeviceSharedToken, "test-device-shared-token")

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
	if cfg.DashScopeAPIKey != "test-dashscope-api-key" {
		t.Errorf("expected DashScopeAPIKey match, got %s", cfg.DashScopeAPIKey)
	}
	if cfg.DeviceSharedToken != "test-device-shared-token" {
		t.Errorf("expected DeviceSharedToken match, got %s", cfg.DeviceSharedToken)
	}
}

func TestLoad_ExampleConfigFile(t *testing.T) {
	t.Setenv(config.EnvDashScopeAPIKey, "test-dashscope-api-key")
	t.Setenv(config.EnvDeviceSharedToken, "test-device-shared-token")

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

func TestLoadFromReader_MissingCredentials(t *testing.T) {
	t.Setenv(config.EnvDashScopeAPIKey, "")
	t.Setenv(config.EnvDeviceSharedToken, "")

	_, err := config.LoadFromReader(strings.NewReader(validYAML))
	if err == nil {
		t.Fatalf("expected error when credentials missing, got nil")
	}
	if !strings.Contains(err.Error(), "dashscope api key is required") {
		t.Errorf("expected error message to mention missing dashscope api key, got %v", err)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	nonExistentPath := filepath.Join(os.TempDir(), "non_existent_config_file_xyz.yaml")
	_, err := config.Load(nonExistentPath)
	if err == nil {
		t.Fatalf("expected error loading non existent file, got nil")
	}
}

func TestConfig_Validate_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		modify      func(c *config.Config)
		expectError string
	}{
		// 标准成功路径
		{
			name:        "valid configuration",
			modify:      func(c *config.Config) {},
			expectError: "",
		},
		{
			name: "proxy enabled with valid http url",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = true
				c.Proxy.URL = "http://127.0.0.1:1080"
			},
			expectError: "",
		},
		{
			name: "proxy enabled with valid socks5 url",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = true
				c.Proxy.URL = "socks5://127.0.0.1:1080"
			},
			expectError: "",
		},
		{
			name: "proxy enabled with valid socks5h url",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = true
				c.Proxy.URL = "socks5h://127.0.0.1:1080"
			},
			expectError: "",
		},
		{
			name: "proxy disabled with valid url",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = false
				c.Proxy.URL = "http://127.0.0.1:1080"
			},
			expectError: "",
		},

		// 1. 必填项缺失
		{
			name: "server listen_addr empty",
			modify: func(c *config.Config) {
				c.Server.ListenAddr = ""
			},
			expectError: "listen_addr is required",
		},
		{
			name: "server listen_addr whitespace only",
			modify: func(c *config.Config) {
				c.Server.ListenAddr = "   "
			},
			expectError: "listen_addr is required",
		},
		{
			name: "server websocket_url empty",
			modify: func(c *config.Config) {
				c.Server.WebSocketURL = ""
			},
			expectError: "url is required",
		},
		{
			name: "session system_prompt empty",
			modify: func(c *config.Config) {
				c.Session.SystemPrompt = ""
			},
			expectError: "system_prompt is required",
		},
		{
			name: "bailian ws_endpoint empty",
			modify: func(c *config.Config) {
				c.AI.Bailian.WSEndpoint = ""
			},
			expectError: "url is required",
		},
		{
			name: "bailian llm_endpoint empty",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMEndpoint = ""
			},
			expectError: "url is required",
		},
		{
			name: "bailian asr_model empty",
			modify: func(c *config.Config) {
				c.AI.Bailian.ASRModel = ""
			},
			expectError: "asr_model is required",
		},
		{
			name: "bailian llm_model empty",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMModel = ""
			},
			expectError: "llm_model is required",
		},
		{
			name: "bailian tts_model empty",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSModel = ""
			},
			expectError: "tts_model is required",
		},
		{
			name: "bailian tts_voice empty",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSVoice = ""
			},
			expectError: "tts_voice is required",
		},
		{
			name: "proxy enabled but url empty",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = true
				c.Proxy.URL = ""
			},
			expectError: "url is required when proxy is enabled",
		},
		{
			name: "credentials dashscope api key empty",
			modify: func(c *config.Config) {
				c.DashScopeAPIKey = ""
			},
			expectError: "dashscope api key is required",
		},
		{
			name: "credentials dashscope api key whitespace only",
			modify: func(c *config.Config) {
				c.DashScopeAPIKey = "   "
			},
			expectError: "dashscope api key is required",
		},
		{
			name: "credentials device shared token empty",
			modify: func(c *config.Config) {
				c.DeviceSharedToken = ""
			},
			expectError: "device shared token is required",
		},
		{
			name: "credentials device shared token whitespace only",
			modify: func(c *config.Config) {
				c.DeviceSharedToken = "   "
			},
			expectError: "device shared token is required",
		},

		// 2. 非法 URL 格式与 Scheme 校验
		{
			name: "server websocket_url invalid scheme http",
			modify: func(c *config.Config) {
				c.Server.WebSocketURL = "http://example.com/xiaozhi/v1/"
			},
			expectError: "scheme must be ws or wss",
		},
		{
			name: "server websocket_url missing host",
			modify: func(c *config.Config) {
				c.Server.WebSocketURL = "wss://"
			},
			expectError: "url host is required",
		},
		{
			name: "bailian ws_endpoint invalid scheme https",
			modify: func(c *config.Config) {
				c.AI.Bailian.WSEndpoint = "https://llm.aliyun.com/ws"
			},
			expectError: "scheme must be ws or wss",
		},
		{
			name: "bailian ws_endpoint missing host",
			modify: func(c *config.Config) {
				c.AI.Bailian.WSEndpoint = "ws://"
			},
			expectError: "url host is required",
		},
		{
			name: "bailian llm_endpoint invalid scheme wss",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMEndpoint = "wss://llm.aliyun.com/compatible-mode/v1"
			},
			expectError: "scheme must be http or https",
		},
		{
			name: "bailian llm_endpoint missing host",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMEndpoint = "https://"
			},
			expectError: "url host is required",
		},
		{
			name: "proxy enabled with invalid scheme ftp",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = true
				c.Proxy.URL = "ftp://127.0.0.1:21"
			},
			expectError: "scheme must be http, https, socks5, or socks5h",
		},
		{
			name: "proxy enabled with missing host",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = true
				c.Proxy.URL = "http://"
			},
			expectError: "url host is required",
		},
		{
			name: "proxy disabled with invalid scheme",
			modify: func(c *config.Config) {
				c.Proxy.Enabled = false
				c.Proxy.URL = "ftp://127.0.0.1:21"
			},
			expectError: "scheme must be http, https, socks5, or socks5h",
		},

		// 3. 非正数与合法区间边界校验
		// ServerConfig
		{
			name: "server max_concurrent_sessions zero",
			modify: func(c *config.Config) {
				c.Server.MaxConcurrentSessions = 0
			},
			expectError: "max_concurrent_sessions must be between 1 and 10000",
		},
		{
			name: "server max_concurrent_sessions negative",
			modify: func(c *config.Config) {
				c.Server.MaxConcurrentSessions = -5
			},
			expectError: "max_concurrent_sessions must be between 1 and 10000",
		},
		{
			name: "server max_concurrent_sessions above max",
			modify: func(c *config.Config) {
				c.Server.MaxConcurrentSessions = 10001
			},
			expectError: "max_concurrent_sessions must be between 1 and 10000",
		},
		{
			name: "server shutdown_timeout below min",
			modify: func(c *config.Config) {
				c.Server.ShutdownTimeout = 500 * time.Millisecond
			},
			expectError: "shutdown_timeout must be between 1s and 1m0s",
		},
		{
			name: "server shutdown_timeout above max",
			modify: func(c *config.Config) {
				c.Server.ShutdownTimeout = 61 * time.Second
			},
			expectError: "shutdown_timeout must be between 1s and 1m0s",
		},
		{
			name: "server http_read_timeout below min",
			modify: func(c *config.Config) {
				c.Server.HTTPReadTimeout = 4 * time.Second
			},
			expectError: "http_read_timeout must be between 5s and 1m0s",
		},
		{
			name: "server http_read_timeout above max",
			modify: func(c *config.Config) {
				c.Server.HTTPReadTimeout = 61 * time.Second
			},
			expectError: "http_read_timeout must be between 5s and 1m0s",
		},
		{
			name: "server http_write_timeout below min",
			modify: func(c *config.Config) {
				c.Server.HTTPWriteTimeout = 4 * time.Second
			},
			expectError: "http_write_timeout must be between 5s and 1m0s",
		},
		{
			name: "server http_write_timeout above max",
			modify: func(c *config.Config) {
				c.Server.HTTPWriteTimeout = 61 * time.Second
			},
			expectError: "http_write_timeout must be between 5s and 1m0s",
		},
		{
			name: "server http_idle_timeout below min",
			modify: func(c *config.Config) {
				c.Server.HTTPIdleTimeout = 9 * time.Second
			},
			expectError: "http_idle_timeout must be between 10s and 5m0s",
		},
		{
			name: "server http_idle_timeout above max",
			modify: func(c *config.Config) {
				c.Server.HTTPIdleTimeout = 301 * time.Second
			},
			expectError: "http_idle_timeout must be between 10s and 5m0s",
		},
		{
			name: "server max_http_body_bytes below min",
			modify: func(c *config.Config) {
				c.Server.MaxHTTPBodyBytes = 1023
			},
			expectError: "max_http_body_bytes must be between 1024 and 10485760",
		},
		{
			name: "server max_http_body_bytes above max",
			modify: func(c *config.Config) {
				c.Server.MaxHTTPBodyBytes = 10485761
			},
			expectError: "max_http_body_bytes must be between 1024 and 10485760",
		},
		{
			name: "server max_http_header_bytes below min",
			modify: func(c *config.Config) {
				c.Server.MaxHTTPHeaderBytes = 127
			},
			expectError: "max_http_header_bytes must be between 128 and 8192",
		},
		{
			name: "server max_http_header_bytes above max",
			modify: func(c *config.Config) {
				c.Server.MaxHTTPHeaderBytes = 8193
			},
			expectError: "max_http_header_bytes must be between 128 and 8192",
		},

		// SessionConfig
		{
			name: "session hello_timeout below min",
			modify: func(c *config.Config) {
				c.Session.HelloTimeout = 2 * time.Second
			},
			expectError: "hello_timeout must be between 3s and 30s",
		},
		{
			name: "session hello_timeout above max",
			modify: func(c *config.Config) {
				c.Session.HelloTimeout = 31 * time.Second
			},
			expectError: "hello_timeout must be between 3s and 30s",
		},
		{
			name: "session max_ws_text_message_bytes below min",
			modify: func(c *config.Config) {
				c.Session.MaxWSTextMessageBytes = 4095
			},
			expectError: "max_ws_text_message_bytes must be between 4096 and 524288",
		},
		{
			name: "session max_ws_text_message_bytes above max",
			modify: func(c *config.Config) {
				c.Session.MaxWSTextMessageBytes = 524289
			},
			expectError: "max_ws_text_message_bytes must be between 4096 and 524288",
		},
		{
			name: "session max_opus_packet_bytes below min",
			modify: func(c *config.Config) {
				c.Session.MaxOpusPacketBytes = 127
			},
			expectError: "max_opus_packet_bytes must be between 128 and 4096",
		},
		{
			name: "session max_opus_packet_bytes above max",
			modify: func(c *config.Config) {
				c.Session.MaxOpusPacketBytes = 4097
			},
			expectError: "max_opus_packet_bytes must be between 128 and 4096",
		},
		{
			name: "session max_listening_duration below min",
			modify: func(c *config.Config) {
				c.Session.MaxListeningDuration = 4 * time.Second
			},
			expectError: "max_listening_duration must be between 5s and 2m0s",
		},
		{
			name: "session max_listening_duration above max",
			modify: func(c *config.Config) {
				c.Session.MaxListeningDuration = 121 * time.Second
			},
			expectError: "max_listening_duration must be between 5s and 2m0s",
		},
		{
			name: "session asr_pcm_queue_capacity below min",
			modify: func(c *config.Config) {
				c.Session.ASRPCMQueueCapacity = 19
			},
			expectError: "asr_pcm_queue_capacity must be between 20 and 500",
		},
		{
			name: "session asr_pcm_queue_capacity above max",
			modify: func(c *config.Config) {
				c.Session.ASRPCMQueueCapacity = 501
			},
			expectError: "asr_pcm_queue_capacity must be between 20 and 500",
		},
		{
			name: "session tts_pcm_queue_capacity below min",
			modify: func(c *config.Config) {
				c.Session.TTSPCMQueueCapacity = 19
			},
			expectError: "tts_pcm_queue_capacity must be between 20 and 500",
		},
		{
			name: "session tts_pcm_queue_capacity above max",
			modify: func(c *config.Config) {
				c.Session.TTSPCMQueueCapacity = 501
			},
			expectError: "tts_pcm_queue_capacity must be between 20 and 500",
		},
		{
			name: "session downlink_opus_queue_capacity below min",
			modify: func(c *config.Config) {
				c.Session.DownlinkOpusQueueCapacity = 19
			},
			expectError: "downlink_opus_queue_capacity must be between 20 and 500",
		},
		{
			name: "session downlink_opus_queue_capacity above max",
			modify: func(c *config.Config) {
				c.Session.DownlinkOpusQueueCapacity = 501
			},
			expectError: "downlink_opus_queue_capacity must be between 20 and 500",
		},
		{
			name: "session max_history_turns below min",
			modify: func(c *config.Config) {
				c.Session.MaxHistoryTurns = 0
			},
			expectError: "max_history_turns must be between 1 and 50",
		},
		{
			name: "session max_history_turns above max",
			modify: func(c *config.Config) {
				c.Session.MaxHistoryTurns = 51
			},
			expectError: "max_history_turns must be between 1 and 50",
		},

		// BailianConfig
		{
			name: "bailian asr_connect_timeout below min",
			modify: func(c *config.Config) {
				c.AI.Bailian.ASRConnectTimeout = 2 * time.Second
			},
			expectError: "asr_connect_timeout must be between 3s and 30s",
		},
		{
			name: "bailian asr_connect_timeout above max",
			modify: func(c *config.Config) {
				c.AI.Bailian.ASRConnectTimeout = 31 * time.Second
			},
			expectError: "asr_connect_timeout must be between 3s and 30s",
		},
		{
			name: "bailian tts_connect_timeout below min",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSConnectTimeout = 2 * time.Second
			},
			expectError: "tts_connect_timeout must be between 3s and 30s",
		},
		{
			name: "bailian tts_connect_timeout above max",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSConnectTimeout = 31 * time.Second
			},
			expectError: "tts_connect_timeout must be between 3s and 30s",
		},
		{
			name: "bailian llm_first_token_timeout below min",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMFirstTokenTimeout = 2 * time.Second
			},
			expectError: "llm_first_token_timeout must be between 3s and 30s",
		},
		{
			name: "bailian llm_first_token_timeout above max",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMFirstTokenTimeout = 31 * time.Second
			},
			expectError: "llm_first_token_timeout must be between 3s and 30s",
		},
		{
			name: "bailian llm_overall_timeout below min",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMOverallTimeout = 9 * time.Second
			},
			expectError: "llm_overall_timeout must be between 10s and 3m0s",
		},
		{
			name: "bailian llm_overall_timeout above max",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMOverallTimeout = 181 * time.Second
			},
			expectError: "llm_overall_timeout must be between 10s and 3m0s",
		},
		{
			name: "bailian tts_first_audio_timeout below min",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSFirstAudioTimeout = 2 * time.Second
			},
			expectError: "tts_first_audio_timeout must be between 3s and 30s",
		},
		{
			name: "bailian tts_first_audio_timeout above max",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSFirstAudioTimeout = 31 * time.Second
			},
			expectError: "tts_first_audio_timeout must be between 3s and 30s",
		},
		{
			name: "bailian tts_sentence_timeout below min",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSSentenceTimeout = 4 * time.Second
			},
			expectError: "tts_sentence_timeout must be between 5s and 1m0s",
		},
		{
			name: "bailian tts_sentence_timeout above max",
			modify: func(c *config.Config) {
				c.AI.Bailian.TTSSentenceTimeout = 61 * time.Second
			},
			expectError: "tts_sentence_timeout must be between 5s and 1m0s",
		},

		// 4. 矛盾超时校验
		{
			name: "bailian conflicting timeout overall equals first token",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMFirstTokenTimeout = 15 * time.Second
				c.AI.Bailian.LLMOverallTimeout = 15 * time.Second
			},
			expectError: "llm_overall_timeout (15s) must be greater than llm_first_token_timeout (15s)",
		},
		{
			name: "bailian conflicting timeout overall less than first token",
			modify: func(c *config.Config) {
				c.AI.Bailian.LLMFirstTokenTimeout = 20 * time.Second
				c.AI.Bailian.LLMOverallTimeout = 15 * time.Second
			},
			expectError: "llm_overall_timeout (15s) must be greater than llm_first_token_timeout (20s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newValidConfig()
			tt.modify(&cfg)

			err := cfg.Validate()
			if tt.expectError == "" {
				if err != nil {
					t.Fatalf("expected valid config, got error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.expectError)
				}
				if !strings.Contains(err.Error(), tt.expectError) {
					t.Fatalf("expected error containing %q, got %q", tt.expectError, err.Error())
				}
			}
		})
	}
}

func TestConfig_Validate_CredentialLeakSafety(t *testing.T) {
	const (
		sensitiveAPIKey = "super-secret-api-key-value-987654321"
		sensitiveToken  = "super-secret-device-token-123456789"
	)

	cfg := newValidConfig()
	cfg.DashScopeAPIKey = sensitiveAPIKey
	cfg.DeviceSharedToken = sensitiveToken
	// 触发某种校验失败
	cfg.Server.ListenAddr = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, sensitiveAPIKey) {
		t.Fatalf("error message leaked sensitive api key: %s", errMsg)
	}
	if strings.Contains(errMsg, sensitiveToken) {
		t.Fatalf("error message leaked sensitive token: %s", errMsg)
	}
}
