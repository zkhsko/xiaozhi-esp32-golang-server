package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/voice"
)

type mockWSConn struct {
	mu          sync.Mutex
	messages    []mockWSMsg
	writeDelay  time.Duration
	writeErr    error
	writeErrIdx int
	writeCalls  int
}

type mockWSMsg struct {
	msgType websocket.MessageType
	payload []byte
}

func (m *mockWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	m.mu.Lock()
	m.writeCalls++
	delay := m.writeDelay
	err := m.writeErr
	errIdx := m.writeErrIdx
	calls := m.writeCalls
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err != nil && (errIdx <= 0 || calls == errIdx) {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	m.messages = append(m.messages, mockWSMsg{msgType: typ, payload: cp})
	return nil
}

func (m *mockWSConn) getMessages() []mockWSMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]mockWSMsg, len(m.messages))
	copy(cp, m.messages)
	return cp
}

func TestOutboundActor_SendTextSession(t *testing.T) {
	conn := &mockWSConn{}
	out := NewOutboundActor(context.Background(), conn, 10, 5*time.Second, nil, nil)
	defer out.Close()

	err := out.SendTextSession(context.Background(), []byte(`{"type":"hello"}`))
	if err != nil {
		t.Fatalf("SendTextSession failed: %v", err)
	}

	msgs := conn.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(msgs))
	}
	if string(msgs[0].payload) != `{"type":"hello"}` {
		t.Fatalf("unexpected payload: %s", string(msgs[0].payload))
	}
}

func TestOutboundActor_TurnOutput_FirstFrameBatch(t *testing.T) {
	conn := &mockWSConn{}
	out := NewOutboundActor(context.Background(), conn, 10, 5*time.Second, nil, nil)
	defer out.Close()

	turnOutput := out.NewTurnOutput(1, "sess-1")

	// 1. 发送 STT
	err := turnOutput.SendSTT(context.Background(), "你好")
	if err != nil {
		t.Fatalf("SendSTT failed: %v", err)
	}

	// 2. 发送首帧音频（带句首字幕）
	frame1 := voice.AudioFrame{
		OpusData:       []byte("opus-frame-1"),
		SentenceStarts: []string{"你好，我是小智。"},
	}
	err = turnOutput.SendAudio(context.Background(), frame1)
	if err != nil {
		t.Fatalf("SendAudio frame 1 failed: %v", err)
	}

	// 3. 发送第二帧普通音频
	frame2 := voice.AudioFrame{
		OpusData: []byte("opus-frame-2"),
	}
	err = turnOutput.SendAudio(context.Background(), frame2)
	if err != nil {
		t.Fatalf("SendAudio frame 2 failed: %v", err)
	}

	// 4. 正常 End
	err = turnOutput.End(context.Background(), voice.TurnEndCompleted)
	if err != nil {
		t.Fatalf("End failed: %v", err)
	}

	msgs := conn.getMessages()
	// 期望顺序：
	// 0: stt
	// 1: tts.start
	// 2: tts.sentence_start
	// 3: opus-frame-1 (binary)
	// 4: opus-frame-2 (binary)
	// 5: tts.stop
	if len(msgs) != 6 {
		t.Fatalf("expected 6 messages in total, got %d", len(msgs))
	}

	var sttMsg ServerSTTMessage
	_ = json.Unmarshal(msgs[0].payload, &sttMsg)
	if sttMsg.Type != "stt" || sttMsg.Text != "你好" {
		t.Fatalf("expected stt message, got %+v", sttMsg)
	}

	var ttsStart ServerTTSMessage
	_ = json.Unmarshal(msgs[1].payload, &ttsStart)
	if ttsStart.Type != "tts" || ttsStart.State != "start" {
		t.Fatalf("expected tts start, got %+v", ttsStart)
	}

	var sentenceStart ServerTTSMessage
	_ = json.Unmarshal(msgs[2].payload, &sentenceStart)
	if sentenceStart.Type != "tts" || sentenceStart.State != "sentence_start" || sentenceStart.Text != "你好，我是小智。" {
		t.Fatalf("expected tts sentence_start, got %+v", sentenceStart)
	}

	if msgs[3].msgType != websocket.MessageBinary || string(msgs[3].payload) != "opus-frame-1" {
		t.Fatalf("expected binary opus-frame-1, got %v", msgs[3])
	}

	if msgs[4].msgType != websocket.MessageBinary || string(msgs[4].payload) != "opus-frame-2" {
		t.Fatalf("expected binary opus-frame-2, got %v", msgs[4])
	}

	var ttsStop ServerTTSMessage
	_ = json.Unmarshal(msgs[5].payload, &ttsStop)
	if ttsStop.Type != "tts" || ttsStop.State != "stop" {
		t.Fatalf("expected tts stop, got %+v", ttsStop)
	}
}

func TestOutboundActor_NoTTSStart_NoTTSStop(t *testing.T) {
	conn := &mockWSConn{}
	out := NewOutboundActor(context.Background(), conn, 10, 5*time.Second, nil, nil)
	defer out.Close()

	turnOutput := out.NewTurnOutput(2, "sess-2")

	// 仅发了 STT，没有下发任何音频
	_ = turnOutput.SendSTT(context.Background(), "你好")

	// 发生 Abort 或失败直接 End
	err := turnOutput.End(context.Background(), voice.TurnEndAborted)
	if err != nil {
		t.Fatalf("End failed: %v", err)
	}

	msgs := conn.getMessages()
	// 应该只有 1 条 STT 消息，绝对不能有 tts.stop！
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 message (STT only), got %d", len(msgs))
	}
	var sttMsg ServerSTTMessage
	_ = json.Unmarshal(msgs[0].payload, &sttMsg)
	if sttMsg.Type != "stt" {
		t.Fatalf("expected stt message, got %+v", sttMsg)
	}
}

func TestOutboundActor_InvalidateTurn_Precision(t *testing.T) {
	conn := &mockWSConn{
		writeDelay: 50 * time.Millisecond,
	}
	out := NewOutboundActor(context.Background(), conn, 20, 5*time.Second, nil, nil)
	defer out.Close()

	turn1 := out.NewTurnOutput(1, "sess-1")
	turn2 := out.NewTurnOutput(2, "sess-1")

	// 启动协程持续向 turn 1 发送
	var turn1Err error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// 发送首帧
		_ = turn1.SendAudio(context.Background(), voice.AudioFrame{OpusData: []byte("frame1")})
		// 发送后续帧
		turn1Err = turn1.SendAudio(context.Background(), voice.AudioFrame{OpusData: []byte("frame2")})
	}()

	time.Sleep(20 * time.Millisecond)
	// 精准失效 turn 1
	out.InvalidateTurn(1)

	// 同时发送 turn 2 消息，应该不受 turn 1 失效影响
	err := turn2.SendSTT(context.Background(), "新轮次STT")
	if err != nil {
		t.Fatalf("turn2 SendSTT failed: %v", err)
	}

	wg.Wait()

	if turn1Err != nil && !errors.Is(turn1Err, ErrTurnAborted) {
		t.Fatalf("expected ErrTurnAborted for invalidated turn, got %v", turn1Err)
	}

	// 验证 Session 级消息也不受影响
	err = out.SendTextSession(context.Background(), []byte(`{"type":"session_msg"}`))
	if err != nil {
		t.Fatalf("SendTextSession failed: %v", err)
	}
}
