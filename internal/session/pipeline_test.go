package session

import (
	"context"
	"encoding/json"
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

	pcmFrame := make([]byte, 2880)
	stream := &mockTTSStream{
		pcmChunks: [][]byte{pcmFrame, pcmFrame},
	}
	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSStream {
			return stream
		},
	}

	pipeline := NewTurnPipeline(PipelineOptions{
		TTSClient: mockTTS,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-consume-test",
		Sender:    writer,
		QueueCap:  100,
	})
	go pacer.Run()
	defer pacer.Stop()

	sentenceCh := make(chan string, 5)
	sentenceCh <- "你好测试"
	close(sentenceCh)

	pcmDone := make(chan error, 1)
	turn := &activeTurn{
		turnId:  1,
		ctx:     ctx,
		cancel:  cancel,
		effects: &TurnEffects{},
	}

	go pipeline.consumeSentencesTTS(turn, "sess-consume-test", sentenceCh, pacer, pcmDone)

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

	pipeline := NewTurnPipeline(PipelineOptions{
		TTSClient: mockTTS,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})

	pacer := NewDownlinkPacer(ctx, DownlinkPacerOptions{
		SessionId: "sess-single-concurrency-test",
		QueueCap:  100,
	})
	go pacer.Run()
	defer pacer.Stop()

	sentenceCh := make(chan string, 10)
	sentenceCh <- "第一句很长的话用来做语音合成测试。"
	sentenceCh <- "第二句很长的话用来做语音合成测试。"
	sentenceCh <- "第三句很长的话用来做语音合成测试。"
	close(sentenceCh)

	pcmDone := make(chan error, 1)
	turn := &activeTurn{
		turnId:  1,
		ctx:     ctx,
		cancel:  cancel,
		effects: &TurnEffects{},
	}

	go pipeline.consumeSentencesTTS(turn, "sess-single-concurrency-test", sentenceCh, pacer, pcmDone)

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

func TestTurnPipeline_MultiTurnTools_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTTS := &mockTTSClient{}
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

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	events := make(chan turnEvent, 10)
	toolProvider := NewToolProvider(nil, slog.Default())
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:    mockLLM,
		TTSClient:    mockTTS,
		Config:       NormalizeConfig(SessionConfig{}),
		ToolProvider: toolProvider,
		Logger:       slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})

	_ = pipeline.StartListening(ctx, 1, "sess-multi-tool-test", ListenModeAuto)
	_ = pipeline.StartResponse(1, "sess-multi-tool-test", "现在几点了", writer)

	select {
	case ev := <-events:
		if ev.typ == turnEventPlaybackStarted {
			// 等待 turnEventTurnCompleted
			ev2 := <-events
			if ev2.typ != turnEventTurnCompleted {
				t.Fatalf("expected turnEventTurnCompleted, got %v", ev2.typ)
			}
		} else if ev.typ != turnEventTurnCompleted {
			t.Fatalf("expected completed or playback started, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartResponse timed out")
	}

	if !toolExecuted {
		t.Fatal("expected server tool to be executed during generate")
	}
}

func TestTurnPipeline_SentenceSubtitleSync(t *testing.T) {
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

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient: mockLLM,
		TTSClient: mockTTS,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
		TickerFactory: func(d time.Duration) Ticker {
			return ticker
		},
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})

	_ = pipeline.StartListening(ctx, 1, "sess-subtitle-sync", ListenModeAuto)
	_ = pipeline.StartResponse(1, "sess-subtitle-sync", "测试音字同步", writer)

	go func() {
		for i := 0; i < 20; i++ {
			ticker.Tick()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var completed bool
	for !completed {
		select {
		case ev := <-events:
			if ev.typ == turnEventTurnCompleted {
				completed = true
			}
		case <-time.After(3 * time.Second):
			t.Fatal("pipeline response timed out")
		}
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
}
