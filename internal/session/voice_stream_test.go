package session

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// mockTTSStream 实现 ai.TTSStream 接口，用于测试流式合成与协程调用行为。
type mockTTSStream struct {
	mu           sync.Mutex
	synthesizeFn func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error
	cancelFn     func(ctx context.Context) error
	closeFn      func() error

	synthesizeCalls []string
	cancelCalls     int
	closeCalls      int
}

func newMockTTSStream() *mockTTSStream {
	return &mockTTSStream{
		synthesizeCalls: make([]string, 0),
	}
}

func (m *mockTTSStream) SynthesizeSentence(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
	m.mu.Lock()
	m.synthesizeCalls = append(m.synthesizeCalls, text)
	m.mu.Unlock()

	if m.synthesizeFn != nil {
		return m.synthesizeFn(ctx, text, onPCM)
	}

	// 默认产生 2880 字节的标准单帧 PCM
	pcm := make([]byte, audio.DownlinkBytesPerFrame)
	return onPCM(ctx, pcm)
}

func (m *mockTTSStream) Cancel(ctx context.Context) error {
	m.mu.Lock()
	m.cancelCalls++
	m.mu.Unlock()

	if m.cancelFn != nil {
		return m.cancelFn(ctx)
	}
	return nil
}

func (m *mockTTSStream) Close() error {
	m.mu.Lock()
	m.closeCalls++
	m.mu.Unlock()

	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

func (m *mockTTSStream) CloseCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeCalls
}

// mockTTSClient 实现 ai.TTSClient 接口。
type mockTTSClient struct {
	mu             sync.Mutex
	createStreamFn func(ctx context.Context) (ai.TTSStream, error)
	createdStreams []*mockTTSStream
	createCalls    int
}

func newMockTTSClient(stream *mockTTSStream) *mockTTSClient {
	c := &mockTTSClient{
		createdStreams: make([]*mockTTSStream, 0),
	}
	if stream != nil {
		c.createdStreams = append(c.createdStreams, stream)
		c.createStreamFn = func(ctx context.Context) (ai.TTSStream, error) {
			return stream, nil
		}
	}
	return c
}

func (c *mockTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls++

	if c.createStreamFn != nil {
		return c.createStreamFn(ctx)
	}
	stream := newMockTTSStream()
	c.createdStreams = append(c.createdStreams, stream)
	return stream, nil
}

// mockVoiceWriter 实现 VoiceWriter 接口。
type mockVoiceWriter struct {
	mu                sync.Mutex
	textFrames        []string
	controlTextFrames []string
	voiceTextFrames   []string
	binaryFrames      [][]byte
	barrierCalls      int
	sendTextFn        func(ctx context.Context, turnId uint64, payload []byte) error
	sendControlTextFn func(ctx context.Context, payload []byte) error
	sendBinaryFn      func(ctx context.Context, turnId uint64, payload []byte) error
	enqueueBarrierFn  func(ctx context.Context, turnId uint64) error
}

func newMockVoiceWriter() *mockVoiceWriter {
	return &mockVoiceWriter{
		textFrames:        make([]string, 0),
		controlTextFrames: make([]string, 0),
		voiceTextFrames:   make([]string, 0),
		binaryFrames:      make([][]byte, 0),
	}
}

func (w *mockVoiceWriter) SendText(ctx context.Context, payload []byte) error {
	w.mu.Lock()
	w.textFrames = append(w.textFrames, string(payload))
	w.controlTextFrames = append(w.controlTextFrames, string(payload))
	w.mu.Unlock()

	if w.sendControlTextFn != nil {
		return w.sendControlTextFn(ctx, payload)
	}
	return nil
}

func (w *mockVoiceWriter) SendVoiceTextWait(ctx context.Context, turnId uint64, payload []byte) error {
	w.mu.Lock()
	w.textFrames = append(w.textFrames, string(payload))
	w.voiceTextFrames = append(w.voiceTextFrames, string(payload))
	w.mu.Unlock()

	if w.sendTextFn != nil {
		return w.sendTextFn(ctx, turnId, payload)
	}
	return nil
}

func (w *mockVoiceWriter) SendVoiceBinaryWait(ctx context.Context, turnId uint64, payload []byte) error {
	w.mu.Lock()
	copied := make([]byte, len(payload))
	copy(copied, payload)
	w.binaryFrames = append(w.binaryFrames, copied)
	w.mu.Unlock()

	if w.sendBinaryFn != nil {
		return w.sendBinaryFn(ctx, turnId, payload)
	}
	return nil
}

func (w *mockVoiceWriter) EnqueueBarrierWait(ctx context.Context, turnId uint64) error {
	w.mu.Lock()
	w.barrierCalls++
	w.mu.Unlock()

	if w.enqueueBarrierFn != nil {
		return w.enqueueBarrierFn(ctx, turnId)
	}
	return nil
}

// TestVoiceStream_ThreeWorkersOwnershipAndCapacity 验证每轮只创建规定的三个职责协程、队列容量与资源所有权。
func TestVoiceStream_ThreeWorkersOwnershipAndCapacity(t *testing.T) {
	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	cfg := SessionConfig{
		TTSPCMQueueCapacity:       42,
		DownlinkOpusQueueCapacity: 64,
		MaxOpusPacketBytes:        1024,
	}

	eventCh := make(chan VoiceStreamEvent, 10)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-test-01",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    cfg,
		OnEvent: func(ev VoiceStreamEvent) {
			eventCh <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(1)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 验证队列容量严格符合规范
	sentenceCap, pcmCap, downlinkCap := vs.QueueCapacities()
	if sentenceCap != DefaultSentenceQueueCapacity {
		t.Fatalf("expected sentence queue capacity %d, got %d", DefaultSentenceQueueCapacity, sentenceCap)
	}
	if pcmCap != 42 {
		t.Fatalf("expected pcm queue capacity 42, got %d", pcmCap)
	}
	if downlinkCap != 64 {
		t.Fatalf("expected downlink queue capacity 64, got %d", downlinkCap)
	}

	// 验证每轮恰好创建 3 个工作协程
	if workers := vs.ActiveWorkers(); workers != 3 {
		t.Fatalf("expected exactly 3 active workers, got %d", workers)
	}

	if activeId := vs.ActiveTurnId(); activeId != turnId {
		t.Fatalf("expected active turn id %d, got %d", turnId, activeId)
	}

	// 送入足以切出单句的文本并完成
	if err := vs.FeedText(ctx, turnId, "你好，我是小智机器人。", 0); err != nil {
		t.Fatalf("FeedText failed: %v", err)
	}
	if err := vs.FinishText(ctx, turnId); err != nil {
		t.Fatalf("FinishText failed: %v", err)
	}

	// 等待完成事件
	var sawSpeaking, sawSuccess bool
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev := <-eventCh:
			if ev.TurnId != turnId {
				t.Fatalf("expected event turnId %d, got %d", turnId, ev.TurnId)
			}
			if ev.Kind == VoiceStreamEventSpeaking {
				sawSpeaking = true
			}
			if ev.Kind == VoiceStreamEventSuccess {
				sawSuccess = true
				break loop
			}
		case <-timeout:
			t.Fatal("timeout waiting for voice stream completion")
		}
	}

	if !sawSpeaking || !sawSuccess {
		t.Fatalf("expected speaking and success events, got speaking=%v, success=%v", sawSpeaking, sawSuccess)
	}

	// 等待工作协程全部平稳退出
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if vs.ActiveWorkers() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 active workers after completion, got %d", workers)
	}

	// 验证 TTSStream 与 Writer 调用
	mockStream.mu.Lock()
	if len(mockStream.synthesizeCalls) != 1 {
		t.Fatalf("expected 1 synthesize call, got %d", len(mockStream.synthesizeCalls))
	}
	if mockStream.closeCalls != 1 {
		t.Fatalf("expected 1 close call on TTSStream, got %d", mockStream.closeCalls)
	}
	mockStream.mu.Unlock()

	writer.mu.Lock()
	if len(writer.textFrames) < 3 {
		t.Fatalf("expected at least 3 text frames (start, sentence_start, stop), got %d", len(writer.textFrames))
	}
	if len(writer.binaryFrames) == 0 {
		t.Fatalf("expected binary audio frames, got 0")
	}
	if writer.barrierCalls != 1 {
		t.Fatalf("expected exactly 1 barrier call, got %d", writer.barrierCalls)
	}
	writer.mu.Unlock()
}

// TestVoiceStream_CancelTurn_NoGoroutineLeak 验证取消轮次后所有阻塞立即解除且无协程泄漏。
func TestVoiceStream_CancelTurn_NoGoroutineLeak(t *testing.T) {
	mockStream := newMockTTSStream()
	// 让 SynthesizeSentence 阻塞等待 context 取消
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-cancel-test",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(10)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	if workers := vs.ActiveWorkers(); workers != 3 {
		t.Fatalf("expected 3 workers running, got %d", workers)
	}

	// 送入单句触发 TTS 开始
	if err := vs.FeedText(ctx, turnId, "这是一句用来测试取消的长句子。", 0); err != nil {
		t.Fatalf("FeedText failed: %v", err)
	}

	// 短暂等待句子进入 SynthesizeSentence
	time.Sleep(30 * time.Millisecond)

	// 执行取消
	vs.CancelTurn(turnId)

	// 验证工作协程迅速归零
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if vs.ActiveWorkers() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 active workers after cancel, got %d", workers)
	}

	// 验证 TTSStream 已被关闭
	mockStream.mu.Lock()
	if mockStream.closeCalls == 0 {
		t.Fatalf("expected TTSStream to be closed after turn cancel")
	}
	mockStream.mu.Unlock()
}

// TestVoiceStream_QueueBackpressure_CanceledContextUnblocks 验证队列满载背压时取消能安全解除所有阻塞。
func TestVoiceStream_QueueBackpressure_CanceledContextUnblocks(t *testing.T) {
	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	// 阻塞下行写入，制造下行队列满载
	blockDownlink := make(chan struct{})
	writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-blockDownlink:
			return nil
		}
	}

	cfg := SessionConfig{
		TTSPCMQueueCapacity:       10,
		DownlinkOpusQueueCapacity: 2,
		MaxOpusPacketBytes:        1024,
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-backpressure",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    cfg,
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(20)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 送入多条句子
	_ = vs.FeedText(ctx, turnId, "第一句测试文本，长度需要超过五个字。", 0)
	_ = vs.FeedText(ctx, turnId, "第二句测试文本，长度同样需要超过五个字。", 0)
	_ = vs.FeedText(ctx, turnId, "第三句测试文本，长度必须超过五个字。", 0)

	// 等待下行队列积压
	time.Sleep(50 * time.Millisecond)

	// 取消轮次，验证全部阻塞被唤醒并退出
	vs.CancelTurn(turnId)
	close(blockDownlink)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if vs.ActiveWorkers() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 active workers after canceling backpressured turn, got %d", workers)
	}
}

// TestVoiceStream_NoTextTurn_DirectSuccess 验证整轮无文本时无需建连直接成功完成。
func TestVoiceStream_NoTextTurn_DirectSuccess(t *testing.T) {
	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	eventCh := make(chan VoiceStreamEvent, 5)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-no-text",
		TTSClient: ttsClient,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			eventCh <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(30)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 不送入任何文本，直接 FinishText
	if err := vs.FinishText(ctx, turnId); err != nil {
		t.Fatalf("FinishText failed: %v", err)
	}

	select {
	case ev := <-eventCh:
		if ev.TurnId != turnId || ev.Kind != VoiceStreamEventSuccess {
			t.Fatalf("expected success event for turn %d, got kind=%v, turnId=%d", turnId, ev.Kind, ev.TurnId)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for success event on empty turn")
	}

	// 验证未调用 CreateStream，未下发协议帧
	ttsClient.mu.Lock()
	if ttsClient.createCalls != 0 {
		t.Fatalf("expected 0 CreateStream calls for empty turn, got %d", ttsClient.createCalls)
	}
	ttsClient.mu.Unlock()

	writer.mu.Lock()
	if len(writer.textFrames) != 0 || len(writer.binaryFrames) != 0 {
		t.Fatalf("expected no writer frames for empty turn, got text=%d, binary=%d", len(writer.textFrames), len(writer.binaryFrames))
	}
	writer.mu.Unlock()

	// 协程安全退出
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if vs.ActiveWorkers() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 workers after empty turn finish, got %d", workers)
	}
}

func TestIsPunctuationOnly(t *testing.T) {
	if !isPunctuationOnly(" ” ") {
		t.Fatal("expected standalone closing quote to be punctuation-only")
	}
	if isPunctuationOnly("这是一句完整文本。") {
		t.Fatal("expected readable text not to be punctuation-only")
	}
}

// TestVoiceStream_ConnectFailure_CleanExit 验证首句建连失败时安全退出并不发送 tts/start。
func TestVoiceStream_ConnectFailure_CleanExit(t *testing.T) {
	ttsClient := &mockTTSClient{
		createStreamFn: func(ctx context.Context) (ai.TTSStream, error) {
			return nil, errors.New("dashscope dial timeout")
		},
	}
	writer := newMockVoiceWriter()

	eventCh := make(chan VoiceStreamEvent, 5)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-conn-fail",
		TTSClient: ttsClient,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			eventCh <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(40)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	_ = vs.FeedText(ctx, turnId, "测试连接失败的句子内容。", 0)
	_ = vs.FinishText(ctx, turnId)

	select {
	case ev := <-eventCh:
		if ev.TurnId != turnId || ev.Kind != VoiceStreamEventFailed {
			t.Fatalf("expected failed event for turn %d, got kind=%v, err=%v", turnId, ev.Kind, ev.Err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for failure event")
	}

	writer.mu.Lock()
	if len(writer.textFrames) != 0 {
		t.Fatalf("expected 0 text frames when stream connect fails, got %d", len(writer.textFrames))
	}
	writer.mu.Unlock()

	// 协程安全退出
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if vs.ActiveWorkers() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 workers after connect failure, got %d", workers)
	}
}

// TestVoiceStream_MultiTurnSuccession 验证多轮连续调用生命周期与跨轮状态完全隔离。
func TestVoiceStream_MultiTurnSuccession(t *testing.T) {
	ttsClient := newMockTTSClient(nil)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-multi-turn",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()

	// 第 1 轮：正常完成
	if err := vs.StartTurn(ctx, 1); err != nil {
		t.Fatalf("StartTurn 1 failed: %v", err)
	}
	if workers := vs.ActiveWorkers(); workers != 3 {
		t.Fatalf("expected 3 workers for turn 1, got %d", workers)
	}
	_ = vs.FeedText(ctx, 1, "第一轮正常的问答文本内容。", 0)
	_ = vs.FinishText(ctx, 1)

	time.Sleep(50 * time.Millisecond)

	// 第 2 轮：执行中被 abort 取消
	if err := vs.StartTurn(ctx, 2); err != nil {
		t.Fatalf("StartTurn 2 failed: %v", err)
	}
	if vs.ActiveTurnId() != 2 {
		t.Fatalf("expected active turn id 2, got %d", vs.ActiveTurnId())
	}
	_ = vs.FeedText(ctx, 2, "第二轮被用户打断的文本内容。", 0)
	vs.CancelTurn(2)

	time.Sleep(50 * time.Millisecond)

	// 第 3 轮：新的一轮正常完成
	if err := vs.StartTurn(ctx, 3); err != nil {
		t.Fatalf("StartTurn 3 failed: %v", err)
	}
	if vs.ActiveTurnId() != 3 {
		t.Fatalf("expected active turn id 3, got %d", vs.ActiveTurnId())
	}
	_ = vs.FeedText(ctx, 3, "第三轮全新的问答文本内容。", 0)
	_ = vs.FinishText(ctx, 3)

	time.Sleep(50 * time.Millisecond)

	vs.Close()
	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 workers after close, got %d", workers)
	}
}

// TestVoiceStream_TurnIdFiltering 验证非当前轮次数据被丢弃。
func TestVoiceStream_TurnIdFiltering(t *testing.T) {
	ttsClient := newMockTTSClient(nil)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-filtering",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()
	if err := vs.StartTurn(ctx, 100); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 传入错误的 turnId，应当返回 ErrTurnMismatch
	err := vs.FeedText(ctx, 99, "错误的轮次文本内容。", 0)
	if !errors.Is(err, ErrTurnMismatch) {
		t.Fatalf("expected ErrTurnMismatch, got %v", err)
	}

	err = vs.FinishText(ctx, 99)
	if !errors.Is(err, ErrTurnMismatch) {
		t.Fatalf("expected ErrTurnMismatch, got %v", err)
	}
}

// TestVoiceStream_IterationFlush_Separation 验证 Iteration 切换时触发残余切句与下发。
func TestVoiceStream_IterationFlush_Separation(t *testing.T) {
	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-iteration",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(200)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 送入未达到 5 字的残余文本（Iteration 0）
	_ = vs.FeedText(ctx, turnId, "你好", 0)

	// 切换到 Iteration 1，旧残余 "你好" 应被 Flush 产出为一个句子
	_ = vs.FeedText(ctx, turnId, "正在为你查询天气情况。", 1)
	_ = vs.FinishText(ctx, turnId)

	time.Sleep(50 * time.Millisecond)

	mockStream.mu.Lock()
	defer mockStream.mu.Unlock()
	if len(mockStream.synthesizeCalls) < 2 {
		t.Fatalf("expected at least 2 synthesized sentences across iterations, got %d", len(mockStream.synthesizeCalls))
	}
	if mockStream.synthesizeCalls[0] != "你好" {
		t.Fatalf("expected first sentence to be '你好', got '%s'", mockStream.synthesizeCalls[0])
	}
}

// TestVoiceStream_FrameSequenceOrder 验证帧下发顺序：start -> sentence_start -> binary opus -> stop -> barrier。
func TestVoiceStream_FrameSequenceOrder(t *testing.T) {
	var seqMu sync.Mutex
	var actionSeq []string

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		return onPCM(ctx, make([]byte, audio.DownlinkBytesPerFrame))
	}

	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	writer.sendTextFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		var msg ServerTTSMessage
		if err := json.Unmarshal(payload, &msg); err == nil {
			seqMu.Lock()
			actionSeq = append(actionSeq, "text:"+msg.State)
			seqMu.Unlock()
		}
		return nil
	}

	writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		seqMu.Lock()
		actionSeq = append(actionSeq, "binary:opus")
		seqMu.Unlock()
		return nil
	}

	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		seqMu.Lock()
		actionSeq = append(actionSeq, "barrier")
		seqMu.Unlock()
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-order",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(300)
	_ = vs.StartTurn(ctx, turnId)
	_ = vs.FeedText(ctx, turnId, "测试完整的语音时序流程。", 0)
	_ = vs.FinishText(ctx, turnId)

	time.Sleep(80 * time.Millisecond)

	seqMu.Lock()
	defer seqMu.Unlock()

	expectedPrefix := []string{
		"text:start",
		"text:sentence_start",
		"binary:opus",
		"text:stop",
		"barrier",
	}

	if len(actionSeq) < len(expectedPrefix) {
		t.Fatalf("action sequence too short: %v", actionSeq)
	}

	for i, exp := range expectedPrefix {
		if actionSeq[i] != exp {
			t.Errorf("step %d mismatch: expected %s, got %s", i, exp, actionSeq[i])
		}
	}
}

// TestVoiceStream_StartTurn_ReplacesPreviousActiveTurn 验证上一轮未结束时再次 StartTurn，自动平稳关闭旧轮次并启动新轮次。
func TestVoiceStream_StartTurn_ReplacesPreviousActiveTurn(t *testing.T) {
	mockStream1 := newMockTTSStream()
	mockStream2 := newMockTTSStream()

	client := &mockTTSClient{
		createStreamFn: func(ctx context.Context) (ai.TTSStream, error) {
			if ctx.Value("turn") == uint64(1) {
				return mockStream1, nil
			}
			return mockStream2, nil
		},
	}
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-replace",
		TTSClient: client,
		Writer:    writer,
	})
	defer vs.Close()

	ctx1 := context.WithValue(context.Background(), "turn", uint64(1))
	if err := vs.StartTurn(ctx1, 1); err != nil {
		t.Fatalf("StartTurn 1 failed: %v", err)
	}
	if workers := vs.ActiveWorkers(); workers != 3 {
		t.Fatalf("expected 3 workers for turn 1, got %d", workers)
	}

	// 启动新轮次，应自动取消并等待上一轮平稳退出
	ctx2 := context.WithValue(context.Background(), "turn", uint64(2))
	if err := vs.StartTurn(ctx2, 2); err != nil {
		t.Fatalf("StartTurn 2 failed: %v", err)
	}
	if vs.ActiveTurnId() != 2 {
		t.Fatalf("expected active turn id 2, got %d", vs.ActiveTurnId())
	}
	if workers := vs.ActiveWorkers(); workers != 3 {
		t.Fatalf("expected 3 workers for turn 2, got %d", workers)
	}
}

// TestVoiceStream_Close_ReleasesAllResources 验证 Close 释放所有资源，阻止后续调用。
func TestVoiceStream_Close_ReleasesAllResources(t *testing.T) {
	ttsClient := newMockTTSClient(nil)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-close-all",
		TTSClient: ttsClient,
		Writer:    writer,
	})

	ctx := context.Background()
	_ = vs.StartTurn(ctx, 1)

	if err := vs.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 workers after close, got %d", workers)
	}

	// 重复 Close 幂等
	if err := vs.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}

	// 后续操作均返回 ErrVoiceStreamClosed
	if err := vs.StartTurn(ctx, 2); !errors.Is(err, ErrVoiceStreamClosed) {
		t.Fatalf("expected ErrVoiceStreamClosed, got %v", err)
	}
	if err := vs.FeedText(ctx, 1, "test", 0); !errors.Is(err, ErrVoiceStreamClosed) {
		t.Fatalf("expected ErrVoiceStreamClosed, got %v", err)
	}
	if err := vs.FinishText(ctx, 1); !errors.Is(err, ErrVoiceStreamClosed) {
		t.Fatalf("expected ErrVoiceStreamClosed, got %v", err)
	}
}

// TestVoiceStream_MultiSentence_StrictOrderAndSingleStream 验证多句流式合成、编码和下行严格时序与单 Stream 复用。
func TestVoiceStream_MultiSentence_StrictOrderAndSingleStream(t *testing.T) {
	var seqMu sync.Mutex
	var actionSeq []string

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		// 每句模拟返回 2 帧标准 PCM（每帧 2880 字节）
		frame1 := make([]byte, audio.DownlinkBytesPerFrame)
		frame2 := make([]byte, audio.DownlinkBytesPerFrame)
		if err := onPCM(ctx, frame1); err != nil {
			return err
		}
		return onPCM(ctx, frame2)
	}

	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	writer.sendTextFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		var msg ServerTTSMessage
		if err := json.Unmarshal(payload, &msg); err == nil {
			seqMu.Lock()
			if msg.State == TTSStateSentenceStart {
				actionSeq = append(actionSeq, "text:sentence_start:"+msg.Text)
			} else {
				actionSeq = append(actionSeq, "text:"+msg.State)
			}
			seqMu.Unlock()
		}
		return nil
	}

	writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		seqMu.Lock()
		actionSeq = append(actionSeq, "binary:opus")
		seqMu.Unlock()
		return nil
	}

	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		seqMu.Lock()
		actionSeq = append(actionSeq, "barrier")
		seqMu.Unlock()
		return nil
	}

	eventCh := make(chan VoiceStreamEvent, 10)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-multi-sentence-order",
		TTSClient: ttsClient,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			eventCh <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(500)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 增量送入流式文本，产生 3 句完整句子
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "今天北京的天气", Iteration: 0})
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "非常好，阳光明媚。", Iteration: 0}) // 第 1 句
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "建议你多出去走走。", Iteration: 0}) // 第 2 句
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "祝你有愉快的一天！", Iteration: 0}) // 第 3 句
	if err := vs.Finish(ctx, turnId); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	// 等待接收 speaking 与 success 事件
	var receivedSpeaking, receivedSuccess bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (!receivedSpeaking || !receivedSuccess) {
		select {
		case ev := <-eventCh:
			if ev.TurnId == turnId {
				if ev.Kind == VoiceStreamEventSpeaking {
					receivedSpeaking = true
				} else if ev.Kind == VoiceStreamEventSuccess {
					receivedSuccess = true
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}

	if !receivedSpeaking {
		t.Fatal("expected speaking event before first sentence audio")
	}
	if !receivedSuccess {
		t.Fatal("expected success event after all frames confirmed by barrier")
	}

	// 1. 验证整个多句回答严格只创建了 1 个物理 TTSStream
	ttsClient.mu.Lock()
	createCalls := ttsClient.createCalls
	ttsClient.mu.Unlock()
	if createCalls != 1 {
		t.Fatalf("expected exactly 1 CreateStream call, got %d", createCalls)
	}

	// 2. 验证 SynthesizeSentence 严格被调用 3 次，且句子文本与切句结果完全一致
	mockStream.mu.Lock()
	synthCalls := append([]string(nil), mockStream.synthesizeCalls...)
	closeCalls := mockStream.closeCalls
	mockStream.mu.Unlock()

	expectedSentences := []string{
		"今天北京的天气非常好，阳光明媚。",
		"建议你多出去走走。",
		"祝你有愉快的一天！",
	}

	if len(synthCalls) != len(expectedSentences) {
		t.Fatalf("expected %d synthesize calls, got %d: %v", len(expectedSentences), len(synthCalls), synthCalls)
	}
	for i, exp := range expectedSentences {
		if synthCalls[i] != exp {
			t.Errorf("synthesize sentence %d mismatch: expected '%s', got '%s'", i, exp, synthCalls[i])
		}
	}

	// 3. 验证 TTSStream 在轮末被幂等关闭
	if closeCalls != 1 {
		t.Fatalf("expected exactly 1 Close call on TTSStream, got %d", closeCalls)
	}

	// 4. 验证设备下行序列严格符合时序规范：
	// start -> sentence_start(1) -> opus(1) -> opus(1) -> sentence_start(2) -> opus(2) -> opus(2) -> sentence_start(3) -> opus(3) -> opus(3) -> stop -> barrier
	seqMu.Lock()
	actualSeq := append([]string(nil), actionSeq...)
	seqMu.Unlock()

	expectedSeq := []string{
		"text:start",
		"text:sentence_start:今天北京的天气非常好，阳光明媚。",
		"binary:opus",
		"binary:opus",
		"text:sentence_start:建议你多出去走走。",
		"binary:opus",
		"binary:opus",
		"text:sentence_start:祝你有愉快的一天！",
		"binary:opus",
		"binary:opus",
		"text:stop",
		"barrier",
	}

	if len(actualSeq) != len(expectedSeq) {
		t.Fatalf("action sequence length mismatch: expected %d, got %d. Sequence:\n%v", len(expectedSeq), len(actualSeq), actualSeq)
	}

	for i, exp := range expectedSeq {
		if actualSeq[i] != exp {
			t.Errorf("sequence step %d mismatch: expected '%s', got '%s'", i, exp, actualSeq[i])
		}
	}
}

// TestVoiceStream_PerSentenceFlush_NoResidualLeakToNextSentence 验证每句单独 Flush，禁止跨句拼接 PCM 残余。
func TestVoiceStream_PerSentenceFlush_NoResidualLeakToNextSentence(t *testing.T) {
	var seqMu sync.Mutex
	var actionSeq []string

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		if text == "第一句不足一帧。" {
			// 交付 1000 字节 PCM（不足标准 2880 字节 1 帧）
			// 预期：Feed 时产生 0 包 Opus，句末 Flush 时补静音产出 1 包 Opus
			return onPCM(ctx, make([]byte, 1000))
		}
		// 第二句：交付标准 2880 字节 PCM
		// 预期：Feed 时产出 1 包 Opus，句末 Flush 时产出 0 包 Opus（若有跨句泄漏则会产出异常包数）
		return onPCM(ctx, make([]byte, audio.DownlinkBytesPerFrame))
	}

	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	writer.sendTextFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		var msg ServerTTSMessage
		if err := json.Unmarshal(payload, &msg); err == nil {
			seqMu.Lock()
			if msg.State == TTSStateSentenceStart {
				actionSeq = append(actionSeq, "text:sentence_start:"+msg.Text)
			} else {
				actionSeq = append(actionSeq, "text:"+msg.State)
			}
			seqMu.Unlock()
		}
		return nil
	}

	writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		seqMu.Lock()
		actionSeq = append(actionSeq, "binary:opus")
		seqMu.Unlock()
		return nil
	}

	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		seqMu.Lock()
		actionSeq = append(actionSeq, "barrier")
		seqMu.Unlock()
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-flush-separation",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(600)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第一句不足一帧。", Iteration: 0})
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第二句完整一帧。", Iteration: 0})
	if err := vs.Finish(ctx, turnId); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	seqMu.Lock()
	actualSeq := append([]string(nil), actionSeq...)
	seqMu.Unlock()

	expectedSeq := []string{
		"text:start",
		"text:sentence_start:第一句不足一帧。",
		"binary:opus", // 第 1 句 Flush 补齐静音产出 1 包 Opus
		"text:sentence_start:第二句完整一帧。",
		"binary:opus", // 第 2 句 Feed 独立产出 1 包 Opus（无上一句残余拼接）
		"text:stop",
		"barrier",
	}

	if len(actualSeq) != len(expectedSeq) {
		t.Fatalf("sequence length mismatch: expected %d, got %d: %v", len(expectedSeq), len(actualSeq), actualSeq)
	}

	for i, exp := range expectedSeq {
		if actualSeq[i] != exp {
			t.Errorf("step %d mismatch: expected '%s', got '%s'", i, exp, actualSeq[i])
		}
	}
}

// TestVoiceStream_FeedChunk_And_Finish_PipelineEntrypoints 验证 TurnPipeline 调用的 FeedChunk/FlushIteration/Finish 入口。
func TestVoiceStream_FeedChunk_And_Finish_PipelineEntrypoints(t *testing.T) {
	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-entrypoints",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(700)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 1. Chunk 1: 未满 5 字短文本，不触发切句
	if err := vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "你好", Iteration: 0}); err != nil {
		t.Fatalf("FeedChunk 1 failed: %v", err)
	}

	// 2. Chunk 2: 切换到 Iteration 1，上一 Iteration 残余 "你好" 强制切出作为第 1 句
	if err := vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "世界", Iteration: 1}); err != nil {
		t.Fatalf("FeedChunk 2 failed: %v", err)
	}

	// 3. 显式调用 FlushIteration，将 Iteration 1 的 "世界" 强制切出作为第 2 句
	if err := vs.FlushIteration(ctx, turnId); err != nil {
		t.Fatalf("FlushIteration failed: %v", err)
	}

	// 4. Chunk 3: Iteration 2 产生完整句子作为第 3 句
	if err := vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "这是第三个测试句子。", Iteration: 2}); err != nil {
		t.Fatalf("FeedChunk 3 failed: %v", err)
	}

	// 5. 调用 Finish 结束本轮
	if err := vs.Finish(ctx, turnId); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	mockStream.mu.Lock()
	calls := append([]string(nil), mockStream.synthesizeCalls...)
	mockStream.mu.Unlock()

	expectedCalls := []string{
		"你好",
		"世界",
		"这是第三个测试句子。",
	}

	if len(calls) != len(expectedCalls) {
		t.Fatalf("expected %d sentences synthesized, got %d: %v", len(expectedCalls), len(calls), calls)
	}
	for i, exp := range expectedCalls {
		if calls[i] != exp {
			t.Errorf("sentence %d mismatch: expected '%s', got '%s'", i, exp, calls[i])
		}
	}
}

// TestVoiceStream_BackpressureAndSlowWriter_MaintainsOrder 验证下行队列背压情况下仍严格保持下发时序且不丢帧。
func TestVoiceStream_BackpressureAndSlowWriter_MaintainsOrder(t *testing.T) {
	var seqMu sync.Mutex
	var actionSeq []string

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		return onPCM(ctx, make([]byte, audio.DownlinkBytesPerFrame))
	}

	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	// 模拟较慢的写入器，制造下行队列背压
	writer.sendTextFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		time.Sleep(5 * time.Millisecond)
		var msg ServerTTSMessage
		if err := json.Unmarshal(payload, &msg); err == nil {
			seqMu.Lock()
			if msg.State == TTSStateSentenceStart {
				actionSeq = append(actionSeq, "text:sentence_start:"+msg.Text)
			} else {
				actionSeq = append(actionSeq, "text:"+msg.State)
			}
			seqMu.Unlock()
		}
		return nil
	}

	writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		time.Sleep(5 * time.Millisecond)
		seqMu.Lock()
		actionSeq = append(actionSeq, "binary:opus")
		seqMu.Unlock()
		return nil
	}

	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		time.Sleep(5 * time.Millisecond)
		seqMu.Lock()
		actionSeq = append(actionSeq, "barrier")
		seqMu.Unlock()
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-backpressure-order",
		TTSClient: ttsClient,
		Writer:    writer,
		Config: SessionConfig{
			TTSPCMQueueCapacity:       2,
			DownlinkOpusQueueCapacity: 2,
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(800)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第一句慢写测试文本。", Iteration: 0})
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第二句慢写测试文本。", Iteration: 0})
	if err := vs.Finish(ctx, turnId); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	seqMu.Lock()
	actualSeq := append([]string(nil), actionSeq...)
	seqMu.Unlock()

	expectedSeq := []string{
		"text:start",
		"text:sentence_start:第一句慢写测试文本。",
		"binary:opus",
		"text:sentence_start:第二句慢写测试文本。",
		"binary:opus",
		"text:stop",
		"barrier",
	}

	if len(actualSeq) != len(expectedSeq) {
		t.Fatalf("backpressure sequence length mismatch: expected %d, got %d: %v", len(expectedSeq), len(actualSeq), actualSeq)
	}

	for i, exp := range expectedSeq {
		if actualSeq[i] != exp {
			t.Errorf("step %d mismatch: expected '%s', got '%s'", i, exp, actualSeq[i])
		}
	}
}

// TestVoiceStream_GoroutineLeak_MultiTurnLoop 循环多轮次验证无 goroutine 泄漏。
func TestVoiceStream_GoroutineLeak_MultiTurnLoop(t *testing.T) {
	baseGoroutines := runtime.NumGoroutine()

	ttsClient := newMockTTSClient(nil)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-leak-check",
		TTSClient: ttsClient,
		Writer:    writer,
	})

	ctx := context.Background()
	for turn := uint64(1); turn <= 10; turn++ {
		if err := vs.StartTurn(ctx, turn); err != nil {
			t.Fatalf("StartTurn %d failed: %v", turn, err)
		}
		_ = vs.FeedText(ctx, turn, "这是一段用于检查多轮循环协程泄漏的测试文本。", 0)
		_ = vs.FinishText(ctx, turn)
		time.Sleep(20 * time.Millisecond)
	}

	_ = vs.Close()

	// 等待所有协程调度退出
	deadline := time.Now().Add(1 * time.Second)
	var finalGoroutines int
	for time.Now().Before(deadline) {
		finalGoroutines = runtime.NumGoroutine()
		if finalGoroutines <= baseGoroutines+2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if finalGoroutines > baseGoroutines+3 {
		t.Fatalf("possible goroutine leak: base=%d, final=%d", baseGoroutines, finalGoroutines)
	}
}

// TestVoiceStream_SingleSentence_CompletionBarrier 验证单句完成屏障：
// 必须同时满足 task-finished、PCM 已交付、句末 Flush 完成且全部 Opus 帧已进入下行队列，
// 下一句才能开始 TTS 任务；句末 Flush 出错时屏障失败并终止轮次。
func TestVoiceStream_SingleSentence_CompletionBarrier(t *testing.T) {
	t.Run("NormalFlushBarrierSequential", func(t *testing.T) {
		var mu sync.Mutex
		trace := make([]string, 0)

		stream := newMockTTSStream()
		stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			mu.Lock()
			trace = append(trace, "synth_start:"+text)
			mu.Unlock()

			// 交付单帧 PCM
			pcm := make([]byte, audio.DownlinkBytesPerFrame)
			if err := onPCM(ctx, pcm); err != nil {
				return err
			}

			mu.Lock()
			trace = append(trace, "synth_pcm_delivered:"+text)
			mu.Unlock()
			return nil
		}

		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()
		writer.sendTextFn = func(ctx context.Context, turnId uint64, payload []byte) error {
			var msg map[string]any
			_ = json.Unmarshal(payload, &msg)
			state, _ := msg["state"].(string)
			text, _ := msg["text"].(string)
			mu.Lock()
			if text != "" {
				trace = append(trace, "downlink:"+state+":"+text)
			} else {
				trace = append(trace, "downlink:"+state)
			}
			mu.Unlock()
			return nil
		}
		writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
			mu.Lock()
			trace = append(trace, "downlink:opus")
			mu.Unlock()
			return nil
		}

		events := make(chan VoiceStreamEvent, 10)
		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-single-barrier",
			TTSClient: ttsClient,
			Writer:    writer,
			OnEvent: func(ev VoiceStreamEvent) {
				events <- ev
			},
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(101)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第一句屏障测试文本。", Iteration: 0})
		_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第二句屏障测试文本。", Iteration: 0})
		if err := vs.Finish(ctx, turnId); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		// 等待最终成功事件
		select {
		case ev := <-events:
			if ev.Kind != VoiceStreamEventSpeaking {
				t.Fatalf("expected speaking event first, got %v", ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for speaking event")
		}

		select {
		case ev := <-events:
			if ev.Kind != VoiceStreamEventSuccess {
				t.Fatalf("expected success event, got %v", ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for success event")
		}

		mu.Lock()
		snapshot := append([]string(nil), trace...)
		mu.Unlock()

		// 验证第一句的 synth_start 必须早于第二句的 synth_start
		var firstSynthStart, secondSynthStart int
		for i, item := range snapshot {
			if item == "synth_start:第一句屏障测试文本。" {
				firstSynthStart = i
			}
			if item == "synth_start:第二句屏障测试文本。" {
				secondSynthStart = i
			}
		}
		if firstSynthStart >= secondSynthStart {
			t.Fatalf("sentence 1 must start synthesize before sentence 2: trace=%v", snapshot)
		}

		// 验证下行队列顺序：第二句 sentence_start 必须严格在第一句全部 opus 之后
		var firstSentenceStart, firstSentenceOpus, secondSentenceStart int
		for i, item := range snapshot {
			if item == "downlink:sentence_start:第一句屏障测试文本。" {
				firstSentenceStart = i
			}
			if item == "downlink:opus" && firstSentenceOpus == 0 {
				firstSentenceOpus = i
			}
			if item == "downlink:sentence_start:第二句屏障测试文本。" {
				secondSentenceStart = i
			}
		}

		if firstSentenceStart >= firstSentenceOpus {
			t.Fatalf("first sentence opus must follow its sentence_start: trace=%v", snapshot)
		}
		if firstSentenceOpus >= secondSentenceStart {
			t.Fatalf("second sentence_start must not overtake first sentence opus: trace=%v", snapshot)
		}
	})

	t.Run("FlushErrorTerminatesTurn", func(t *testing.T) {
		stream := newMockTTSStream()
		secondSentenceStarted := make(chan struct{})
		stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			if text == "第二句绝不会被执行。" {
				close(secondSentenceStarted)
				return nil
			}
			return errors.New("dashscope task finished with error")
		}

		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()
		events := make(chan VoiceStreamEvent, 10)

		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-barrier-err",
			TTSClient: ttsClient,
			Writer:    writer,
			OnEvent: func(ev VoiceStreamEvent) {
				events <- ev
			},
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(102)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第一句合成将失败。", Iteration: 0})
		_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第二句绝不会被执行。", Iteration: 0})
		_ = vs.Finish(ctx, turnId)

		select {
		case ev := <-events:
			if ev.Kind == VoiceStreamEventSpeaking {
				select {
				case ev2 := <-events:
					if ev2.Kind != VoiceStreamEventFailed {
						t.Fatalf("expected failed event, got %v", ev2)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timeout waiting for failed event")
				}
			} else if ev.Kind != VoiceStreamEventFailed {
				t.Fatalf("expected failed event, got %v", ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for event")
		}

		select {
		case <-secondSentenceStarted:
			t.Fatal("sentence 2 synthesize should never be called when sentence 1 fails")
		default:
		}
	})
}

// TestVoiceStream_MultiSentence_TurnEndClose 验证多句轮末先关闭 TTSStream，再排入 tts/stop 和 Writer 屏障。
func TestVoiceStream_MultiSentence_TurnEndClose(t *testing.T) {
	var mu sync.Mutex
	actionOrder := make([]string, 0)

	stream := newMockTTSStream()
	stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		mu.Lock()
		actionOrder = append(actionOrder, "synth:"+text)
		mu.Unlock()
		return onPCM(ctx, make([]byte, audio.DownlinkBytesPerFrame))
	}
	stream.closeFn = func() error {
		mu.Lock()
		actionOrder = append(actionOrder, "tts_stream_close")
		mu.Unlock()
		return nil
	}

	ttsClient := newMockTTSClient(stream)
	writer := newMockVoiceWriter()
	writer.sendTextFn = func(ctx context.Context, turnId uint64, payload []byte) error {
		var msg map[string]any
		_ = json.Unmarshal(payload, &msg)
		state, _ := msg["state"].(string)
		msgType, _ := msg["type"].(string)
		mu.Lock()
		actionOrder = append(actionOrder, "writer_text:"+msgType+"/"+state)
		mu.Unlock()
		return nil
	}
	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		mu.Lock()
		actionOrder = append(actionOrder, "writer_barrier")
		mu.Unlock()
		return nil
	}

	events := make(chan VoiceStreamEvent, 10)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-multi-close",
		TTSClient: ttsClient,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			events <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(201)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第一句多句轮末测试文本。", Iteration: 0})
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第二句多句轮末测试文本。", Iteration: 0})
	if err := vs.Finish(ctx, turnId); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	successReceived := false
	for !successReceived {
		select {
		case ev := <-events:
			if ev.Kind == VoiceStreamEventSuccess {
				successReceived = true
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for VoiceStreamEventSuccess")
		}
	}

	mu.Lock()
	seq := append([]string(nil), actionOrder...)
	mu.Unlock()

	var idxSynth1, idxSynth2, idxClose, idxStop, idxBarrier int
	for i, item := range seq {
		switch item {
		case "synth:第一句多句轮末测试文本。":
			idxSynth1 = i
		case "synth:第二句多句轮末测试文本。":
			idxSynth2 = i
		case "tts_stream_close":
			idxClose = i
		case "writer_text:tts/stop":
			idxStop = i
		case "writer_barrier":
			idxBarrier = i
		}
	}

	if idxSynth1 >= idxSynth2 {
		t.Fatalf("sentence 1 should be synthesized before sentence 2: seq=%v", seq)
	}
	if idxSynth2 >= idxClose {
		t.Fatalf("all sentences must finish before tts_stream_close: seq=%v", seq)
	}
	if idxClose >= idxStop {
		t.Fatalf("tts_stream_close must occur before tts/stop: seq=%v", seq)
	}
	if idxStop >= idxBarrier {
		t.Fatalf("tts/stop must be sent before writer_barrier: seq=%v", seq)
	}
}

// TestVoiceStream_Stop_NotCompletedBeforeBarrierWritten 验证在 Writer 屏障确认写出前，轮次不得完成。
func TestVoiceStream_Stop_NotCompletedBeforeBarrierWritten(t *testing.T) {
	barrierStarted := make(chan struct{})
	barrierRelease := make(chan struct{})

	stream := newMockTTSStream()
	ttsClient := newMockTTSClient(stream)
	writer := newMockVoiceWriter()

	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		close(barrierStarted)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-barrierRelease:
			return nil
		}
	}

	events := make(chan VoiceStreamEvent, 10)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-stop-barrier-wait",
		TTSClient: ttsClient,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			events <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(301)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "这是一句用于测试屏障阻塞的文本。", Iteration: 0})
	if err := vs.Finish(ctx, turnId); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	select {
	case <-barrierStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for EnqueueBarrierWait to be called")
	}

	hasSpeaking := false
drainLoop:
	for {
		select {
		case ev := <-events:
			if ev.Kind == VoiceStreamEventSpeaking {
				hasSpeaking = true
			} else if ev.Kind == VoiceStreamEventSuccess {
				t.Fatal("received VoiceStreamEventSuccess prematurely before barrier written confirmation")
			}
		default:
			break drainLoop
		}
	}

	if !hasSpeaking {
		t.Fatal("expected speaking event to be delivered before barrier")
	}

	close(barrierRelease)

	select {
	case ev := <-events:
		if ev.Kind != VoiceStreamEventSuccess {
			t.Fatalf("expected VoiceStreamEventSuccess after barrier released, got %v", ev)
		}
		if ev.TurnId != turnId {
			t.Fatalf("expected turnId %d, got %d", turnId, ev.TurnId)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for VoiceStreamEventSuccess after barrier release")
	}
}

// TestVoiceStream_Stop_WriteFailure_CannotSucceed 验证 stop 写失败或屏障失败时绝不能报告成功，且终态事件只发射一次。
func TestVoiceStream_Stop_WriteFailure_CannotSucceed(t *testing.T) {
	t.Run("BarrierFailure_CannotSucceed", func(t *testing.T) {
		stream := newMockTTSStream()
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()

		barrierErr := errors.New("underlying socket barrier failure")
		writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
			return barrierErr
		}

		events := make(chan VoiceStreamEvent, 10)
		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-barrier-fail",
			TTSClient: ttsClient,
			Writer:    writer,
			OnEvent: func(ev VoiceStreamEvent) {
				events <- ev
			},
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(401)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "测试屏障失败时绝不能成功。", Iteration: 0})
		if err := vs.Finish(ctx, turnId); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		var failedCount, successCount int
		var terminalErr error

		// 等待终态 Failed 事件
		select {
		case ev := <-events:
			if ev.Kind == VoiceStreamEventSpeaking {
				select {
				case ev2 := <-events:
					if ev2.Kind == VoiceStreamEventFailed {
						failedCount++
						terminalErr = ev2.Err
					} else if ev2.Kind == VoiceStreamEventSuccess {
						successCount++
					}
				case <-time.After(1 * time.Second):
					t.Fatal("timeout waiting for terminal event after speaking")
				}
			} else if ev.Kind == VoiceStreamEventFailed {
				failedCount++
				terminalErr = ev.Err
			} else if ev.Kind == VoiceStreamEventSuccess {
				successCount++
			}
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for terminal event")
		}

		// 排空事件队列，确认无 Success 到达
		time.Sleep(20 * time.Millisecond)
	drainLoop1:
		for {
			select {
			case ev := <-events:
				if ev.Kind == VoiceStreamEventFailed {
					failedCount++
				} else if ev.Kind == VoiceStreamEventSuccess {
					successCount++
				}
			default:
				break drainLoop1
			}
		}

		if successCount > 0 {
			t.Fatalf("must not succeed on barrier failure, got %d success events", successCount)
		}
		if failedCount != 1 {
			t.Fatalf("expected exactly 1 failed event, got %d", failedCount)
		}
		if !errors.Is(terminalErr, barrierErr) {
			t.Fatalf("expected error %v, got %v", barrierErr, terminalErr)
		}
	})

	t.Run("StopTextFrameFailure_CannotSucceed", func(t *testing.T) {
		stream := newMockTTSStream()
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()

		stopSendErr := errors.New("network broken when sending tts/stop")
		writer.sendTextFn = func(ctx context.Context, turnId uint64, payload []byte) error {
			var msg map[string]any
			_ = json.Unmarshal(payload, &msg)
			if msg["type"] == "tts" && msg["state"] == "stop" {
				return stopSendErr
			}
			return nil
		}

		events := make(chan VoiceStreamEvent, 10)
		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-stop-text-fail",
			TTSClient: ttsClient,
			Writer:    writer,
			OnEvent: func(ev VoiceStreamEvent) {
				events <- ev
			},
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(402)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "测试发送 stop 失败时绝不成功。", Iteration: 0})
		if err := vs.Finish(ctx, turnId); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		var failedCount, successCount int
		var terminalErr error

		// 等待终态 Failed 事件
		select {
		case ev := <-events:
			if ev.Kind == VoiceStreamEventSpeaking {
				select {
				case ev2 := <-events:
					if ev2.Kind == VoiceStreamEventFailed {
						failedCount++
						terminalErr = ev2.Err
					} else if ev2.Kind == VoiceStreamEventSuccess {
						successCount++
					}
				case <-time.After(1 * time.Second):
					t.Fatal("timeout waiting for terminal event after speaking")
				}
			} else if ev.Kind == VoiceStreamEventFailed {
				failedCount++
				terminalErr = ev.Err
			} else if ev.Kind == VoiceStreamEventSuccess {
				successCount++
			}
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for terminal event")
		}

		// 排空事件队列，确认无 Success 到达
		time.Sleep(20 * time.Millisecond)
	drainLoop2:
		for {
			select {
			case ev := <-events:
				if ev.Kind == VoiceStreamEventFailed {
					failedCount++
				} else if ev.Kind == VoiceStreamEventSuccess {
					successCount++
				}
			default:
				break drainLoop2
			}
		}

		if successCount > 0 {
			t.Fatalf("must not succeed on stop frame write failure, got %d success events", successCount)
		}
		if failedCount != 1 {
			t.Fatalf("expected exactly 1 failed event, got %d", failedCount)
		}
		if !errors.Is(terminalErr, stopSendErr) {
			t.Fatalf("expected error %v, got %v", stopSendErr, terminalErr)
		}
	})
}

// TestVoiceStream_TTS_CloseError_DoesNotAffectSuccess 验证多句场景下 TTS 关闭错误仅打日志，不影响最终轮次成功。
func TestVoiceStream_TTS_CloseError_DoesNotAffectSuccess(t *testing.T) {
	stream := newMockTTSStream()
	ttsCloseErr := errors.New("websocket close handshake connection reset by peer")
	stream.closeFn = func() error {
		return ttsCloseErr
	}

	ttsClient := newMockTTSClient(stream)
	writer := newMockVoiceWriter()

	events := make(chan VoiceStreamEvent, 10)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-tts-close-err",
		TTSClient: ttsClient,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			events <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(501)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第一句关闭异常测试文本。", Iteration: 0})
	_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "第二句关闭异常测试文本。", Iteration: 0})
	if err := vs.Finish(ctx, turnId); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	var hasSpeaking, hasSuccess, hasFailed bool
	deadline := time.After(3 * time.Second)

drainLoop:
	for {
		select {
		case ev := <-events:
			switch ev.Kind {
			case VoiceStreamEventSpeaking:
				hasSpeaking = true
			case VoiceStreamEventSuccess:
				hasSuccess = true
			case VoiceStreamEventFailed:
				hasFailed = true
			}
		case <-deadline:
			break drainLoop
		}
		if hasSuccess {
			break drainLoop
		}
	}

	if !hasSpeaking {
		t.Error("expected speaking event")
	}
	if !hasSuccess {
		t.Error("expected success event even if tts close failed")
	}
	if hasFailed {
		t.Error("tts close handshake error should not trigger failure event")
	}

	if stream.closeCalls != 1 {
		t.Fatalf("expected close calls 1, got %d", stream.closeCalls)
	}
	if writer.barrierCalls != 1 {
		t.Fatalf("expected barrier calls 1, got %d", writer.barrierCalls)
	}
}

// TestVoiceStream_NoText_DirectSuccess 验证整轮无文本直接发射 VoiceStreamEventSuccess，不建连且不下发控制帧。
func TestVoiceStream_NoText_DirectSuccess(t *testing.T) {
	t.Run("DirectFinishWithoutFeed", func(t *testing.T) {
		stream := newMockTTSStream()
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()

		events := make(chan VoiceStreamEvent, 10)
		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-no-text-1",
			TTSClient: ttsClient,
			Writer:    writer,
			OnEvent: func(ev VoiceStreamEvent) {
				events <- ev
			},
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(601)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		if err := vs.FinishText(ctx, turnId); err != nil {
			t.Fatalf("FinishText failed: %v", err)
		}

		select {
		case ev := <-events:
			if ev.Kind != VoiceStreamEventSuccess {
				t.Fatalf("expected success event, got %v", ev)
			}
			if ev.TurnId != turnId {
				t.Fatalf("expected turnId %d, got %d", turnId, ev.TurnId)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for success event")
		}

		select {
		case ev := <-events:
			t.Fatalf("unexpected extra event: %v", ev)
		default:
		}

		if ttsClient.createCalls != 0 {
			t.Fatalf("expected 0 tts client CreateStream calls, got %d", ttsClient.createCalls)
		}
		if len(writer.textFrames) != 0 {
			t.Fatalf("expected 0 writer text frames, got %d", len(writer.textFrames))
		}
		if len(writer.binaryFrames) != 0 {
			t.Fatalf("expected 0 writer binary frames, got %d", len(writer.binaryFrames))
		}
		if writer.barrierCalls != 0 {
			t.Fatalf("expected 0 writer barrier calls, got %d", writer.barrierCalls)
		}

		time.Sleep(20 * time.Millisecond)
		if workers := vs.ActiveWorkers(); workers != 0 {
			t.Fatalf("expected 0 active workers, got %d", workers)
		}
	})

	t.Run("OnlyWhitespaceFeedDirectSuccess", func(t *testing.T) {
		stream := newMockTTSStream()
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()

		events := make(chan VoiceStreamEvent, 10)
		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-no-text-2",
			TTSClient: ttsClient,
			Writer:    writer,
			OnEvent: func(ev VoiceStreamEvent) {
				events <- ev
			},
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(602)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedChunk(ctx, turnId, ai.LLMChunk{Text: "    \n\t   ", Iteration: 0})
		if err := vs.Finish(ctx, turnId); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		select {
		case ev := <-events:
			if ev.Kind != VoiceStreamEventSuccess {
				t.Fatalf("expected success event, got %v", ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for success event")
		}

		if ttsClient.createCalls != 0 {
			t.Fatalf("expected 0 tts create calls, got %d", ttsClient.createCalls)
		}
		if len(writer.textFrames) != 0 || writer.barrierCalls != 0 {
			t.Fatalf("expected 0 frames sent, got %d text frames, %d barrier calls", len(writer.textFrames), writer.barrierCalls)
		}
	})
}

// TestVoiceStream_ThreeQueuesBackpressureAndCancelUnblock 验证文本、PCM、下行三队列满载背压与取消唤醒解除。
func TestVoiceStream_ThreeQueuesBackpressureAndCancelUnblock(t *testing.T) {
	t.Run("SentenceQueueBackpressureAndCancelUnblock", func(t *testing.T) {
		synthBlock := make(chan struct{})
		synthStarted := make(chan struct{}, 1)

		stream := newMockTTSStream()
		stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			select {
			case synthStarted <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-synthBlock:
				return nil
			}
		}
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()

		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-backpressure-sentence",
			TTSClient: ttsClient,
			Writer:    writer,
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(701)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		// 投递第 1 句，使 sentenceWorker 阻塞在 SynthesizeSentence
		_ = vs.FeedText(ctx, turnId, "第一句测试文本，用于阻塞句子工作协程！", 0)
		select {
		case <-synthStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for synthesis to start")
		}

		// 填满文本句队列（容量为 100）
		for i := 0; i < DefaultSentenceQueueCapacity; i++ {
			err := vs.FeedText(ctx, turnId, "这是填满队列的测试句子！", 0)
			if err != nil {
				t.Fatalf("unexpected error feeding sentence %d: %v", i, err)
			}
		}

		// 第 102 句投递将因队列满载而阻塞
		feedErrCh := make(chan error, 1)
		feedStarted := make(chan struct{})
		go func() {
			close(feedStarted)
			err := vs.FeedText(ctx, turnId, "这是超额阻塞的测试句子！", 0)
			feedErrCh <- err
		}()

		<-feedStarted
		select {
		case err := <-feedErrCh:
			t.Fatalf("feed should block on full queue, but returned immediately: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		// 取消当前轮次，满载等待必须被立即唤醒并解除
		vs.CancelTurn(turnId)

		select {
		case err := <-feedErrCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled on unblock, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for blocked feed to unblock after cancel")
		}

		close(synthBlock)
	})

	t.Run("PCMQueueBackpressureAndCancelUnblock", func(t *testing.T) {
		pcmBlock := make(chan struct{})
		writerBlock := make(chan struct{})

		stream := newMockTTSStream()
		stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			pcmChunk := make([]byte, audio.DownlinkBytesPerFrame)
			for i := 0; i < 10; i++ {
				if err := onPCM(ctx, pcmChunk); err != nil {
					return err
				}
			}
			<-pcmBlock
			return nil
		}
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()
		writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-writerBlock:
				return nil
			}
		}

		cfg := SessionConfig{
			TTSPCMQueueCapacity:       2,
			DownlinkOpusQueueCapacity: 2,
			MaxOpusPacketBytes:        1275,
		}

		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-backpressure-pcm",
			TTSClient: ttsClient,
			Writer:    writer,
			Config:    cfg,
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(702)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedText(ctx, turnId, "测试 PCM 队列背压与取消解除！", 0)

		// 等待 PCM 队列填满并进入背压阻塞
		time.Sleep(100 * time.Millisecond)

		// 取消轮次，所有队列背压必须立即解除
		vs.CancelTurn(turnId)

		close(pcmBlock)
		close(writerBlock)

		time.Sleep(50 * time.Millisecond)
		if workers := vs.ActiveWorkers(); workers != 0 {
			t.Fatalf("expected 0 active workers after cancel, got %d", workers)
		}
	})

	t.Run("DownlinkQueueBackpressureAndCancelUnblock", func(t *testing.T) {
		writerBlock := make(chan struct{})
		stream := newMockTTSStream()
		stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			// 发送足够多的音频帧使下行队列填满
			for i := 0; i < 20; i++ {
				pcmChunk := make([]byte, audio.DownlinkBytesPerFrame)
				if err := onPCM(ctx, pcmChunk); err != nil {
					return err
				}
			}
			return nil
		}
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()
		writer.sendBinaryFn = func(ctx context.Context, turnId uint64, payload []byte) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-writerBlock:
				return nil
			}
		}

		cfg := SessionConfig{
			TTSPCMQueueCapacity:       10,
			DownlinkOpusQueueCapacity: 2,
			MaxOpusPacketBytes:        1275,
		}

		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-backpressure-downlink",
			TTSClient: ttsClient,
			Writer:    writer,
			Config:    cfg,
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(703)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedText(ctx, turnId, "测试下行队列背压与取消解除！", 0)

		time.Sleep(100 * time.Millisecond)

		// 取消轮次，下行阻塞立即解除
		vs.CancelTurn(turnId)

		close(writerBlock)

		time.Sleep(50 * time.Millisecond)
		if workers := vs.ActiveWorkers(); workers != 0 {
			t.Fatalf("expected 0 active workers after cancel, got %d", workers)
		}
	})
}

// TestVoiceStream_StaleEventsIsolationAndDrop 验证迟到 PCM、Opus 与终态事件隔离丢弃。
func TestVoiceStream_StaleEventsIsolationAndDrop(t *testing.T) {
	t.Run("StalePCMAndDownlinkFramesDropped", func(t *testing.T) {
		stream := newMockTTSStream()
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()

		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-stale-isolation",
			TTSClient: ttsClient,
			Writer:    writer,
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(801)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		// 获取内部 turn 引用以注入异轮次/迟到数据
		vs.mu.Lock()
		activeTurn := vs.activeTurn
		vs.mu.Unlock()

		// 注入携带旧轮次 turnId 的 PCM 任务
		stalePCM := pcmJob{
			turnId:   799,
			sequence: 1,
			data:     make([]byte, audio.DownlinkBytesPerFrame),
		}
		activeTurn.pcmQueue <- stalePCM

		// 注入携带旧轮次 turnId 的下行帧
		staleDownlink := downlinkFrame{
			turnId:   799,
			sequence: 1,
			kind:     frameKindBinary,
			payload:  []byte("stale-opus-data"),
		}
		activeTurn.downlinkQueue <- staleDownlink

		// 投递本轮正常文本并收尾
		_ = vs.FeedText(ctx, turnId, "这是当前轮次的正常文本！", 0)
		_ = vs.FinishText(ctx, turnId)

		time.Sleep(100 * time.Millisecond)

		// 验证 Writer 从未收到异轮次的二进制数据
		writer.mu.Lock()
		for _, bin := range writer.binaryFrames {
			if string(bin) == "stale-opus-data" {
				t.Fatalf("stale downlink frame should be dropped, but was sent to writer")
			}
		}
		writer.mu.Unlock()
	})

	t.Run("StaleTerminalEventsIsolation", func(t *testing.T) {
		stream := newMockTTSStream()
		synthStarted := make(chan struct{})
		stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			close(synthStarted)
			<-ctx.Done()
			return errors.New("stale dashscope task failed")
		}
		ttsClient := newMockTTSClient(stream)
		writer := newMockVoiceWriter()

		events := make(chan VoiceStreamEvent, 10)
		vs := NewVoiceStream(VoiceStreamOptions{
			SessionId: "sess-stale-terminal",
			TTSClient: ttsClient,
			Writer:    writer,
			OnEvent: func(ev VoiceStreamEvent) {
				events <- ev
			},
		})
		defer vs.Close()

		ctx := context.Background()
		turnId := uint64(802)
		if err := vs.StartTurn(ctx, turnId); err != nil {
			t.Fatalf("StartTurn failed: %v", err)
		}

		_ = vs.FeedText(ctx, turnId, "测试迟到终态事件隔离！", 0)

		select {
		case <-synthStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for synth to start")
		}

		// 主动取消当前轮次
		vs.CancelTurn(turnId)

		time.Sleep(100 * time.Millisecond)

		// 校验外部监听器绝不收到由于取消引发的迟到 Failed 或 Success 终态事件
		for {
			select {
			case ev := <-events:
				if ev.Kind == VoiceStreamEventSuccess {
					t.Fatalf("unexpected success event received after turn canceled: %v", ev)
				}
				if ev.Kind == VoiceStreamEventFailed {
					t.Fatalf("stale failed event should be isolated, but received: %v", ev)
				}
			default:
				return
			}
		}
	})
}

// TestVoiceStream_AbortWithStartSent_SendsFallbackStop 验证已发送 start 后的 abort 必须补发普通控制帧 tts/stop。
func TestVoiceStream_AbortWithStartSent_SendsFallbackStop(t *testing.T) {
	stream := newMockTTSStream()
	synthStarted := make(chan struct{})
	stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		close(synthStarted)
		// 交付一部分 PCM 并等待取消
		pcmChunk := make([]byte, audio.DownlinkBytesPerFrame)
		_ = onPCM(ctx, pcmChunk)
		<-ctx.Done()
		return ctx.Err()
	}
	ttsClient := newMockTTSClient(stream)
	writer := newMockVoiceWriter()

	events := make(chan VoiceStreamEvent, 10)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-abort-with-start",
		TTSClient: ttsClient,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			events <- ev
		},
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(901)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	_ = vs.FeedText(ctx, turnId, "你好，这是一条需要发送 start 的测试文本！", 0)

	select {
	case <-synthStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for synth to start")
	}

	// 此时已发送 tts/start，收到 speaking 事件
	select {
	case ev := <-events:
		if ev.Kind != VoiceStreamEventSpeaking {
			t.Fatalf("expected speaking event, got %v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for speaking event")
	}

	// 执行 abort 取消
	vs.CancelTurn(turnId)

	// 等待后台异步有界清理执行
	time.Sleep(100 * time.Millisecond)

	// 验证必须通过 SendText 补发非语音普通控制帧 tts/stop
	writer.mu.Lock()
	controlFrames := make([]string, len(writer.controlTextFrames))
	copy(controlFrames, writer.controlTextFrames)
	writer.mu.Unlock()

	if len(controlFrames) == 0 {
		t.Fatal("expected fallback tts/stop in controlTextFrames, but none was sent")
	}

	var foundStop bool
	for _, frame := range controlFrames {
		var msg map[string]any
		if err := json.Unmarshal([]byte(frame), &msg); err == nil {
			if msg["type"] == "tts" && msg["state"] == "stop" && msg["session_id"] == "sess-abort-with-start" {
				foundStop = true
				break
			}
		}
	}

	if !foundStop {
		t.Fatalf("expected valid tts/stop message in controlTextFrames, got: %v", controlFrames)
	}

	// 验证只补发了一次 stop
	stopCount := 0
	for _, frame := range controlFrames {
		var msg map[string]any
		if err := json.Unmarshal([]byte(frame), &msg); err == nil {
			if msg["type"] == "tts" && msg["state"] == "stop" {
				stopCount++
			}
		}
	}
	if stopCount != 1 {
		t.Fatalf("expected exactly 1 fallback tts/stop, got %d", stopCount)
	}
}

// TestVoiceStream_AbortWithoutStartSent_NoConnectAndNoStop 验证未发送 start 的 abort 绝不建连且绝不补发 tts/stop。
func TestVoiceStream_AbortWithoutStartSent_NoConnectAndNoStop(t *testing.T) {
	stream := newMockTTSStream()
	ttsClient := newMockTTSClient(stream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-abort-without-start",
		TTSClient: ttsClient,
		Writer:    writer,
	})
	defer vs.Close()

	ctx := context.Background()
	turnId := uint64(902)
	if err := vs.StartTurn(ctx, turnId); err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}

	// 尚未输入任何文本，直接执行 abort 取消
	vs.CancelTurn(turnId)

	time.Sleep(100 * time.Millisecond)

	// 验证绝不建立 TTSStream 连接
	if ttsClient.createCalls != 0 {
		t.Fatalf("expected 0 tts client CreateStream calls, got %d", ttsClient.createCalls)
	}

	// 验证绝不下发任何 tts/stop 控制帧
	writer.mu.Lock()
	textFramesCount := len(writer.textFrames)
	writer.mu.Unlock()

	if textFramesCount != 0 {
		t.Fatalf("expected 0 text frames, got %d", textFramesCount)
	}
}

// TestVoiceStream_NextTurnUnblockedAndUnaffectedByOldTurnCancel 验证新一轮回答不受旧轮次取消影响且立即接受不阻塞。
func TestVoiceStream_NextTurnUnblockedAndUnaffectedByOldTurnCancel(t *testing.T) {
	stream1 := newMockTTSStream()
	stream1Started := make(chan struct{})
	stream1.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		close(stream1Started)
		pcmChunk := make([]byte, audio.DownlinkBytesPerFrame)
		_ = onPCM(ctx, pcmChunk)
		<-ctx.Done()
		return ctx.Err()
	}

	stream2 := newMockTTSStream()
	stream2.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		pcmChunk := make([]byte, audio.DownlinkBytesPerFrame)
		return onPCM(ctx, pcmChunk)
	}

	client := &mockTTSClient{
		createStreamFn: func(ctx context.Context) (ai.TTSStream, error) {
			if ctx.Value("turn") == uint64(1) {
				return stream1, nil
			}
			return stream2, nil
		},
	}
	writer := newMockVoiceWriter()

	events := make(chan VoiceStreamEvent, 20)
	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-multi-turn-isolate",
		TTSClient: client,
		Writer:    writer,
		OnEvent: func(ev VoiceStreamEvent) {
			events <- ev
		},
	})
	defer vs.Close()

	// 启动第 1 轮
	ctx1 := context.WithValue(context.Background(), "turn", uint64(1))
	if err := vs.StartTurn(ctx1, 1); err != nil {
		t.Fatalf("StartTurn 1 failed: %v", err)
	}
	_ = vs.FeedText(ctx1, 1, "这是第 1 轮的测试文本！", 0)

	select {
	case <-stream1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for stream1 to start")
	}

	// 立即开启第 2 轮，必须立即返回且不被第 1 轮有界清理阻塞
	start := time.Now()
	ctx2 := context.WithValue(context.Background(), "turn", uint64(2))
	if err := vs.StartTurn(ctx2, 2); err != nil {
		t.Fatalf("StartTurn 2 failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("StartTurn 2 took too long (%v), should return immediately", elapsed)
	}

	// 第 2 轮正常投递文本并收尾
	_ = vs.FeedText(ctx2, 2, "这是第 2 轮正常回答文本！", 0)
	_ = vs.FinishText(ctx2, 2)

	// 等待第 2 轮成功事件
	var turn2Success bool
	timeout := time.After(3 * time.Second)
	for !turn2Success {
		select {
		case ev := <-events:
			if ev.TurnId == 2 && ev.Kind == VoiceStreamEventSuccess {
				turn2Success = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for turn 2 success event")
		}
	}

	// 验证旧轮次 1 的流被正确关闭
	if stream1.CloseCalls() == 0 {
		t.Fatal("stream1 should be closed after cancel")
	}

	// 验证第 1 轮补发了 tts/stop
	writer.mu.Lock()
	controlFrames := make([]string, len(writer.controlTextFrames))
	copy(controlFrames, writer.controlTextFrames)
	writer.mu.Unlock()

	var foundFallbackStop bool
	for _, frame := range controlFrames {
		var msg map[string]any
		if err := json.Unmarshal([]byte(frame), &msg); err == nil {
			if msg["type"] == "tts" && msg["state"] == "stop" {
				foundFallbackStop = true
				break
			}
		}
	}
	if !foundFallbackStop {
		t.Fatal("expected fallback stop sent for turn 1")
	}
}
