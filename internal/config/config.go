package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// 敏感配置所需的环境变量名称。
const (
	EnvDashScopeAPIKey   = "DASHSCOPE_API_KEY"
	EnvDeviceSharedToken = "DEVICE_SHARED_TOKEN"
)

// 合法取值区间边界常量定义。
const (
	minConcurrentSessions = 1
	maxConcurrentSessions = 10000

	minShutdownTimeout = 1 * time.Second
	maxShutdownTimeout = 60 * time.Second

	minHTTPReadTimeout = 5 * time.Second
	maxHTTPReadTimeout = 60 * time.Second

	minHTTPWriteTimeout = 5 * time.Second
	maxHTTPWriteTimeout = 60 * time.Second

	minHTTPIdleTimeout = 10 * time.Second
	maxHTTPIdleTimeout = 300 * time.Second

	minHTTPBodyBytes = 1024             // 1 KiB
	maxHTTPBodyBytes = 10 * 1024 * 1024 // 10 MiB

	minHTTPHeaderBytes = 128
	maxHTTPHeaderBytes = 8192

	minHelloTimeout = 3 * time.Second
	maxHelloTimeout = 30 * time.Second

	minWSTextMessageBytes = 4096   // 4 KiB
	maxWSTextMessageBytes = 524288 // 512 KiB

	minOpusPacketBytes = 128
	maxOpusPacketBytes = 4096

	minListeningDuration = 5 * time.Second
	maxListeningDuration = 120 * time.Second

	minPCMQueueCapacity = 20
	maxPCMQueueCapacity = 500

	minHistoryTurns = 1
	maxHistoryTurns = 50

	minASRConnectTimeout = 3 * time.Second
	maxASRConnectTimeout = 30 * time.Second

	minTTSConnectTimeout = 3 * time.Second
	maxTTSConnectTimeout = 30 * time.Second

	minLLMFirstTokenTimeout = 3 * time.Second
	maxLLMFirstTokenTimeout = 30 * time.Second

	minLLMOverallTimeout = 10 * time.Second
	maxLLMOverallTimeout = 180 * time.Second

	minTTSFirstAudioTimeout = 3 * time.Second
	maxTTSFirstAudioTimeout = 30 * time.Second

	minTTSSentenceTimeout = 5 * time.Second
	maxTTSSentenceTimeout = 60 * time.Second

	minASRResultTimeout = 1 * time.Second
	maxASRResultTimeout = 30 * time.Second
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
	ASRResultTimeout          time.Duration `yaml:"asr_result_timeout,omitempty"`
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

// Load 从指定路径的 YAML 文件加载非敏感配置，合并环境变量中的敏感凭据并完成校验。
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file %s: %w", path, err)
	}
	defer file.Close()

	return LoadFromReader(file)
}

// LoadFromReader 从 io.Reader 解析 YAML 配置，合并环境变量并完成全面校验。
func LoadFromReader(r io.Reader) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode yaml config: %w", err)
	}

	cfg.DashScopeAPIKey = os.Getenv(EnvDashScopeAPIKey)
	cfg.DeviceSharedToken = os.Getenv(EnvDeviceSharedToken)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// Validate 检查配置的完整性与合法性。
func (c *Config) Validate() error {
	if err := c.validateServer(); err != nil {
		return fmt.Errorf("server config: %w", err)
	}
	if err := c.validateSession(); err != nil {
		return fmt.Errorf("session config: %w", err)
	}
	if err := c.validateAI(); err != nil {
		return fmt.Errorf("ai config: %w", err)
	}
	if err := c.validateProxy(); err != nil {
		return fmt.Errorf("proxy config: %w", err)
	}
	if err := c.validateCredentials(); err != nil {
		return fmt.Errorf("credentials: %w", err)
	}
	return nil
}

func (c *Config) validateServer() error {
	if strings.TrimSpace(c.Server.ListenAddr) == "" {
		return errors.New("listen_addr is required")
	}
	if err := validateWebSocketURL(c.Server.WebSocketURL); err != nil {
		return fmt.Errorf("websocket_url: %w", err)
	}
	if err := validateInt("max_concurrent_sessions", c.Server.MaxConcurrentSessions, minConcurrentSessions, maxConcurrentSessions); err != nil {
		return err
	}
	if err := validateDuration("shutdown_timeout", c.Server.ShutdownTimeout, minShutdownTimeout, maxShutdownTimeout); err != nil {
		return err
	}
	if err := validateDuration("http_read_timeout", c.Server.HTTPReadTimeout, minHTTPReadTimeout, maxHTTPReadTimeout); err != nil {
		return err
	}
	if err := validateDuration("http_write_timeout", c.Server.HTTPWriteTimeout, minHTTPWriteTimeout, maxHTTPWriteTimeout); err != nil {
		return err
	}
	if err := validateDuration("http_idle_timeout", c.Server.HTTPIdleTimeout, minHTTPIdleTimeout, maxHTTPIdleTimeout); err != nil {
		return err
	}
	if err := validateInt64("max_http_body_bytes", c.Server.MaxHTTPBodyBytes, minHTTPBodyBytes, maxHTTPBodyBytes); err != nil {
		return err
	}
	if err := validateInt("max_http_header_bytes", c.Server.MaxHTTPHeaderBytes, minHTTPHeaderBytes, maxHTTPHeaderBytes); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateSession() error {
	if err := validateDuration("hello_timeout", c.Session.HelloTimeout, minHelloTimeout, maxHelloTimeout); err != nil {
		return err
	}
	if err := validateInt64("max_ws_text_message_bytes", c.Session.MaxWSTextMessageBytes, minWSTextMessageBytes, maxWSTextMessageBytes); err != nil {
		return err
	}
	if err := validateInt("max_opus_packet_bytes", c.Session.MaxOpusPacketBytes, minOpusPacketBytes, maxOpusPacketBytes); err != nil {
		return err
	}
	if err := validateDuration("max_listening_duration", c.Session.MaxListeningDuration, minListeningDuration, maxListeningDuration); err != nil {
		return err
	}
	if c.Session.ASRResultTimeout > 0 {
		if err := validateDuration("asr_result_timeout", c.Session.ASRResultTimeout, minASRResultTimeout, maxASRResultTimeout); err != nil {
			return err
		}
	}
	if err := validateInt("asr_pcm_queue_capacity", c.Session.ASRPCMQueueCapacity, minPCMQueueCapacity, maxPCMQueueCapacity); err != nil {
		return err
	}
	if err := validateInt("tts_pcm_queue_capacity", c.Session.TTSPCMQueueCapacity, minPCMQueueCapacity, maxPCMQueueCapacity); err != nil {
		return err
	}
	if err := validateInt("downlink_opus_queue_capacity", c.Session.DownlinkOpusQueueCapacity, minPCMQueueCapacity, maxPCMQueueCapacity); err != nil {
		return err
	}
	if err := validateInt("max_history_turns", c.Session.MaxHistoryTurns, minHistoryTurns, maxHistoryTurns); err != nil {
		return err
	}
	if strings.TrimSpace(c.Session.SystemPrompt) == "" {
		return errors.New("system_prompt is required")
	}
	return nil
}

func (c *Config) validateAI() error {
	b := &c.AI.Bailian
	if err := validateWebSocketURL(b.WSEndpoint); err != nil {
		return fmt.Errorf("ws_endpoint: %w", err)
	}
	if err := validateHTTPURL(b.LLMEndpoint); err != nil {
		return fmt.Errorf("llm_endpoint: %w", err)
	}
	if strings.TrimSpace(b.ASRModel) == "" {
		return errors.New("asr_model is required")
	}
	if strings.TrimSpace(b.LLMModel) == "" {
		return errors.New("llm_model is required")
	}
	if strings.TrimSpace(b.TTSModel) == "" {
		return errors.New("tts_model is required")
	}
	if strings.TrimSpace(b.TTSVoice) == "" {
		return errors.New("tts_voice is required")
	}
	if err := validateDuration("asr_connect_timeout", b.ASRConnectTimeout, minASRConnectTimeout, maxASRConnectTimeout); err != nil {
		return err
	}
	if err := validateDuration("tts_connect_timeout", b.TTSConnectTimeout, minTTSConnectTimeout, maxTTSConnectTimeout); err != nil {
		return err
	}
	if err := validateDuration("llm_first_token_timeout", b.LLMFirstTokenTimeout, minLLMFirstTokenTimeout, maxLLMFirstTokenTimeout); err != nil {
		return err
	}
	if err := validateDuration("llm_overall_timeout", b.LLMOverallTimeout, minLLMOverallTimeout, maxLLMOverallTimeout); err != nil {
		return err
	}
	if b.LLMOverallTimeout <= b.LLMFirstTokenTimeout {
		return fmt.Errorf("llm_overall_timeout (%v) must be greater than llm_first_token_timeout (%v)", b.LLMOverallTimeout, b.LLMFirstTokenTimeout)
	}
	if err := validateDuration("tts_first_audio_timeout", b.TTSFirstAudioTimeout, minTTSFirstAudioTimeout, maxTTSFirstAudioTimeout); err != nil {
		return err
	}
	if err := validateDuration("tts_sentence_timeout", b.TTSSentenceTimeout, minTTSSentenceTimeout, maxTTSSentenceTimeout); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateProxy() error {
	if !c.Proxy.Enabled {
		if c.Proxy.URL != "" {
			if err := validateProxyURL(c.Proxy.URL); err != nil {
				return fmt.Errorf("url: %w", err)
			}
		}
		return nil
	}
	if strings.TrimSpace(c.Proxy.URL) == "" {
		return errors.New("url is required when proxy is enabled")
	}
	if err := validateProxyURL(c.Proxy.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	return nil
}

func (c *Config) validateCredentials() error {
	if strings.TrimSpace(c.DashScopeAPIKey) == "" {
		return fmt.Errorf("dashscope api key is required (environment variable %s)", EnvDashScopeAPIKey)
	}
	if strings.TrimSpace(c.DeviceSharedToken) == "" {
		return fmt.Errorf("device shared token is required (environment variable %s)", EnvDeviceSharedToken)
	}
	return nil
}

func validateWebSocketURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url format: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("scheme must be ws or wss, got %s", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("url host is required")
	}
	return nil
}

func validateHTTPURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url format: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %s", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("url host is required")
	}
	return nil
}

func validateProxyURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url format: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("scheme must be http, https, socks5, or socks5h, got %s", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("url host is required")
	}
	return nil
}

func validateDuration(name string, val, min, max time.Duration) error {
	if val < min || val > max {
		return fmt.Errorf("%s must be between %v and %v, got %v", name, min, max, val)
	}
	return nil
}

func validateInt(name string, val, min, max int) error {
	if val < min || val > max {
		return fmt.Errorf("%s must be between %d and %d, got %d", name, min, max, val)
	}
	return nil
}

func validateInt64(name string, val, min, max int64) error {
	if val < min || val > max {
		return fmt.Errorf("%s must be between %d and %d, got %d", name, min, max, val)
	}
	return nil
}
