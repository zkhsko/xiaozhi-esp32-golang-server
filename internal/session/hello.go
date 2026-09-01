package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// WebSocket hello 握手相关的协议常量。
const (
	// ExpectedClientAudioFormat 客户端期望的上行音频格式。
	ExpectedClientAudioFormat = "opus"

	// ExpectedClientSampleRate 客户端期望的上行采样率（16 kHz）。
	ExpectedClientSampleRate = 16000

	// ExpectedClientChannels 客户端期望的上行声道数（单声道）。
	ExpectedClientChannels = 1

	// ExpectedClientFrameDuration 客户端期望的上行帧时长（60 ms）。
	ExpectedClientFrameDuration = 60

	// ServerAudioFormat 服务端下行音频格式。
	ServerAudioFormat = "opus"

	// ServerSampleRate 服务端下行采样率（24 kHz）。
	ServerSampleRate = 24000

	// ServerChannels 服务端下行声道数（单声道）。
	ServerChannels = 1

	// ServerFrameDuration 服务端下行帧时长（60 ms）。
	ServerFrameDuration = 60

	// MessageTypeHello hello 握手消息类型。
	MessageTypeHello = "hello"

	// TransportWebSocket 传输层协议标识。
	TransportWebSocket = "websocket"

	// ClientProtocolVersion 客户端协议版本号。
	ClientProtocolVersion = 1

	// DefaultHelloTimeout 默认 hello 握手超时时间。
	DefaultHelloTimeout = 10 * time.Second

	// DefaultMaxWSTextMessageBytes 默认最大 WebSocket 文本消息大小（32 KiB）。
	DefaultMaxWSTextMessageBytes = 32768
)

// 客户端 hello 校验与握手相关的哨兵错误。
var (
	ErrInvalidMessageType   = errors.New("invalid message type, expected hello")
	ErrInvalidProtocolVer   = errors.New("invalid protocol version, expected 1")
	ErrInvalidTransport     = errors.New("invalid transport, expected websocket")
	ErrInvalidAudioFormat   = errors.New("invalid audio format, expected opus")
	ErrInvalidSampleRate    = errors.New("invalid sample rate, expected 16000")
	ErrInvalidChannels      = errors.New("invalid audio channels, expected 1")
	ErrInvalidFrameDuration = errors.New("invalid frame duration, expected 60")
	ErrDuplicateHello       = errors.New("duplicate hello message received")
	ErrHelloTimeout         = errors.New("hello handshake timeout")
	ErrBinaryFirstMessage   = errors.New("first message must be text hello, binary received")
)

// ClientAudioParams 定义客户端声明的上行音频参数。
type ClientAudioParams struct {
	Format        string `json:"format"`
	SampleRate    int    `json:"sample_rate"`
	Channels      int    `json:"channels"`
	FrameDuration int    `json:"frame_duration"`
}

// ClientHelloMessage 定义客户端握手首包 hello 消息结构。
type ClientHelloMessage struct {
	Type        string            `json:"type"`
	Version     int               `json:"version"`
	Transport   string            `json:"transport"`
	AudioParams ClientAudioParams `json:"audio_params"`
}

// ServerAudioParams 定义服务端下行的音频参数。
type ServerAudioParams struct {
	Format        string `json:"format"`
	SampleRate    int    `json:"sample_rate"`
	Channels      int    `json:"channels"`
	FrameDuration int    `json:"frame_duration"`
}

// ServerHelloMessage 定义服务端下发的 hello 握手响应消息。
type ServerHelloMessage struct {
	Type        string            `json:"type"`
	Transport   string            `json:"transport"`
	SessionId   string            `json:"session_id"`
	AudioParams ServerAudioParams `json:"audio_params"`
}

// genericMessageHeader 用于快速提取消息类型。
type genericMessageHeader struct {
	Type string `json:"type"`
}

// GenerateSessionId 生成 16 字节（32 个十六进制字符）加密安全的随机会话 Id。
func GenerateSessionId() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidateClientHello 严格校验客户端 hello 握手消息的各项字段。
func ValidateClientHello(msg *ClientHelloMessage) error {
	if msg == nil {
		return errors.New("nil client hello message")
	}
	if msg.Type != MessageTypeHello {
		return ErrInvalidMessageType
	}
	if msg.Version != ClientProtocolVersion {
		return ErrInvalidProtocolVer
	}
	if msg.Transport != TransportWebSocket {
		return ErrInvalidTransport
	}
	if msg.AudioParams.Format != ExpectedClientAudioFormat {
		return ErrInvalidAudioFormat
	}
	if msg.AudioParams.SampleRate != ExpectedClientSampleRate {
		return ErrInvalidSampleRate
	}
	if msg.AudioParams.Channels != ExpectedClientChannels {
		return ErrInvalidChannels
	}
	if msg.AudioParams.FrameDuration != ExpectedClientFrameDuration {
		return ErrInvalidFrameDuration
	}
	return nil
}

// NewServerHello 构建服务端 hello 握手响应消息对象。
func NewServerHello(sessionId string) ServerHelloMessage {
	return ServerHelloMessage{
		Type:      MessageTypeHello,
		Transport: TransportWebSocket,
		SessionId: sessionId,
		AudioParams: ServerAudioParams{
			Format:        ServerAudioFormat,
			SampleRate:    ServerSampleRate,
			Channels:      ServerChannels,
			FrameDuration: ServerFrameDuration,
		},
	}
}
