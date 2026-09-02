package session

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockLLMClient struct {
	mu          sync.Mutex
	callCount   int
	generate    func(ctx context.Context, request ai.LLMRequest, callback ai.LLMStreamCallback) (string, error)
	reqReceived []ai.LLMRequest
}

func (m *mockLLMClient) Generate(ctx context.Context, request ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
	m.mu.Lock()
	m.callCount++
	m.reqReceived = append(m.reqReceived, request)
	fn := m.generate
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, request, callback)
	}
	if callback != nil {
		_ = callback(ctx, ai.LLMChunk{Text: "默认回复", Iteration: 0})
	}
	return "默认回复", nil
}

func waitTurnCompleted(t *testing.T, events <-chan turnEvent) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.typ == turnEventTurnCompleted {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for turnEventTurnCompleted")
		}
	}
}

func TestTurnPipeline_MultiTurnTools_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockLLM := &mockLLMClient{}

	var toolExecuted bool
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		for _, tool := range req.Tools {
			if tool.Name == agentkit.ToolGetCurrentTime {
				res, err := tool.Run(ctx, map[string]any{})
				if err != nil {
					return "", err
				}
				if res != nil {
					toolExecuted = true
				}
			}
		}

		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "正在为您查询当前时间。", Iteration: 0})
			_ = callback(ctx, ai.LLMChunk{Text: "当前时间是上午十点。", Iteration: 1})
		}
		return "当前时间是上午十点。", nil
	}

	events := make(chan turnEvent, 10)
	toolProvider := NewToolProvider(nil, nil, slog.Default())
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:    mockLLM,
		Config:       NormalizeConfig(SessionConfig{}),
		ToolProvider: toolProvider,
		Logger:       slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})

	_ = pipeline.StartListening(ctx, 1, "sess-multi-tool-test", ListenModeAuto)
	_ = pipeline.StartResponse(1, "sess-multi-tool-test", "现在几点了")

	select {
	case ev := <-events:
		if ev.typ != turnEventTurnCompleted {
			t.Fatalf("expected turnEventTurnCompleted, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartResponse timed out")
	}

	if !toolExecuted {
		t.Fatal("expected server tool to be executed during generate")
	}
}

// TestTurnPipeline_VoiceStream_ProductionChainEndToEnd 验证生产调用链完整连通：
// LLM 流式输出文本进入 VoiceStream、切句、调用 TTSClient 合成并通过 VoiceWriter 下发。
func TestTurnPipeline_VoiceStream_ProductionChainEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-prod-chain-e2e",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "你好世界，很高兴认识你。", Iteration: 0})
		}
		return "你好世界，很高兴认识你。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-prod-chain-e2e", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-prod-chain-e2e", "打个招呼"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	waitTurnCompleted(t, events)

	// 验证 TTS 合成确实被调用
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mockStream.mu.Lock()
		calls := len(mockStream.synthesizeCalls)
		mockStream.mu.Unlock()
		if calls > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mockStream.mu.Lock()
	calls := mockStream.synthesizeCalls
	mockStream.mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("expected TTSStream.SynthesizeSentence to be called")
	}
	if calls[0] != "你好世界，很高兴认识你。" {
		t.Fatalf("expected sentence '你好世界，很高兴认识你。', got %q", calls[0])
	}

	// 验证下行消息包含控制帧与屏障确认
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		writer.mu.Lock()
		barriers := writer.barrierCalls
		writer.mu.Unlock()
		if barriers > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	writer.mu.Lock()
	frames := writer.textFrames
	barriers := writer.barrierCalls
	writer.mu.Unlock()

	if barriers == 0 {
		t.Fatal("expected barrier to be enqueued")
	}

	hasStart := false
	hasSentenceStart := false
	hasStop := false
	for _, f := range frames {
		if strings.Contains(f, `"type":"tts"`) && strings.Contains(f, `"state":"start"`) {
			hasStart = true
		}
		if strings.Contains(f, `"type":"tts"`) && strings.Contains(f, `"state":"sentence_start"`) {
			hasSentenceStart = true
		}
		if strings.Contains(f, `"type":"tts"`) && strings.Contains(f, `"state":"stop"`) {
			hasStop = true
		}
	}
	if !hasStart {
		t.Error("expected tts/start control frame")
	}
	if !hasSentenceStart {
		t.Error("expected tts/sentence_start control frame")
	}
	if !hasStop {
		t.Error("expected tts/stop control frame")
	}
}

// TestTurnPipeline_VoiceStream_MultiIterationSwitchFlush 验证多 Iteration 切换时强制 Flush 旧 Iteration 残余：
// Iteration 0 的未闭合残余在 Iteration 1 到来时先行切出并发送合成，不与 Iteration 1 混拼。
func TestTurnPipeline_VoiceStream_MultiIterationSwitchFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-multi-iteration",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			// Iteration 0: 输出无终止标点的片段（未达强标点或单句阈值）
			_ = callback(ctx, ai.LLMChunk{Text: "正在为您查询天气", Iteration: 0})
			// 切换到 Iteration 1: 必须触发 Iteration 0 的残余文本 Flush
			_ = callback(ctx, ai.LLMChunk{Text: "今天北京天气晴朗。", Iteration: 1})
		}
		return "今天北京天气晴朗。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-multi-iteration", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-multi-iteration", "天气如何"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	waitTurnCompleted(t, events)

	// 验证两个 Iteration 的文本均被顺序合成，且 Iteration 0 的残余先被切出
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mockStream.mu.Lock()
		calls := len(mockStream.synthesizeCalls)
		mockStream.mu.Unlock()
		if calls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mockStream.mu.Lock()
	calls := mockStream.synthesizeCalls
	mockStream.mu.Unlock()

	if len(calls) < 2 {
		t.Fatalf("expected at least 2 synthesized sentences, got %d: %v", len(calls), calls)
	}
	if calls[0] != "正在为您查询天气" {
		t.Errorf("expected first sentence %q, got %q", "正在为您查询天气", calls[0])
	}
	if calls[1] != "今天北京天气晴朗。" {
		t.Errorf("expected second sentence %q, got %q", "今天北京天气晴朗。", calls[1])
	}
}

// TestTurnPipeline_VoiceStream_TrailingResidueFlush 验证尾部最终残余 Flush：
// LLM 正常结束时，末尾无标点的残余文本通过 Finish 完整切出并合成，不丢字。
func TestTurnPipeline_VoiceStream_TrailingResidueFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-trailing-residue",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			// 输出一段未以标点符号结尾的文本
			_ = callback(ctx, ai.LLMChunk{Text: "这是尾部未带标点的残余文本", Iteration: 0})
		}
		return "这是尾部未带标点的残余文本", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-trailing-residue", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-trailing-residue", "请说话"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	waitTurnCompleted(t, events)

	// 验证残余文本被完整下发合成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mockStream.mu.Lock()
		calls := len(mockStream.synthesizeCalls)
		mockStream.mu.Unlock()
		if calls > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mockStream.mu.Lock()
	calls := mockStream.synthesizeCalls
	mockStream.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 synthesized sentence, got %d: %v", len(calls), calls)
	}
	if calls[0] != "这是尾部未带标点的残余文本" {
		t.Fatalf("expected sentence %q, got %q", "这是尾部未带标点的残余文本", calls[0])
	}
}

// TestTurnPipeline_VoiceStream_FallbackReading_NoStreamedText 验证无流式文本时 finalText 兜底朗读：
// 若流式回调完全未产生非空文本，但 Generate 返回非空 finalText，则以 finalText 执行兜底朗读。
func TestTurnPipeline_VoiceStream_FallbackReading_NoStreamedText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-fallback-reading",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		// 模拟整个流式回调均为空文本块
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "", Iteration: 0})
			_ = callback(ctx, ai.LLMChunk{Text: "   ", Iteration: 0})
		}
		return "这是兜底朗读的完整文本。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-fallback-reading", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-fallback-reading", "你好"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	waitTurnCompleted(t, events)

	// 验证 finalText 作为兜底送入 TTSStream 合成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mockStream.mu.Lock()
		calls := len(mockStream.synthesizeCalls)
		mockStream.mu.Unlock()
		if calls > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mockStream.mu.Lock()
	calls := mockStream.synthesizeCalls
	mockStream.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 fallback sentence, got %d: %v", len(calls), calls)
	}
	if calls[0] != "这是兜底朗读的完整文本。" {
		t.Fatalf("expected fallback sentence %q, got %q", "这是兜底朗读的完整文本。", calls[0])
	}
}

// TestTurnPipeline_VoiceStream_NoDuplicateFallback_WhenStreamedTextExists 验证有流式文本时不重复朗读 finalText：
// 流式已正常输出切句内容时，尾部绝对不重复朗读 finalText。
func TestTurnPipeline_VoiceStream_NoDuplicateFallback_WhenStreamedTextExists(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-no-duplicate-final",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "这是正常的流式回答内容。", Iteration: 0})
		}
		// 返回与流式内容相同的 finalText
		return "这是正常的流式回答内容。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-no-duplicate-final", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-no-duplicate-final", "请回答"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	waitTurnCompleted(t, events)

	// 验证仅合成 1 次，绝不重复合成 finalText
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mockStream.mu.Lock()
		calls := len(mockStream.synthesizeCalls)
		mockStream.mu.Unlock()
		if calls > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 略微等待以确认无后续重复调用
	time.Sleep(100 * time.Millisecond)

	mockStream.mu.Lock()
	calls := mockStream.synthesizeCalls
	mockStream.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 synthesized sentence without duplicate, got %d: %v", len(calls), calls)
	}
	if calls[0] != "这是正常的流式回答内容。" {
		t.Fatalf("expected sentence %q, got %q", "这是正常的流式回答内容。", calls[0])
	}
}

// TestTurnPipeline_VoiceStream_Abort_CancelsVoiceStream 验证轮次中止时正确取消 VoiceStream 协程与资源。
func TestTurnPipeline_VoiceStream_Abort_CancelsVoiceStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-abort-pipeline",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	startedGenerating := make(chan struct{})
	blockGenerating := make(chan struct{})

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		close(startedGenerating)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-blockGenerating:
			return "done", nil
		}
	}

	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-abort-pipeline", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-abort-pipeline", "测试打断"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	<-startedGenerating

	if workers := vs.ActiveWorkers(); workers != 3 {
		t.Fatalf("expected 3 active workers before abort, got %d", workers)
	}

	// 触发流水线打断
	pipeline.Abort(1)

	// 验证 VoiceStream 协程完全退出
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if vs.ActiveWorkers() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 active workers after abort, got %d", workers)
	}
}

// TestTurnPipeline_VoiceStream_LLMError_CancelsVoiceStream 验证 LLM 报错时 VoiceStream 正确取消。
func TestTurnPipeline_VoiceStream_LLMError_CancelsVoiceStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-error-pipeline",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		return "", errors.New("upstream llm connection refused")
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-error-pipeline", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-error-pipeline", "测试报错"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.typ != turnEventTurnFailed {
			t.Fatalf("expected turnEventTurnFailed, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected turnEventTurnFailed timed out")
	}

	// 验证 VoiceStream 协程由于错误取消而迅速退出
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if vs.ActiveWorkers() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workers := vs.ActiveWorkers(); workers != 0 {
		t.Fatalf("expected 0 active workers after error cancel, got %d", workers)
	}
}

// TestTurnPipeline_History_DelayedUntilVoiceStreamBarrier 验证历史记录严格在 tts/stop 屏障写出确认后才追加：
// LLM 完成但屏障未确认时，历史记录为 0；屏障确认后历史记录正确追加。
func TestTurnPipeline_History_DelayedUntilVoiceStreamBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	barrierBlock := make(chan struct{})
	barrierDone := make(chan struct{})
	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		<-barrierBlock
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-delay-hist",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	llmFinished := make(chan struct{})
	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "请等待语音下行完成确认。", Iteration: 0})
		}
		close(llmFinished)
		return "请等待语音下行完成确认。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-delay-hist", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-delay-hist", "何时提交历史"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	// 等待 LLM 执行完成
	select {
	case <-llmFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("LLM generate timed out")
	}

	// 此时首句已下发，会产生 Speaking 事件
	select {
	case ev := <-events:
		if ev.typ != turnEventSpeaking {
			t.Fatalf("expected turnEventSpeaking, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turnEventSpeaking")
	}

	// 关键断言：此时屏障尚未写出确认，历史记录严格为 0 条
	if histLen := pipeline.History().Len(); histLen != 0 {
		t.Fatalf("expected 0 history messages before barrier written confirmation, got %d", histLen)
	}

	// 放行屏障确认
	close(barrierBlock)

	// 等待轮次完成事件
	select {
	case ev := <-events:
		if ev.typ != turnEventTurnCompleted {
			t.Fatalf("expected turnEventTurnCompleted, got %v", ev.typ)
		}
		close(barrierDone)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turnEventTurnCompleted")
	}

	// 关键断言：屏障确认写出后，历史记录严格追加 1 轮（2 条消息）
	if histLen := pipeline.History().Len(); histLen != 2 {
		t.Fatalf("expected 2 history messages after barrier written confirmation, got %d", histLen)
	}
	msgs := pipeline.History().Messages()
	if msgs[0].Content != "何时提交历史" || msgs[1].Content != "请等待语音下行完成确认。" {
		t.Fatalf("unexpected history content: %+v", msgs)
	}
}

// TestTurnPipeline_History_NoTextTurn_DirectSuccess 验证无文本轮次（0 句）直接成功，绝不发送 speaking 事件。
func TestTurnPipeline_History_NoTextTurn_DirectSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-notext-hist",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		// 返回无文本回复（0 句）
		return "", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-notext-hist", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-notext-hist", "无语音问答"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	// 应当直接收到 turnEventTurnCompleted，绝不产生 turnEventSpeaking
	select {
	case ev := <-events:
		if ev.typ != turnEventTurnCompleted {
			t.Fatalf("expected turnEventTurnCompleted directly, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turnEventTurnCompleted")
	}

	// 检查事件队列无其他事件（确保无 turnEventSpeaking）
	select {
	case ev := <-events:
		t.Fatalf("unexpected extra event: %v", ev)
	default:
	}
}

// TestTurnPipeline_History_Abort_DoesNotCommitHistory 验证轮次被中止（Abort）时绝不追加历史记录。
func TestTurnPipeline_History_Abort_DoesNotCommitHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	barrierBlock := make(chan struct{})
	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		<-barrierBlock
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-abort-nohist",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	llmFinished := make(chan struct{})
	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "正在播报但在播报中将被打断。", Iteration: 0})
		}
		close(llmFinished)
		return "正在播报但在播报中将被打断。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-abort-nohist", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-abort-nohist", "播报打断测试"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	select {
	case <-llmFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("LLM generate timed out")
	}

	// 消费 speaking 事件
	select {
	case ev := <-events:
		if ev.typ != turnEventSpeaking {
			t.Fatalf("expected turnEventSpeaking, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turnEventSpeaking")
	}

	// 执行中止
	pipeline.Abort(1)

	// 放行屏障
	close(barrierBlock)

	// 等待并排空事件（可能无后续事件或被取消）
	time.Sleep(100 * time.Millisecond)

	// 关键断言：中止后历史记录严格为 0，绝不追加
	if histLen := pipeline.History().Len(); histLen != 0 {
		t.Fatalf("expected 0 history messages after abort, got %d", histLen)
	}
}

// TestTurnPipeline_History_TTSFailure_DoesNotCommitHistory 验证 TTS 发生错误时绝不追加历史记录。
func TestTurnPipeline_History_TTSFailure_DoesNotCommitHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		return errors.New("upstream tts quota exceeded")
	}
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-tts-fail-nohist",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "该回复合成时将失败。", Iteration: 0})
		}
		return "该回复合成时将失败。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-tts-fail-nohist", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-tts-fail-nohist", "合成失败测试"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	// 等待直到收到 turnEventTurnFailed
	var sawFailed bool
	deadline := time.After(2 * time.Second)
	for !sawFailed {
		select {
		case ev := <-events:
			if ev.typ == turnEventTurnFailed {
				sawFailed = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for turnEventTurnFailed")
		}
	}

	// 关键断言：TTS 失败绝不追加历史
	if histLen := pipeline.History().Len(); histLen != 0 {
		t.Fatalf("expected 0 history messages on tts failure, got %d", histLen)
	}
}

// TestTurnPipeline_History_WriterBarrierFailure_DoesNotCommitHistory 验证写出屏障失败时绝不追加历史记录。
func TestTurnPipeline_History_WriterBarrierFailure_DoesNotCommitHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	writer := newMockVoiceWriter()
	writer.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		return errors.New("underlying socket broken during barrier wait")
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "sess-barrier-fail-nohist",
		TTSClient: ttsClient,
		Writer:    writer,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{}
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "屏障写出将失败。", Iteration: 0})
		}
		return "屏障写出将失败。", nil
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:   mockLLM,
		VoiceStream: vs,
		Config:      NormalizeConfig(SessionConfig{}),
		Logger:      slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})
	defer pipeline.Close()

	if err := pipeline.StartListening(ctx, 1, "sess-barrier-fail-nohist", ListenModeAuto); err != nil {
		t.Fatalf("StartListening failed: %v", err)
	}
	if err := pipeline.StartResponse(1, "sess-barrier-fail-nohist", "屏障写出失败测试"); err != nil {
		t.Fatalf("StartResponse failed: %v", err)
	}

	// 等待直到收到 turnEventTurnFailed
	var sawFailed bool
	deadline := time.After(2 * time.Second)
	for !sawFailed {
		select {
		case ev := <-events:
			if ev.typ == turnEventTurnFailed {
				sawFailed = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for turnEventTurnFailed")
		}
	}

	// 关键断言：屏障失败绝不追加历史
	if histLen := pipeline.History().Len(); histLen != 0 {
		t.Fatalf("expected 0 history messages on barrier failure, got %d", histLen)
	}
}
