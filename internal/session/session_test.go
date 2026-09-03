package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hraban/opus"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

func createValid16kOpusPacket() []byte {
	enc, err := opus.NewEncoder(16000, 1, opus.AppVoIP)
	if err != nil {
		return nil
	}
	pcm := make([]int16, 960)
	out := make([]byte, 1024)
	n, err := enc.Encode(pcm, out)
	if err != nil {
		return nil
	}
	return out[:n]
}

type mockASRClient struct {
	text string
	err  error
}

func (m *mockASRClient) Recognize(ctx context.Context, req ai.ASRRequest, pcm <-chan []byte) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.text, nil
}

type mockLLMClient struct {
	chunks    []ai.LLMChunk
	finalText string
	err       error
	onRunTool func(ctx context.Context, req ai.LLMRequest)
}

func (m *mockLLMClient) Generate(ctx context.Context, req ai.LLMRequest, chunks chan<- ai.LLMChunk) (ai.LLMResult, error) {
	if m.onRunTool != nil {
		m.onRunTool(ctx, req)
	}
	if m.err != nil {
		return ai.LLMResult{}, m.err
	}
	if chunks != nil {
		for _, c := range m.chunks {
			select {
			case chunks <- c:
			case <-ctx.Done():
				return ai.LLMResult{}, ctx.Err()
			}
		}
	}
	return ai.LLMResult{FinalText: m.finalText}, nil
}

type mockTTSClient struct {
	mu           sync.Mutex
	sessionCount int
}

func (m *mockTTSClient) CreateSession(ctx context.Context) (ai.TTSSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionCount++
	return &mockTTSSessionImpl{}, nil
}

type mockTTSSessionImpl struct{}

func (s *mockTTSSessionImpl) Synthesize(ctx context.Context, text string, pcm chan<- ai.PCMChunk) error {
	chunk := ai.PCMChunk{
		Data:          make([]byte, 2880),
		SentenceStart: text,
	}
	select {
	case pcm <- chunk:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *mockTTSSessionImpl) Close() error {
	return nil
}

func waitForCondition(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestSession_Handshake_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	sess := NewSession(ctx, Options{
		Conn:         nil,
		Outbound:     NewOutboundActor(ctx, conn, 10, 5*time.Second, nil, nil),
		SerialNumber: "SN-12345678",
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	raw, _ := json.Marshal(helloMsg)

	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	ok := waitForCondition(2*time.Second, func() bool {
		return sess.SessionId() != "" && len(conn.getMessages()) == 1
	})
	if !ok {
		t.Fatal("handshake timed out")
	}

	msgs := conn.getMessages()
	var resp ServerHelloMessage
	if err := json.Unmarshal(msgs[0].payload, &resp); err != nil {
		t.Fatalf("unmarshal server hello failed: %v", err)
	}
	if resp.Type != "hello" || resp.Transport != "websocket" {
		t.Fatalf("unexpected server hello: %+v", resp)
	}

	sess.Close()
	<-sess.Done()
}

func TestSession_DuplicateHello_Rejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	sess := NewSession(ctx, Options{
		Conn:         nil,
		Outbound:     NewOutboundActor(ctx, conn, 10, 5*time.Second, nil, nil),
		SerialNumber: "SN-12345678",
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	raw, _ := json.Marshal(helloMsg)

	// 第一次 hello
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	waitForCondition(time.Second, func() bool {
		return sess.SessionId() != ""
	})

	// 第二次 hello
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	select {
	case <-sess.Done():
		// 正常关闭
	case <-time.After(2 * time.Second):
		t.Fatal("expected session to close after duplicate hello")
	}
}

func TestSession_AutoTurn_FullCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asr := &mockASRClient{text: "你好小智"}
	llm := &mockLLMClient{
		chunks: []ai.LLMChunk{
			{Text: "你好！有什么我可以帮你的吗？"},
		},
	}
	tts := &mockTTSClient{}

	sess := NewSession(ctx, Options{
		Outbound:     NewOutboundActor(ctx, conn, 20, 5*time.Second, nil, nil),
		SerialNumber: "SN-12345678",
		ASRClient:    asr,
		LLMClient:    llm,
		TTSClient:    tts,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	// 1. 握手
	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	raw, _ := json.Marshal(helloMsg)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	waitForCondition(time.Second, func() bool {
		return sess.SessionId() != ""
	})

	// 2. 发送 listen.start (auto 模式)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 3. 发送一帧合法上行音频
	validOpus := createValid16kOpusPacket()
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: true,
		data:     validOpus,
	})

	// 等待完整流程跑完（STT, TTS start, sentence_start, audio, tts.stop）
	ok := waitForCondition(3*time.Second, func() bool {
		msgs := conn.getMessages()
		// hello (1) + STT (1) + tts.start (1) + sentence_start (1) + opus (1) + tts.stop (1) = 6
		return len(msgs) >= 6
	})

	if !ok {
		msgs := conn.getMessages()
		t.Fatalf("expected at least 6 messages, got %d", len(msgs))
	}

	sess.Close()
	<-sess.Done()

	// 验证历史记录已提交
	if sess.runtime.history.Len() != 2 {
		t.Fatalf("expected 2 history messages (user + assistant), got %d", sess.runtime.history.Len())
	}
}

func TestSession_CloseSession_Tool_ClosesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asr := &mockASRClient{text: "退出会话"}
	llm := &mockLLMClient{
		chunks: []ai.LLMChunk{
			{Text: "好的，再见！"},
		},
		onRunTool: func(ctx context.Context, req ai.LLMRequest) {
			for _, tool := range req.Tools {
				if tool.Name == agentkit.ToolCloseSession {
					_, _ = tool.Run(ctx, map[string]any{"reason": "用户要求退出"})
				}
			}
		},
	}
	tts := &mockTTSClient{}

	sess := NewSession(ctx, Options{
		Outbound:     NewOutboundActor(ctx, conn, 20, 5*time.Second, nil, nil),
		SerialNumber: "SN-12345678",
		ASRClient:    asr,
		LLMClient:    llm,
		TTSClient:    tts,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	// 握手
	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	raw, _ := json.Marshal(helloMsg)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	waitForCondition(time.Second, func() bool {
		return sess.SessionId() != ""
	})

	// listen.start
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 会话应在 Turn 播报完成交付后优雅关闭
	select {
	case <-sess.Done():
		// 成功关闭
	case <-time.After(3 * time.Second):
		t.Fatal("expected session to close after close_session tool execution")
	}
}

func TestSession_Abort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asr := &mockASRClient{text: "测试打断"}
	llm := &mockLLMClient{
		chunks: []ai.LLMChunk{{Text: "正在回复..."}},
	}
	tts := &mockTTSClient{}

	sess := NewSession(ctx, Options{
		Outbound:     NewOutboundActor(ctx, conn, 20, 5*time.Second, nil, nil),
		SerialNumber: "SN-12345678",
		ASRClient:    asr,
		LLMClient:    llm,
		TTSClient:    tts,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	// 握手
	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	raw, _ := json.Marshal(helloMsg)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	waitForCondition(time.Second, func() bool {
		return sess.SessionId() != ""
	})

	// listen.start
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 发送 abort
	abortRaw := []byte(`{"type":"abort","reason":"user interrupt"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     abortRaw,
	})

	time.Sleep(100 * time.Millisecond)

	sess.Close()
	<-sess.Done()

	if sess.runtime.history.Len() != 0 {
		t.Fatalf("expected 0 history items after abort, got %d", sess.runtime.history.Len())
	}
}

func TestSession_Manual_NoSpeech_ResetsToReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asr := &mockASRClient{text: ""} // manual 模式无有效识别文本
	llm := &mockLLMClient{}
	tts := &mockTTSClient{}

	sess := NewSession(ctx, Options{
		Outbound:     NewOutboundActor(ctx, conn, 20, 5*time.Second, nil, nil),
		SerialNumber: "SN-12345678",
		ASRClient:    asr,
		LLMClient:    llm,
		TTSClient:    tts,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	// 握手
	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	raw, _ := json.Marshal(helloMsg)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	waitForCondition(time.Second, func() bool {
		return sess.SessionId() != ""
	})

	// start (manual)
	listenStartRaw := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenStartRaw,
	})

	// stop (manual)
	listenStopRaw := []byte(`{"type":"listen","state":"stop"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenStopRaw,
	})

	time.Sleep(100 * time.Millisecond)

	sess.Close()
	<-sess.Done()

	if sess.runtime.history.Len() != 0 {
		t.Fatalf("expected 0 history items, got %d", sess.runtime.history.Len())
	}
}

func TestSession_TurnFailed_ClosesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asr := &mockASRClient{err: errors.New("asr service unavailable")}
	llm := &mockLLMClient{}
	tts := &mockTTSClient{}

	sess := NewSession(ctx, Options{
		Outbound:     NewOutboundActor(ctx, conn, 20, 5*time.Second, nil, nil),
		SerialNumber: "SN-12345678",
		ASRClient:    asr,
		LLMClient:    llm,
		TTSClient:    tts,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	// 握手
	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	raw, _ := json.Marshal(helloMsg)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	waitForCondition(time.Second, func() bool {
		return sess.SessionId() != ""
	})

	// start
	listenStartRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenStartRaw,
	})

	select {
	case <-sess.Done():
		// 正常关闭
	case <-time.After(2 * time.Second):
		t.Fatal("expected session to close on turn failure")
	}
}
