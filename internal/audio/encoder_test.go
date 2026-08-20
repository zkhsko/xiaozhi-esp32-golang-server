package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hraban/opus"
)

// generate24kSinePCM 在内存中生成 24 kHz 单声道 60 ms（1440 采样点 / 2880 字节）的正弦波 PCM。
func generate24kSinePCM(freq float64, amp float64) []byte {
	pcmBytes := make([]byte, DownlinkBytesPerFrame)
	for i := 0; i < DownlinkSamplesPerFrame; i++ {
		t := float64(i) / float64(DownlinkSampleRate)
		val := int16(amp * math.Sin(2.0*math.Pi*freq*t))
		binary.LittleEndian.PutUint16(pcmBytes[i*2:i*2+2], uint16(val))
	}
	return pcmBytes
}

// generate24kSilencePCM 在内存中生成 24 kHz 单声道 60 ms（1440 采样点 / 2880 字节）的静音 PCM。
func generate24kSilencePCM() []byte {
	return make([]byte, DownlinkBytesPerFrame)
}

// decode24kOpus 在内存中使用 24 kHz libopus 解码器将 Opus 包解码为 1440 采样点 PCM。
func decode24kOpus(t *testing.T, opusData []byte) []int16 {
	t.Helper()
	dec, err := opus.NewDecoder(DownlinkSampleRate, DownlinkChannels)
	if err != nil {
		t.Fatalf("failed to create opus decoder: %v", err)
	}

	pcm := make([]int16, DownlinkSamplesPerFrame)
	n, err := dec.Decode(opusData, pcm)
	if err != nil {
		t.Fatalf("failed to decode opus packet: %v", err)
	}
	if n != DownlinkSamplesPerFrame {
		t.Fatalf("expected %d decoded samples, got %d", DownlinkSamplesPerFrame, n)
	}
	return pcm
}

// TestEncoder_SingleFrameSineWave 验证单帧 24 kHz 60 ms（2880 字节）正弦波 PCM 的 Opus 编码与解码还原。
func TestEncoder_SingleFrameSineWave(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	if enc.MaxPacketBytes() != DefaultMaxOpusPacketBytes {
		t.Fatalf("expected max packet bytes %d, got %d", DefaultMaxOpusPacketBytes, enc.MaxPacketBytes())
	}

	sinePCM := generate24kSinePCM(440.0, 20000.0)
	packet, err := enc.Encode(sinePCM)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	if len(packet) == 0 {
		t.Fatal("encoded opus packet is empty")
	}
	if len(packet) > DefaultMaxOpusPacketBytes {
		t.Fatalf("encoded opus packet size %d exceeds max limit %d", len(packet), DefaultMaxOpusPacketBytes)
	}

	decodedSamples := decode24kOpus(t, packet)
	var hasNonZero bool
	for _, sample := range decodedSamples {
		if sample != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Error("expected decoded sine wave to contain non-zero samples")
	}
}

// TestEncoder_SingleFrameSilence 验证单帧 2880 字节静音 PCM 的编码与解码。
func TestEncoder_SingleFrameSilence(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	silencePCM := generate24kSilencePCM()
	packet, err := enc.Encode(silencePCM)
	if err != nil {
		t.Fatalf("encode silence failed: %v", err)
	}

	if len(packet) == 0 {
		t.Fatal("encoded silence packet is empty")
	}

	decodedSamples := decode24kOpus(t, packet)
	if len(decodedSamples) != DownlinkSamplesPerFrame {
		t.Fatalf("expected %d decoded samples, got %d", DownlinkSamplesPerFrame, len(decodedSamples))
	}
	for i, sample := range decodedSamples {
		if sample < -5 || sample > 5 {
			t.Fatalf("expected near-zero sample at index %d for silence, got %d", i, sample)
		}
	}
}

// TestEncoder_EncodeSamples 验证直接传入 int16 采样点切片的编码。
func TestEncoder_EncodeSamples(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	samples := make([]int16, DownlinkSamplesPerFrame)
	for i := 0; i < DownlinkSamplesPerFrame; i++ {
		samples[i] = int16(i % 1000)
	}

	packet, err := enc.EncodeSamples(samples)
	if err != nil {
		t.Fatalf("encode samples failed: %v", err)
	}

	if len(packet) == 0 {
		t.Fatal("encoded packet is empty")
	}

	decoded := decode24kOpus(t, packet)
	if len(decoded) != DownlinkSamplesPerFrame {
		t.Fatalf("expected %d samples, got %d", DownlinkSamplesPerFrame, len(decoded))
	}
}

// TestEncoder_InvalidInputs 验证非法大小 PCM 输入的错误拦截。
func TestEncoder_InvalidInputs(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	invalidSizes := []int{0, 1, 100, 1440, 2879, 2881, 5760}
	for _, size := range invalidSizes {
		buf := make([]byte, size)
		_, err := enc.Encode(buf)
		if !errors.Is(err, ErrInvalidPCMBytes) {
			t.Errorf("expected ErrInvalidPCMBytes for size %d, got: %v", size, err)
		}
	}

	invalidSampleCounts := []int{0, 100, 960, 1439, 1441, 2880}
	for _, count := range invalidSampleCounts {
		s := make([]int16, count)
		_, err := enc.EncodeSamples(s)
		if !errors.Is(err, ErrInvalidSampleCount) {
			t.Errorf("expected ErrInvalidSampleCount for sample count %d, got: %v", count, err)
		}
	}
}

// TestPCMFramer_ArbitraryChunkSizes 验证任意非 2880 对齐分块（100 字节、1000 字节、512 字节等）切分下的分帧正确性与数据完整性。
func TestPCMFramer_ArbitraryChunkSizes(t *testing.T) {
	totalFrames := 4
	totalBytes := totalFrames * DownlinkBytesPerFrame // 11520 字节

	rawPCM := make([]byte, totalBytes)
	for i := 0; i < totalBytes; i++ {
		rawPCM[i] = byte((i*7 + 13) % 256)
	}

	chunkSizes := []int{50, 100, 137, 500, 1000, 1440, 2048, 3000, 4096}
	for _, chunkSize := range chunkSizes {
		t.Run(fmt.Sprintf("chunk_size_%d", chunkSize), func(t *testing.T) {
			framer := NewPCMFramer()
			var collectedFrames [][]byte

			for offset := 0; offset < totalBytes; offset += chunkSize {
				end := offset + chunkSize
				if end > totalBytes {
					end = totalBytes
				}
				chunk := rawPCM[offset:end]
				frames := framer.Feed(chunk)
				collectedFrames = append(collectedFrames, frames...)
			}

			if framer.BufferedBytes() != 0 {
				t.Fatalf("expected 0 buffered bytes after exact totalBytes, got %d", framer.BufferedBytes())
			}

			flushFrames := framer.Flush()
			if len(flushFrames) != 0 {
				t.Fatalf("expected 0 flush frames for exact multiple of frame size, got %d", len(flushFrames))
			}

			if len(collectedFrames) != totalFrames {
				t.Fatalf("expected %d collected frames, got %d", totalFrames, len(collectedFrames))
			}

			var reconstructed []byte
			for i, frame := range collectedFrames {
				if len(frame) != DownlinkBytesPerFrame {
					t.Fatalf("frame %d: expected %d bytes, got %d", i, DownlinkBytesPerFrame, len(frame))
				}
				reconstructed = append(reconstructed, frame...)
			}

			if !bytes.Equal(reconstructed, rawPCM) {
				t.Fatal("reconstructed pcm data does not match original raw pcm")
			}
		})
	}
}

// TestPCMFramer_OddByteChunks 验证奇数边界分块（1 字节、3 字节、5 字节、7 字节、17 字节等）拼接的正确性。
func TestPCMFramer_OddByteChunks(t *testing.T) {
	totalFrames := 2
	totalBytes := totalFrames * DownlinkBytesPerFrame // 5760 字节

	rawPCM := make([]byte, totalBytes)
	for i := 0; i < totalBytes; i++ {
		rawPCM[i] = byte(i % 251)
	}

	oddChunkSizes := []int{1, 3, 5, 7, 11, 17, 33, 99, 127}
	for _, chunkSize := range oddChunkSizes {
		framer := NewPCMFramer()
		var collectedFrames [][]byte

		for offset := 0; offset < totalBytes; offset += chunkSize {
			end := offset + chunkSize
			if end > totalBytes {
				end = totalBytes
			}
			chunk := rawPCM[offset:end]
			frames := framer.Feed(chunk)
			collectedFrames = append(collectedFrames, frames...)
		}

		if len(collectedFrames) != totalFrames {
			t.Fatalf("chunk size %d: expected %d frames, got %d", chunkSize, totalFrames, len(collectedFrames))
		}

		var reconstructed []byte
		for _, frame := range collectedFrames {
			reconstructed = append(reconstructed, frame...)
		}

		if !bytes.Equal(reconstructed, rawPCM) {
			t.Fatalf("chunk size %d: reconstructed pcm does not match original", chunkSize)
		}
	}
}

// TestPCMFramer_FlushPadding 验证在流结束时对残余数据的静音补齐逻辑：
// 1. 无残余（0 字节）：Flush 返回 nil，不补多余帧；
// 2. 有残余（1 字节、100 字节、2879 字节）：用 0x00 静音补齐至 2880 字节并输出且仅输出 1 帧；
// 3. Flush 后的幂等性与清空验证。
func TestPCMFramer_FlushPadding(t *testing.T) {
	testCases := []struct {
		name              string
		inputBytes        int
		expectedFeedCount int
		expectedFlushLen  int // 0 表示 Flush 返回 nil
	}{
		{
			name:              "zero bytes initial flush",
			inputBytes:        0,
			expectedFeedCount: 0,
			expectedFlushLen:  0,
		},
		{
			name:              "exact one frame (2880 bytes)",
			inputBytes:        2880,
			expectedFeedCount: 1,
			expectedFlushLen:  0,
		},
		{
			name:              "exact two frames (5760 bytes)",
			inputBytes:        5760,
			expectedFeedCount: 2,
			expectedFlushLen:  0,
		},
		{
			name:              "single odd residual byte (1 byte)",
			inputBytes:        1,
			expectedFeedCount: 0,
			expectedFlushLen:  1,
		},
		{
			name:              "small residual (100 bytes)",
			inputBytes:        100,
			expectedFeedCount: 0,
			expectedFlushLen:  1,
		},
		{
			name:              "large residual (2879 bytes - 1 byte short)",
			inputBytes:        2879,
			expectedFeedCount: 0,
			expectedFlushLen:  1,
		},
		{
			name:              "one frame plus 1 byte residual (2881 bytes)",
			inputBytes:        2881,
			expectedFeedCount: 1,
			expectedFlushLen:  1,
		},
		{
			name:              "one frame plus 1000 bytes residual (3880 bytes)",
			inputBytes:        3880,
			expectedFeedCount: 1,
			expectedFlushLen:  1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			framer := NewPCMFramer()
			data := make([]byte, tc.inputBytes)
			for i := 0; i < tc.inputBytes; i++ {
				data[i] = byte((i + 1) % 255) // 非 0 数据
			}

			feedFrames := framer.Feed(data)
			if len(feedFrames) != tc.expectedFeedCount {
				t.Fatalf("expected %d feed frames, got %d", tc.expectedFeedCount, len(feedFrames))
			}

			flushFrames := framer.Flush()
			if tc.expectedFlushLen == 0 {
				if len(flushFrames) != 0 {
					t.Fatalf("expected 0 flush frames, got %d", len(flushFrames))
				}
			} else {
				if len(flushFrames) != 1 {
					t.Fatalf("expected 1 flush frame, got %d", len(flushFrames))
				}
				frame := flushFrames[0]
				if len(frame) != DownlinkBytesPerFrame {
					t.Fatalf("expected flush frame size %d, got %d", DownlinkBytesPerFrame, len(frame))
				}

				residualLen := tc.inputBytes % DownlinkBytesPerFrame
				if residualLen == 0 && tc.inputBytes > 0 {
					residualLen = DownlinkBytesPerFrame
				}

				// 校验残余前缀与原始数据一致
				expectedPrefix := data[tc.inputBytes-residualLen:]
				if !bytes.Equal(frame[:residualLen], expectedPrefix) {
					t.Fatal("flush frame residual prefix mismatch")
				}

				// 校验补齐部分全部为 0x00 静音
				for i := residualLen; i < DownlinkBytesPerFrame; i++ {
					if frame[i] != 0 {
						t.Fatalf("expected silence 0x00 at index %d, got 0x%02x", i, frame[i])
					}
				}
			}

			// 验证 Flush 后清空，再次 Flush 返回 nil
			secondFlush := framer.Flush()
			if len(secondFlush) != 0 {
				t.Fatalf("expected 0 frames on second flush, got %d", len(secondFlush))
			}
			if framer.BufferedBytes() != 0 {
				t.Fatalf("expected 0 buffered bytes after flush, got %d", framer.BufferedBytes())
			}
		})
	}
}

// TestPCMFramer_Reset 验证重置清空内部缓冲区功能。
func TestPCMFramer_Reset(t *testing.T) {
	framer := NewPCMFramer()
	framer.Feed([]byte{1, 2, 3, 4, 5})
	if framer.BufferedBytes() != 5 {
		t.Fatalf("expected 5 buffered bytes, got %d", framer.BufferedBytes())
	}

	framer.Reset()
	if framer.BufferedBytes() != 0 {
		t.Fatalf("expected 0 buffered bytes after reset, got %d", framer.BufferedBytes())
	}

	frames := framer.Flush()
	if len(frames) != 0 {
		t.Fatalf("expected 0 frames from flush after reset, got %d", len(frames))
	}
}

// TestStreamEncoder_FeedAndFlush 验证 StreamEncoder 完整流式编码，包括分块输入、整帧编码与末尾静音补齐。
func TestStreamEncoder_FeedAndFlush(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	streamEnc := NewStreamEncoder(enc)
	if streamEnc.Encoder() != enc {
		t.Fatal("stream encoder returned wrong underlying encoder")
	}

	// 准备 2 帧完整数据 + 500 字节残余（共 2880*2 + 500 = 6260 字节）
	totalBytes := 2*DownlinkBytesPerFrame + 500
	rawPCM := make([]byte, totalBytes)
	for i := 0; i < totalBytes; i++ {
		rawPCM[i] = byte((i * 3) % 256)
	}

	// 按 200 字节分块流式喂入
	var packets [][]byte
	chunkSize := 200
	for offset := 0; offset < totalBytes; offset += chunkSize {
		end := offset + chunkSize
		if end > totalBytes {
			end = totalBytes
		}
		pkts, err := streamEnc.Feed(rawPCM[offset:end])
		if err != nil {
			t.Fatalf("stream encoder feed error: %v", err)
		}
		packets = append(packets, pkts...)
	}

	// 此时应恰好产生 2 个整帧 Opus 包
	if len(packets) != 2 {
		t.Fatalf("expected 2 packets before flush, got %d", len(packets))
	}

	// 刷新尾帧，应补静音产出第 3 个 Opus 包
	flushPkts, err := streamEnc.Flush()
	if err != nil {
		t.Fatalf("stream encoder flush error: %v", err)
	}
	if len(flushPkts) != 1 {
		t.Fatalf("expected 1 packet on flush for 500 bytes residual, got %d", len(flushPkts))
	}
	packets = append(packets, flushPkts...)

	if len(packets) != 3 {
		t.Fatalf("expected 3 total packets, got %d", len(packets))
	}

	// 解码所有 Opus 包，验证每个包均为 1440 采样点
	for i, pkt := range packets {
		if len(pkt) == 0 {
			t.Fatalf("packet %d is empty", i)
		}
		decoded := decode24kOpus(t, pkt)
		if len(decoded) != DownlinkSamplesPerFrame {
			t.Fatalf("packet %d decoded sample count mismatch: expected %d, got %d", i, DownlinkSamplesPerFrame, len(decoded))
		}
	}

	// 验证独立内存分配
	packets[0][0] ^= 0xFF
	// 修改第 0 个包不应影响第 1 个包
	if len(packets) >= 2 && bytes.Equal(packets[0], packets[1]) {
		t.Fatal("packets share memory buffer")
	}
}

// TestStreamEncoder_NilEncoder 验证 Encoder 为 nil 时的防御性错误拦截。
func TestStreamEncoder_NilEncoder(t *testing.T) {
	streamEnc := NewStreamEncoder(nil)
	_, err := streamEnc.Feed([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for nil encoder in Feed")
	}
	_, err = streamEnc.Flush()
	if err == nil {
		t.Fatal("expected error for nil encoder in Flush")
	}
}

// TestEncoder_Close 验证 Encoder 显式关闭后的状态置空与后续调用的错误拦截。
func TestEncoder_Close(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	frame := generate24kSinePCM(440.0, 10000.0)
	samples := make([]int16, DownlinkSamplesPerFrame)

	if err := enc.Close(); err != nil {
		t.Fatalf("close returned error: %v", err)
	}

	// 验证关闭后 Encode 拦截
	if _, err := enc.Encode(frame); !errors.Is(err, ErrEncoderClosed) {
		t.Fatalf("expected ErrEncoderClosed after Close in Encode, got %v", err)
	}

	// 验证关闭后 EncodeSamples 拦截
	if _, err := enc.EncodeSamples(samples); !errors.Is(err, ErrEncoderClosed) {
		t.Fatalf("expected ErrEncoderClosed after Close in EncodeSamples, got %v", err)
	}

	// 验证重复 Close 安全
	if err := enc.Close(); err != nil {
		t.Fatalf("subsequent close returned error: %v", err)
	}

	// 验证 nil 接收者安全
	var nilEnc *Encoder
	if _, err := nilEnc.Encode(frame); !errors.Is(err, ErrEncoderClosed) {
		t.Fatalf("expected ErrEncoderClosed for nil encoder in Encode, got %v", err)
	}
	if _, err := nilEnc.EncodeSamples(samples); !errors.Is(err, ErrEncoderClosed) {
		t.Fatalf("expected ErrEncoderClosed for nil encoder in EncodeSamples, got %v", err)
	}
	if err := nilEnc.Close(); err != nil {
		t.Fatalf("nil encoder Close returned error: %v", err)
	}
}

// TestStreamEncoder_Close 验证 StreamEncoder 显式关闭与级联关闭底层 Encoder。
func TestStreamEncoder_Close(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	streamEnc := NewStreamEncoder(enc)
	if err := streamEnc.Close(); err != nil {
		t.Fatalf("stream encoder close error: %v", err)
	}

	frame := generate24kSinePCM(440.0, 10000.0)
	if _, err := streamEnc.Feed(frame); err == nil {
		t.Fatal("expected error after stream encoder close in Feed")
	}

	var nilStreamEnc *StreamEncoder
	if err := nilStreamEnc.Close(); err != nil {
		t.Fatalf("nil stream encoder close error: %v", err)
	}
}

// TestStreamEncoder_Concurrency 验证 50 个 goroutine 并发独立执行分帧与 Opus 编码的竞态安全与数据隔离。
func TestStreamEncoder_Concurrency(t *testing.T) {
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()

			enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
			if err != nil {
				t.Errorf("goroutine %d failed to create encoder: %v", id, err)
				return
			}
			streamEnc := NewStreamEncoder(enc)

			sine := generate24kSinePCM(float64(200+id*10), 15000.0)
			// 切分为 3 个不等分块
			chunks := [][]byte{
				sine[:500],
				sine[500:2000],
				sine[2000:],
			}

			var packets [][]byte
			for _, chunk := range chunks {
				pkts, err := streamEnc.Feed(chunk)
				if err != nil {
					t.Errorf("goroutine %d feed failed: %v", id, err)
					return
				}
				packets = append(packets, pkts...)
			}

			flushPkts, err := streamEnc.Flush()
			if err != nil {
				t.Errorf("goroutine %d flush failed: %v", id, err)
				return
			}
			packets = append(packets, flushPkts...)

			if len(packets) != 1 {
				t.Errorf("goroutine %d expected 1 total packet, got %d", id, len(packets))
				return
			}

			decoded := decode24kOpus(t, packets[0])
			if len(decoded) != DownlinkSamplesPerFrame {
				t.Errorf("goroutine %d invalid decoded sample count: %d", id, len(decoded))
			}
		}(i)
	}

	wg.Wait()
}

// TestEncoder_NoDiskFileCreated 严格断言测试过程中严禁在磁盘上生成任何音频文件（.pcm, .opus, .wav 等）。
func TestEncoder_NoDiskFileCreated(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current working directory: %v", err)
	}

	forbiddenExts := map[string]bool{
		".pcm":  true,
		".opus": true,
		".wav":  true,
		".raw":  true,
		".mp3":  true,
	}

	err = filepath.Walk(cwd, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if forbiddenExts[ext] {
			t.Fatalf("forbidden audio file found on disk: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to scan directory for disk files: %v", err)
	}
}
