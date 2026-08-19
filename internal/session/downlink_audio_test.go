package session

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hraban/opus"

	"xiaozhi-esp32-golang-server/internal/audio"
)

// generate24kSinePCMForSession 生成 24 kHz 单声道 60 ms（1440 采样点 / 2880 字节）的正弦波 PCM 字节。
func generate24kSinePCMForSession(freq float64, amp float64) []byte {
	pcmBytes := make([]byte, audio.DownlinkBytesPerFrame)
	for i := 0; i < audio.DownlinkSamplesPerFrame; i++ {
		t := float64(i) / float64(audio.DownlinkSampleRate)
		val := int16(amp * math.Sin(2.0*math.Pi*freq*t))
		binary.LittleEndian.PutUint16(pcmBytes[i*2:i*2+2], uint16(val))
	}
	return pcmBytes
}

// decode24kOpusForSession 使用 24 kHz libopus 解码器将 Opus 包解码为 1440 采样点 PCM。
func decode24kOpusForSession(t *testing.T, opusData []byte) []int16 {
	t.Helper()
	dec, err := opus.NewDecoder(audio.DownlinkSampleRate, audio.DownlinkChannels)
	if err != nil {
		t.Fatalf("failed to create opus decoder: %v", err)
	}

	pcm := make([]int16, audio.DownlinkSamplesPerFrame)
	n, err := dec.Decode(opusData, pcm)
	if err != nil {
		t.Fatalf("failed to decode opus packet: %v", err)
	}
	if n != audio.DownlinkSamplesPerFrame {
		t.Fatalf("expected %d decoded samples, got %d", audio.DownlinkSamplesPerFrame, n)
	}
	return pcm
}

// TestSession_HoldEncoderAndStreamEncoder 验证 Session 初始化后持有独立的 audio.Encoder 与 audio.StreamEncoder 实例。
func TestSession_HoldEncoderAndStreamEncoder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, nil, nil)
	if sess.Encoder() == nil {
		t.Fatal("expected session to hold non-nil audio.Encoder")
	}
	if sess.StreamEncoder() == nil {
		t.Fatal("expected session to hold non-nil audio.StreamEncoder")
	}
	if sess.StreamEncoder().Encoder() != sess.Encoder() {
		t.Fatal("expected StreamEncoder to bind with session's Encoder")
	}
}

// TestSession_ConsumeTTSPCM_NormalFlow_ExactFrames 验证会话消费 TTS PCM 正常流程：整帧（2 帧 = 5760 字节）被正确分帧并编码为 2 个 Opus 包。
func TestSession_ConsumeTTSPCM_NormalFlow_ExactFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frame1 := generate24kSinePCMForSession(440.0, 18000.0)
	frame2 := generate24kSinePCMForSession(880.0, 18000.0)

	mockTTS := &mockTTSStream{
		pcmDataToReturn: [][]byte{frame1, frame2},
	}

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, nil, nil)

	var mu sync.Mutex
	var encodedPackets [][]byte

	sess.SetOnEncodedOpus(func(gen uint64, packet []byte) {
		mu.Lock()
		defer mu.Unlock()
		encodedPackets = append(encodedPackets, packet)
	})

	gen := uint64(1)
	sess.consumeTTSPCM(ctx, gen, mockTTS)

	mu.Lock()
	packetCount := len(encodedPackets)
	mu.Unlock()

	if packetCount != 2 {
		t.Fatalf("expected exactly 2 encoded opus packets, got %d", packetCount)
	}

	// 解码并校验每个 Opus 包
	for i, pkt := range encodedPackets {
		if len(pkt) == 0 {
			t.Fatalf("packet %d is empty", i)
		}
		decoded := decode24kOpusForSession(t, pkt)
		if len(decoded) != audio.DownlinkSamplesPerFrame {
			t.Fatalf("packet %d sample count mismatch", i)
		}
	}
}

// TestSession_ConsumeTTSPCM_ArbitraryChunksAndOddBoundary 验证会话对任意非 2880 对齐分块及奇数边界分块（1 字节、3 字节、5 字节等）的拼接与分帧编码。
func TestSession_ConsumeTTSPCM_ArbitraryChunksAndOddBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frame1 := generate24kSinePCMForSession(500.0, 15000.0)
	frame2 := generate24kSinePCMForSession(1000.0, 15000.0)
	combined := append(frame1, frame2...) // 5760 字节 (2 帧)

	// 切分为奇偶混合的不规则分块
	var chunks [][]byte
	chunkLens := []int{1, 3, 5, 7, 100, 500, 1000, 137, 2880 - 1653, 2880}
	offset := 0
	for _, l := range chunkLens {
		if offset >= len(combined) {
			break
		}
		end := offset + l
		if end > len(combined) {
			end = len(combined)
		}
		chunks = append(chunks, combined[offset:end])
		offset = end
	}
	if offset < len(combined) {
		chunks = append(chunks, combined[offset:])
	}

	mockTTS := &mockTTSStream{
		pcmDataToReturn: chunks,
	}

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, nil, nil)

	var mu sync.Mutex
	var encodedPackets [][]byte

	sess.SetOnEncodedOpus(func(gen uint64, packet []byte) {
		mu.Lock()
		defer mu.Unlock()
		encodedPackets = append(encodedPackets, packet)
	})

	sess.consumeTTSPCM(ctx, 1, mockTTS)

	mu.Lock()
	packetCount := len(encodedPackets)
	mu.Unlock()

	if packetCount != 2 {
		t.Fatalf("expected 2 encoded opus packets for 5760 bytes input, got %d", packetCount)
	}

	for i, pkt := range encodedPackets {
		decoded := decode24kOpusForSession(t, pkt)
		if len(decoded) != audio.DownlinkSamplesPerFrame {
			t.Fatalf("packet %d sample count mismatch", i)
		}
	}
}

// TestSession_ConsumeTTSPCM_TailSilencePadding 验证在输入结束时，未满 2880 字节的残余 PCM（1 帧 + 100 字节残余）用静音（0x00）补齐至 2880 字节并编码输出最后一帧。
func TestSession_ConsumeTTSPCM_TailSilencePadding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frame1 := generate24kSinePCMForSession(600.0, 16000.0)
	residual := make([]byte, 100)
	for i := 0; i < 100; i++ {
		residual[i] = byte(i + 1)
	}

	mockTTS := &mockTTSStream{
		pcmDataToReturn: [][]byte{frame1, residual},
	}

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, nil, nil)

	var mu sync.Mutex
	var encodedPackets [][]byte

	sess.SetOnEncodedOpus(func(gen uint64, packet []byte) {
		mu.Lock()
		defer mu.Unlock()
		encodedPackets = append(encodedPackets, packet)
	})

	sess.consumeTTSPCM(ctx, 1, mockTTS)

	mu.Lock()
	packetCount := len(encodedPackets)
	mu.Unlock()

	if packetCount != 2 {
		t.Fatalf("expected 2 encoded packets (1 full frame + 1 padded residual frame), got %d", packetCount)
	}

	// 校验第 2 个包解码后为 1440 采样点
	decodedResidual := decode24kOpusForSession(t, encodedPackets[1])
	if len(decodedResidual) != audio.DownlinkSamplesPerFrame {
		t.Fatalf("expected 1440 decoded samples for tail frame, got %d", len(decodedResidual))
	}
}

// TestSession_ConsumeTTSPCM_ZeroResidualNoExtraFrame 验证整帧数据（恰好 2880 字节）结束时，Flush 返回 nil，绝不补多余静音包。
func TestSession_ConsumeTTSPCM_ZeroResidualNoExtraFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frame1 := generate24kSinePCMForSession(440.0, 10000.0)
	mockTTS := &mockTTSStream{
		pcmDataToReturn: [][]byte{frame1},
	}

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, nil, nil)

	var mu sync.Mutex
	var encodedPackets [][]byte

	sess.SetOnEncodedOpus(func(gen uint64, packet []byte) {
		mu.Lock()
		defer mu.Unlock()
		encodedPackets = append(encodedPackets, packet)
	})

	sess.consumeTTSPCM(ctx, 1, mockTTS)

	mu.Lock()
	packetCount := len(encodedPackets)
	mu.Unlock()

	if packetCount != 1 {
		t.Fatalf("expected exactly 1 encoded packet without extra silence frame, got %d", packetCount)
	}
}

// TestSession_ConsumeTTSPCM_StreamError 验证 TTSStream.NextPCM 发生异常时，正确记录并向会话投递错误事件。
func TestSession_ConsumeTTSPCM_StreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSStream{
		nextPCMErr: errors.New("mock network broken"),
	}

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, nil, nil)

	sess.consumeTTSPCM(ctx, 1, mockTTS)

	// 检查事件通道中是否收到 eventKindError
	select {
	case ev := <-sess.events:
		if ev.kind != eventKindError {
			t.Fatalf("expected eventKindError, got %v", ev.kind)
		}
		if ev.generation != 1 {
			t.Fatalf("expected generation 1, got %d", ev.generation)
		}
		if !ev.fatal {
			t.Fatal("expected fatal error flag")
		}
	default:
		t.Fatal("expected error event posted to session events channel")
	}
}

// TestSession_ConsumeTTSPCM_ContextCancellation 验证 Context 取消时快速退出且不投递错误事件。
func TestSession_ConsumeTTSPCM_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	mockTTS := &mockTTSStream{
		pcmDataToReturn: [][]byte{generate24kSinePCMForSession(440.0, 10000.0)},
	}

	sess, _, _ := createTestSessionForOrchestration(ctx, nil, nil, nil)
	sess.consumeTTSPCM(ctx, 1, mockTTS)

	// 验证未投递错误事件
	select {
	case ev := <-sess.events:
		t.Fatalf("unexpected event on canceled context: %v", ev)
	default:
	}
}

// TestSession_ConsumeTTSPCM_EndToEndOrchestration 验证 LLM 文本分句到回答级 TTS 合成并消费 PCM 进行分帧 Opus 编码的完整端到端链路。
func TestSession_ConsumeTTSPCM_EndToEndOrchestration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockLLMStream([]string{"今天", "天气", "很好。"}, nil)
	llmClient := newMockLLMClient(mockStream, nil)

	frame := generate24kSinePCMForSession(500.0, 15000.0)
	mockTTS := newMockTTSStream(nil)
	mockTTS.pcmDataToReturn = [][]byte{frame}
	ttsClient := newMockTTSClient(mockTTS, nil)

	sess, conn, _ := createTestSessionForOrchestration(ctx, llmClient, ttsClient, nil)

	var mu sync.Mutex
	var encodedPackets [][]byte

	sess.SetOnEncodedOpus(func(gen uint64, packet []byte) {
		mu.Lock()
		defer mu.Unlock()
		encodedPackets = append(encodedPackets, packet)
	})

	gen := uint64(1)
	sess.orchestrateLLMAndTTS(ctx, gen, "测试端到端")

	// 等待后台消费协程处理完毕
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		mu.Lock()
		count := len(encodedPackets)
		mu.Unlock()
		if count >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	count := len(encodedPackets)
	mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 encoded opus packet from orchestrated flow, got %d", count)
	}

	// 验证句子送入 TTSStream
	sentSentences := mockTTS.SentSentences()
	if len(sentSentences) != 1 || sentSentences[0] != "今天天气很好。" {
		t.Fatalf("unexpected sent sentences to tts: %v", sentSentences)
	}

	// 等待 Writer 消费并验证下发的 sentence_start
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(conn.TextMessages()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	textMsgs := conn.TextMessages()
	if len(textMsgs) == 0 {
		t.Fatal("expected at least 1 text message from writer")
	}
}

// TestSession_ConsumeTTSPCM_NoDiskFiles 验证在下行音频消费与编码过程中严禁向磁盘写入任何音频文件。
func TestSession_ConsumeTTSPCM_NoDiskFiles(t *testing.T) {
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
		t.Fatalf("failed to scan directory: %v", err)
	}
}
