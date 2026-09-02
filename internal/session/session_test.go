package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockWSMsg struct {
	typ     websocket.MessageType
	payload []byte
}

type mockWSConn struct {
	mu          sync.Mutex
	messages    []mockWSMsg
	writeErr    error
	beforeWrite func(typ websocket.MessageType, p []byte)
}

func (m *mockWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	if m.beforeWrite != nil {
		m.beforeWrite(typ, p)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writeErr != nil {
		return m.writeErr
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	m.messages = append(m.messages, mockWSMsg{typ: typ, payload: cp})
	return nil
}

func (m *mockWSConn) getMessages() []mockWSMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]mockWSMsg, len(m.messages))
	copy(res, m.messages)
	return res
}

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

	// 发送 listen.start
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
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

func TestSession_Abort_InvalidatesOldVoiceFramesAndPreservesControlFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var writeHoldStarted sync.Once
	startedChan := make(chan struct{})
	proceedChan := make(chan struct{})

	// 在收到 hello 握手回执后，下一条消息进入 beforeWrite 时挂起，以便排入测试混排队列
	writeCount := 0
	conn := &mockWSConn{
		beforeWrite: func(typ websocket.MessageType, p []byte) {
			writeCount++
			if writeCount == 2 {
				writeHoldStarted.Do(func() {
					close(startedChan)
					<-proceedChan
				})
			}
		},
	}

	writer := NewWriter(ctx, conn, 50, nil)

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-12345678",
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	// 1. 完成握手（发送 hello，生成一条 hello 响应并写入，即第 1 次 write）
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

	// 2. 发送 listen.start -> 进入 Listening 状态，turnId 递增为 1
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 3. 向 writer 队列排入一条控制帧（触发 writeCount == 2 并暂停 writeLoop）
	if err := writer.SendTextMessage(ctx, `{"jsonrpc":"2.0","method":"ui/call","id":1}`); err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}

	<-startedChan

	// 4. 在 writeLoop 暂停期间，排入属于 turnId=1 的旧语音帧、MCP 控制帧、旧语音二进制帧
	if err := writer.SendVoiceText(ctx, 1, []byte(`{"type":"tts","state":"sentence_start","text":"旧轮次待跳过语音"}`)); err != nil {
		t.Fatalf("SendVoiceText turn 1 failed: %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 1, []byte{0xDE, 0xAD, 0xBE, 0xEF}); err != nil {
		t.Fatalf("SendVoiceBinary turn 1 failed: %v", err)
	}
	if err := writer.SendTextMessage(ctx, `{"jsonrpc":"2.0","method":"ui/notification","params":{"key":"val"}}`); err != nil {
		t.Fatalf("SendTextMessage MCP notification failed: %v", err)
	}

	// 5. 客户端发送 abort 消息，Session.handleAbort 会递增 currentTurnId 并使旧 turnId=1 失效
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

	// 6. 此时排入新轮次 (turnId=2) 的语音帧与新的控制帧
	if err := writer.SendVoiceText(ctx, 2, []byte(`{"type":"tts","state":"sentence_start","text":"新轮次语音"}`)); err != nil {
		t.Fatalf("SendVoiceText turn 2 failed: %v", err)
	}
	if err := writer.SendTextMessage(ctx, `{"jsonrpc":"2.0","method":"ui/response","id":3}`); err != nil {
		t.Fatalf("SendTextMessage MCP response failed: %v", err)
	}

	// 7. 恢复 writeLoop 并关闭
	close(proceedChan)
	_ = writer.Close()
	sess.Close()

	// 8. 校验底层连接实际接收到的消息：
	// 预期包含：Hello 响应、MCP call(id=1)、MCP notification、新轮次语音、MCP response(id=3)
	// turnId=1 的语音文本和语音二进制必须已被完全跳过
	messages := conn.getMessages()
	for _, m := range messages {
		payloadStr := string(m.payload)
		if strings.Contains(payloadStr, "旧轮次待跳过语音") {
			t.Errorf("found invalidated turn 1 voice text in written messages: %s", payloadStr)
		}
		if bytes.Equal(m.payload, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
			t.Error("found invalidated turn 1 voice binary in written messages")
		}
	}

	// 确认 MCP 控制帧和新轮次语音帧存在
	foundMCP1 := false
	foundMCPNotification := false
	foundTurn2Voice := false
	foundMCP3 := false

	for _, m := range messages {
		s := string(m.payload)
		if strings.Contains(s, `"ui/call"`) {
			foundMCP1 = true
		}
		if strings.Contains(s, `"ui/notification"`) {
			foundMCPNotification = true
		}
		if strings.Contains(s, "新轮次语音") {
			foundTurn2Voice = true
		}
		if strings.Contains(s, `"ui/response"`) {
			foundMCP3 = true
		}
	}

	if !foundMCP1 {
		t.Error("expected MCP call message to be written, but was not found")
	}
	if !foundMCPNotification {
		t.Error("expected MCP notification message to be written, but was not found")
	}
	if !foundTurn2Voice {
		t.Error("expected turn 2 voice message to be written, but was not found")
	}
	if !foundMCP3 {
		t.Error("expected MCP response message to be written, but was not found")
	}
}

func TestSession_CloseTool_ClosesSessionAfterTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

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

	// 发送 listen.start
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
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

// TestSession_AsyncWriterError_ClosesSession 验证当 Writer 发生底层写错误时，Session 主循环能够从 ErrorNotify 监听到错误并主动关闭会话。
func TestSession_AsyncWriterError_ClosesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-12345678",
		Logger:       slog.Default(),
	})

	runDoneCh := make(chan error, 1)
	go func() {
		runDoneCh <- sess.Run()
	}()

	// 1. 完成 Hello 握手
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

	// 2. 模拟底层连接写入失败
	expectedErr := errors.New("underlying network write pipe broken")
	conn.mu.Lock()
	conn.writeErr = expectedErr
	conn.mu.Unlock()

	// 3. 触发一次下行写入
	_ = writer.SendTextMessage(ctx, "trigger-write-fail")

	// 4. 等待 Session 检测到写错误并关闭
	select {
	case <-runDoneCh:
		// Run 正常返回
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for Session.Run to exit on async writer error")
	}

	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed after writer error, got %v", sess.State())
	}
}
