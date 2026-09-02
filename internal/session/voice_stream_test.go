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
	mu               sync.Mutex
	textFrames       []string
	binaryFrames     [][]byte
	barrierCalls     int
	sendTextFn       func(ctx context.Context, turnId uint64, payload []byte) error
	sendBinaryFn     func(ctx context.Context, turnId uint64, payload []byte) error
	enqueueBarrierFn func(ctx context.Context, turnId uint64) error
}

func newMockVoiceWriter() *mockVoiceWriter {
	return &mockVoiceWriter{
		textFrames:   make([]string, 0),
		binaryFrames: make([][]byte, 0),
	}
}

func (w *mockVoiceWriter) SendVoiceTextWait(ctx context.Context, turnId uint64, payload []byte) error {
	w.mu.Lock()
	w.textFrames = append(w.textFrames, string(payload))
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
