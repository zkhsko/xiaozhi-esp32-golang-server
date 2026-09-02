package session

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestWriter_MessageMetadata_ControlFrames 验证普通控制/业务帧不携带语音轮次 (turnId=0) 且来源标记为控制帧。
func TestWriter_MessageMetadata_ControlFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Writer{
		queue:  make(chan writeMessage, 10),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	if err := w.SendText(ctx, []byte("ctrl-payload")); err != nil {
		t.Fatalf("SendText failed: %v", err)
	}
	if err := w.SendTextMessage(ctx, "ctrl-text-msg"); err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}
	if err := w.SendBinary(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatalf("SendBinary failed: %v", err)
	}

	// 验证第一条：SendText
	select {
	case msg := <-w.queue:
		if msg.source != messageSourceControl {
			t.Errorf("expected source messageSourceControl, got %v", msg.source)
		}
		if msg.turnId != 0 {
			t.Errorf("expected turnId 0 for control frame, got %d", msg.turnId)
		}
		if msg.msgType != websocket.MessageText {
			t.Errorf("expected MessageText, got %v", msg.msgType)
		}
		if string(msg.payload) != "ctrl-payload" {
			t.Errorf("expected payload 'ctrl-payload', got %q", string(msg.payload))
		}
	default:
		t.Fatal("expected message in queue")
	}

	// 验证第二条：SendTextMessage
	select {
	case msg := <-w.queue:
		if msg.source != messageSourceControl {
			t.Errorf("expected source messageSourceControl, got %v", msg.source)
		}
		if msg.turnId != 0 {
			t.Errorf("expected turnId 0 for control frame, got %d", msg.turnId)
		}
		if msg.msgType != websocket.MessageText {
			t.Errorf("expected MessageText, got %v", msg.msgType)
		}
		if string(msg.payload) != "ctrl-text-msg" {
			t.Errorf("expected payload 'ctrl-text-msg', got %q", string(msg.payload))
		}
	default:
		t.Fatal("expected message in queue")
	}

	// 验证第三条：SendBinary
	select {
	case msg := <-w.queue:
		if msg.source != messageSourceControl {
			t.Errorf("expected source messageSourceControl, got %v", msg.source)
		}
		if msg.turnId != 0 {
			t.Errorf("expected turnId 0 for control frame, got %d", msg.turnId)
		}
		if msg.msgType != websocket.MessageBinary {
			t.Errorf("expected MessageBinary, got %v", msg.msgType)
		}
		if !bytes.Equal(msg.payload, []byte{1, 2, 3}) {
			t.Errorf("expected payload [1, 2, 3], got %v", msg.payload)
		}
	default:
		t.Fatal("expected message in queue")
	}
}

// TestWriter_MessageMetadata_VoiceFrames 验证语音文本和语音二进制帧携带正确 turnId 且来源标记为语音帧。
func TestWriter_MessageMetadata_VoiceFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Writer{
		queue:  make(chan writeMessage, 10),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	const expectedTurnId = uint64(42)

	if err := w.SendVoiceText(ctx, expectedTurnId, []byte(`{"type":"tts","state":"start"}`)); err != nil {
		t.Fatalf("SendVoiceText failed: %v", err)
	}
	if err := w.SendVoiceBinary(ctx, expectedTurnId, []byte{0xAA, 0xBB, 0xCC}); err != nil {
		t.Fatalf("SendVoiceBinary failed: %v", err)
	}

	// 验证语音文本帧
	select {
	case msg := <-w.queue:
		if msg.source != messageSourceVoice {
			t.Errorf("expected source messageSourceVoice, got %v", msg.source)
		}
		if msg.turnId != expectedTurnId {
			t.Errorf("expected turnId %d, got %d", expectedTurnId, msg.turnId)
		}
		if msg.msgType != websocket.MessageText {
			t.Errorf("expected MessageText, got %v", msg.msgType)
		}
		if string(msg.payload) != `{"type":"tts","state":"start"}` {
			t.Errorf("expected payload match, got %q", string(msg.payload))
		}
	default:
		t.Fatal("expected voice text message in queue")
	}

	// 验证语音二进制帧
	select {
	case msg := <-w.queue:
		if msg.source != messageSourceVoice {
			t.Errorf("expected source messageSourceVoice, got %v", msg.source)
		}
		if msg.turnId != expectedTurnId {
			t.Errorf("expected turnId %d, got %d", expectedTurnId, msg.turnId)
		}
		if msg.msgType != websocket.MessageBinary {
			t.Errorf("expected MessageBinary, got %v", msg.msgType)
		}
		if !bytes.Equal(msg.payload, []byte{0xAA, 0xBB, 0xCC}) {
			t.Errorf("expected payload [0xAA, 0xBB, 0xCC], got %v", msg.payload)
		}
	default:
		t.Fatal("expected voice binary message in queue")
	}
}

// TestWriter_PayloadDeepCopy 验证跨异步边界的数据深拷贝，确保修改外部切片不污染入队数据。
func TestWriter_PayloadDeepCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Writer{
		queue:  make(chan writeMessage, 10),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	// 测试 SendText 深拷贝
	textBuf := []byte("initial-text")
	if err := w.SendText(ctx, textBuf); err != nil {
		t.Fatalf("SendText failed: %v", err)
	}
	textBuf[0] = 'X'

	// 测试 SendBinary 深拷贝
	binBuf := []byte{10, 20, 30}
	if err := w.SendBinary(ctx, binBuf); err != nil {
		t.Fatalf("SendBinary failed: %v", err)
	}
	binBuf[0] = 99

	// 测试 SendVoiceText 深拷贝
	voiceTextBuf := []byte("voice-initial")
	if err := w.SendVoiceText(ctx, 1, voiceTextBuf); err != nil {
		t.Fatalf("SendVoiceText failed: %v", err)
	}
	voiceTextBuf[0] = 'Y'

	// 测试 SendVoiceBinary 深拷贝
	voiceBinBuf := []byte{40, 50, 60}
	if err := w.SendVoiceBinary(ctx, 1, voiceBinBuf); err != nil {
		t.Fatalf("SendVoiceBinary failed: %v", err)
	}
	voiceBinBuf[0] = 88

	// 校验队列中的内容未被外部篡改
	msg1 := <-w.queue
	if string(msg1.payload) != "initial-text" {
		t.Errorf("SendText payload was mutated: %q", string(msg1.payload))
	}

	msg2 := <-w.queue
	if !bytes.Equal(msg2.payload, []byte{10, 20, 30}) {
		t.Errorf("SendBinary payload was mutated: %v", msg2.payload)
	}

	msg3 := <-w.queue
	if string(msg3.payload) != "voice-initial" {
		t.Errorf("SendVoiceText payload was mutated: %q", string(msg3.payload))
	}

	msg4 := <-w.queue
	if !bytes.Equal(msg4.payload, []byte{40, 50, 60}) {
		t.Errorf("SendVoiceBinary payload was mutated: %v", msg4.payload)
	}
}

// TestWriter_SingleWriteLoop_FIFOOrder 验证所有普通帧与语音帧仍由同一个写循环按入队顺序 FIFO 写入底层连接。
func TestWriter_SingleWriteLoop_FIFOOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 20, nil)

	type expectedMsg struct {
		typ     websocket.MessageType
		payload []byte
	}

	expected := []expectedMsg{
		{typ: websocket.MessageText, payload: []byte(`{"type":"hello"}`)},
		{typ: websocket.MessageText, payload: []byte(`{"type":"tts","state":"start"}`)},
		{typ: websocket.MessageBinary, payload: []byte{0x01, 0x02, 0x03}},
		{typ: websocket.MessageText, payload: []byte(`{"jsonrpc":"2.0","method":"ui/call"}`)},
		{typ: websocket.MessageBinary, payload: []byte{0x04, 0x05, 0x06}},
		{typ: websocket.MessageText, payload: []byte(`{"type":"tts","state":"stop"}`)},
		{typ: websocket.MessageBinary, payload: []byte{0xAA, 0xBB}},
		{typ: websocket.MessageText, payload: []byte(`{"type":"tts","state":"sentence_start","text":"hello"}`)},
		{typ: websocket.MessageBinary, payload: []byte{0x07, 0x08, 0x09}},
	}

	// 按顺序发送混排消息：普通控制帧、语音帧 (turn 1)、MCP 普通文本帧、语音帧 (turn 1)、普通二进制帧、语音帧 (turn 2)
	if err := writer.SendText(ctx, expected[0].payload); err != nil {
		t.Fatalf("SendText failed: %v", err)
	}
	if err := writer.SendVoiceText(ctx, 1, expected[1].payload); err != nil {
		t.Fatalf("SendVoiceText failed: %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 1, expected[2].payload); err != nil {
		t.Fatalf("SendVoiceBinary failed: %v", err)
	}
	if err := writer.SendTextMessage(ctx, string(expected[3].payload)); err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 1, expected[4].payload); err != nil {
		t.Fatalf("SendVoiceBinary failed: %v", err)
	}
	if err := writer.SendVoiceText(ctx, 1, expected[5].payload); err != nil {
		t.Fatalf("SendVoiceText failed: %v", err)
	}
	if err := writer.SendBinary(ctx, expected[6].payload); err != nil {
		t.Fatalf("SendBinary failed: %v", err)
	}
	if err := writer.SendVoiceText(ctx, 2, expected[7].payload); err != nil {
		t.Fatalf("SendVoiceText failed: %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 2, expected[8].payload); err != nil {
		t.Fatalf("SendVoiceBinary failed: %v", err)
	}

	// 优雅关闭并等待写循环清空队列
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	messages := conn.getMessages()
	if len(messages) != len(expected) {
		t.Fatalf("expected %d written messages, got %d", len(expected), len(messages))
	}

	for i, want := range expected {
		got := messages[i]
		if got.typ != want.typ {
			t.Errorf("msg[%d] typ mismatch: expected %v, got %v", i, want.typ, got.typ)
		}
		if !bytes.Equal(got.payload, want.payload) {
			t.Errorf("msg[%d] payload mismatch: expected %s, got %s", i, string(want.payload), string(got.payload))
		}
	}
}

// TestWriter_QueueFull_Backpressure 验证当写队列满载时非阻塞入队返回 ErrWriteQueueFull。
func TestWriter_QueueFull_Backpressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Writer{
		queue:  make(chan writeMessage, 2),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	if err := w.SendText(ctx, []byte("msg-1")); err != nil {
		t.Fatalf("SendText 1 failed: %v", err)
	}
	if err := w.SendVoiceText(ctx, 1, []byte("msg-2")); err != nil {
		t.Fatalf("SendVoiceText 2 failed: %v", err)
	}

	// 队列已满，第 3 条消息应该返回 ErrWriteQueueFull
	if err := w.SendBinary(ctx, []byte{3}); !errors.Is(err, ErrWriteQueueFull) {
		t.Fatalf("expected ErrWriteQueueFull for SendBinary, got %v", err)
	}
	if err := w.SendVoiceBinary(ctx, 1, []byte{4}); !errors.Is(err, ErrWriteQueueFull) {
		t.Fatalf("expected ErrWriteQueueFull for SendVoiceBinary, got %v", err)
	}
}

// TestWriter_Closed_RejectsEnqueue 验证 Writer 关闭后入队立即返回 ErrWriterClosed。
func TestWriter_Closed_RejectsEnqueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	writer.Stop()

	if err := writer.SendText(ctx, []byte("test")); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed from SendText, got %v", err)
	}
	if err := writer.SendVoiceText(ctx, 1, []byte("test")); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed from SendVoiceText, got %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 1, []byte{1}); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed from SendVoiceBinary, got %v", err)
	}
	if err := writer.SendBinary(ctx, []byte{2}); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed from SendBinary, got %v", err)
	}
}

// TestWriter_CanceledContext 验证入队时传入已取消的 Context 返回 context.Canceled。
func TestWriter_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &Writer{
		queue:  make(chan writeMessage, 10),
		ctx:    context.Background(),
		cancel: func() {},
		done:   make(chan struct{}),
	}

	if err := w.SendText(ctx, []byte("test")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if err := w.SendVoiceText(ctx, 1, []byte("test")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if err := w.SendVoiceBinary(ctx, 1, []byte{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestWriter_WriteError_RecordsErrAndStops 验证底层写入失败时写循环记录错误并安全退出。
func TestWriter_WriteError_RecordsErrAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("network write failure")
	conn := &mockWSConn{writeErr: expectedErr}

	writer := NewWriter(ctx, conn, 10, nil)

	if err := writer.SendVoiceText(ctx, 1, []byte("will-fail")); err != nil {
		t.Fatalf("SendVoiceText failed: %v", err)
	}

	select {
	case <-writer.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not stop after write error")
	}

	if !errors.Is(writer.Err(), expectedErr) {
		t.Fatalf("expected Err() %v, got %v", expectedErr, writer.Err())
	}
}

// TestWriter_InvalidateVoiceTurn_SkipsInvalidatedVoiceFrames 构造旧语音帧、当前语音帧与 MCP 控制帧混排场景，
// 验证轮次失效后仅旧语音帧被跳过，普通控制帧、MCP 帧与新轮次语音帧仍按原序成功写入。
func TestWriter_InvalidateVoiceTurn_SkipsInvalidatedVoiceFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var firstWriteStarted sync.Once
	startedChan := make(chan struct{})
	proceedChan := make(chan struct{})

	conn := &mockWSConn{
		beforeWrite: func(typ websocket.MessageType, p []byte) {
			firstWriteStarted.Do(func() {
				close(startedChan)
				<-proceedChan
			})
		},
	}

	writer := NewWriter(ctx, conn, 50, nil)

	// 1. 发送第一条首帧控制消息，用于挂起 writeLoop
	if err := writer.SendTextMessage(ctx, `{"type":"hello_ack"}`); err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}

	// 等待 writeLoop 取出首帧并在 beforeWrite 中暂停
	<-startedChan

	// 2. 此时队列中追加：旧轮次语音帧 (turnId=1)、MCP 帧、旧轮次语音帧、普通控制二进制帧
	if err := writer.SendVoiceText(ctx, 1, []byte(`{"type":"tts","state":"start"}`)); err != nil {
		t.Fatalf("SendVoiceText 1 failed: %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 1, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("SendVoiceBinary 1 failed: %v", err)
	}
	if err := writer.SendTextMessage(ctx, `{"jsonrpc":"2.0","method":"ui/call","id":1}`); err != nil {
		t.Fatalf("SendTextMessage MCP failed: %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 1, []byte{0x04, 0x05}); err != nil {
		t.Fatalf("SendVoiceBinary 2 failed: %v", err)
	}
	if err := writer.SendBinary(ctx, []byte{0xAA, 0xBB}); err != nil {
		t.Fatalf("SendBinary failed: %v", err)
	}
	if err := writer.SendVoiceText(ctx, 1, []byte(`{"type":"tts","state":"stop"}`)); err != nil {
		t.Fatalf("SendVoiceText stop failed: %v", err)
	}

	// 3. 模拟 abort 触发：标记旧轮次 1 失效
	writer.InvalidateVoiceTurn(1)

	// 4. 继续追加新轮次语音帧 (turnId=2) 与新的 MCP 响应帧
	if err := writer.SendVoiceText(ctx, 2, []byte(`{"type":"tts","state":"start"}`)); err != nil {
		t.Fatalf("SendVoiceText turn 2 failed: %v", err)
	}
	if err := writer.SendVoiceBinary(ctx, 2, []byte{0x06, 0x07, 0x08}); err != nil {
		t.Fatalf("SendVoiceBinary turn 2 failed: %v", err)
	}
	if err := writer.SendTextMessage(ctx, `{"jsonrpc":"2.0","method":"ui/response","id":2}`); err != nil {
		t.Fatalf("SendTextMessage MCP 2 failed: %v", err)
	}

	// 5. 解除 writeLoop 暂停并关闭 writer 排空队列
	close(proceedChan)

	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// 6. 验证写入消息结果
	messages := conn.getMessages()

	type expectedMsg struct {
		typ     websocket.MessageType
		payload []byte
	}

	expected := []expectedMsg{
		{typ: websocket.MessageText, payload: []byte(`{"type":"hello_ack"}`)},
		{typ: websocket.MessageText, payload: []byte(`{"jsonrpc":"2.0","method":"ui/call","id":1}`)},
		{typ: websocket.MessageBinary, payload: []byte{0xAA, 0xBB}},
		{typ: websocket.MessageText, payload: []byte(`{"type":"tts","state":"start"}`)},
		{typ: websocket.MessageBinary, payload: []byte{0x06, 0x07, 0x08}},
		{typ: websocket.MessageText, payload: []byte(`{"jsonrpc":"2.0","method":"ui/response","id":2}`)},
	}

	if len(messages) != len(expected) {
		t.Fatalf("expected %d messages, got %d", len(expected), len(messages))
	}

	for i, want := range expected {
		got := messages[i]
		if got.typ != want.typ {
			t.Errorf("msg[%d] typ mismatch: expected %v, got %v", i, want.typ, got.typ)
		}
		if !bytes.Equal(got.payload, want.payload) {
			t.Errorf("msg[%d] payload mismatch: expected %s, got %s", i, string(want.payload), string(got.payload))
		}
	}
}

// TestWriter_InvalidateVoiceTurn_MultiTurnInvalidation 验证多次 abort 导致多个历史语音轮次失效的场景。
func TestWriter_InvalidateVoiceTurn_MultiTurnInvalidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var firstWriteStarted sync.Once
	startedChan := make(chan struct{})
	proceedChan := make(chan struct{})

	conn := &mockWSConn{
		beforeWrite: func(typ websocket.MessageType, p []byte) {
			firstWriteStarted.Do(func() {
				close(startedChan)
				<-proceedChan
			})
		},
	}

	writer := NewWriter(ctx, conn, 50, nil)

	if err := writer.SendTextMessage(ctx, `{"type":"init"}`); err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}

	<-startedChan

	// turn 1 语音帧与 MCP 1
	_ = writer.SendVoiceText(ctx, 1, []byte("turn1-voice-text"))
	_ = writer.SendVoiceBinary(ctx, 1, []byte{1})
	_ = writer.SendTextMessage(ctx, `{"mcp":1}`)

	// turn 2 语音帧与 MCP 2
	_ = writer.SendVoiceText(ctx, 2, []byte("turn2-voice-text"))
	_ = writer.SendVoiceBinary(ctx, 2, []byte{2})
	_ = writer.SendTextMessage(ctx, `{"mcp":2}`)

	// 失效 turn 1 与 turn 2
	writer.InvalidateVoiceTurn(1)
	writer.InvalidateVoiceTurn(2)

	// turn 3 语音帧与 MCP 3
	_ = writer.SendVoiceText(ctx, 3, []byte("turn3-voice-text"))
	_ = writer.SendVoiceBinary(ctx, 3, []byte{3})
	_ = writer.SendTextMessage(ctx, `{"mcp":3}`)

	close(proceedChan)
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	messages := conn.getMessages()
	expectedPayloads := []string{
		`{"type":"init"}`,
		`{"mcp":1}`,
		`{"mcp":2}`,
		"turn3-voice-text",
		string([]byte{3}),
		`{"mcp":3}`,
	}

	if len(messages) != len(expectedPayloads) {
		t.Fatalf("expected %d messages, got %d", len(expectedPayloads), len(messages))
	}

	for i, want := range expectedPayloads {
		if string(messages[i].payload) != want {
			t.Errorf("msg[%d] mismatch: expected %q, got %q", i, want, string(messages[i].payload))
		}
	}
}

// TestWriter_InvalidateVoiceTurn_ZeroTurnIdAndNilSafety 验证 turnId=0 不会导致语音帧或控制帧被误失效，
// 且在 nil Writer 上调用 InvalidateVoiceTurn 安全无 panic。
func TestWriter_InvalidateVoiceTurn_ZeroTurnIdAndNilSafety(t *testing.T) {
	// 验证 nil 安全
	var nilWriter *Writer
	nilWriter.InvalidateVoiceTurn(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)

	// turnId=0 应当是 no-op
	writer.InvalidateVoiceTurn(0)

	if err := writer.SendVoiceText(ctx, 1, []byte("valid-voice")); err != nil {
		t.Fatalf("SendVoiceText failed: %v", err)
	}
	if err := writer.SendTextMessage(ctx, "valid-control"); err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	messages := conn.getMessages()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if string(messages[0].payload) != "valid-voice" {
		t.Errorf("expected 'valid-voice', got %q", string(messages[0].payload))
	}
	if string(messages[1].payload) != "valid-control" {
		t.Errorf("expected 'valid-control', got %q", string(messages[1].payload))
	}
}
