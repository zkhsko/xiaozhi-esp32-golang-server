package config

import (
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// 敏感配置所需的环境变量名称。
const (
	EnvDashScopeAPIKey   = "DASHSCOPE_API_KEY"
	EnvDeviceSharedToken = "DEVICE_SHARED_TOKEN"
)

// ServerConfig 定义 HTTP 和 WebSocket 基础服务配置。
type ServerConfig struct {
	ListenAddr            string        `yaml:"listen_addr"`
	WebSocketURL          string        `yaml:"websocket_url"`
	MaxConcurrentSessions int           `yaml:"max_concurrent_sessions"`
	ShutdownTimeout       time.Duration `yaml:"shutdown_timeout"`
	HTTPReadTimeout       time.Duration `yaml:"http_read_timeout"`
	HTTPWriteTimeout      time.Duration `yaml:"http_write_timeout"`
	HTTPIdleTimeout       time.Duration `yaml:"http_idle_timeout"`
	MaxHTTPBodyBytes      int64         `yaml:"max_http_body_bytes"`
	MaxHTTPHeaderBytes    int           `yaml:"max_http_header_bytes"`
}

// SessionConfig 定义会话生命周期、音频队列与上下文配置。
type SessionConfig struct {
	HelloTimeout              time.Duration `yaml:"hello_timeout"`
	MaxWSTextMessageBytes     int64         `yaml:"max_ws_text_message_bytes"`
	MaxOpusPacketBytes        int           `yaml:"max_opus_packet_bytes"`
	MaxListeningDuration      time.Duration `yaml:"max_listening_duration"`
	ASRPCMQueueCapacity       int           `yaml:"asr_pcm_queue_capacity"`
	TTSPCMQueueCapacity       int           `yaml:"tts_pcm_queue_capacity"`
	DownlinkOpusQueueCapacity int           `yaml:"downlink_opus_queue_capacity"`
	MaxHistoryTurns           int           `yaml:"max_history_turns"`
	SystemPrompt              string        `yaml:"system_prompt"`
}

// BailianConfig 定义阿里云百炼 ASR、LLM 与 TTS 模型及超时配置。
type BailianConfig struct {
	WSEndpoint           string        `yaml:"ws_endpoint"`
	LLMEndpoint          string        `yaml:"llm_endpoint"`
	ASRModel             string        `yaml:"asr_model"`
	LLMModel             string        `yaml:"llm_model"`
	TTSModel             string        `yaml:"tts_model"`
	TTSVoice             string        `yaml:"tts_voice"`
	ASRConnectTimeout    time.Duration `yaml:"asr_connect_timeout"`
	TTSConnectTimeout    time.Duration `yaml:"tts_connect_timeout"`
	LLMFirstTokenTimeout time.Duration `yaml:"llm_first_token_timeout"`
	LLMOverallTimeout    time.Duration `yaml:"llm_overall_timeout"`
	TTSFirstAudioTimeout time.Duration `yaml:"tts_first_audio_timeout"`
	TTSSentenceTimeout   time.Duration `yaml:"tts_sentence_timeout"`
}

// AIConfig 定义人工智能模型服务配置。
type AIConfig struct {
	Bailian BailianConfig `yaml:"bailian"`
}

// ProxyConfig 定义出站接口调用的网络代理配置。
type ProxyConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
}

// Config 包含服务端运行所需的全部配置项。
type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Session SessionConfig `yaml:"session"`
	AI      AIConfig      `yaml:"ai"`
	Proxy   ProxyConfig   `yaml:"proxy"`

	// 敏感凭据从环境变量注入，不出现在 YAML 配置文件中
	DashScopeAPIKey   string `yaml:"-"`
	DeviceSharedToken string `yaml:"-"`
}

// Load 从指定路径的 YAML 文件加载非敏感配置，并合并环境变量中的敏感凭据。
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file %s: %w", path, err)
	}
	defer file.Close()

	return LoadFromReader(file)
}

// LoadFromReader 从 io.Reader 解析 YAML 配置并合并环境变量。
func LoadFromReader(r io.Reader) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode yaml config: %w", err)
	}

	cfg.DashScopeAPIKey = os.Getenv(EnvDashScopeAPIKey)
	cfg.DeviceSharedToken = os.Getenv(EnvDeviceSharedToken)

	return &cfg, nil
}
