package session

import (
	"time"

	"xiaozhi-esp32-golang-server/internal/audio"
)

// 会话管理相关的默认常量。
const (
	DefaultMaxOpusPacketBytes   = 1024
	DefaultMaxListeningDuration = 30 * time.Second
	DefaultASRResultTimeout     = 5 * time.Second
	DefaultMaxHistoryTurns      = 6
	DefaultEventChannelCapacity = 100
)

// SessionConfig 定义单个会话运行所需的不可变配置参数。
type SessionConfig struct {
	HelloTimeout              time.Duration
	MaxWSTextMessageBytes     int64
	MaxOpusPacketBytes        int
	MaxListeningDuration      time.Duration
	ASRResultTimeout          time.Duration
	ASRPCMQueueCapacity       int
	TTSPCMQueueCapacity       int
	DownlinkOpusQueueCapacity int
	MaxHistoryTurns           int
	ListenPromptEnabled       bool
}

// NormalizeConfig 对传入的会话配置进行防御性校验并补齐默认值。
func NormalizeConfig(cfg SessionConfig) SessionConfig {
	if cfg.HelloTimeout <= 0 {
		cfg.HelloTimeout = DefaultHelloTimeout
	}
	if cfg.MaxWSTextMessageBytes <= 0 {
		cfg.MaxWSTextMessageBytes = DefaultMaxWSTextMessageBytes
	}
	if cfg.MaxOpusPacketBytes == 0 {
		cfg.MaxOpusPacketBytes = DefaultMaxOpusPacketBytes
	}
	if cfg.MaxListeningDuration <= 0 {
		cfg.MaxListeningDuration = DefaultMaxListeningDuration
	}
	if cfg.ASRResultTimeout <= 0 {
		cfg.ASRResultTimeout = DefaultASRResultTimeout
	}
	if cfg.ASRPCMQueueCapacity <= 0 {
		cfg.ASRPCMQueueCapacity = audio.DefaultASRPCMQueueCapacity
	}
	if cfg.TTSPCMQueueCapacity <= 0 {
		cfg.TTSPCMQueueCapacity = DefaultWriteQueueCapacity
	}
	if cfg.DownlinkOpusQueueCapacity <= 0 {
		cfg.DownlinkOpusQueueCapacity = DefaultWriteQueueCapacity
	}
	if cfg.MaxHistoryTurns <= 0 {
		cfg.MaxHistoryTurns = DefaultMaxHistoryTurns
	}
	return cfg
}
