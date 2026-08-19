package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/config"
)

// fakeWSConn 用于单元测试捕获串行写入的消息。
type fakeWSConn struct {
	mu       sync.Mutex
	messages []fakeWSMessage
	writeErr error
}

type fakeWSMessage struct {
	typ     websocket.MessageType
	payload []byte
}

func (f *fakeWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	copied := make([]byte, len(p))
	copy(copied, p)
	f.messages = append(f.messages, fakeWSMessage{
		typ:     typ,
		payload: copied,
	})
	return nil
}

func (f *fakeWSConn) Messages() []fakeWSMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]fakeWSMessage, len(f.messages))
	copy(result, f.messages)
	return result
}

// waitState 等待会话状态达到目标状态。
func waitState(t *testing.T, sess *Session, target State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sess.State() == target {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %v, current state: %v", target, sess.State())
}

// waitGeneration 等待问答代次达到目标值。
func waitGeneration(t *testing.T, sess *Session, target uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sess.Generation() == target {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for generation %d, current generation: %d", target, sess.Generation())
}

// createTestSession 创建测试用的 Session 对象及其关联的 fake writer。
func createTestSession(ctx context.Context) (*Session, *fakeWSConn, *Writer) {
	fakeConn := &fakeWSConn{}
	writer := NewWriter(ctx, fakeConn, 100, slog.Default())
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:         5 * time.Second,
			MaxOpusPacketBytes:   1024,
			MaxListeningDuration: 30 * time.Second,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "test-device",
		ClientID:     "test-client",
		SerialNumber: "test-sn",
	}
	sess := NewSessionWithWriter(ctx, nil, writer, info, cfg, nil, nil, nil, slog.Default())
	return sess, fakeConn, writer
}

// TestState_String 验证 State 枚举值的字符串表示。
func TestState_String(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateConnected, "CONNECTED"},
		{StateReady, "READY"},
		{StateListening, "LISTENING"},
		{StateProcessing, "PROCESSING"},
		{StateSpeaking, "SPEAKING"},
		{StateClosed, "CLOSED"},
		{State(99), "UNKNOWN(99)"},
		{State(-1), "UNKNOWN(-1)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("State(%d).String() = %q, expected %q", int(tt.state), got, tt.expected)
			}
		})
	}
}

// TestStateMachine_LegalPath_FullLifecycle 验证完整合法状态流转路径及多轮对话代次递增。
func TestStateMachine_LegalPath_FullLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, fakeConn, _ := createTestSession(ctx)

	go func() {
		_ = sess.Run()
	}()
	defer sess.Close()

	// 1. 初始状态为 CONNECTED
	if sess.State() != StateConnected {
		t.Fatalf("expected initial state CONNECTED, got %v", sess.State())
	}
	if sess.Generation() != 0 {
		t.Fatalf("expected initial generation 0, got %d", sess.Generation())
	}

	// 2. CONNECTED -> READY (hello 握手)
	validHello := []byte(`{
		"type": "hello",
		"version": 1,
		"transport": "websocket",
		"audio_params": {
			"format": "opus",
			"sample_rate": 16000,
			"channels": 1,
			"frame_duration": 60
		}
	}`)
	sess.postEvent(event{
		kind:     eventKindClientHello,
		rawBytes: validHello,
		isBinary: false,
	})
	waitState(t, sess, StateReady, 2*time.Second)

	if sess.SessionID() == "" {
		t.Fatal("expected non-empty session_id after hello handshake")
	}

	// 3. 第一轮问答 (auto 模式): READY -> LISTENING
	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	waitGeneration(t, sess, 1, 2*time.Second)
	if sess.Mode() != ListenModeAuto {
		t.Fatalf("expected mode %q, got %q", ListenModeAuto, sess.Mode())
	}

	// LISTENING -> PROCESSING (ASR 最终识别文本)
	sess.PostASRFinal(1, "今天天气怎么样")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// PROCESSING -> SPEAKING (TTS 首音频就绪)
	sess.PostTTSStarted(1)
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// SPEAKING -> READY (轮次结束)
	sess.PostTurnFinished(1)
	waitState(t, sess, StateReady, 2*time.Second)

	// 4. 第二轮问答 (manual 模式): READY -> LISTENING
	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeManual,
	})
	waitState(t, sess, StateListening, 2*time.Second)
	waitGeneration(t, sess, 2, 2*time.Second)
	if sess.Mode() != ListenModeManual {
		t.Fatalf("expected mode %q, got %q", ListenModeManual, sess.Mode())
	}

	// LISTENING -> PROCESSING
	sess.PostASRFinal(2, "帮我讲个笑话")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// PROCESSING -> SPEAKING
	sess.PostTTSStarted(2)
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// SPEAKING -> READY
	sess.PostTurnFinished(2)
	waitState(t, sess, StateReady, 2*time.Second)

	// 5. 校验下发给客户端的所有消息类型与顺序
	messages := fakeConn.Messages()
	if len(messages) < 6 {
		t.Fatalf("expected at least 6 messages, got %d", len(messages))
	}

	// 首条为 server hello
	var helloResp ServerHelloMessage
	if err := json.Unmarshal(messages[0].payload, &helloResp); err != nil {
		t.Fatalf("failed to parse server hello: %v", err)
	}
	if helloResp.Type != MessageTypeHello || helloResp.SessionID != sess.SessionID() {
		t.Errorf("server hello response mismatch: %+v", helloResp)
	}

	// 第二条为第一轮 STT
	var stt1 ServerSTTMessage
	if err := json.Unmarshal(messages[1].payload, &stt1); err != nil {
		t.Fatalf("failed to parse stt 1: %v", err)
	}
	if stt1.Type != MessageTypeSTT || stt1.Text != "今天天气怎么样" {
		t.Errorf("stt 1 mismatch: %+v", stt1)
	}

	// 第三条为第一轮 tts.start
	var ttsStart1 ServerTTSStartMessage
	if err := json.Unmarshal(messages[2].payload, &ttsStart1); err != nil {
		t.Fatalf("failed to parse tts.start 1: %v", err)
	}
	if ttsStart1.Type != MessageTypeTTS || ttsStart1.State != TTSStateStart {
		t.Errorf("tts.start 1 mismatch: %+v", ttsStart1)
	}

	// 第四条为第一轮 tts.stop
	var ttsStop1 ServerTTSStopMessage
	if err := json.Unmarshal(messages[3].payload, &ttsStop1); err != nil {
		t.Fatalf("failed to parse tts.stop 1: %v", err)
	}
	if ttsStop1.Type != MessageTypeTTS || ttsStop1.State != TTSStateStop {
		t.Errorf("tts.stop 1 mismatch: %+v", ttsStop1)
	}

	// 第五条为第二轮 STT
	var stt2 ServerSTTMessage
	if err := json.Unmarshal(messages[4].payload, &stt2); err != nil {
		t.Fatalf("failed to parse stt 2: %v", err)
	}
	if stt2.Type != MessageTypeSTT || stt2.Text != "帮我讲个笑话" {
		t.Errorf("stt 2 mismatch: %+v", stt2)
	}

	// 第六条为第二轮 tts.start
	var ttsStart2 ServerTTSStartMessage
	if err := json.Unmarshal(messages[5].payload, &ttsStart2); err != nil {
		t.Fatalf("failed to parse tts.start 2: %v", err)
	}
	if ttsStart2.Type != MessageTypeTTS || ttsStart2.State != TTSStateStart {
		t.Errorf("tts.start 2 mismatch: %+v", ttsStart2)
	}
}

// TestStateMachine_ReadyState_EarlyAudioDiscard 验证 READY 状态下收到 Opus 音频直接丢弃且不触发状态改变。
func TestStateMachine_ReadyState_EarlyAudioDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, _, _ := createTestSession(ctx)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手进入 READY 状态
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 模拟固件唤醒词残留音频（发送 5 个 Opus 音频包）
	for i := 0; i < 5; i++ {
		audioPacket := make([]byte, 80)
		sess.PostClientAudio(audioPacket)
	}

	// 短暂等待并确认状态仍为 READY，代次保持 0
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY after audio discard, got %v", sess.State())
	}
	if sess.Generation() != 0 {
		t.Fatalf("expected generation to remain 0, got %d", sess.Generation())
	}
}

// TestStateMachine_InvalidMessages_TableDriven 表驱动测试各类非法消息在不同状态下的忽略与隔离行为。
func TestStateMachine_InvalidMessages_TableDriven(t *testing.T) {
	tests := []struct {
		name          string
		setupState    func(s *Session)
		injectEvent   func(s *Session)
		expectedState State
		checkGen      func(t *testing.T, s *Session)
	}{
		{
			name: "READY 状态收到 listen.stop 应忽略",
			setupState: func(s *Session) {
				// READY
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStop})
			},
			expectedState: StateReady,
		},
		{
			name: "READY 状态收到 listen.detect 应忽略",
			setupState: func(s *Session) {
				// READY
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenDetect, DetectText: "小智小智"})
			},
			expectedState: StateReady,
		},
		{
			name: "READY 状态收到未知扩展消息应忽略",
			setupState: func(s *Session) {
				// READY
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindUnknownExtension, RawType: "custom_mcp"})
			},
			expectedState: StateReady,
		},
		{
			name: "LISTENING 状态收到重复 listen.start 应忽略",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
			},
			expectedState: StateListening,
			checkGen: func(t *testing.T, s *Session) {
				if s.Generation() != 1 {
					t.Errorf("expected generation 1, got %d", s.Generation())
				}
			},
		},
		{
			name: "LISTENING 状态在 auto 模式下收到 listen.stop 应忽略",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStop})
			},
			expectedState: StateListening,
		},
		{
			name: "PROCESSING 状态收到 listen.start 应忽略",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				s.PostASRFinal(1, "hello")
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
			},
			expectedState: StateProcessing,
		},
		{
			name: "PROCESSING 状态收到 listen.stop 应忽略",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				s.PostASRFinal(1, "hello")
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStop})
			},
			expectedState: StateProcessing,
		},
		{
			name: "SPEAKING 状态收到 listen.start 应忽略",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				s.PostASRFinal(1, "hello")
				s.PostTTSStarted(1)
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
			},
			expectedState: StateSpeaking,
		},
		{
			name: "SPEAKING 状态收到 listen.stop 应忽略",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				s.PostASRFinal(1, "hello")
				s.PostTTSStarted(1)
			},
			injectEvent: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStop})
			},
			expectedState: StateSpeaking,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sess, _, _ := createTestSession(ctx)
			go func() { _ = sess.Run() }()
			defer sess.Close()

			// 握手就绪
			validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
			sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
			waitState(t, sess, StateReady, 2*time.Second)

			tt.setupState(sess)
			time.Sleep(20 * time.Millisecond)

			tt.injectEvent(sess)
			time.Sleep(20 * time.Millisecond)

			if sess.State() != tt.expectedState {
				t.Errorf("expected state %v, got %v", tt.expectedState, sess.State())
			}
			if tt.checkGen != nil {
				tt.checkGen(t, sess)
			}
		})
	}
}

// TestStateMachine_AbortHandling 验证在各类状态下收到 abort 的代次增加、上下文取消与状态重置行为。
func TestStateMachine_AbortHandling(t *testing.T) {
	tests := []struct {
		name              string
		setupState        func(s *Session)
		initialGen        uint64
		expectedFinalGen  uint64
		expectTTSStopSent bool
	}{
		{
			name: "READY 状态下 abort",
			setupState: func(s *Session) {
				// READY
			},
			initialGen:        0,
			expectedFinalGen:  1,
			expectTTSStopSent: false,
		},
		{
			name: "LISTENING 状态下 abort",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
			},
			initialGen:        1,
			expectedFinalGen:  2,
			expectTTSStopSent: false,
		},
		{
			name: "PROCESSING 状态下 abort",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				s.PostASRFinal(1, "test text")
			},
			initialGen:        1,
			expectedFinalGen:  2,
			expectTTSStopSent: false,
		},
		{
			name: "SPEAKING 状态下 abort (需补发 tts.stop)",
			setupState: func(s *Session) {
				s.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				s.PostASRFinal(1, "test text")
				s.PostTTSStarted(1)
			},
			initialGen:        1,
			expectedFinalGen:  2,
			expectTTSStopSent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sess, fakeConn, _ := createTestSession(ctx)
			go func() { _ = sess.Run() }()
			defer sess.Close()

			// 握手进入 READY
			validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
			sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
			waitState(t, sess, StateReady, 2*time.Second)

			tt.setupState(sess)
			waitGeneration(t, sess, tt.initialGen, 2*time.Second)

			// 记录发送前消息数
			msgsBefore := len(fakeConn.Messages())

			// 触发 abort
			sess.PostAbort("user_cancel")
			waitState(t, sess, StateReady, 2*time.Second)
			waitGeneration(t, sess, tt.expectedFinalGen, 2*time.Second)

			// 验证是否按需补发了 tts.stop
			msgsAfter := fakeConn.Messages()
			if tt.expectTTSStopSent {
				foundTTSStop := false
				for _, msg := range msgsAfter[msgsBefore:] {
					var ttsStop ServerTTSStopMessage
					if err := json.Unmarshal(msg.payload, &ttsStop); err == nil && ttsStop.Type == MessageTypeTTS && ttsStop.State == TTSStateStop {
						foundTTSStop = true
						break
					}
				}
				if !foundTTSStop {
					t.Errorf("expected tts.stop message to be sent after abort in SPEAKING state, but was not found")
				}
			}
		})
	}
}

// TestStateMachine_GenerationIsolation 验证旧代次的异步迟到事件被完全丢弃。
func TestStateMachine_GenerationIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, _, _ := createTestSession(ctx)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手就绪
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 1. 进入第 1 轮收音
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	waitGeneration(t, sess, 1, 2*time.Second)

	// 中途 abort，代次增为 2 并回到 READY
	sess.PostAbort("user_interrupted")
	waitState(t, sess, StateReady, 2*time.Second)
	waitGeneration(t, sess, 2, 2*time.Second)

	// 2. 再次开启第 2 轮收音，代次增为 3
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	waitGeneration(t, sess, 3, 2*time.Second)

	// 3. 投递来自第 1 轮 (gen=1) 的迟到 ASR 结果 -> 应当被丢弃，状态仍为 LISTENING
	sess.PostASRFinal(1, "stale text from gen 1")
	time.Sleep(30 * time.Millisecond)
	if sess.State() != StateListening {
		t.Fatalf("expected state to remain LISTENING after stale ASR event, got %v", sess.State())
	}

	// 4. 投递来自当前第 2 轮 (gen=3) 的 ASR 结果 -> 正常流转至 PROCESSING
	sess.PostASRFinal(3, "valid text for gen 3")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 5. 投递来自第 1 轮 (gen=1) 的迟到 TTSStarted -> 应当被丢弃，状态仍为 PROCESSING
	sess.PostTTSStarted(1)
	time.Sleep(30 * time.Millisecond)
	if sess.State() != StateProcessing {
		t.Fatalf("expected state to remain PROCESSING after stale TTS started event, got %v", sess.State())
	}

	// 6. 投递当前代次 (gen=3) 的 TTSStarted -> 正常流转至 SPEAKING
	sess.PostTTSStarted(3)
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 7. 投递来自第 1 轮 (gen=1) 的迟到 TurnFinished -> 应当被丢弃，状态仍为 SPEAKING
	sess.PostTurnFinished(1)
	time.Sleep(30 * time.Millisecond)
	if sess.State() != StateSpeaking {
		t.Fatalf("expected state to remain SPEAKING after stale TurnFinished event, got %v", sess.State())
	}

	// 8. 投递当前代次 (gen=3) 的 TurnFinished -> 正常回到 READY
	sess.PostTurnFinished(3)
	waitState(t, sess, StateReady, 2*time.Second)
}

// TestStateMachine_ClosedState_IgnoresAllEvents 验证进入 CLOSED 状态后忽略所有后续事件。
func TestStateMachine_ClosedState_IgnoresAllEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, _, _ := createTestSession(ctx)
	go func() { _ = sess.Run() }()

	// 主动关闭会话
	sess.Close()
	waitState(t, sess, StateClosed, 2*time.Second)

	// 投递各类事件
	sess.PostClientText(&ClientMessage{Kind: KindListenStart})
	sess.PostClientAudio([]byte{0x01, 0x02})
	sess.PostASRFinal(1, "text")
	sess.PostTTSStarted(1)
	sess.PostTurnFinished(1)
	sess.PostAbort("stop")
	sess.PostError(1, errors.New("err"), true)
	sess.PostTimeout(1, "timeout")

	time.Sleep(30 * time.Millisecond)
	if sess.State() != StateClosed {
		t.Fatalf("expected state to stay CLOSED, got %v", sess.State())
	}
}

// TestStateMachine_ListeningTimeout 验证收音超时事件触发会话正常关闭。
func TestStateMachine_ListeningTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, _, _ := createTestSession(ctx)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手就绪
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 开始收音
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	// 模拟收音超时事件
	sess.PostTimeout(1, "max listening duration exceeded")
	waitState(t, sess, StateClosed, 2*time.Second)
}

// TestStateMachine_FatalErrorInSpeaking_SendsTTSStop 验证在 SPEAKING 状态发生致命错误时尽力下发 tts.stop 并关闭会话。
func TestStateMachine_FatalErrorInSpeaking_SendsTTSStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, fakeConn, _ := createTestSession(ctx)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> 收音 -> 处理 -> 播报
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	sess.PostASRFinal(1, "test")
	waitState(t, sess, StateProcessing, 2*time.Second)

	sess.PostTTSStarted(1)
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 触发致命错误
	sess.PostError(1, errors.New("downlink audio failed"), true)
	waitState(t, sess, StateClosed, 2*time.Second)

	// 验证下发了 tts.stop
	foundTTSStop := false
	for _, msg := range fakeConn.Messages() {
		var stop ServerTTSStopMessage
		if err := json.Unmarshal(msg.payload, &stop); err == nil && stop.Type == MessageTypeTTS && stop.State == TTSStateStop {
			foundTTSStop = true
			break
		}
	}
	if !foundTTSStop {
		t.Errorf("expected tts.stop to be sent on fatal error in SPEAKING state")
	}
}

// TestStateMachine_AudioPacketValidation 验证 LISTENING 状态下音频包大小边界校验。
func TestStateMachine_AudioPacketValidation(t *testing.T) {
	tests := []struct {
		name          string
		packet        []byte
		expectedState State
	}{
		{
			name:          "合法 Opus 音频包 (60 字节)",
			packet:        make([]byte, 60),
			expectedState: StateListening,
		},
		{
			name:          "合法最大边界音频包 (1024 字节)",
			packet:        make([]byte, 1024),
			expectedState: StateListening,
		},
		{
			name:          "空音频包 (0 字节) 触发策略违规关闭",
			packet:        []byte{},
			expectedState: StateClosed,
		},
		{
			name:          "超大音频包 (1025 字节) 触发策略违规关闭",
			packet:        make([]byte, 1025),
			expectedState: StateClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sess, _, _ := createTestSession(ctx)
			go func() { _ = sess.Run() }()
			defer sess.Close()

			// 握手 -> 进入 LISTENING
			validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
			sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
			waitState(t, sess, StateReady, 2*time.Second)

			sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
			waitState(t, sess, StateListening, 2*time.Second)

			sess.PostClientAudio(tt.packet)
			waitState(t, sess, tt.expectedState, 2*time.Second)
		})
	}
}

// TestStateMachine_ConcurrencyAndRace 验证多 goroutine 高并发提交事件与状态查询下的绝对线程安全与竞争无害性。
func TestStateMachine_ConcurrencyAndRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sess, _, _ := createTestSession(ctx)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手就绪
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	const goroutines = 10
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// 并发提交各类事件
				switch (gid + i) % 7 {
				case 0:
					sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
				case 1:
					sess.PostClientAudio(make([]byte, 40))
				case 2:
					sess.PostASRFinal(sess.Generation(), fmt.Sprintf("text_%d_%d", gid, i))
				case 3:
					sess.PostTTSStarted(sess.Generation())
				case 4:
					sess.PostTurnFinished(sess.Generation())
				case 5:
					sess.PostAbort("concurrent_abort")
				case 6:
					sess.PostClientText(&ClientMessage{Kind: KindUnknownExtension, RawType: "ping"})
				}

				// 并发读取状态属性
				_ = sess.State()
				_ = sess.Generation()
				_ = sess.Mode()
				_ = sess.SessionID()
				_ = sess.TurnContext()
			}
		}(g)
	}

	wg.Wait()
}
