package audio

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/hraban/opus"
)

const (
	// UplinkSampleRate 上行音频采样率（16000 Hz）。
	UplinkSampleRate = 16000

	// UplinkChannels 上行音频声道数（单声道）。
	UplinkChannels = 1

	// UplinkFrameDurationMs 上行音频单帧时长（60 ms）。
	UplinkFrameDurationMs = 60

	// UplinkSamplesPerFrame 上行音频单帧采样点数（960 个 int16 采样点）。
	UplinkSamplesPerFrame = UplinkSampleRate * UplinkFrameDurationMs / 1000 // 960

	// UplinkBytesPerFrame 上行单帧 16-bit PCM 字节数（1920 字节）。
	UplinkBytesPerFrame = UplinkSamplesPerFrame * 2 // 1920

	// DefaultMaxOpusPacketBytes 默认单 Opus 包最大字节数（1024 字节）。
	DefaultMaxOpusPacketBytes = 1024
)

var (
	// ErrEmptyPacket 表示传入的 Opus 音频包长度为 0。
	ErrEmptyPacket = errors.New("empty opus packet")

	// ErrPacketTooLarge 表示 Opus 音频包大小超过配置上限。
	ErrPacketTooLarge = errors.New("opus packet size exceeds maximum limit")

	// ErrDecodeFailed 表示 Opus 音频包解码失败。
	ErrDecodeFailed = errors.New("failed to decode opus packet")

	// ErrInvalidSampleCount 表示解码出的采样点数与期望的帧长不符。
	ErrInvalidSampleCount = errors.New("decoded invalid sample count")
)

// Decoder 负责将 16 kHz 60 ms 单声道 Opus 数据包解码为 16-bit signed little-endian PCM。
// 每个会话独立创建并持有 Decoder 实例。
type Decoder struct {
	dec            *opus.Decoder
	maxPacketBytes int
	pcmBuf         []int16
}

// NewDecoder 创建配置就绪的 16 kHz 单声道 Opus 解码器。
func NewDecoder(maxPacketBytes int) (*Decoder, error) {
	if maxPacketBytes <= 0 {
		maxPacketBytes = DefaultMaxOpusPacketBytes
	}

	dec, err := opus.NewDecoder(UplinkSampleRate, UplinkChannels)
	if err != nil {
		return nil, fmt.Errorf("initialize opus decoder: %w", err)
	}

	return &Decoder{
		dec:            dec,
		maxPacketBytes: maxPacketBytes,
		pcmBuf:         make([]int16, UplinkSamplesPerFrame),
	}, nil
}

// MaxPacketBytes 返回当前解码器配置的单包最大字节数上限。
func (d *Decoder) MaxPacketBytes() int {
	return d.maxPacketBytes
}

// Decode 将单个 16 kHz 60 ms Opus 包解码为 1920 字节的 16-bit 小端序 PCM 数据。
// 解码前执行空包与超限检查；返回的字节切片为独立拷贝，保证跨异步边界的内存安全。
func (d *Decoder) Decode(opusData []byte) ([]byte, error) {
	if len(opusData) == 0 {
		return nil, ErrEmptyPacket
	}
	if len(opusData) > d.maxPacketBytes {
		return nil, ErrPacketTooLarge
	}

	n, err := d.dec.Decode(opusData, d.pcmBuf)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}
	if n != UplinkSamplesPerFrame {
		return nil, fmt.Errorf("%w: expected %d samples, got %d", ErrInvalidSampleCount, UplinkSamplesPerFrame, n)
	}

	pcmBytes := make([]byte, UplinkBytesPerFrame)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(pcmBytes[i*2:i*2+2], uint16(d.pcmBuf[i]))
	}

	return pcmBytes, nil
}
