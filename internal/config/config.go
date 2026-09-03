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

	minASRResultTimeout = 1 * time.Second
	maxASRResultTimeout = 30 * time.Second

	minDatabaseConns       = 1
	maxDatabaseConns       = 1000
	minDatabasePingTimeout = 500 * time.Millisecond
	maxDatabasePingTimeout = 30 * time.Second
	maxConnLifetime        = 24 * time.Hour
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
}

// DatabaseConfig 定义数据库连接与连接池配置。
type DatabaseConfig struct {
	Driver                string        `yaml:"driver"`
	DSN                   string        `yaml:"dsn"`
	MaxOpenConns          int           `yaml:"max_open_conns"`
	MaxIdleConns          int           `yaml:"max_idle_conns"`
	ConnectionMaxLifetime time.Duration `yaml:"connection_max_lifetime"`
	ConnectionMaxIdleTime time.Duration `yaml:"connection_max_idle_time"`
	PingTimeout           time.Duration `yaml:"ping_timeout"`
}

// Config 包含服务端运行所需的全部基础设施配置项。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Session  SessionConfig  `yaml:"session"`
	Database DatabaseConfig `yaml:"database"`
}

// Load 从指定路径的 YAML 文件加载配置并完成校验。
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file %s: %w", path, err)
	}
	defer file.Close()

	return LoadFromReader(file)
}

// LoadFromReader 从 io.Reader 解析 YAML 配置并完成全面校验。
func LoadFromReader(r io.Reader) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode yaml config: %w", err)
	}

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
	if err := c.validateDatabase(); err != nil {
		return fmt.Errorf("database config: %w", err)
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
	return nil
}

func (c *Config) validateDatabase() error {
	if strings.TrimSpace(c.Database.Driver) == "" {
		return errors.New("driver is required")
	}
	switch c.Database.Driver {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported database driver %q", c.Database.Driver)
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return errors.New("dsn is required")
	}
	if err := validateInt("max_open_conns", c.Database.MaxOpenConns, minDatabaseConns, maxDatabaseConns); err != nil {
		return err
	}
	if err := validateInt("max_idle_conns", c.Database.MaxIdleConns, minDatabaseConns, maxDatabaseConns); err != nil {
		return err
	}
	if c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("max_idle_conns (%d) cannot exceed max_open_conns (%d)", c.Database.MaxIdleConns, c.Database.MaxOpenConns)
	}
	if c.Database.ConnectionMaxLifetime < 0 || c.Database.ConnectionMaxLifetime > maxConnLifetime {
		return fmt.Errorf("connection_max_lifetime must be between 0 and %v, got %v", maxConnLifetime, c.Database.ConnectionMaxLifetime)
	}
	if c.Database.ConnectionMaxIdleTime < 0 || c.Database.ConnectionMaxIdleTime > maxConnLifetime {
		return fmt.Errorf("connection_max_idle_time must be between 0 and %v, got %v", maxConnLifetime, c.Database.ConnectionMaxIdleTime)
	}
	if err := validateDuration("ping_timeout", c.Database.PingTimeout, minDatabasePingTimeout, maxDatabasePingTimeout); err != nil {
		return err
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
