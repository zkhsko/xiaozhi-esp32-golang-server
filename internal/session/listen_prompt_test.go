package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// blockingASRStream 阻塞直到上下文取消或显式投递结果的 ASRStream 桩。
type blockingASRStream struct {
	resultCh chan string
}

func (s *blockingASRStream) Result(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res, ok := <-s.resultCh:
		if !ok {
			return "", nil
		}
		return res, nil
	}
}

func (s *blockingASRStream) WritePCM(ctx context.Context, pcm []byte) error {
	return nil
}

func (s *blockingASRStream) Finish(ctx context.Context) error {
	return nil
}

func (s *blockingASRStream) Close() error {
	return nil
}

// countASRClient 统计 CreateStream 被调用次数并返回阻塞式 stream 的 ASRClient 桩实现。
type countASRClient struct {
	createCount atomic.Int32
}

func (c *countASRClient) CreateStream(ctx context.Context) (ai.ASRStream, error) {
	c.createCount.Add(1)
	return &blockingASRStream{
		resultCh: make(chan string),
	}, nil
}

// setupHandshakedSession 创建并完成 WebSocket hello 握手就绪的 Session 测试实例。
func setupHandshakedSession(t *testing.T, ctx context.Context, conn *mockWSConn, asrClient ai.ASRClient, vs *VoiceStream) *Session {
	t.Helper()
	writer := NewWriter(ctx, conn, 50, nil)

	sess := NewSession(ctx, Options{
		Conn:         nil,
		Writer:       writer,
		SerialNumber: "SN-PROMPT-TEST",
		Config:       NormalizeConfig(SessionConfig{}),
		ASRClient:    asrClient,
		VoiceStream:  vs,
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

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after hello handshake, got %v", sess.State())
	}

	return sess
}

// TestSession_ListenPrompt_AutoMode_SequenceAndBarrier 验证自动模式下提示音按严格顺序下发并在屏障确认后才转入 Listening 并启动收音。
func TestSession_ListenPrompt_AutoMode_SequenceAndBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asrClient := &countASRClient{}
	sess := setupHandshakedSession(t, ctx, conn, asrClient, nil)
	defer sess.Close()

	expectedPackets, err := audio.GetListenPromptOpusPackets()
	if err != nil {
		t.Fatalf("failed to get expected prompt opus packets: %v", err)
	}
	expectedPacketCount := len(expectedPackets)
	if expectedPacketCount == 0 {
		t.Fatal("expected non-empty listen prompt opus packets")
	}

	// 1. 发送 listen.start (auto 模式)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 2. 等待提示音播放完成并成功转入 StateListening
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening after prompt barrier confirmed, got %v", sess.State())
	}

	// 3. 验证 ASR 启动次数严格为 1
	if count := asrClient.createCount.Load(); count != 1 {
		t.Fatalf("expected ASR CreateStream to be called exactly 1 time, got %d", count)
	}

	// 4. 验证下发的底层消息序列：包含 Hello 响应、tts/start、全部提示音 Opus 帧、tts/stop
	messages := conn.getMessages()
	// 第 0 条为 hello 响应
	if len(messages) < 1+1+expectedPacketCount+1 {
		t.Fatalf("expected at least %d messages, got %d", 1+1+expectedPacketCount+1, len(messages))
	}

	promptMessages := messages[1:]
	if len(promptMessages) != 1+expectedPacketCount+1 {
		t.Fatalf("expected exactly %d prompt messages, got %d", 1+expectedPacketCount+1, len(promptMessages))
	}

	// 验证第一条为 tts/start
	firstMsg := promptMessages[0]
	if firstMsg.typ != websocket.MessageText {
		t.Fatalf("expected first prompt msg to be text, got %v", firstMsg.typ)
	}
	var startObj map[string]any
	if err := json.Unmarshal(firstMsg.payload, &startObj); err != nil {
		t.Fatalf("failed to unmarshal tts/start: %v", err)
	}
	if startObj["type"] != "tts" || startObj["state"] != "start" {
		t.Fatalf("expected tts/start message, got %+v", startObj)
	}
	if startObj["session_id"] != sess.SessionId() {
		t.Fatalf("expected session_id %q, got %v", sess.SessionId(), startObj["session_id"])
	}

	// 验证中间为全部提示音 Opus 二进制包
	for i := 0; i < expectedPacketCount; i++ {
		opusMsg := promptMessages[1+i]
		if opusMsg.typ != websocket.MessageBinary {
			t.Fatalf("expected prompt opus frame %d to be binary, got %v", i, opusMsg.typ)
		}
		if !bytes.Equal(opusMsg.payload, expectedPackets[i]) {
			t.Fatalf("prompt opus frame %d content mismatch", i)
		}
	}

	// 验证最后一条为 tts/stop
	lastMsg := promptMessages[1+expectedPacketCount]
	if lastMsg.typ != websocket.MessageText {
		t.Fatalf("expected last prompt msg to be text, got %v", lastMsg.typ)
	}
	var stopObj map[string]any
	if err := json.Unmarshal(lastMsg.payload, &stopObj); err != nil {
		t.Fatalf("failed to unmarshal tts/stop: %v", err)
	}
	if stopObj["type"] != "tts" || stopObj["state"] != "stop" {
		t.Fatalf("expected tts/stop message, got %+v", stopObj)
	}
	if stopObj["session_id"] != sess.SessionId() {
		t.Fatalf("expected session_id %q, got %v", sess.SessionId(), stopObj["session_id"])
	}

	// 验证整个序列中绝无 tts/sentence_start
	for _, m := range promptMessages {
		if m.typ == websocket.MessageText && bytes.Contains(m.payload, []byte("sentence_start")) {
			t.Fatalf("unexpected sentence_start in listen prompt sequence: %s", string(m.payload))
		}
	}
}

// TestSession_ListenPrompt_AutoMode_BarrierBlocksListening 验证屏障写出完成前绝不进入 Listening 且绝不启动 ASR。
func TestSession_ListenPrompt_AutoMode_BarrierBlocksListening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var holdStopOnce sync.Once
	stopWritten := make(chan struct{})
	proceedStop := make(chan struct{})

	conn := &mockWSConn{
		beforeWrite: func(typ websocket.MessageType, p []byte) {
			if typ == websocket.MessageText && bytes.Contains(p, []byte(`"state":"stop"`)) {
				holdStopOnce.Do(func() {
					close(stopWritten)
					<-proceedStop
				})
			}
		},
	}
	asrClient := &countASRClient{}
	sess := setupHandshakedSession(t, ctx, conn, asrClient, nil)
	defer sess.Close()

	// 1. 发送 listen.start (auto)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 2. 等待 tts/stop 开始写入（底层写入被挂起阻塞）
	select {
	case <-stopWritten:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for prompt tts/stop write")
	}

	// 3. 验证在屏障被阻塞期间，Session 状态保持在 Speaking，绝未进入 Listening，ASRClient 绝未调用
	for i := 0; i < 10; i++ {
		if st := sess.State(); st != StateSpeaking {
			t.Fatalf("expected session to remain in StateSpeaking while barrier is blocked, got %v", st)
		}
		if count := asrClient.createCount.Load(); count != 0 {
			t.Fatalf("expected ASR CreateStream to not be called before barrier completion, got %d", count)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 4. 放行底层写入，等待屏障完成
	close(proceedStop)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening after barrier unblocked, got %v", sess.State())
	}

	if count := asrClient.createCount.Load(); count != 1 {
		t.Fatalf("expected ASR CreateStream to be called exactly 1 time after barrier, got %d", count)
	}
}

// TestSession_ListenPrompt_AutoMode_AbortDuringPrompt_NoStaleListening 验证提示音等待期间 abort 重置为 Ready 且旧轮次屏障完成绝不产生迟到收音。
func TestSession_ListenPrompt_AutoMode_AbortDuringPrompt_NoStaleListening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var holdStartOnce sync.Once
	startWritten := make(chan struct{})
	proceedStart := make(chan struct{})

	conn := &mockWSConn{
		beforeWrite: func(typ websocket.MessageType, p []byte) {
			if typ == websocket.MessageText && bytes.Contains(p, []byte(`"state":"start"`)) {
				holdStartOnce.Do(func() {
					close(startWritten)
					<-proceedStart
				})
			}
		},
	}
	asrClient := &countASRClient{}
	sess := setupHandshakedSession(t, ctx, conn, asrClient, nil)
	defer sess.Close()

	// 1. 发送 listen.start (auto)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 2. 等待提示音 tts/start 开始写入
	select {
	case <-startWritten:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for prompt tts/start write")
	}

	// 3. 发送 abort 消息
	abortRaw := []byte(`{"type":"abort","reason":"user cancel during prompt"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     abortRaw,
	})

	// 4. 验证会话立即重置为 StateReady
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after abort, got %v", sess.State())
	}

	// 5. 放行底层写入，Writer 将跳过旧轮次语音帧，屏障返回后丢弃过期完成事件
	close(proceedStart)

	// 持续验证 Session 保持在 StateReady，绝不进入 StateListening，ASRClient 绝不被调用
	for i := 0; i < 15; i++ {
		if st := sess.State(); st != StateReady {
			t.Fatalf("expected session to remain in StateReady, got %v", st)
		}
		if count := asrClient.createCount.Load(); count != 0 {
			t.Fatalf("expected ASR CreateStream to not be called for aborted prompt turn, got %d", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSession_ListenPrompt_AutoMode_DuplicateStart_Ignored 验证提示音播放期间重复 start 被安全忽略且只启动一次收音。
func TestSession_ListenPrompt_AutoMode_DuplicateStart_Ignored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var holdStartOnce sync.Once
	startWritten := make(chan struct{})
	proceedStart := make(chan struct{})

	conn := &mockWSConn{
		beforeWrite: func(typ websocket.MessageType, p []byte) {
			if typ == websocket.MessageText && bytes.Contains(p, []byte(`"state":"start"`)) {
				holdStartOnce.Do(func() {
					close(startWritten)
					<-proceedStart
				})
			}
		},
	}
	asrClient := &countASRClient{}
	sess := setupHandshakedSession(t, ctx, conn, asrClient, nil)
	defer sess.Close()

	// 1. 发送第 1 个 listen.start (auto)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 等待挂起在 tts/start
	select {
	case <-startWritten:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for prompt tts/start")
	}

	// 2. 发送第 2 个重复的 listen.start
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})
	time.Sleep(20 * time.Millisecond)

	// 3. 放行写入并等待进入 Listening
	close(proceedStart)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening, got %v", sess.State())
	}

	// 4. 验证收音只启动了一次
	if count := asrClient.createCount.Load(); count != 1 {
		t.Fatalf("expected exactly 1 ASR CreateStream call, got %d", count)
	}
}

// TestSession_ListenPrompt_AutoMode_WriteFailure_NoStaleListening 验证提示音写出失败时安全关闭会话且绝不进入 Listening。
func TestSession_ListenPrompt_AutoMode_WriteFailure_NoStaleListening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var conn *mockWSConn
	conn = &mockWSConn{
		beforeWrite: func(typ websocket.MessageType, p []byte) {
			if typ == websocket.MessageBinary {
				// 在写入提示音 Opus 帧时触发写失败
				conn.mu.Lock()
				conn.writeErr = errors.New("simulated underlying prompt write error")
				conn.mu.Unlock()
			}
		},
	}
	asrClient := &countASRClient{}
	sess := setupHandshakedSession(t, ctx, conn, asrClient, nil)
	defer sess.Close()

	// 发送 listen.start (auto)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"auto"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 验证因底层写失败转为 StateClosed
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateClosed {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed after prompt write error, got %v", sess.State())
	}

	// 验证 ASR 绝未被启动
	if count := asrClient.createCount.Load(); count != 0 {
		t.Fatalf("expected 0 ASR calls on write error, got %d", count)
	}
}

// TestSession_ListenPrompt_ManualMode_NoPrompt_DirectListening 验证手动模式直接进入 Listening 且绝不下发提示音语音帧。
func TestSession_ListenPrompt_ManualMode_NoPrompt_DirectListening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asrClient := &countASRClient{}
	sess := setupHandshakedSession(t, ctx, conn, asrClient, nil)
	defer sess.Close()

	// 1. 发送 listen.start (manual 模式)
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	// 2. 验证直接进入 StateListening
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateListening {
		t.Fatalf("expected StateListening in manual mode, got %v", sess.State())
	}

	// 3. 验证 ASR 立即启动
	if count := asrClient.createCount.Load(); count != 1 {
		t.Fatalf("expected exactly 1 ASR call in manual mode, got %d", count)
	}

	// 4. 验证底层消息除 Hello 握手回执外，没有发送任何 tts 消息或 Opus 音频
	messages := conn.getMessages()
	if len(messages) != 1 {
		t.Fatalf("expected only 1 hello response message in manual mode, got %d messages: %+v", len(messages), messages)
	}
}

// TestSession_ListenPrompt_EndPaths_NoPromptAudio 验证各结束路径均不额外播放结束提示音。
func TestSession_ListenPrompt_EndPaths_NoPromptAudio(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	asrClient := &countASRClient{}
	sess := setupHandshakedSession(t, ctx, conn, asrClient, nil)
	defer sess.Close()

	// 路径 1: 手动模式收音中收到 listen.stop -> 不额外下发提示音
	listenRaw := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw,
	})

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}

	stopRaw := []byte(`{"type":"listen","state":"stop"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     stopRaw,
	})
	time.Sleep(50 * time.Millisecond)

	// 路径 2: 收到 abort -> 不额外下发提示音
	abortRaw := []byte(`{"type":"abort","reason":"user interrupt"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     abortRaw,
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after abort, got %v", sess.State())
	}

	// 路径 3: 正常 ASR 文本为空回到 Ready -> 不额外下发提示音
	listenRaw2 := []byte(`{"type":"listen","state":"start","mode":"manual"}`)
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     listenRaw2,
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateListening {
		time.Sleep(5 * time.Millisecond)
	}

	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: 3,
			typ:    turnEventASRFinal,
			text:   "",
		},
	})

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateReady {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateReady {
		t.Fatalf("expected StateReady after empty asr, got %v", sess.State())
	}

	// 路径 4: 会话正常关闭 -> 不额外下发提示音
	sess.Close()
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && sess.State() != StateClosed {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.State() != StateClosed {
		t.Fatalf("expected StateClosed, got %v", sess.State())
	}

	// 验证整个过程底层只有 1 条 hello 回执，无任何提示音或结束提示音
	messages := conn.getMessages()
	if len(messages) != 1 {
		t.Fatalf("expected exactly 1 message (hello), got %d: %+v", len(messages), messages)
	}
}
