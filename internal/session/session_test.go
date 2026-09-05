package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
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
		for _, m := range msgs {
			if bytes.Contains(m.payload, []byte(`"state":"stop"`)) {
				return true
			}
		}
		return false
	})

	if !ok {
		msgs := conn.getMessages()
		t.Fatalf("expected tts.stop received, message count: %d", len(msgs))
	}

	// 等待会话完成 TurnCompleted 并提交历史
	ok = waitForCondition(2*time.Second, func() bool {
		return sess.runtime.history.Len() == 2
	})
	if !ok {
		t.Fatalf("expected 2 history messages (user + assistant), got %d", sess.runtime.history.Len())
	}

	sess.Close()
	<-sess.Done()
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

// TestSession_MCPDiscovery_AsyncNoDeadlock_DuringTurn 验证设备在握手后立即进入问答轮次时，
// 主 Actor 协程不会死锁，MCP 发现与工具快照能够异步正常就绪并注入大模型。
func TestSession_MCPDiscovery_AsyncNoDeadlock_DuringTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		sess        *Session
		discovered  sync.Map
		llmCalled   = make(chan struct{})
		llmReqTools []ai.Tool
	)

	conn := &interactiveWSConn{
		onWrite: func(p []byte) {
			var msg map[string]json.RawMessage
			if err := json.Unmarshal(p, &msg); err != nil {
				return
			}
			var msgType string
			if rawType, ok := msg["type"]; ok {
				_ = json.Unmarshal(rawType, &msgType)
			}
			if msgType != MessageTypeMCP {
				return
			}

			var payload map[string]any
			if rawPayload, ok := msg["payload"]; ok {
				_ = json.Unmarshal(rawPayload, &payload)
			}

			method, _ := payload["method"].(string)
			reqIdFloat, _ := payload["id"].(float64)
			reqId := int64(reqIdFloat)
			var sessionId string
			if rawSessId, ok := msg["session_id"]; ok {
				_ = json.Unmarshal(rawSessId, &sessionId)
			}

			switch method {
			case "initialize":
				resp := map[string]any{
					"session_id": sessionId,
					"type":       "mcp",
					"payload": map[string]any{
						"jsonrpc": "2.0",
						"id":      reqId,
						"result": map[string]any{
							"protocolVersion": "2024-11-05",
							"capabilities":    map[string]any{},
						},
					},
				}
				respBytes, _ := json.Marshal(resp)
				go func() {
					time.Sleep(10 * time.Millisecond)
					if sess != nil {
						sess.postEvent(sessionEvent{
							kind:     eventKindClientFrame,
							isBinary: false,
							data:     respBytes,
						})
					}
				}()

			case "tools/list":
				resp := map[string]any{
					"session_id": sessionId,
					"type":       "mcp",
					"payload": map[string]any{
						"jsonrpc": "2.0",
						"id":      reqId,
						"result": map[string]any{
							"tools": []map[string]any{
								{
									"name":        "self.audio_speaker.set_volume",
									"description": "Set speaker volume",
									"inputSchema": map[string]any{
										"volume": map[string]any{
											"type":    "integer",
											"minimum": 0,
											"maximum": 100,
										},
									},
								},
							},
						},
					},
				}
				respBytes, _ := json.Marshal(resp)
				go func() {
					time.Sleep(10 * time.Millisecond)
					if sess != nil {
						sess.postEvent(sessionEvent{
							kind:     eventKindClientFrame,
							isBinary: false,
							data:     respBytes,
						})
					}
				}()
			}
		},
	}

	asr := &mockASRClient{text: "把声音调大"}
	llm := &mockLLMClient{
		chunks: []ai.LLMChunk{
			{Text: "好的，已为您调大音量。"},
		},
		onRunTool: func(runCtx context.Context, req ai.LLMRequest) {
			llmReqTools = req.Tools
			for _, tool := range req.Tools {
				discovered.Store(tool.Name, true)
			}
			close(llmCalled)
		},
	}
	tts := &mockTTSClient{}

	sess = NewSession(ctx, Options{
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

	start := time.Now()

	// 1. 发送带 MCP 支持的 hello 握手
	helloMsg := ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		Features: &ClientFeatures{
			MCP: true,
		},
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

	if !waitForCondition(time.Second, func() bool { return sess.SessionId() != "" }) {
		t.Fatal("session handshake timed out")
	}

	// 2. 握手后立即发送 listen.start (manual)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"listen","state":"start","mode":"manual"}`),
	})

	// 3. 立即发送 listen.stop (manual)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"listen","state":"stop"}`),
	})

	// 4. 等待大模型被调用
	select {
	case <-llmCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for LLM generation (possible deadlock or long delay)")
	}

	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("turn took too long (%v), expected < 3s", elapsed)
	}

	// 5. 校验大模型收到的工具列表中是否成功包含了设备 MCP 工具
	if _, ok := discovered.Load("self.audio_speaker.set_volume"); !ok {
		t.Fatalf("expected self.audio_speaker.set_volume in LLM tools, got: %+v", llmReqTools)
	}

	sess.Close()
	<-sess.Done()
}

type interactiveWSConn struct {
	onWrite func(p []byte)
}

func (c *interactiveWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	if c.onWrite != nil {
		c.onWrite(p)
	}
	return nil
}

