package audio

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/hraban/opus"
)

// generateSinePCM 在内存中生成 16 kHz 单声道 60 ms（960 采样点）的正弦波 PCM。
func generateSinePCM(freq float64, amp float64) []int16 {
	pcm := make([]int16, UplinkSamplesPerFrame)
	for i := 0; i < UplinkSamplesPerFrame; i++ {
		t := float64(i) / float64(UplinkSampleRate)
		val := amp * math.Sin(2.0*math.Pi*freq*t)
		pcm[i] = int16(val)
	}
	return pcm
}

// generateSilencePCM 在内存中生成 16 kHz 单声道 60 ms（960 采样点）的纯静音 PCM。
func generateSilencePCM() []int16 {
	return make([]int16, UplinkSamplesPerFrame)
}

// encodeOpus 在内存中使用 libopus 编码器将 960 采样点 PCM 编码为单个 Opus 包。
func encodeOpus(t *testing.T, pcm []int16) []byte {
	t.Helper()
	enc, err := opus.NewEncoder(UplinkSampleRate, UplinkChannels, opus.AppVoIP)
	if err != nil {
		t.Fatalf("failed to create opus encoder: %v", err)
	}
	buf := make([]byte, DefaultMaxOpusPacketBytes)
	n, err := enc.Encode(pcm, buf)
	if err != nil {
		t.Fatalf("failed to encode opus packet: %v", err)
	}
	res := make([]byte, n)
	copy(res, buf[:n])
	return res
}

// TestDecoder_SyntheticAudio_SineWave 验证内存合成 440 Hz 正弦波的编码与解码，严格断言 960 采样点与 1920 字节。
func TestDecoder_SyntheticAudio_SineWave(t *testing.T) {
	dec, err := NewDecoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}

	sinePCM := generateSinePCM(440.0, 20000.0)
	opusData := encodeOpus(t, sinePCM)

	if len(opusData) == 0 {
		t.Fatal("encoded opus packet is empty")
	}

	pcmBytes, err := dec.Decode(opusData)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if len(pcmBytes) != UplinkBytesPerFrame {
		t.Fatalf("expected %d bytes, got %d", UplinkBytesPerFrame, len(pcmBytes))
	}

	// 验证解码后的采样点数量与数值有效性
	sampleCount := len(pcmBytes) / 2
	if sampleCount != UplinkSamplesPerFrame {
		t.Fatalf("expected %d samples, got %d", UplinkSamplesPerFrame, sampleCount)
	}

	var hasNonZero bool
	for i := 0; i < sampleCount; i++ {
		sample := int16(binary.LittleEndian.Uint16(pcmBytes[i*2 : i*2+2]))
		if sample != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Error("expected decoded sine wave to contain non-zero samples")
	}
}

// TestDecoder_SyntheticAudio_Silence 验证内存合成静音音频的编码与解码。
func TestDecoder_SyntheticAudio_Silence(t *testing.T) {
	dec, err := NewDecoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}

	silencePCM := generateSilencePCM()
	opusData := encodeOpus(t, silencePCM)

	pcmBytes, err := dec.Decode(opusData)
	if err != nil {
		t.Fatalf("decode silence failed: %v", err)
	}

	if len(pcmBytes) != UplinkBytesPerFrame {
		t.Fatalf("expected %d bytes, got %d", UplinkBytesPerFrame, len(pcmBytes))
	}
}

// TestDecoder_MultipleConsecutiveFrames 验证连续多帧合成音频解码的稳定性与状态延续性。
func TestDecoder_MultipleConsecutiveFrames(t *testing.T) {
	dec, err := NewDecoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}

	for frame := 0; frame < 10; frame++ {
		pcmIn := generateSinePCM(float64(300+frame*50), 15000.0)
		opusData := encodeOpus(t, pcmIn)

		pcmOut, err := dec.Decode(opusData)
		if err != nil {
			t.Fatalf("frame %d decode failed: %v", frame, err)
		}
		if len(pcmOut) != UplinkBytesPerFrame {
			t.Fatalf("frame %d expected %d bytes, got %d", frame, UplinkBytesPerFrame, len(pcmOut))
		}
	}
}

// TestDecoder_ErrorBoundaries 表驱动测试空包、超限包、损坏包等所有错误边界。
func TestDecoder_ErrorBoundaries(t *testing.T) {
	dec, err := NewDecoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}

	tests := []struct {
		name        string
		data        []byte
		expectedErr error
	}{
		{
			name:        "nil 切片返回 ErrEmptyPacket",
			data:        nil,
			expectedErr: ErrEmptyPacket,
		},
		{
			name:        "0 字节切片返回 ErrEmptyPacket",
			data:        []byte{},
			expectedErr: ErrEmptyPacket,
		},
		{
			name:        "超过 1024 字节返回 ErrPacketTooLarge",
			data:        make([]byte, DefaultMaxOpusPacketBytes+1),
			expectedErr: ErrPacketTooLarge,
		},
		{
			name:        "极长包 4096 字节返回 ErrPacketTooLarge",
			data:        make([]byte, 4096),
			expectedErr: ErrPacketTooLarge,
		},
		{
			name:        "损坏乱码数据返回 ErrDecodeFailed",
			data:        []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB, 0xFA},
			expectedErr: ErrDecodeFailed,
		},
		{
			name:        "非法单字节返回非 60ms 帧错误 ErrInvalidSampleCount",
			data:        []byte{0x00},
			expectedErr: ErrInvalidSampleCount,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dec.Decode(tt.data)
			if err == nil {
				t.Fatalf("expected error %v, got nil", tt.expectedErr)
			}
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("expected error %v, got %v", tt.expectedErr, err)
			}
		})
	}
}

// TestDecoder_CustomMaxPacketBytes 验证自定义单包上限生效。
func TestDecoder_CustomMaxPacketBytes(t *testing.T) {
	customMax := 512
	dec, err := NewDecoder(customMax)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}

	if dec.MaxPacketBytes() != customMax {
		t.Errorf("expected max %d, got %d", customMax, dec.MaxPacketBytes())
	}

	// 513 字节包超限
	_, err = dec.Decode(make([]byte, 513))
	if !errors.Is(err, ErrPacketTooLarge) {
		t.Errorf("expected ErrPacketTooLarge, got %v", err)
	}

	// 默认参数值补全
	decDef, err := NewDecoder(0)
	if err != nil {
		t.Fatalf("failed to create default decoder: %v", err)
	}
	if decDef.MaxPacketBytes() != DefaultMaxOpusPacketBytes {
		t.Errorf("expected default max %d, got %d", DefaultMaxOpusPacketBytes, decDef.MaxPacketBytes())
	}
}
