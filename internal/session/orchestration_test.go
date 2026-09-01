package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockTTSStream struct {
	mu        sync.Mutex
	pcmChunks [][]byte
	idx       int
	err       error
	finished  bool
	closed    bool
	sentences []string
	onClose   func()
}

func (m *mockTTSStream) SendSentence(ctx context.Context, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentences = append(m.sentences, text)
	return nil
}

func (m *mockTTSStream) Finish(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finished = true
	return nil
}

func (m *mockTTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if m.idx >= len(m.pcmChunks) {
		return nil, io.EOF
	}
	chunk := m.pcmChunks[m.idx]
	m.idx++
	return chunk, nil
}

func (m *mockTTSStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.onClose != nil {
		m.onClose()
	}
	return nil
}

type mockTTSClient struct {
	mu        sync.Mutex
	err       error
	streams   []*mockTTSStream
	newStream func() *mockTTSStream
}

func (m *mockTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	var stream *mockTTSStream
	if m.newStream != nil {
		stream = m.newStream()
	} else {
		stream = &mockTTSStream{
			pcmChunks: [][]byte{make([]byte, 2880)},
		}
	}
	m.streams = append(m.streams, stream)
	return stream, nil
}

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

func TestConsumeSentencesTTS_ExplicitContract_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	// 24000 Hz, 16-bit mono = 48000 bytes/sec, 60ms = 2880 bytes.
	pcmFrame := make([]byte, 2880)
	stream := &mockTTSStream{
		pcmChunks: [][]byte{pcmFrame, pcmFrame},
	}
	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return stream
		},
	}

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		writer:     writer,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-consume-test",
		generation: 1,
		state:      StateSpeaking,
		ttsClient:  mockTTS,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	sentenceCh := make(chan string, 5)
	sentenceCh <- "你好测试"
	close(sentenceCh)

	pcmDone := make(chan error, 1)

	go sess.consumeSentencesTTS(ctx, 1, sentenceCh, pacer, pcmDone)

	select {
	case err, ok := <-pcmDone:
		if !ok {
			t.Fatal("expected channel open before read")
		}
		if err != nil {
			t.Fatalf("consumeSentencesTTS returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumeSentencesTTS timed out")
	}

	select {
	case _, ok := <-pcmDone:
		if ok {
			t.Fatal("expected pcmDone to be closed")
		}
	default:
	}
}

func TestConsumeSentencesTTS_ExplicitContract_StreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("simulated tts stream failure")
	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return &mockTTSStream{err: expectedErr}
		},
	}

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-consume-err-test",
		generation: 1,
		state:      StateSpeaking,
		ttsClient:  mockTTS,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	sentenceCh := make(chan string, 5)
	sentenceCh <- "测试错误句子"
	close(sentenceCh)

	pcmDone := make(chan error, 1)

	go sess.consumeSentencesTTS(ctx, 1, sentenceCh, pacer, pcmDone)

	select {
	case err := <-pcmDone:
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected %v, got %v", expectedErr, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumeSentencesTTS did not return on stream error")
	}

	select {
	case ev := <-sess.events:
		if ev.kind != eventKindError {
			t.Fatalf("expected eventKindError, got %v", ev.kind)
		}
		if !errors.Is(ev.err, expectedErr) {
			t.Fatalf("expected error event with %v, got %v", expectedErr, ev.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected error event posted to session")
	}
}

func TestConsumeSentencesTTS_ExplicitContract_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return &mockTTSStream{pcmChunks: [][]byte{make([]byte, 2880)}}
		},
	}

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-consume-cancel-test",
		generation: 1,
		state:      StateSpeaking,
		ttsClient:  mockTTS,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	cancel()

	sentenceCh := make(chan string, 5)
	sentenceCh <- "测试取消句子"
	close(sentenceCh)

	pcmDone := make(chan error, 1)

	go sess.consumeSentencesTTS(ctx, 1, sentenceCh, pacer, pcmDone)

	select {
	case <-pcmDone:
	case <-time.After(1 * time.Second):
		t.Fatal("consumeSentencesTTS did not exit promptly after context cancellation")
	}
}

func TestConsumeSentencesTTS_SingleConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var activeStreams atomic.Int32
	var maxObservedConcurrency atomic.Int32

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			curr := activeStreams.Add(1)
			for {
				max := maxObservedConcurrency.Load()
				if curr <= max || maxObservedConcurrency.CompareAndSwap(max, curr) {
					break
				}
			}
			return &mockTTSStream{
				pcmChunks: [][]byte{make([]byte, 2880), make([]byte, 2880)},
				onClose: func() {
					activeStreams.Add(-1)
				},
			}
		},
	}

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		sessionId:  "sess-single-concurrency-test",
		generation: 1,
		state:      StateSpeaking,
		ttsClient:  mockTTS,
	}

	pacer := NewDownlinkPacer(ctx, sess, 1, 100, nil)
	go pacer.Run()
	defer pacer.Stop()

	sentenceCh := make(chan string, 10)
	sentenceCh <- "第一句很长的话用来做语音合成测试。"
	sentenceCh <- "第二句很长的话用来做语音合成测试。"
	sentenceCh <- "第三句很长的话用来做语音合成测试。"
	close(sentenceCh)

	pcmDone := make(chan error, 1)
	go sess.consumeSentencesTTS(ctx, 1, sentenceCh, pacer, pcmDone)

	select {
	case err := <-pcmDone:
		if err != nil {
			t.Fatalf("unexpected error from consumeSentencesTTS: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumeSentencesTTS timed out")
	}

	if max := maxObservedConcurrency.Load(); max != 1 {
		t.Fatalf("expected max observed concurrency to be 1, got %d", max)
	}

	if count := len(mockTTS.streams); count != 3 {
		t.Fatalf("expected 3 streams created sequentially, got %d", count)
	}
}

func TestSession_PostTurnFinished_ExplicitContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		generation: 2,
		state:      StateSpeaking,
	}

	ok := sess.postTurnFinished(2, "问题", "回答")
	if !ok {
		t.Fatal("postTurnFinished returned false")
	}

	select {
	case ev := <-sess.events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.generation != 2 {
			t.Fatalf("expected generation 2, got %d", ev.generation)
		}
		if ev.userText != "问题" || ev.assistantText != "回答" {
			t.Fatalf("unexpected userText/assistantText: user=%s, assistant=%s", ev.userText, ev.assistantText)
		}
	default:
		t.Fatal("expected event in queue")
	}
}

func TestSession_PostTurnFinished_StaleGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     make(chan event, 10),
		generation: 3,
		state:      StateSpeaking,
	}

	ok := sess.postTurnFinished(2, "旧问题", "旧回答")
	if !ok {
		t.Fatal("postTurnFinished returned false")
	}

	select {
	case ev := <-sess.events:
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected event in queue")
	}

	if sess.State() != StateSpeaking {
		t.Fatalf("expected state StateSpeaking, got %v", sess.State())
	}
	sess.mu.RLock()
	historyLen := len(sess.history)
	sess.mu.RUnlock()
	if historyLen != 0 {
		t.Fatalf("expected 0 history messages, got %d", historyLen)
	}
}

func TestOrchestrateLLMAndTTS_MultiTurnTools_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	var toolExecuted bool
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		// 验证传入了可执行工具
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

		// 模拟流式生成分句
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "正在为您查询当前时间。", Iteration: 0})
			_ = callback(ctx, ai.LLMChunk{Text: "当前时间是上午十点。", Iteration: 1})
		}
		return "当前时间是上午十点。", nil
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionId:  "sess-multi-tool-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "现在几点了")

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		if ev.userText != "现在几点了" {
			t.Fatalf("expected userText '现在几点了', got %q", ev.userText)
		}
		if ev.assistantText != "当前时间是上午十点。" {
			t.Fatalf("expected assistantText '当前时间是上午十点。', got %q", ev.assistantText)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out")
	}

	if !toolExecuted {
		t.Fatal("expected server tool to be executed during generate")
	}
}

func TestOrchestrateLLMAndTTS_MaxTurnsExceeded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		return "", ai.ErrMaxTurnsExceeded
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionId:  "sess-max-turns-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "测试超出最大轮次")

	select {
	case ev := <-events:
		if ev.kind != eventKindError {
			t.Fatalf("expected eventKindError, got %v", ev.kind)
		}
		if !errors.Is(ev.err, ai.ErrMaxTurnsExceeded) {
			t.Fatalf("expected ErrMaxTurnsExceeded, got: %v", ev.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out")
	}
}

func TestOrchestrateLLMAndTTS_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{}

	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		cancel()
		return "", context.Canceled
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		events:     events,
		sessionId:  "sess-cancel-test",
		generation: 1,
		state:      StateSpeaking,
		llmClient:  mockLLM,
		ttsClient:  mockTTS,
	}

	done := make(chan struct{})
	go func() {
		sess.orchestrateLLMAndTTS(ctx, 1, "测试取消")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("orchestrateLLMAndTTS did not exit promptly upon context cancellation")
	}

	select {
	case ev := <-events:
		if ev.kind == eventKindTurnFinished {
			t.Fatalf("unexpected turn finished event after cancellation: %v", ev)
		}
	default:
	}
}

func TestOrchestrateLLMAndTTS_SentenceSubtitleSync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	ticker := newManualTicker()

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return &mockTTSStream{
				pcmChunks: [][]byte{make([]byte, 2880*3)},
			}
		},
	}

	s1 := "你好世界很高兴在这个美好的清晨与你相遇。"
	s2 := "今天天气真好微风徐徐让人心情格外舒畅愉快。"
	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: s1, Iteration: 0})
				_ = callback(ctx, ai.LLMChunk{Text: s2, Iteration: 0})
			}
			return s1 + s2, nil
		},
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:           ctx,
		cancel:        cancel,
		writer:        writer,
		logger:        slog.Default(),
		events:        events,
		sessionId:     "sess-subtitle-sync",
		generation:    1,
		state:         StateSpeaking,
		llmClient:     mockLLM,
		ttsClient:     mockTTS,
		tickerFactory: func(d time.Duration) Ticker { return ticker },
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "测试音字同步")

	// 驱动 ticker 推进所有音频包下发
	go func() {
		for i := 0; i < 20; i++ {
			ticker.Tick()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished, got %v", ev.kind)
		}
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(3 * time.Second):
		t.Fatal("orchestrateLLMAndTTS timed out")
	}

	msgs := conn.getMessages()
	if len(msgs) == 0 {
		t.Fatal("expected messages sent to websocket")
	}

	var sentenceStarts []string
	for _, m := range msgs {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "tts" && parsed["state"] == "sentence_start" {
					if txt, ok := parsed["text"].(string); ok {
						sentenceStarts = append(sentenceStarts, txt)
					}
				}
			}
		}
	}

	if len(sentenceStarts) != 2 {
		t.Fatalf("expected 2 sentence_start messages, got %d: %v", len(sentenceStarts), sentenceStarts)
	}

	if sentenceStarts[0] != s1 {
		t.Fatalf("expected first sentence %q, got %q", s1, sentenceStarts[0])
	}
	if sentenceStarts[1] != s2 {
		t.Fatalf("expected second sentence %q, got %q", s2, sentenceStarts[1])
	}

	// 验证消息类型的精确时间序：
	// tts.start -> sentence_start(你好世界) -> binary chunks -> sentence_start(今天天气真好) -> binary chunks -> tts.stop
	var order []string
	for _, m := range msgs {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "tts" {
					if parsed["state"] == "sentence_start" {
						order = append(order, "sentence:"+parsed["text"].(string))
					} else if parsed["state"] == "start" {
						order = append(order, "tts.start")
					} else if parsed["state"] == "stop" {
						order = append(order, "tts.stop")
					}
				}
			}
		} else if m.typ == websocket.MessageBinary {
			order = append(order, "audio")
		}
	}

	if len(order) < 6 {
		t.Fatalf("unexpected message order sequence length: %d: %v", len(order), order)
	}
	if order[0] != "tts.start" {
		t.Fatalf("expected first item 'tts.start', got %s", order[0])
	}
	if order[1] != "sentence:"+s1 {
		t.Fatalf("expected second item %q, got %s", "sentence:"+s1, order[1])
	}
	// 中间应有音频包，随后才是第二句字幕
	foundSecondSentence := false
	for i := 2; i < len(order); i++ {
		if order[i] == "sentence:"+s2 {
			foundSecondSentence = true
			if order[i-1] != "audio" {
				t.Fatalf("expected audio packet before second sentence start, got %s", order[i-1])
			}
			break
		}
	}
	if !foundSecondSentence {
		t.Fatal("second sentence not found in order sequence")
	}
	if order[len(order)-1] != "tts.stop" {
		t.Fatalf("expected last item 'tts.stop', got %s", order[len(order)-1])
	}
}

func TestTTSPipeline_EndToEndSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	ticker := newManualTicker()

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			s := &mockTTSStream{
				pcmChunks: [][]byte{make([]byte, 2880), make([]byte, 2880)},
			}
			return s
		},
	}

	// 模拟流式生成：
	// chunk 1: "你好。" (3字 < 5，不切句)
	// chunk 2: "我是小智助理。" (累积8字，切出 "你好。我是小智助理。")
	// chunk 3: "很高兴为您服务。" (切出 "很高兴为您服务。")
	// chunk 4: "再见" (2字，流结束 Flush 出 "再见")
	chunks := []string{
		"你好。",
		"我是小智助理。",
		"很高兴为您服务。",
		"再见",
	}

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				for _, c := range chunks {
					_ = callback(ctx, ai.LLMChunk{Text: c, Iteration: 0})
				}
			}
			return "你好。我是小智助理。很高兴为您服务。再见", nil
		},
	}

	events := make(chan event, 10)
	sess := &Session{
		ctx:           ctx,
		cancel:        cancel,
		writer:        writer,
		logger:        slog.Default(),
		events:        events,
		sessionId:     "sess-tts-pipeline",
		generation:    1,
		state:         StateSpeaking,
		llmClient:     mockLLM,
		ttsClient:     mockTTS,
		tickerFactory: func(d time.Duration) Ticker { return ticker },
	}

	go sess.orchestrateLLMAndTTS(ctx, 1, "启动测试")

	// 驱动 ticker 推进所有音频帧下发
	go func() {
		for i := 0; i < 50; i++ {
			ticker.Tick()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected TurnFinished, got %v", ev.kind)
		}
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline timed out")
	}

	msgs := conn.getMessages()
	if len(msgs) == 0 {
		t.Fatal("expected messages sent")
	}

	// 提取出所有 text 消息和 binary 消息的顺序
	var sentenceStarts []string
	var eventOrder []string
	for _, m := range msgs {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "tts" {
					st := parsed["state"].(string)
					if st == "sentence_start" {
						txt := parsed["text"].(string)
						sentenceStarts = append(sentenceStarts, txt)
						eventOrder = append(eventOrder, "sentence:"+txt)
					} else if st == "start" {
						eventOrder = append(eventOrder, "tts.start")
					} else if st == "stop" {
						eventOrder = append(eventOrder, "tts.stop")
					}
				}
			}
		} else if m.typ == websocket.MessageBinary {
			eventOrder = append(eventOrder, "binary_audio")
		}
	}

	// 验证切出的 3 个句子：
	// 1. "你好。我是小智助理。" (至少5字切出)
	// 2. "很高兴为您服务。" (至少5字切出)
	// 3. "再见" (响应结束 Flush 切出)
	expectedSentences := []string{
		"你好。我是小智助理。",
		"很高兴为您服务。",
		"再见",
	}

	if len(sentenceStarts) != len(expectedSentences) {
		t.Fatalf("expected %d sentence_start messages, got %d: %v", len(expectedSentences), len(sentenceStarts), sentenceStarts)
	}

	for i, exp := range expectedSentences {
		if sentenceStarts[i] != exp {
			t.Fatalf("sentence %d expected %q, got %q", i, exp, sentenceStarts[i])
		}
	}

	// 验证时间序：
	// tts.start -> sentence 1 -> audio -> sentence 2 -> audio -> sentence 3 -> audio -> tts.stop
	if len(eventOrder) == 0 || eventOrder[0] != "tts.start" {
		t.Fatalf("expected first event to be tts.start, got: %v", eventOrder)
	}
	if eventOrder[len(eventOrder)-1] != "tts.stop" {
		t.Fatalf("expected last event to be tts.stop, got: %v", eventOrder[len(eventOrder)-1])
	}
}

func TestTTSPipeline_AbortClearsQueuesAndResets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	ticker := newManualTicker()

	var streamsCreated int
	var ttsClosed atomic.Bool

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			streamsCreated++
			return &mockTTSStream{
				pcmChunks: [][]byte{make([]byte, 2880), make([]byte, 2880), make([]byte, 2880)},
				onClose: func() {
					ttsClosed.Store(true)
				},
			}
		},
	}

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "第一句非常长的回复语句用于测试中断。", Iteration: 0})
				_ = callback(ctx, ai.LLMChunk{Text: "第二句非常长的回复语句用于测试中断。", Iteration: 0})
			}
			return "第一句非常长的回复语句用于测试中断。第二句非常长的回复语句用于测试中断。", nil
		},
	}

	events := make(chan event, 50)
	turnCtx, turnCancel := context.WithCancel(ctx)
	sess := &Session{
		ctx:           ctx,
		cancel:        cancel,
		writer:        writer,
		logger:        slog.Default(),
		events:        events,
		sessionId:     "sess-tts-abort",
		generation:    1,
		state:         StateSpeaking,
		llmClient:     mockLLM,
		ttsClient:     mockTTS,
		tickerFactory: func(d time.Duration) Ticker { return ticker },
		turnCtx:       turnCtx,
		turnCancel:    turnCancel,
	}

	go sess.orchestrateLLMAndTTS(turnCtx, 1, "测试中断")

	// 发送一个 tick 让首帧音频发出并进入 speaking
	time.Sleep(20 * time.Millisecond)
	ticker.Tick()
	time.Sleep(20 * time.Millisecond)

	// 触发 abort 中断
	sess.handleAbortEvent("user interruption")

	// 验证状态重置为 StateReady
	if sess.State() != StateReady {
		t.Fatalf("expected state StateReady after abort, got: %v", sess.State())
	}

	// 验证代次递增
	if sess.Generation() != 2 {
		t.Fatalf("expected generation 2, got %d", sess.Generation())
	}

	// 验证 Pacer 已停止并清空
	sess.mu.RLock()
	pacerNil := sess.pacer == nil
	sess.mu.RUnlock()
	if !pacerNil {
		t.Fatalf("expected pacer to be nil after abort")
	}

	// 验证下发了 tts.stop
	var hasStop bool
	for i := 0; i < 20; i++ {
		msgs := conn.getMessages()
		for _, m := range msgs {
			if m.typ == websocket.MessageText {
				var parsed map[string]any
				if err := json.Unmarshal(m.payload, &parsed); err == nil {
					if parsed["type"] == "tts" && parsed["state"] == "stop" {
						hasStop = true
						break
					}
				}
			}
		}
		if hasStop {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !hasStop {
		t.Fatal("expected tts.stop message sent on abort from speaking state")
	}

	// 验证在新代次可以重新开始
	newTurnCtx, newTurnCancel := context.WithCancel(ctx)
	sess.turnCtx = newTurnCtx
	sess.turnCancel = newTurnCancel
	sess.state = StateSpeaking

	go sess.orchestrateLLMAndTTS(newTurnCtx, 2, "重新开始")

	go func() {
		for i := 0; i < 20; i++ {
			ticker.Tick()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	select {
	case ev := <-events:
		if ev.kind != eventKindTurnFinished {
			t.Fatalf("expected eventKindTurnFinished on new turn, got %v", ev.kind)
		}
		if ev.generation != 2 {
			t.Fatalf("expected turn finished generation 2, got %d", ev.generation)
		}
		sess.handleTurnFinishedEvent(ev)
	case <-time.After(2 * time.Second):
		t.Fatal("new turn timed out")
	}

	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after second turn, got %v", sess.State())
	}
}
