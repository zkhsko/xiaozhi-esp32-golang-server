package session

import (
	"context"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hraban/opus"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
	"xiaozhi-esp32-golang-server/internal/config"
)

// mockSessionASRStream 实现测试用的 ai.ASRStream。
type mockSessionASRStream struct {
	mu           sync.Mutex
	pcmFrames    [][]byte
	writeErr     error
	writeBlocked chan struct{}
	blockStarted chan struct{}
	finished     bool
	closed       bool
}

func newMockSessionASRStream() *mockSessionASRStream {
	return &mockSessionASRStream{
		pcmFrames: make([][]byte, 0),
	}
}

func (m *mockSessionASRStream) WritePCM(ctx context.Context, data []byte) error {
	m.mu.Lock()
	if m.writeBlocked != nil {
		if m.blockStarted != nil {
			select {
			case <-m.blockStarted:
			default:
				close(m.blockStarted)
			}
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.writeBlocked:
		}
		m.mu.Lock()
	}

	if m.writeErr != nil {
		err := m.writeErr
		m.mu.Unlock()
		return err
	}

	frameCopy := make([]byte, len(data))
	copy(frameCopy, data)
	m.pcmFrames = append(m.pcmFrames, frameCopy)
	m.mu.Unlock()
	return nil
}

func (m *mockSessionASRStream) Finish(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finished = true
	return nil
}

func (m *mockSessionASRStream) Result(ctx context.Context) (string, error) {
	return "mock result", nil
}

func (m *mockSessionASRStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockSessionASRStream) FrameCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pcmFrames)
}

func (m *mockSessionASRStream) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// mockSessionASRClient 实现测试用的 ai.ASRClient。
type mockSessionASRClient struct {
	mu          sync.Mutex
	createErr   error
	lastStream  *mockSessionASRStream
	streamCount int
}

func newMockSessionASRClient() *mockSessionASRClient {
	return &mockSessionASRClient{}
}

func (c *mockSessionASRClient) CreateStream(ctx context.Context) (ai.ASRStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.createErr != nil {
		return nil, c.createErr
	}
	stream := newMockSessionASRStream()
	c.lastStream = stream
	c.streamCount++
	return stream, nil
}

func (c *mockSessionASRClient) LastStream() *mockSessionASRStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStream
}

// encodeSineOpusPacket 在内存中生成 16 kHz 60 ms 正弦波并编码为 Opus 包。
func encodeSineOpusPacket(t *testing.T, freq float64) []byte {
	t.Helper()
	enc, err := opus.NewEncoder(audio.UplinkSampleRate, audio.UplinkChannels, opus.AppVoIP)
	if err != nil {
		t.Fatalf("failed to create opus encoder: %v", err)
	}

	pcm := make([]int16, audio.UplinkSamplesPerFrame)
	for i := 0; i < audio.UplinkSamplesPerFrame; i++ {
		tVal := float64(i) / float64(audio.UplinkSampleRate)
		pcm[i] = int16(20000.0 * math.Sin(2.0*math.Pi*freq*tVal))
	}

	buf := make([]byte, audio.DefaultMaxOpusPacketBytes)
	n, err := enc.Encode(pcm, buf)
	if err != nil {
		t.Fatalf("failed to encode opus packet: %v", err)
	}

	res := make([]byte, n)
	copy(res, buf[:n])
	return res
}

// createTestSessionWithASR 创建包含 mock ASR 客户端的测试会话。
func createTestSessionWithASR(ctx context.Context, asrClient ai.ASRClient, queueCap int) (*Session, *fakeWSConn, *Writer) {
	fakeConn := &fakeWSConn{}
	writer := NewWriter(ctx, fakeConn, 100, nil)
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:         5 * time.Second,
			MaxOpusPacketBytes:   1024,
			MaxListeningDuration: 30 * time.Second,
			ASRPCMQueueCapacity:  queueCap,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "test-device",
		ClientID:     "test-client",
		SerialNumber: "test-sn",
	}
	sess := NewSessionWithWriter(ctx, nil, writer, info, cfg, asrClient, nil)
	return sess, fakeConn, writer
}

// TestSession_AudioInListening_DecodedAndEnqueued 验证在 LISTENING 状态下收到的上行 Opus 正确解码并按序推入 ASR 流。
func TestSession_AudioInListening_DecodedAndEnqueued(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// listen.start -> LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := asrClient.LastStream()
	if stream == nil {
		t.Fatal("expected ASR stream to be created upon entering LISTENING")
	}

	// 发送 3 帧合成的正弦波 Opus 数据
	const frameCount = 3
	for i := 0; i < frameCount; i++ {
		packet := encodeSineOpusPacket(t, float64(400+i*100))
		ok := sess.PostClientAudio(packet)
		if !ok {
			t.Fatalf("failed to post client audio frame %d", i)
		}
	}

	// 等待后台消费协程消费写入 ASR 流
	deadline := time.Now().Add(2 * time.Second)
	for stream.FrameCount() < frameCount && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if stream.FrameCount() != frameCount {
		t.Fatalf("expected %d PCM frames in ASR stream, got %d", frameCount, stream.FrameCount())
	}

	// 验证每帧 PCM 长度精确为 1920 字节
	stream.mu.Lock()
	for i, frame := range stream.pcmFrames {
		if len(frame) != audio.UplinkBytesPerFrame {
			t.Errorf("frame %d expected %d bytes, got %d", i, audio.UplinkBytesPerFrame, len(frame))
		}
	}
	stream.mu.Unlock()
}

// TestSession_AudioInReady_Discarded 验证在 READY 状态下收到的音频直接丢弃，不送入 ASR，不创建 ASR 流。
func TestSession_AudioInReady_Discarded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 在 READY 状态发送合法 Opus 包
	packet := encodeSineOpusPacket(t, 440.0)
	sess.PostClientAudio(packet)

	time.Sleep(50 * time.Millisecond)

	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY, got %v", sess.State())
	}
	if asrClient.LastStream() != nil {
		t.Fatal("expected no ASR stream to be created in READY state")
	}
}

// TestSession_AudioInListening_CorruptOpusPacket 验证在 LISTENING 状态下收到损坏 Opus 包报错并关闭连接。
func TestSession_AudioInListening_CorruptOpusPacket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	// 发送损坏的 Opus 乱码包
	corruptPacket := []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB, 0xFA}
	sess.PostClientAudio(corruptPacket)

	waitState(t, sess, StateClosed, 2*time.Second)
}

// TestSession_AudioInListening_QueueFullBackpressure 验证 ASR 写入阻塞导致队列满时触发背压保护关闭连接。
func TestSession_AudioInListening_QueueFullBackpressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	const queueCap = 2
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, queueCap)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := asrClient.LastStream()
	if stream == nil {
		t.Fatal("expected ASR stream to be created")
	}

	// 阻塞 mock ASR 写入
	stream.writeBlocked = make(chan struct{})
	stream.blockStarted = make(chan struct{})
	defer close(stream.writeBlocked)

	packet := encodeSineOpusPacket(t, 440.0)

	// 第 1 帧被推入并被 worker 取走阻塞在 WritePCM
	sess.PostClientAudio(packet)

	select {
	case <-stream.blockStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter WritePCM")
	}

	// 连续推入满队列容量 + 1 帧，触发队列满载背压关闭
	for i := 0; i < queueCap+2; i++ {
		sess.PostClientAudio(packet)
	}

	waitState(t, sess, StateClosed, 2*time.Second)
}

// TestSession_Abort_CleansUpASRStream 验证中断 abort 时安全清空并关闭 ASR 流。
func TestSession_Abort_CleansUpASRStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := asrClient.LastStream()
	if stream == nil {
		t.Fatal("expected ASR stream")
	}

	// 触发 abort
	sess.PostAbort("user aborted")
	waitState(t, sess, StateReady, 2*time.Second)

	// 验证 ASRStream 已被关闭且 ASRQueue 为 nil
	if !stream.IsClosed() {
		t.Error("expected ASR stream to be closed after abort")
	}
	if sess.ASRQueue() != nil {
		t.Error("expected session ASRQueue to be nil after abort")
	}
}

// TestSession_NoDiskFilesCreated 验证测试与运行过程中未生成任何磁盘音频文件。
func TestSession_NoDiskFilesCreated(t *testing.T) {
	// 检查当前目录下是否存在任何 .pcm, .opus, .wav 文件
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if len(name) > 4 {
			ext := name[len(name)-4:]
			if ext == ".pcm" || ext == ".wav" || len(name) > 5 && name[len(name)-5:] == ".opus" {
				t.Fatalf("forbidden disk audio file found in repository: %s", name)
			}
		}
	}
}
