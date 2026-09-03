package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockASRStream struct {
	mu     sync.Mutex
	text   string
	err    error
	closed bool
}

func (m *mockASRStream) Result(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	return m.text, nil
}

func (m *mockASRStream) WritePCM(ctx context.Context, pcm []byte) error {
	return nil
}

func (m *mockASRStream) Finish(ctx context.Context) error {
	return nil
}

func (m *mockASRStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

type mockASRClient struct {
	mu     sync.Mutex
	stream *mockASRStream
	err    error
}

func (m *mockASRClient) CreateStream(ctx context.Context) (ai.ASRStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if m.stream != nil {
		return m.stream, nil
	}
	return &mockASRStream{text: "你好测试"}, nil
}

func TestSession_Handshake_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	sess := NewSession(ctx, Options{
		Writer:       writer,
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

	time.Sleep(50 * time.Millisecond)

	if sess.State() != StateReady {
		t.Fatalf("expected state StateReady, got %v", sess.State())
	}
	if sess.SessionId() == "" {
		t.Fatal("expected non-empty session_id")
	}

	msgs := conn.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(msgs))
	}

	var resp ServerHelloMessage
	if err := json.Unmarshal(msgs[0].payload, &resp); err != nil {
		t.Fatalf("unmarshal server hello failed: %v", err)
	}
	if resp.Type != "hello" || resp.Transport != "websocket" {
		t.Fatalf("unexpected server hello: %+v", resp)
	}

	sess.Close()
}

func TestSession_DuplicateHello_Rejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	sess := NewSession(ctx, Options{
		Writer:       writer,
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

	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady, got %v", sess.State())
	}

	// 第二次 hello
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})

	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed after duplicate hello, got %v", sess.State())
	}
}

func TestSession_Abort_ResetsToReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-12345678",
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
	time.Sleep(50 * time.Millisecond)

	// 发送 listen.start (manual 模式直接进入收音)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 发送 abort
	abortRaw := []byte(`{"type":"abort","reason":"user interrupt"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     abortRaw,
	})

	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after abort, got %v", sess.State())
	}

	sess.Close()
}

func TestSession_CloseTool_ClosesSessionAfterTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	mockTTS := &mockTTSClient{}
	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			for _, tool := range req.Tools {
				if tool.Name == agentkit.ToolCloseSession {
					_, _ = tool.Run(ctx, map[string]any{"reason": "再见"})
				}
			}
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "再见！", Iteration: 0})
			}
			return "再见！", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-12345678",
		LLMClient:    mockLLM,
		TTSClient:    mockTTS,
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
	time.Sleep(50 * time.Millisecond)

	// 发送 listen.start (manual 模式直接进入收音)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})
	time.Sleep(50 * time.Millisecond)

	// 触发 ASR 识别结果
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   "退出会话",
		},
	})

	time.Sleep(100 * time.Millisecond)

	// 等待直到会话因 close_session 工具指令关闭
	for i := 0; i < 20; i++ {
		if sess.State() == StateClosed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if sess.State() != StateClosed {
		t.Fatalf("expected session to be closed after close_session tool turn, got %v", sess.State())
	}
}

func TestSession_History_MaintainedCorrectly(t *testing.T) {
	hist := NewConversationHistory(2) // 最多保留 2 轮（4 条消息）

	hist.AppendTurn("你好", "你好！")
	hist.AppendTurn("今天天气", "天气晴朗。")
	if hist.Len() != 4 {
		t.Fatalf("expected 4 messages, got %d", hist.Len())
	}

	// 追加第 3 轮，触发淘汰第 1 轮
	hist.AppendTurn("讲个笑话", "这是一个笑话。")
	if hist.Len() != 4 {
		t.Fatalf("expected 4 messages after eviction, got %d", hist.Len())
	}

	msgs := hist.Messages()
	if msgs[0].Content != "今天天气" || msgs[1].Content != "天气晴朗。" {
		t.Fatalf("unexpected oldest turn remaining: %+v", msgs)
	}
	if msgs[2].Content != "讲个笑话" || msgs[3].Content != "这是一个笑话。" {
		t.Fatalf("unexpected latest turn: %+v", msgs)
	}

	// 验证 BuildLLMMessages
	fullMsgs := hist.BuildLLMMessages("系统提示词", "新问题")
	if len(fullMsgs) != 6 { // 1 system + 4 history + 1 user
		t.Fatalf("expected 6 messages, got %d", len(fullMsgs))
	}
	if fullMsgs[0].Role != ai.RoleSystem || fullMsgs[0].Content != "系统提示词" {
		t.Fatalf("unexpected system message: %+v", fullMsgs[0])
	}
	if fullMsgs[5].Role != ai.RoleUser || fullMsgs[5].Content != "新问题" {
		t.Fatalf("unexpected user message: %+v", fullMsgs[5])
	}
}

func TestSession_AutoMode_PlaysPromptBeforeListening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 100, nil)
	defer writer.Close()

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-12345678",
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
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after handshake, got %v", sess.State())
	}

	// 首次发送 listen.start (auto 模式)，固定播放提示音进入 Speaking
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateSpeaking {
		t.Fatalf("expected StateSpeaking when playing prompt in auto mode, got %v", sess.State())
	}

	// 模拟提示音播放完成事件
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventTurnCompleted,
		},
	})
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after prompt completed, got %v", sess.State())
	}

	// 固件再次发送 listen.start (auto 模式)，此时应直接进入 Listening
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening on second auto listen.start, got %v", sess.State())
	}

	sess.Close()
}
