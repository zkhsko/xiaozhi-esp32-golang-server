package audio

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/hraban/opus"
)

const (
	// DownlinkSampleRate 下行音频采样率（24000 Hz）。
	DownlinkSampleRate = 24000

	// DownlinkChannels 下行音频声道数（单声道）。
	DownlinkChannels = 1

	// DownlinkFrameDurationMs 下行音频单帧时长（60 ms）。
	DownlinkFrameDurationMs = 60

	// DownlinkSamplesPerFrame 下行音频单帧采样点数（1440 个 int16 采样点）。
	DownlinkSamplesPerFrame = DownlinkSampleRate * DownlinkFrameDurationMs / 1000 // 1440

	// DownlinkBytesPerFrame 下行单帧 16-bit PCM 字节数（2880 字节）。
	DownlinkBytesPerFrame = DownlinkSamplesPerFrame * 2 // 2880
)

var (
	// ErrInvalidPCMBytes 表示传入的 PCM 字节切片长度与下行标准帧长（2880 字节）不匹配。
	ErrInvalidPCMBytes = errors.New("invalid pcm frame size")

	// ErrEncodeFailed 表示 Opus 音频编码失败。
	ErrEncodeFailed = errors.New("failed to encode opus packet")
)

// Encoder 负责将 24 kHz 60 ms 单声道 16-bit signed little-endian PCM 帧编码为单个 Opus 包。
// 每个会话独立创建并持有 Encoder 实例。
type Encoder struct {
	enc            *opus.Encoder
	maxPacketBytes int
	pcmBuf         []int16
	opusBuf        []byte
}

// NewEncoder 创建配置就绪的 24 kHz 单声道 Opus 编码器。
func NewEncoder(maxPacketBytes int) (*Encoder, error) {
	if maxPacketBytes <= 0 {
		maxPacketBytes = DefaultMaxOpusPacketBytes
	}

	enc, err := opus.NewEncoder(DownlinkSampleRate, DownlinkChannels, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("initialize opus encoder: %w", err)
	}

	return &Encoder{
		enc:            enc,
		maxPacketBytes: maxPacketBytes,
		pcmBuf:         make([]int16, DownlinkSamplesPerFrame),
		opusBuf:        make([]byte, maxPacketBytes),
	}, nil
}

// MaxPacketBytes 返回当前编码器配置的单包最大字节数上限。
func (e *Encoder) MaxPacketBytes() int {
	return e.maxPacketBytes
}

// Encode 将单帧 2880 字节 16-bit signed little-endian PCM 编码为单个 Opus 包。
// 传入数据必须严格为 2880 字节；返回的切片为独立分配的内存，确保跨异步边界安全。
func (e *Encoder) Encode(pcmFrame []byte) ([]byte, error) {
	if len(pcmFrame) != DownlinkBytesPerFrame {
		return nil, fmt.Errorf("%w: expected %d bytes, got %d", ErrInvalidPCMBytes, DownlinkBytesPerFrame, len(pcmFrame))
	}

	for i := 0; i < DownlinkSamplesPerFrame; i++ {
		e.pcmBuf[i] = int16(binary.LittleEndian.Uint16(pcmFrame[i*2 : i*2+2]))
	}

	n, err := e.enc.Encode(e.pcmBuf, e.opusBuf)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}

	packet := make([]byte, n)
	copy(packet, e.opusBuf[:n])
	return packet, nil
}

// EncodeSamples 将 1440 个 int16 采样点直接编码为单个 Opus 包。
func (e *Encoder) EncodeSamples(samples []int16) ([]byte, error) {
	if len(samples) != DownlinkSamplesPerFrame {
		return nil, fmt.Errorf("%w: expected %d samples, got %d", ErrInvalidSampleCount, DownlinkSamplesPerFrame, len(samples))
	}

	n, err := e.enc.Encode(samples, e.opusBuf)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}

	packet := make([]byte, n)
	copy(packet, e.opusBuf[:n])
	return packet, nil
}

// PCMFramer 负责将任意大小分块（包括奇偶字节分块、非 2880 对齐）的 16-bit PCM 字节流组装为 2880 字节的标准帧。
type PCMFramer struct {
	buf []byte
}

// NewPCMFramer 创建新的 PCM 分帧器实例。
func NewPCMFramer() *PCMFramer {
	return &PCMFramer{
		buf: make([]byte, 0, DownlinkBytesPerFrame*2),
	}
}

// Feed 接收任意大小的 PCM 数据块（包括奇数字节、非 2880 对齐），
// 每积攒满 2880 字节（1440 采样点）产出一帧完整 PCM。
// 返回的每一帧切片均独立分配内存。
func (f *PCMFramer) Feed(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}

	f.buf = append(f.buf, chunk...)
	var frames [][]byte
	for len(f.buf) >= DownlinkBytesPerFrame {
		frame := make([]byte, DownlinkBytesPerFrame)
		copy(frame, f.buf[:DownlinkBytesPerFrame])
		frames = append(frames, frame)
		f.buf = f.buf[DownlinkBytesPerFrame:]
	}

	if len(f.buf) == 0 && cap(f.buf) > DownlinkBytesPerFrame*4 {
		f.buf = make([]byte, 0, DownlinkBytesPerFrame*2)
	}

	return frames
}

// Flush 在音频输入结束时调用。
// 若有未满 2880 字节的残余 PCM（哪怕是 1 字节），用静音（0x00）补齐至 2880 字节并返回最后一帧；
// 若残余为 0 字节，则返回 nil（不补多余静音帧）。
// 调用后清空缓冲区。
func (f *PCMFramer) Flush() [][]byte {
	if len(f.buf) == 0 {
		return nil
	}

	frame := make([]byte, DownlinkBytesPerFrame)
	copy(frame, f.buf)
	f.buf = f.buf[:0]
	return [][]byte{frame}
}

// Reset 清空分帧器内部缓冲区。
func (f *PCMFramer) Reset() {
	f.buf = f.buf[:0]
}

// BufferedBytes 返回当前缓冲区中尚未成帧的残余字节数。
func (f *PCMFramer) BufferedBytes() int {
	return len(f.buf)
}

// StreamEncoder 组合了 Opus 编码器与 PCM 分帧器，支持流式输入任意大小的 PCM 块并输出编码后的 Opus 包。
type StreamEncoder struct {
	encoder *Encoder
	framer  *PCMFramer
}

// NewStreamEncoder 创建与指定编码器绑定的流式分帧编码器。
func NewStreamEncoder(encoder *Encoder) *StreamEncoder {
	return &StreamEncoder{
		encoder: encoder,
		framer:  NewPCMFramer(),
	}
}

// Feed 接收任意大小 PCM 块，若积攒出完整帧则执行 Opus 编码并返回 Opus 包列表。
// 每个返回的 Opus 包拥有独立分配的内存。
func (s *StreamEncoder) Feed(chunk []byte) ([][]byte, error) {
	if s.encoder == nil {
		return nil, errors.New("encoder is nil")
	}

	frames := s.framer.Feed(chunk)
	if len(frames) == 0 {
		return nil, nil
	}

	packets := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		pkt, err := s.encoder.Encode(frame)
		if err != nil {
			return nil, err
		}
		packets = append(packets, pkt)
	}
	return packets, nil
}

// Flush 在音频流结束时刷新残余数据。若有不足一帧的数据，补静音编码输出最后一包 Opus；若无残余则返回 nil。
func (s *StreamEncoder) Flush() ([][]byte, error) {
	if s.encoder == nil {
		return nil, errors.New("encoder is nil")
	}

	frames := s.framer.Flush()
	if len(frames) == 0 {
		return nil, nil
	}

	packets := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		pkt, err := s.encoder.Encode(frame)
		if err != nil {
			return nil, err
		}
		packets = append(packets, pkt)
	}
	return packets, nil
}

// Reset 清空内部未成帧的残余数据。
func (s *StreamEncoder) Reset() {
	s.framer.Reset()
}

// BufferedBytes 返回内部尚未成帧的 PCM 残余字节数。
func (s *StreamEncoder) BufferedBytes() int {
	return s.framer.BufferedBytes()
}

// Encoder 返回底层持有的 Opus 编码器。
func (s *StreamEncoder) Encoder() *Encoder {
	return s.encoder
}
