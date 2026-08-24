package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
)

// waitForStreamFinish 等待 mock ASR Stream 收到 Finish 调用。
func waitForStreamFinish(t *testing.T, stream *mockSessionASRStream, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if stream.IsFinished() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for ASR stream finish")
}

// createTestSessionWithCustomDuration 创建包含自定义收音时长的测试会话。
func createTestSessionWithCustomDuration(ctx context.Context, asrClient *mockSessionASRClient, duration time.Duration) (*Session, *fakeWSConn, *Writer) {
	fakeConn := &fakeWSConn{}
	writer := NewWriter(ctx, fakeConn, 100, nil)
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:         5 * time.Second,
			MaxOpusPacketBytes:   1024,
			MaxListeningDuration: duration,
			ASRPCMQueueCapacity:  50,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "test-device",
		ClientID:     "test-client",
		SerialNumber: "test-sn",
	}
	sess := NewSession(ctx, Options{
		Writer:     writer,
		ClientInfo: info,
		Config:     cfg,
		ASRClient:  asrClient,
	})
	return sess, fakeConn, writer
}

// createTestSessionWithASRResultTimeout 创建包含自定义 ASR 识别结果等待超时时限的测试会话。
func createTestSessionWithASRResultTimeout(ctx context.Context, asrClient *mockSessionASRClient, resultTimeout time.Duration) (*Session, *fakeWSConn, *Writer) {
	fakeConn := &fakeWSConn{}
	writer := NewWriter(ctx, fakeConn, 100, nil)
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:         5 * time.Second,
			MaxOpusPacketBytes:   1024,
			MaxListeningDuration: 30 * time.Second,
			ASRResultTimeout:     resultTimeout,
			ASRPCMQueueCapacity:  50,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "test-device",
		ClientID:     "test-client",
		SerialNumber: "test-sn",
	}
	sess := NewSession(ctx, Options{
		Writer:     writer,
		ClientInfo: info,
		Config:     cfg,
		ASRClient:  asrClient,
	})
	return sess, fakeConn, writer
}

// TestManualMode_NormalFlow_StopTriggersFinishAndSTT 验证 manual 模式下：
// 收到 listen.stop 后正常触发 ASR 流 Finish，ASR 返回最终识别文本后下发 STT 并流转至 PROCESSING 状态。
func TestManualMode_NormalFlow_StopTriggersFinishAndSTT(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// listen.start manual -> 进入 LISTENING 状态
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	if sess.Mode() != ListenModeManual {
		t.Fatalf("expected session mode %q, got %q", ListenModeManual, sess.Mode())
	}

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 发送 2 帧上行音频
	for i := 0; i < 2; i++ {
		packet := encodeSineOpusPacket(t, float64(440+i*50))
		sess.PostClientAudio(packet)
	}

	// 等待音频被消费写入流
	deadline := time.Now().Add(2 * time.Second)
	for stream.FrameCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stream.FrameCount() != 2 {
		t.Fatalf("expected 2 frames in stream, got %d", stream.FrameCount())
	}

	// 客户端发送 listen.stop
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})

	// 验证 ASR 流收到了 Finish 调用
	waitForStreamFinish(t, stream, 2*time.Second)

	// ASR 产生最终文本
	const expectedText = "手动按键识别测试"
	stream.SetResult(expectedText, nil)

	// 断言状态流转至 PROCESSING
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 断言 ASR 流已被关闭且 ASRQueue 为 nil
	if !stream.IsClosed() {
		t.Error("expected ASR stream to be closed after final result")
	}
	if sess.ASRQueue() != nil {
		t.Error("expected session ASRQueue to be nil after final result")
	}

	// 验证下发 STT 消息
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 1 {
		t.Fatalf("expected exactly 1 STT message, got %d", len(sttMsgs))
	}
	if sttMsgs[0].SessionID != sess.SessionID() {
		t.Errorf("expected STT session_id %q, got %q", sess.SessionID(), sttMsgs[0].SessionID)
	}
	if sttMsgs[0].Text != expectedText {
		t.Errorf("expected STT text %q, got %q", expectedText, sttMsgs[0].Text)
	}
}

// TestManualMode_DuplicateListenStop_Ignored 验证在 LISTENING 状态下收到重复的 listen.stop 安全忽略且不破坏会话。
func TestManualMode_DuplicateListenStop_Ignored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING (manual)
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 首次发送 listen.stop
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream, 2*time.Second)

	// 重复发送多次 listen.stop
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})

	time.Sleep(50 * time.Millisecond)

	// 状态仍应为 LISTENING（等待 ASR 结果），未崩溃、未关闭
	if sess.State() != StateListening {
		t.Errorf("expected state to remain LISTENING, got %v", sess.State())
	}

	// 触发 ASR 最终结果
	stream.SetResult("重复stop测试结果", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)

	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 1 {
		t.Fatalf("expected exactly 1 STT message, got %d", len(sttMsgs))
	}
	if sttMsgs[0].Text != "重复stop测试结果" {
		t.Errorf("expected STT text %q, got %q", "重复stop测试结果", sttMsgs[0].Text)
	}
}

// TestManualMode_ListenStopInInvalidStates_Ignored 验证在非 LISTENING 状态或 auto 模式下收到 listen.stop 被安全忽略。
func TestManualMode_ListenStopInInvalidStates_Ignored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 1. 在 CONNECTED 状态发送 listen.stop（握手前文本消息会触发错误，这里直接测试状态机安全性）
	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 2. 在 READY 状态发送 listen.stop -> 维持 READY
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	time.Sleep(30 * time.Millisecond)
	if sess.State() != StateReady {
		t.Errorf("expected state to remain READY, got %v", sess.State())
	}

	// 3. 在 auto 模式的 LISTENING 状态发送 listen.stop -> 忽略且维持 LISTENING，不触发 finish
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	autoStream := waitForStream(t, asrClient, 2*time.Second)
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	time.Sleep(30 * time.Millisecond)

	if sess.State() != StateListening {
		t.Errorf("expected state to remain LISTENING in auto mode, got %v", sess.State())
	}
	if autoStream.IsFinished() {
		t.Error("expected auto stream not to be finished on listen.stop")
	}

	// 触发 auto 模式识别结果 -> 进入 PROCESSING
	autoStream.SetResult("auto模式识别", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 4. 在 PROCESSING 状态发送 listen.stop -> 维持 PROCESSING
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	time.Sleep(30 * time.Millisecond)
	if sess.State() != StateProcessing {
		t.Errorf("expected state to remain PROCESSING, got %v", sess.State())
	}

	// 进入 SPEAKING
	gen := sess.Generation()
	sess.PostTTSStarted(gen)
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 5. 在 SPEAKING 状态发送 listen.stop -> 维持 SPEAKING
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	time.Sleep(30 * time.Millisecond)
	if sess.State() != StateSpeaking {
		t.Errorf("expected state to remain SPEAKING, got %v", sess.State())
	}
}

// TestManualMode_PostStopExtraAudio_Discarded 验证 manual 模式收到 listen.stop 之后到达的多余音频被安全丢弃。
func TestManualMode_PostStopExtraAudio_Discarded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING (manual)
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 发送 1 帧音频
	packet := encodeSineOpusPacket(t, 440.0)
	sess.PostClientAudio(packet)

	// 等待第 1 帧被消费
	deadline := time.Now().Add(2 * time.Second)
	for stream.FrameCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stream.FrameCount() != 1 {
		t.Fatalf("expected 1 frame in stream, got %d", stream.FrameCount())
	}

	// 发送 listen.stop
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream, 2*time.Second)

	// 在 listen.stop 之后继续发送 3 帧残留音频
	for i := 0; i < 3; i++ {
		extraPacket := encodeSineOpusPacket(t, float64(500+i*20))
		sess.PostClientAudio(extraPacket)
	}

	time.Sleep(50 * time.Millisecond)

	// 验证 ASR 流中的帧数仍为 1，多余音频已被丢弃
	if stream.FrameCount() != 1 {
		t.Errorf("expected stream frame count to remain 1, got %d", stream.FrameCount())
	}

	// 识别结果就绪 -> PROCESSING
	stream.SetResult("收音完成文本", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)
}

// TestSession_ListeningTimeout_ClosesConnectionAndCleansUp 验证单次收音超时未结束时，
// 自动投递超时事件，取消本轮 context，清理 ASR 资源并关闭连接。
func TestSession_ListeningTimeout_ClosesConnectionAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	// 设置极短的收音时限 60ms 进行确定性验证
	shortDuration := 60 * time.Millisecond
	sess, _, _ := createTestSessionWithCustomDuration(ctx, asrClient, shortDuration)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// listen.start manual -> LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 不发送 listen.stop，也不触发 ASR 识别结果，等待收音定时器触发超时
	waitState(t, sess, StateClosed, 2*time.Second)

	// 验证 ASR 流已关闭且 ASRQueue 为 nil
	if !stream.IsClosed() {
		t.Error("expected ASR stream to be closed on listening timeout")
	}
	if sess.ASRQueue() != nil {
		t.Error("expected session ASRQueue to be nil on listening timeout")
	}

	// 验证会话 Done 通道已关闭
	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session Done channel did not close after timeout")
	}
}

// TestSession_ListeningTimeoutCancelledOnNormalStop 验证在收音时限到期前收到 listen.stop，
// 收音定时器被及时取消，不会因为后续 ASR 耗时而误触发超时断开。
func TestSession_ListeningTimeoutCancelledOnNormalStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	// 设置收音时限 150ms
	listeningLimit := 150 * time.Millisecond
	sess, fakeConn, _ := createTestSessionWithCustomDuration(ctx, asrClient, listeningLimit)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 在 20ms 内迅速发送 listen.stop（远早于 150ms 超时）
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream, 2*time.Second)

	// 模拟 ASR 耗时 200ms 后才返回最终识别结果（耗时超过了原 150ms 收音上限）
	time.Sleep(200 * time.Millisecond)

	// 此时会话仍然存活在 LISTENING 等待 ASR 结果，未被超时关闭
	if sess.State() != StateListening {
		t.Fatalf("expected state to remain LISTENING, got %v", sess.State())
	}

	// ASR 最终返回结果
	stream.SetResult("延时返回但正常成功", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)

	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 1 {
		t.Fatalf("expected exactly 1 STT message, got %d", len(sttMsgs))
	}
	if sttMsgs[0].Text != "延时返回但正常成功" {
		t.Errorf("expected STT text %q, got %q", "延时返回但正常成功", sttMsgs[0].Text)
	}
}

// TestSession_ListeningTimeoutCancelledOnAbort 验证在收音时限到期前收到 abort，
// 定时器被取消，会话回到 READY 状态且不会被旧收音定时器触发超时断开。
func TestSession_ListeningTimeoutCancelledOnAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	listeningLimit := 120 * time.Millisecond
	sess, _, _ := createTestSessionWithCustomDuration(ctx, asrClient, listeningLimit)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	// 迅速发送 abort
	sess.PostAbort("用户主动取消")
	waitState(t, sess, StateReady, 2*time.Second)

	// 等待超过 120ms 的超时时限
	time.Sleep(160 * time.Millisecond)

	// 验证状态维持在 READY，未被超时关闭
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY after timer duration, got %v", sess.State())
	}
}

// TestManualMode_EmptyResult_FallbackToReady 验证 manual 模式下 ASR 返回空识别文本时：
// 不下发 STT，安全清理资源并回退到 READY 状态，后续问答轮次可正常开启。
func TestManualMode_EmptyResult_FallbackToReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 第 1 轮 manual：listen.start -> listen.stop -> 空结果
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream1 := waitForStream(t, asrClient, 2*time.Second)
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream1, 2*time.Second)

	stream1.SetResult("", nil)

	// 回退到 READY
	waitState(t, sess, StateReady, 2*time.Second)

	// 验证未下发 STT
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 0 {
		t.Fatalf("expected 0 STT messages on empty result, got %d", len(sttMsgs))
	}

	// 第 2 轮 manual：正常识别
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for asrClient.StreamCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if asrClient.StreamCount() < 2 {
		t.Fatal("expected second stream to be created")
	}

	stream2 := asrClient.LastStream()
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream2, 2*time.Second)

	stream2.SetResult("第二轮手动识别文本", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)

	sttMsgs2 := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs2) != 1 {
		t.Fatalf("expected 1 STT message, got %d", len(sttMsgs2))
	}
	if sttMsgs2[0].Text != "第二轮手动识别文本" {
		t.Errorf("expected text %q, got %q", "第二轮手动识别文本", sttMsgs2[0].Text)
	}
}

// TestManualMode_MultiTurnLifeCycle 验证连续多轮 manual 模式问答生命周期的完整流转。
func TestManualMode_MultiTurnLifeCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	const totalTurns = 3
	for turn := 1; turn <= totalTurns; turn++ {
		// listen.start manual -> LISTENING
		sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
		waitState(t, sess, StateListening, 2*time.Second)

		var stream *mockSessionASRStream
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if asrClient.StreamCount() >= turn {
				stream = asrClient.LastStream()
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if stream == nil {
			t.Fatalf("turn %d: expected ASR stream", turn)
		}

		// 发送音频
		packet := encodeSineOpusPacket(t, float64(350+turn*50))
		sess.PostClientAudio(packet)

		// listen.stop -> finish
		sess.PostClientText(&ClientMessage{Kind: KindListenStop})
		waitForStreamFinish(t, stream, 2*time.Second)

		// 识别完成
		turnText := fmt.Sprintf("第%d次手动提问", turn)
		stream.SetResult(turnText, nil)

		// PROCESSING
		waitState(t, sess, StateProcessing, 2*time.Second)
		gen := sess.Generation()

		// SPEAKING
		sess.PostTTSStarted(gen)
		waitState(t, sess, StateSpeaking, 2*time.Second)

		// 轮次结束 -> READY
		sess.PostTurnFinished(gen)
		waitState(t, sess, StateReady, 2*time.Second)
	}

	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != totalTurns {
		t.Fatalf("expected %d STT messages across turns, got %d", totalTurns, len(sttMsgs))
	}
	for i := 0; i < totalTurns; i++ {
		expectedText := fmt.Sprintf("第%d次手动提问", i+1)
		if sttMsgs[i].Text != expectedText {
			t.Errorf("turn %d expected text %q, got %q", i+1, expectedText, sttMsgs[i].Text)
		}
	}
}

// TestManualMode_ConcurrentStopAndAudio_Race 验证并发发送音频与 listen.stop、查询状态时的竞态安全性。
func TestManualMode_ConcurrentStopAndAudio_Race(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 100)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING (manual)
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	var wg sync.WaitGroup
	const workerCount = 4
	const framesPerWorker = 20

	// 启动多个并发音频发送协程
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < framesPerWorker; i++ {
				packet := encodeSineOpusPacket(t, float64(300+workerID*20))
				sess.PostClientAudio(packet)
				time.Sleep(500 * time.Microsecond)
			}
		}(w)
	}

	// 并发查询协程
	for q := 0; q < 3; q++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				_ = sess.State()
				_ = sess.Generation()
				_ = sess.ASRQueue()
				_ = sess.Mode()
				_ = sess.SessionID()
				time.Sleep(500 * time.Microsecond)
			}
		}()
	}

	// 并发发送 listen.stop
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		sess.PostClientText(&ClientMessage{Kind: KindListenStop})
		// 重复发送一次
		time.Sleep(2 * time.Millisecond)
		sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	}()

	// 触发 ASR 最终结果
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(15 * time.Millisecond)
		stream.SetResult("并发manual测试识别文本", nil)
	}()

	wg.Wait()

	waitState(t, sess, StateProcessing, 2*time.Second)

	if sess.State() != StateProcessing {
		t.Errorf("expected final state to be PROCESSING, got %v", sess.State())
	}
}

// TestManualMode_MidSpeechSentenceEnd_DoesNotPrematurelyCutOff 验证 manual 模式按住说话过程中，
// 即使百炼上游在用户说话中途停顿产生中间分句 VAD 信号，会话也不会提前截断或切入 PROCESSING，
// 只有在用户松开按键发送 listen.stop 触发 Finish 后，才会等待百炼最终整句识别结果并转入 PROCESSING。
func TestManualMode_MidSpeechSentenceEnd_DoesNotPrematurelyCutOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// listen.start manual -> LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 1. 用户按住按键发送第一句音频
	for i := 0; i < 2; i++ {
		sess.PostClientAudio(encodeSineOpusPacket(t, float64(440+i*20)))
	}

	deadline := time.Now().Add(2 * time.Second)
	for stream.FrameCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stream.FrameCount() != 2 {
		t.Fatalf("expected 2 frames in stream, got %d", stream.FrameCount())
	}

	// 2. 模拟百炼上游在用户说话中途停顿时就绪了中间分句结果（模拟中间 VAD 分句）
	// 注意：此时用户尚未发送 listen.stop（按键仍处于按下状态）
	stream.SetResult("第一句话分句结果", nil)

	// 稍作等待，验证会话依然稳定处于 LISTENING 状态，绝不提前切入 PROCESSING，且未下发 STT
	time.Sleep(50 * time.Millisecond)
	if sess.State() != StateListening {
		t.Fatalf("expected state to remain LISTENING during manual speech hold, got %v", sess.State())
	}
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 0 {
		t.Fatalf("expected 0 STT messages before listen.stop, got %d", len(sttMsgs))
	}

	// 3. 用户继续发送第二句音频
	for i := 0; i < 2; i++ {
		sess.PostClientAudio(encodeSineOpusPacket(t, float64(500+i*20)))
	}

	deadline = time.Now().Add(2 * time.Second)
	for stream.FrameCount() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if stream.FrameCount() != 4 {
		t.Fatalf("expected 4 frames in stream, got %d", stream.FrameCount())
	}

	// 重置 mock stream 的最终完整结果
	const expectedFullText = "第一句话分句结果。第二句话完整识别。"
	stream.SetResult(expectedFullText, nil)

	// 4. 用户松开按键，发送 listen.stop
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream, 2*time.Second)

	// 断言会话在收到 listen.stop 后成功获取最终完整文本并流转至 PROCESSING
	waitState(t, sess, StateProcessing, 2*time.Second)

	sttMsgs = extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 1 {
		t.Fatalf("expected exactly 1 STT message after listen.stop, got %d", len(sttMsgs))
	}
	if sttMsgs[0].Text != expectedFullText {
		t.Errorf("expected STT text %q, got %q", expectedFullText, sttMsgs[0].Text)
	}
}

// TestManualMode_PostStopASRHang_TimeoutClosesConnection 验证 manual 模式收到 listen.stop 并触发 finishASR 后，
// 若百炼上游假死挂起未返回识别结果，识别超时定时器到期后自动触发超时保护，安全关闭连接并清理 ASR 资源。
func TestManualMode_PostStopASRHang_TimeoutClosesConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	// 设置极短的 ASR 识别等待时限 80ms 进行确定性验证
	shortTimeout := 80 * time.Millisecond
	sess, _, _ := createTestSessionWithASRResultTimeout(ctx, asrClient, shortTimeout)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING (manual)
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 发送 1 帧音频
	sess.PostClientAudio(encodeSineOpusPacket(t, 440.0))

	// 发送 listen.stop -> 触发 finishASR
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream, 2*time.Second)

	// 模拟百炼上游假死：不调用 stream.SetResult，让其无期限阻塞
	// 等待识别超时定时器到期 -> 会话应自动迁移至 CLOSED 状态并断开
	waitState(t, sess, StateClosed, 2*time.Second)

	// 验证 ASR 流已关闭且 ASRQueue 为 nil
	if !stream.IsClosed() {
		t.Error("expected ASR stream to be closed on ASR result timeout")
	}
	if sess.ASRQueue() != nil {
		t.Error("expected session ASRQueue to be nil on ASR result timeout")
	}

	// 验证会话 Done 通道已关闭
	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session Done channel did not close after ASR result timeout")
	}
}

// TestManualMode_PostStopAbort_CancelsASRResultTimeout 验证 manual 模式收到 listen.stop 之后、
// ASR 结果返回之前的等待期间，若收到 abort 打断指令，识别结果等待定时器被安全取消，
// 会话重置回 READY 状态且不会被超时的旧定时器断开连接。
func TestManualMode_PostStopAbort_CancelsASRResultTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	resultTimeout := 120 * time.Millisecond
	sess, _, _ := createTestSessionWithASRResultTimeout(ctx, asrClient, resultTimeout)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING (manual)
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 发送 listen.stop -> 启动识别超时定时器
	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream, 2*time.Second)

	// 迅速发送 abort
	sess.PostAbort("用户在等待识别时取消")
	waitState(t, sess, StateReady, 2*time.Second)

	// 等待超过 120ms 的超时时限
	time.Sleep(160 * time.Millisecond)

	// 验证状态依然稳定保持在 READY，未被旧的 ASR 超时定时器触发关闭
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY after ASR timeout duration, got %v", sess.State())
	}
}

// TestManualMode_PostStopASRFailure_ClosesConnection 验证 manual 模式收到 listen.stop 后，
// 若百炼上游返回错误，会话安全清理资源并关闭连接。
func TestManualMode_PostStopASRFailure_ClosesConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING (manual)
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeManual})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStop})
	waitForStreamFinish(t, stream, 2*time.Second)

	// 模拟上游返回识别错误
	stream.SetResult("", errors.New("upstream asr recognition failure"))

	// 断言会话迁移至 CLOSED
	waitState(t, sess, StateClosed, 2*time.Second)

	if !stream.IsClosed() {
		t.Error("expected ASR stream to be closed after error")
	}
	if sess.ASRQueue() != nil {
		t.Error("expected session ASRQueue to be nil after error")
	}
}
