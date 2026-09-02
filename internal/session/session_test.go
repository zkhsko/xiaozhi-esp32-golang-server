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

	// 2. 发送 listen.start (manual) -> 进入 Listening 状态，turnId 递增为 1
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

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)

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
		TTSClient:    ttsClient,
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

// TestSession_StateTransitions_NormalVoice_ProcessingToSpeakingToReady 验证正常语音轮次状态流转：
// Processing -> 首次下发 tts/start 后进入 Speaking -> stop 屏障确认完成后回到 Ready，且历史在此刻提交。
func TestSession_StateTransitions_NormalVoice_ProcessingToSpeakingToReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	voiceWriter := newMockVoiceWriter()

	barrierBlock := make(chan struct{})
	voiceWriter.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		<-barrierBlock
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "",
		TTSClient: ttsClient,
		Writer:    voiceWriter,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	startGenerate := make(chan struct{})
	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			<-startGenerate
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "你好，很高兴为您服务。", Iteration: 0})
			}
			return "你好，很高兴为您服务。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-TRANS-01",
		VoiceStream:  vs,
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
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

	// 等待进入 Ready
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after handshake, got %v", sess.State())
	}

	// 2. 发送 listen.start 进入 Listening
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 等待确已进入 Listening
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 3. 投递 ASR 识别结果，触发进入 Processing
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   "今天天气怎么样",
		},
	})

	// 验证确定性进入 Processing 状态（因 startGenerate 阻塞，稳定处于 Processing）
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateProcessing {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateProcessing {
		t.Fatalf("expected StateProcessing, got %v", sess.State())
	}

	// 4. 放行 LLM 生成流式文本，等待到达 Speaking 状态（因 barrierBlock 阻塞，稳定处于 Speaking）
	close(startGenerate)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateSpeaking {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateSpeaking {
		t.Fatalf("expected session to enter StateSpeaking, got %v", sess.State())
	}

	// 此时屏障尚未确认，历史记录严格为 0
	if histLen := sess.History().Len(); histLen != 0 {
		t.Fatalf("expected 0 history messages while in Speaking state before barrier confirmation, got %d", histLen)
	}

	// 5. 放行屏障，等待语音写出与屏障确认后回到 Ready 状态
	close(barrierBlock)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected session to return to StateReady, got %v", sess.State())
	}

	// 6. 验证历史在此刻成功追加
	if histLen := sess.History().Len(); histLen != 2 {
		t.Fatalf("expected 2 history messages after turn completion, got %d", histLen)
	}
	msgs := sess.History().Messages()
	if msgs[0].Content != "今天天气怎么样" || msgs[1].Content != "你好，很高兴为您服务。" {
		t.Fatalf("unexpected history content: %+v", msgs)
	}
}

// TestSession_StateTransitions_NoText_ProcessingDirectToReady 验证无可朗读文本轮次（0 句）：
// 直接从 Processing 回到 Ready，严格不进入 Speaking。
func TestSession_StateTransitions_NoText_ProcessingDirectToReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	voiceWriter := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "",
		TTSClient: ttsClient,
		Writer:    voiceWriter,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	// mockLLM 生成无文本回复（0 句）
	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			return "", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-TRANS-NOTEXT",
		VoiceStream:  vs,
		TTSClient:    ttsClient,
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}

	// 状态变迁轨迹监控
	var statesMu sync.Mutex
	var stateTrace []State
	recordState := func(s State) {
		statesMu.Lock()
		defer statesMu.Unlock()
		if len(stateTrace) == 0 || stateTrace[len(stateTrace)-1] != s {
			stateTrace = append(stateTrace, s)
		}
	}

	stopPoll := make(chan struct{})
	defer close(stopPoll)
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopPoll:
				return
			case <-ticker.C:
				recordState(sess.State())
			}
		}
	}()

	// 发送 listen.start 进入 Listening (使用 manual 模式验证无语音输出轮次绝不进入 Speaking)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 投递 ASR 识别结果进入 Processing
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   "执行静默指令",
		},
	})

	// 等待直接回到 Ready 状态
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected session to return to StateReady directly, got %v", sess.State())
	}

	// 验证在整个过程中绝未进入 Speaking 状态
	statesMu.Lock()
	traceCopy := make([]State, len(stateTrace))
	copy(traceCopy, stateTrace)
	statesMu.Unlock()

	for _, st := range traceCopy {
		if st == StateSpeaking {
			t.Fatalf("session must not enter StateSpeaking for 0-sentence turn, got trace: %v", traceCopy)
		}
	}
}

// TestSession_Abort_WhileSpeaking_ResetsToReady_NoHistory 验证在 Speaking 状态下发生 Abort 立即重置为 Ready 且绝不追加历史。
func TestSession_Abort_WhileSpeaking_ResetsToReady_NoHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	voiceWriter := newMockVoiceWriter()

	barrierBlock := make(chan struct{})
	voiceWriter.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		<-barrierBlock
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "",
		TTSClient: ttsClient,
		Writer:    voiceWriter,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "长文本播报第一句。", Iteration: 0})
			}
			return "长文本播报第一句。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-ABORT-SPEAKING",
		VoiceStream:  vs,
		TTSClient:    ttsClient,
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}

	// 发送 listen.start 进入 Listening
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 投递 ASR 识别结果进入 Processing 并触发首句
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   "开始播报",
		},
	})

	// 等待达到 Speaking 状态（因屏障阻塞而稳定处于 Speaking）
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateSpeaking {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateSpeaking {
		t.Fatalf("expected session to enter StateSpeaking before abort, got %v", sess.State())
	}

	// 发送 abort 控制帧
	abortRaw := []byte(`{"type":"abort","reason":"user interrupt"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     abortRaw,
	})

	// 验证立即重置为 Ready
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected session to reset to StateReady after abort, got %v", sess.State())
	}

	// 放行屏障
	close(barrierBlock)
	time.Sleep(50 * time.Millisecond)

	// 关键断言：Abort 发生后历史记录严格为 0
	if histLen := sess.History().Len(); histLen != 0 {
		t.Fatalf("expected 0 history messages after abort, got %d", histLen)
	}
}

// TestSession_VoiceStreamFailure_ResetsToReady_NoHistory 验证语音流非致命失败时会话转回 Ready 且绝不追加历史。
func TestSession_VoiceStreamFailure_ResetsToReady_NoHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		return errors.New("dashscope quota exceeded")
	}
	ttsClient := newMockTTSClient(mockStream)
	voiceWriter := newMockVoiceWriter()

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "",
		TTSClient: ttsClient,
		Writer:    voiceWriter,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "合成将失败的句子。", Iteration: 0})
			}
			return "合成将失败的句子。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-FAIL-RESET-READY",
		VoiceStream:  vs,
		TTSClient:    ttsClient,
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}

	// 发送 listen.start 进入 Listening
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(10 * time.Millisecond)
	}

	// 投递 ASR 识别结果进入 Processing
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   "触发失败流程",
		},
	})

	// 等待进入 Processing 或 Speaking
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() == StateListening {
		time.Sleep(10 * time.Millisecond)
	}

	// 等待语音流失败后会话转回 Ready 状态
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady on voice stream failure, got %v", sess.State())
	}

	// 关键断言：失败后历史记录严格为 0
	if histLen := sess.History().Len(); histLen != 0 {
		t.Fatalf("expected 0 history messages after voice failure, got %d", histLen)
	}
}

// TestSession_LayeredFailureSemantics 表驱动测试验证分层失败语义：
// 1. 首句建连失败、task-failed、协议错误、编码失败：非致命错误，回到 Ready 状态，不提交历史；
// 2. Writer 写失败、LLM 失败：致命错误，关闭会话（StateClosed），不提交历史；
// 3. 非致命失败后下一轮问答：正常新建 TTSStream 独立合成，成功提交该轮历史。
func TestSession_LayeredFailureSemantics(t *testing.T) {
	type testCase struct {
		name                 string
		setupFailure         func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream
		wantFatal            bool
		wantStateAfterFirst  State
		wantHistoryLenFirst  int
		testSecondTurn       bool
		wantStateAfterSecond State
		wantHistoryLenSecond int
	}

	tests := []testCase{
		{
			name: "TTS_ConnectFailure",
			setupFailure: func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream {
				tts.createStreamFn = func(ctx context.Context) (ai.TTSStream, error) {
					return nil, errors.New("dashscope dial timeout")
				}
				return nil
			},
			wantFatal:           false,
			wantStateAfterFirst: StateReady,
			wantHistoryLenFirst: 0,
		},
		{
			name: "TTS_TaskFailed",
			setupFailure: func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream {
				stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
					return errors.New("dashscope task failed: quota exceeded")
				}
				return nil
			},
			wantFatal:           false,
			wantStateAfterFirst: StateReady,
			wantHistoryLenFirst: 0,
		},
		{
			name: "TTS_ProtocolError",
			setupFailure: func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream {
				stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
					return errors.New("tts websocket protocol error: invalid frame payload")
				}
				return nil
			},
			wantFatal:           false,
			wantStateAfterFirst: StateReady,
			wantHistoryLenFirst: 0,
		},
		{
			name: "Audio_EncodingFailure",
			setupFailure: func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream {
				vsCfg := *cfg
				vsCfg.MaxOpusPacketBytes = -1
				return NewVoiceStream(VoiceStreamOptions{
					TTSClient: tts,
					Writer:    writer,
					Config:    vsCfg,
					Logger:    slog.Default(),
				})
			},
			wantFatal:           false,
			wantStateAfterFirst: StateReady,
			wantHistoryLenFirst: 0,
		},
		{
			name: "Writer_WriteFailure",
			setupFailure: func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream {
				conn.beforeWrite = func(typ websocket.MessageType, p []byte) {
					if strings.Contains(string(p), "tts") || typ == websocket.MessageBinary {
						conn.mu.Lock()
						conn.writeErr = errors.New("underlying network write pipe broken")
						conn.mu.Unlock()
					}
				}
				return nil
			},
			wantFatal:           true,
			wantStateAfterFirst: StateClosed,
			wantHistoryLenFirst: 0,
		},
		{
			name: "LLM_Failure",
			setupFailure: func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream {
				llm.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
					return "", errors.New("upstream llm connection refused")
				}
				return nil
			},
			wantFatal:           true,
			wantStateAfterFirst: StateClosed,
			wantHistoryLenFirst: 0,
		},
		{
			name: "NonFatalFailure_NextTurn_Success",
			setupFailure: func(conn *mockWSConn, writer *Writer, tts *mockTTSClient, stream *mockTTSStream, llm *mockLLMClient, cfg *SessionConfig) *VoiceStream {
				var calls int
				tts.createStreamFn = func(ctx context.Context) (ai.TTSStream, error) {
					calls++
					if calls == 1 {
						return nil, errors.New("dashscope dial timeout")
					}
					return newMockTTSStream(), nil
				}
				return nil
			},
			wantFatal:            false,
			wantStateAfterFirst:  StateReady,
			wantHistoryLenFirst:  0,
			testSecondTurn:       true,
			wantStateAfterSecond: StateReady,
			wantHistoryLenSecond: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			conn := &mockWSConn{}
			writer := NewWriter(ctx, conn, 10, nil)

			stream := newMockTTSStream()
			ttsClient := newMockTTSClient(stream)

			cfg := NormalizeConfig(SessionConfig{})

			mockLLM := &mockLLMClient{
				generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
					if callback != nil {
						_ = callback(ctx, ai.LLMChunk{Text: "这是第一轮回答。", Iteration: 0})
					}
					return "这是第一轮回答。", nil
				},
			}

			var customVS *VoiceStream
			if tc.setupFailure != nil {
				customVS = tc.setupFailure(conn, writer, ttsClient, stream, mockLLM, &cfg)
			}

			sess := NewSession(ctx, Options{
				Writer:       writer,
				SerialNumber: "SN-LAYERED-TEST",
				VoiceStream:  customVS,
				TTSClient:    ttsClient,
				LLMClient:    mockLLM,
				Config:       cfg,
				Logger:       slog.Default(),
			})

			go func() {
				_ = sess.Run()
			}()

			// 执行握手流程
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
			rawHello, _ := json.Marshal(helloMsg)
			sess.postEvent(sessionEvent{
				kind:     eventKindClientFrame,
				isBinary: false,
				data:     rawHello,
			})

			deadline := time.Now().Add(1 * time.Second)
			for time.Now().Before(deadline) && sess.State() != StateReady {
				time.Sleep(10 * time.Millisecond)
			}
			if sess.State() != StateReady {
				t.Fatalf("expected StateReady after handshake, got %v", sess.State())
			}

			// 发送 listen.start 开启第一轮
			listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
			sess.postEvent(sessionEvent{
				kind:     eventKindClientFrame,
				isBinary: false,
				data:     listenRaw,
			})

			deadline = time.Now().Add(1 * time.Second)
			for time.Now().Before(deadline) && sess.State() != StateListening {
				time.Sleep(10 * time.Millisecond)
			}

			// 投递第 1 轮 ASR 结果
			sess.postEvent(sessionEvent{
				kind: eventKindTurnEvent,
				turnEv: turnEvent{
					turnId: 1,
					typ:    turnEventASRFinal,
					text:   "第一轮问题",
				},
			})

			// 等待第一轮状态机流转结束
			deadline = time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && sess.State() != tc.wantStateAfterFirst {
				time.Sleep(10 * time.Millisecond)
			}
			if sess.State() != tc.wantStateAfterFirst {
				t.Fatalf("expected first turn final state %v, got %v", tc.wantStateAfterFirst, sess.State())
			}

			// 验证第一轮后的历史记录条目数
			if histLen := sess.History().Len(); histLen != tc.wantHistoryLenFirst {
				t.Fatalf("expected %d history turns after first turn, got %d", tc.wantHistoryLenFirst, histLen)
			}

			// 若不需要测试第二轮，则单轮测试结束
			if !tc.testSecondTurn {
				return
			}

			// 覆盖第二轮 LLM 回答
			mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
				if callback != nil {
					_ = callback(ctx, ai.LLMChunk{Text: "第二轮回答成功。", Iteration: 0})
				}
				return "第二轮回答成功。", nil
			}

			// 发送 listen.start 开启第二轮
			sess.postEvent(sessionEvent{
				kind:     eventKindClientFrame,
				isBinary: false,
				data:     listenRaw,
			})

			deadline = time.Now().Add(1 * time.Second)
			for time.Now().Before(deadline) && sess.State() != StateListening {
				time.Sleep(10 * time.Millisecond)
			}

			// 投递第 2 轮 ASR 结果
			sess.postEvent(sessionEvent{
				kind: eventKindTurnEvent,
				turnEv: turnEvent{
					turnId: 2,
					typ:    turnEventASRFinal,
					text:   "第二轮问题",
				},
			})

			// 等待第二轮成功应答并回到 Ready
			deadline = time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && sess.State() != tc.wantStateAfterSecond {
				time.Sleep(10 * time.Millisecond)
			}
			if sess.State() != tc.wantStateAfterSecond {
				t.Fatalf("expected second turn final state %v, got %v", tc.wantStateAfterSecond, sess.State())
			}

			// 关键断言：历史记录严格只有第二轮（共 2 条消息）
			hist := sess.History()
			if hist.Len() != tc.wantHistoryLenSecond*2 {
				t.Fatalf("expected %d messages in history after second turn, got %d", tc.wantHistoryLenSecond*2, hist.Len())
			}
			msgs := hist.Messages()
			if len(msgs) == 2 {
				if msgs[0].Content != "第二轮问题" || msgs[1].Content != "第二轮回答成功。" {
					t.Fatalf("unexpected history content: %+v", msgs)
				}
			}

			// 验证创建了全新的 TTSStream（共调用了 2 次 CreateStream）
			if ttsClient.createCalls != 2 {
				t.Fatalf("expected 2 CreateStream calls across two turns, got %d", ttsClient.createCalls)
			}
		})
	}
}

// TestSession_CloseSession_DelayedUntilStopBarrierWritten 验证正常告别后延迟关闭链路：
// 告别文本完整切句、TTS 播放、Opus 下行、tts/stop 与 Writer 屏障确认写出后，才触发设备连接优雅关闭并成功提交历史。
func TestSession_CloseSession_DelayedUntilStopBarrierWritten(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 20, nil)
	defer writer.Close()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)

	farewellText := "好的，祝您生活愉快，再见！"
	userQuestion := "退出会话"

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			for _, tool := range req.Tools {
				if tool.Name == agentkit.ToolCloseSession {
					_, _ = tool.Run(ctx, map[string]any{"reason": "用户请求退出"})
				}
			}
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: farewellText, Iteration: 0})
			}
			return farewellText, nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-CLOSE-DELAYED",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	// 1. 发送 hello 完成握手
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady, got %v", sess.State())
	}

	// 2. 发送 listen.start 进入 Listening
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"listen","state":"start","mode":"auto"}`),
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 3. 发送 ASRFinal 识别结果
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   userQuestion,
		},
	})

	// 4. 等待直到会话完成全部下行、stop 屏障写出并关闭会话
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateClosed {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed after farewell voice played and stop barrier written, got %v", sess.State())
	}

	// 5. 验证历史记录已成功提交
	hist := sess.History()
	if hist == nil || hist.Len() != 2 {
		t.Fatalf("expected history to have 2 messages (1 turn), got %d", hist.Len())
	}
	msgs := hist.Messages()
	if msgs[0].Role != ai.RoleUser || msgs[0].Content != userQuestion {
		t.Fatalf("expected user question %q, got %+v", userQuestion, msgs[0])
	}
	if msgs[1].Role != ai.RoleAssistant || msgs[1].Content != farewellText {
		t.Fatalf("expected farewell assistant text %q, got %+v", farewellText, msgs[1])
	}

	// 6. 验证底层 WebSocket 写入的消息序列包含完整的 TTS start/sentence_start/opus/stop 流程
	messages := conn.getMessages()
	var (
		foundSTT           bool
		foundTTSStart      bool
		foundSentenceStart bool
		sentenceStartText  string
		foundOpusBinary    bool
		foundTTSStop       bool
		stopIdx            = -1
		sentenceStartIdx   = -1
		startIdx           = -1
	)

	for i, m := range messages {
		if m.typ == websocket.MessageText {
			var parsed map[string]any
			if err := json.Unmarshal(m.payload, &parsed); err == nil {
				if parsed["type"] == "stt" {
					foundSTT = true
				}
				if parsed["type"] == "tts" {
					state, _ := parsed["state"].(string)
					switch state {
					case "start":
						foundTTSStart = true
						startIdx = i
					case "sentence_start":
						foundSentenceStart = true
						sentenceStartIdx = i
						sentenceStartText, _ = parsed["text"].(string)
					case "stop":
						foundTTSStop = true
						stopIdx = i
					}
				}
			}
		} else if m.typ == websocket.MessageBinary {
			foundOpusBinary = true
		}
	}

	if !foundSTT {
		t.Fatal("expected STT message to be sent")
	}
	if !foundTTSStart {
		t.Fatal("expected tts/start message to be sent")
	}
	if !foundSentenceStart {
		t.Fatal("expected tts/sentence_start message to be sent")
	}
	if sentenceStartText != farewellText {
		t.Fatalf("expected sentence_start text to match farewell text %q, got %q", farewellText, sentenceStartText)
	}
	if !foundOpusBinary {
		t.Fatal("expected Opus binary audio packets to be sent")
	}
	if !foundTTSStop {
		t.Fatal("expected tts/stop message to be sent")
	}
	if !(startIdx < sentenceStartIdx && sentenceStartIdx < stopIdx) {
		t.Fatalf("unexpected message order: startIdx=%d, sentenceStartIdx=%d, stopIdx=%d", startIdx, sentenceStartIdx, stopIdx)
	}
}

// TestSession_CloseSession_NotClosedBeforeStopBarrier 验证 stop 尚未写出时不提前关闭：
// 在写入屏障未完成前，设备会话保持开启且不提交历史；屏障完成后才关闭并提交历史。
func TestSession_CloseSession_NotClosedBeforeStopBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)
	voiceWriter := newMockVoiceWriter()

	barrierBlock := make(chan struct{})
	voiceWriter.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		<-barrierBlock
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "",
		TTSClient: ttsClient,
		Writer:    voiceWriter,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	farewellText := "好的，再见！"
	userText := "退出系统"

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			for _, tool := range req.Tools {
				if tool.Name == agentkit.ToolCloseSession {
					_, _ = tool.Run(ctx, map[string]any{"reason": "用户离开"})
				}
			}
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: farewellText, Iteration: 0})
			}
			return farewellText, nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-CLOSE-NOT-BEFORE-BARRIER",
		VoiceStream:  vs,
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady, got %v", sess.State())
	}

	// 2. 发送 listen.start
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"listen","state":"start","mode":"auto"}`),
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 3. 投递 ASRFinal 识别结果
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   userText,
		},
	})

	// 4. 等待进入 Speaking 状态（屏障被 barrierBlock 阻塞，stop 尚未完成确认）
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateSpeaking {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateSpeaking {
		t.Fatalf("expected StateSpeaking before barrier written, got %v", sess.State())
	}

	// 验证在屏障未通过前：设备会话保持开启，历史记录不提前提交
	time.Sleep(100 * time.Millisecond)
	if sess.State() == StateClosed {
		t.Fatal("session must not be closed before stop barrier is written")
	}
	if sess.History().Len() != 0 {
		t.Fatalf("history must not be committed before stop barrier is written, got len %d", sess.History().Len())
	}

	// 5. 放行屏障，允许 stop 确认写出
	close(barrierBlock)

	// 6. 等待屏障写出后会话优雅关闭
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateClosed {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed after barrier written, got %v", sess.State())
	}

	// 7. 验证屏障通过并关闭后，历史记录成功提交
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages after close, got %d", sess.History().Len())
	}
	msgs := sess.History().Messages()
	if msgs[0].Content != userText || msgs[1].Content != farewellText {
		t.Fatalf("unexpected history content: %+v", msgs)
	}
}

// TestSession_CloseSession_AbortClearsCloseIntentAndReturnsToReady 验证 abort 优先语义：
// 在告别语播报期间收到 abort 帧时，清除当前轮次关闭意图与暂存历史，恢复 StateReady 且连接保持打开，后续可正常执行下一轮。
func TestSession_CloseSession_AbortClearsCloseIntentAndReturnsToReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	ttsClient := newMockTTSClient(nil)
	voiceWriter := newMockVoiceWriter()

	barrierBlock := make(chan struct{})
	voiceWriter.enqueueBarrierFn = func(ctx context.Context, turnId uint64) error {
		if turnId == 1 {
			<-barrierBlock
		}
		return nil
	}

	vs := NewVoiceStream(VoiceStreamOptions{
		SessionId: "",
		TTSClient: ttsClient,
		Writer:    voiceWriter,
		Config:    NormalizeConfig(SessionConfig{}),
		Logger:    slog.Default(),
	})
	defer vs.Close()

	var (
		mu            sync.Mutex
		generateCount int
	)

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			mu.Lock()
			generateCount++
			count := generateCount
			mu.Unlock()

			if count == 1 {
				for _, tool := range req.Tools {
					if tool.Name == agentkit.ToolCloseSession {
						_, _ = tool.Run(ctx, map[string]any{"reason": "退出"})
					}
				}
				if callback != nil {
					_ = callback(ctx, ai.LLMChunk{Text: "好的，再见！", Iteration: 0})
				}
				return "好的，再见！", nil
			}

			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "我在呢，请问有什么可以帮您？", Iteration: 0})
			}
			return "我在呢，请问有什么可以帮您？", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-CLOSE-ABORT-PRIORITY",
		VoiceStream:  vs,
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady, got %v", sess.State())
	}

	// 2. 发起第 1 轮：listen.start -> ASRFinal
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"listen","state":"start","mode":"auto"}`),
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   "退出会话",
		},
	})

	// 等待进入 Speaking 状态（告别语正在播报，屏障被 barrierBlock 阻塞）
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateSpeaking {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateSpeaking {
		t.Fatalf("expected StateSpeaking, got %v", sess.State())
	}

	// 3. 在 Speaking 播报期间收到客户端 abort 帧
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"abort","reason":"wake_word_detected"}`),
	})

	// 4. 验证会话立即重置为 StateReady，绝不进入 StateClosed
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after abort, got %v", sess.State())
	}

	// 放行第 1 轮被阻塞的屏障
	close(barrierBlock)
	time.Sleep(50 * time.Millisecond)

	// 验证关闭意图与暂存历史已彻底清除，会话未关闭
	if sess.State() != StateReady {
		t.Fatalf("session should stay in StateReady, got %v", sess.State())
	}
	if sess.History().Len() != 0 {
		t.Fatalf("history should not be committed after abort, got len %d", sess.History().Len())
	}

	// 5. 验证会话存活并能顺利执行第 2 轮问答
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"listen","state":"start","mode":"auto"}`),
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening for turn 2, got %v", sess.State())
	}

	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 3,
			typ:    turnEventASRFinal,
			text:   "继续聊天",
		},
	})

	// 先等待离开 Listening 进入处理或播报
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() == StateListening {
		time.Sleep(5 * time.Millisecond)
	}

	// 再等待第 2 轮正常完成并返回 StateReady
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after turn 2 finished, got %v", sess.State())
	}

	// 验证历史记录仅包含第 2 轮的内容（第 1 轮被 abort 丢弃）
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages (turn 2 only), got %d", sess.History().Len())
	}
	msgs := sess.History().Messages()
	if msgs[0].Content != "继续聊天" || msgs[1].Content != "我在呢，请问有什么可以帮您？" {
		t.Fatalf("unexpected history messages: %+v", msgs)
	}
}

// TestSession_CloseSession_FatalErrorClosesSessionWithoutHistory 验证不可恢复错误语义：
// 已标记关闭意图时若发生非用户的不可恢复失败（如 LLM 致命失败），仍关闭设备会话，但绝不提交失败轮次历史。
func TestSession_CloseSession_FatalErrorClosesSessionWithoutHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)

	expectedErr := errors.New("simulated fatal llm failure")

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			for _, tool := range req.Tools {
				if tool.Name == agentkit.ToolCloseSession {
					_, _ = tool.Run(ctx, map[string]any{"reason": "退出"})
				}
			}
			return "", expectedErr
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-CLOSE-FATAL",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady, got %v", sess.State())
	}

	// 2. 发送 listen.start
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     []byte(`{"type":"listen","state":"start","mode":"auto"}`),
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 3. 投递 ASRFinal
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 1,
			typ:    turnEventASRFinal,
			text:   "退出会话",
		},
	})

	// 4. 等待会话因不可恢复错误关闭
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateClosed {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed on fatal error, got %v", sess.State())
	}

	// 5. 验证绝不提交失败轮次历史
	if sess.History().Len() != 0 {
		t.Fatalf("expected 0 history messages after fatal error, got %d", sess.History().Len())
	}
}
