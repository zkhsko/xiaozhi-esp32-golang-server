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

type mockTTSPacketStream struct {
	mu      sync.Mutex
	packets [][]byte
	idx     int
	err     error
	closed  bool
	onClose func()
}

func (m *mockTTSPacketStream) NextPacket(ctx context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if m.idx >= len(m.packets) {
		return nil, io.EOF
	}
	pkt := m.packets[m.idx]
	m.idx++
	return pkt, nil
}

func (m *mockTTSPacketStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.onClose != nil {
		m.onClose()
	}
	return nil
}

type mockTTSClient struct {
	mu         sync.Mutex
	err        error
	sessionErr error
	sessions   []*mockTTSSession
	streams    []*mockTTSPacketStream
	newStream  func() *mockTTSPacketStream
}

type mockTTSSession struct {
	client *mockTTSClient
	closed bool
}

func (m *mockTTSClient) CreateSession(ctx context.Context) (ai.TTSSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionErr != nil {
		return nil, m.sessionErr
	}
	sess := &mockTTSSession{client: m}
	m.sessions = append(m.sessions, sess)
	return sess, nil
}

func (s *mockTTSSession) Synthesize(ctx context.Context, text string) (ai.TTSPacketStream, error) {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if s.client.err != nil {
		return nil, s.client.err
	}
	var stream *mockTTSPacketStream
	if s.client.newStream != nil {
		stream = s.client.newStream()
	} else {
		stream = &mockTTSPacketStream{
			packets: [][]byte{[]byte{0x01, 0x02, 0x03}},
		}
	}
	s.client.streams = append(s.client.streams, stream)
	return stream, nil
}

func (s *mockTTSSession) Close() error {
	s.closed = true
	return nil
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

	pkt := []byte{0x01, 0x02, 0x03}
	stream := &mockTTSPacketStream{
		packets: [][]byte{pkt, pkt},
	}
	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSPacketStream {
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
	ttsSess1, _ := mockTTS.CreateSession(ctx)
	turn := &activeTurn{
		turnId:     1,
		ctx:        ctx,
		cancel:     cancel,
		effects:    &TurnEffects{},
		ttsSession: ttsSess1,
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
		newStream: func() *mockTTSPacketStream {
			curr := activeStreams.Add(1)
			for {
				max := maxObservedConcurrency.Load()
				if curr <= max || maxObservedConcurrency.CompareAndSwap(max, curr) {
					break
				}
			}
			return &mockTTSPacketStream{
				packets: [][]byte{[]byte{0x01, 0x02}, []byte{0x03, 0x04}},
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
	ttsSess2, _ := mockTTS.CreateSession(ctx)
	turn := &activeTurn{
		turnId:     1,
		ctx:        ctx,
		cancel:     cancel,
		effects:    &TurnEffects{},
		ttsSession: ttsSess2,
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
	toolProvider := NewToolProvider(nil, nil, slog.Default())
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
		newStream: func() *mockTTSPacketStream {
			return &mockTTSPacketStream{
				packets: [][]byte{[]byte{0x01, 0x02, 0x03}, []byte{0x04, 0x05, 0x06}},
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

	// 验证单轮独占 1 个 TTSSession，单句复用且轮次结束自动释放
	if len(mockTTS.sessions) != 1 {
		t.Fatalf("expected exactly 1 TTSSession created per turn, got %d", len(mockTTS.sessions))
	}
	if len(mockTTS.streams) != 2 {
		t.Fatalf("expected 2 TTSPacketStreams synthesized on the single session, got %d", len(mockTTS.streams))
	}
	if !mockTTS.sessions[0].closed {
		t.Fatal("expected TTSSession to be closed on turn completion")
	}
}

func TestTurnPipeline_TTSLazyConnection_OnlyWhenSentenceGenerated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	mockTTS := &mockTTSClient{
		newStream: func() *mockTTSPacketStream {
			return &mockTTSPacketStream{
				packets: [][]byte{[]byte{0x01, 0x02, 0x03}},
			}
		},
	}

	llmStarted := make(chan struct{})
	allowLLMToProceed := make(chan struct{})

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			close(llmStarted)
			<-allowLLMToProceed

			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "这是第一句完整的话用于触发语音合成。", Iteration: 0})
			}
			return "这是第一句完整的话用于触发语音合成。", nil
		},
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient: mockLLM,
		TTSClient: mockTTS,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})

	_ = pipeline.StartListening(ctx, 1, "sess-lazy-test", ListenModeAuto)
	_ = pipeline.StartResponse(1, "sess-lazy-test", "测试懒加载", writer)

	select {
	case <-llmStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for LLM to start")
	}

	mockTTS.mu.Lock()
	sessCount := len(mockTTS.sessions)
	mockTTS.mu.Unlock()
	if sessCount != 0 {
		t.Fatalf("expected 0 TTS sessions before first sentence is produced, got %d", sessCount)
	}

	close(allowLLMToProceed)

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

	mockTTS.mu.Lock()
	finalSessCount := len(mockTTS.sessions)
	mockTTS.mu.Unlock()
	if finalSessCount != 1 {
		t.Fatalf("expected exactly 1 TTS session after sentence generated, got %d", finalSessCount)
	}
}

func TestTurnPipeline_TTSLazyConnection_NoConnectionOnEmptyResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			return "", nil
		},
	}

	events := make(chan turnEvent, 10)
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient: mockLLM,
		TTSClient: mockTTS,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})

	_ = pipeline.StartListening(ctx, 1, "sess-lazy-empty-test", ListenModeAuto)
	_ = pipeline.StartResponse(1, "sess-lazy-empty-test", "测试空回复", writer)

	select {
	case ev := <-events:
		if ev.typ != turnEventTurnCompleted {
			t.Fatalf("expected turnEventTurnCompleted, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline response timed out")
	}

	mockTTS.mu.Lock()
	sessCount := len(mockTTS.sessions)
	mockTTS.mu.Unlock()
	if sessCount != 0 {
		t.Fatalf("expected 0 TTS sessions for empty LLM response, got %d", sessCount)
	}
}
