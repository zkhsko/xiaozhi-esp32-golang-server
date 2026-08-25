package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
	"xiaozhi-esp32-golang-server/internal/config"
)

// waitForStream 等待 mock ASR Client 创建出最新的流。
func waitForStream(t *testing.T, client *mockSessionASRClient, timeout time.Duration) *mockSessionASRStream {
	return waitForStreamCount(t, client, 1, timeout)
}

// waitForStreamCount 等待 mock ASR Client 创建出至少 minCount 个流并返回最新流。
func waitForStreamCount(t *testing.T, client *mockSessionASRClient, minCount int, timeout time.Duration) *mockSessionASRStream {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client.StreamCount() >= minCount {
			return client.LastStream()
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ASR stream count >= %d", minCount)
	return nil
}

// extractSTTMessages 从捕获的 WebSocket 消息列表中过滤并反序列化所有 STT 消息。
func extractSTTMessages(messages []fakeWSMessage) []ServerSTTMessage {
	var sttMsgs []ServerSTTMessage
	for _, m := range messages {
		if m.typ != websocket.MessageText {
			continue
		}
		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(m.payload, &base); err != nil {
			continue
		}
		if base.Type == MessageTypeSTT {
			var stt ServerSTTMessage
			if err := json.Unmarshal(m.payload, &stt); err == nil {
				sttMsgs = append(sttMsgs, stt)
			}
		}
	}
	return sttMsgs
}

// TestAutoMode_FinalResult_STTAndProcessing 验证 auto 模式下收到非空最终识别结果时：
// 停止并清理 ASR 音频队列与 ASR 流，下发且仅下发一次 STT 消息，并进入 PROCESSING 状态。
func TestAutoMode_FinalResult_STTAndProcessing(t *testing.T) {
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

	// listen.start auto -> 进入 LISTENING 状态
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 发送一帧上行音频
	packet := encodeSineOpusPacket(t, 440.0)
	sess.PostClientAudio(packet)

	// ASR 最终非空识别结果就绪
	const expectedText = "今天北京天气怎么样"
	stream.SetResult(expectedText, nil)

	// 断言状态流转至 PROCESSING
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 断言 ASR 流已被关闭且会话 ASRQueue 已被清理置空
	if !stream.IsClosed() {
		t.Error("expected ASR stream to be closed after final result")
	}
	if sess.ASRQueue() != nil {
		t.Error("expected session ASRQueue to be nil after final result")
	}

	// 验证下发给客户端的消息
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
	if sttMsgs[0].Type != MessageTypeSTT {
		t.Errorf("expected STT type %q, got %q", MessageTypeSTT, sttMsgs[0].Type)
	}
}

// TestAutoMode_ProcessingState_ExtraAudioDiscarded 验证处于 PROCESSING 状态时：
// 设备继续上行的残留 Opus 音频被直接丢弃，不报错、不送入 ASR，且会话维持 PROCESSING 状态。
func TestAutoMode_ProcessingState_ExtraAudioDiscarded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)
	stream.SetResult("识别成功", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 记录进入 PROCESSING 时 ASR stream 接收的帧数
	initialFrames := stream.FrameCount()

	// 模拟设备在 PROCESSING 状态下继续发送 5 帧残留 Opus 数据
	for i := 0; i < 5; i++ {
		packet := encodeSineOpusPacket(t, float64(440+i*20))
		ok := sess.PostClientAudio(packet)
		if !ok {
			t.Fatalf("failed to post client audio in processing state at frame %d", i)
		}
	}

	time.Sleep(50 * time.Millisecond)

	// 验证状态依然为 PROCESSING，连接未断开
	if sess.State() != StateProcessing {
		t.Errorf("expected state to remain PROCESSING, got %v", sess.State())
	}

	// 验证残留音频未被送入已关闭的 ASR stream
	if stream.FrameCount() != initialFrames {
		t.Errorf("expected frame count in stream to remain %d, got %d", initialFrames, stream.FrameCount())
	}

	// 验证未产生额外的 STT 消息
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 1 {
		t.Errorf("expected exactly 1 STT message, got %d", len(sttMsgs))
	}
}

// TestAutoMode_ProcessingState_InvalidOpusPacketClosed 验证在 PROCESSING 状态下收到协议违规包（空包或超大包）仍然按照规范关闭连接。
func TestAutoMode_ProcessingState_InvalidOpusPacketClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING -> PROCESSING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)
	stream.SetResult("测试识别", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 发送 0 字节空包（协议错误）
	sess.PostClientAudio([]byte{})
	waitState(t, sess, StateClosed, 2*time.Second)
}

// TestAutoMode_EmptyResult_FallbackToReady 验证 ASR 返回空识别文本时：
// 不下发 STT 消息，安全清理资源并回退到 READY 状态，且后续问答轮次可正常开启。
func TestAutoMode_EmptyResult_FallbackToReady(t *testing.T) {
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

	// 第 1 轮：listen.start -> LISTENING -> 空识别结果
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream1 := waitForStream(t, asrClient, 2*time.Second)
	stream1.SetResult("", nil)

	// 断言回退到 READY 状态
	waitState(t, sess, StateReady, 2*time.Second)

	// 验证 ASR 流已关闭且 ASRQueue 为 nil
	if !stream1.IsClosed() {
		t.Error("expected first ASR stream to be closed after empty result")
	}
	if sess.ASRQueue() != nil {
		t.Error("expected session ASRQueue to be nil after empty result")
	}

	// 验证未发送任何 STT 消息
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 0 {
		t.Fatalf("expected 0 STT messages on empty result, got %d", len(sttMsgs))
	}

	// 第 2 轮：重新发送 listen.start -> 能够正常进入 LISTENING 并处理非空识别
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	// 等待第 2 个流创建
	deadline := time.Now().Add(2 * time.Second)
	for asrClient.StreamCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if asrClient.StreamCount() < 2 {
		t.Fatal("expected second ASR stream to be created")
	}
	stream2 := asrClient.LastStream()
	stream2.SetResult("第二轮识别成功", nil)

	waitState(t, sess, StateProcessing, 2*time.Second)

	sttMsgs2 := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs2) != 1 {
		t.Fatalf("expected exactly 1 STT message after turn 2, got %d", len(sttMsgs2))
	}
	if sttMsgs2[0].Text != "第二轮识别成功" {
		t.Errorf("expected STT text '第二轮识别成功', got %q", sttMsgs2[0].Text)
	}
}

// TestAutoMode_DuplicateFinalResult_Idempotent 验证同一代次下重复投递的最终识别结果被安全丢弃，保证幂等性。
func TestAutoMode_DuplicateFinalResult_Idempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	gen := sess.Generation()

	// 首次投递有效最终结果 -> 进入 PROCESSING
	sess.PostASRFinal(gen, "首次非空结果")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 重复投递相同代次的最终结果（文本不同或相同）
	sess.PostASRFinal(gen, "重复投递的结果")
	sess.PostASRFinal(gen, "")

	time.Sleep(50 * time.Millisecond)

	// 验证状态维持在 PROCESSING
	if sess.State() != StateProcessing {
		t.Errorf("expected state to remain PROCESSING, got %v", sess.State())
	}

	// 验证仅产生一条 STT 消息，且文本为首次结果
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 1 {
		t.Fatalf("expected exactly 1 STT message, got %d", len(sttMsgs))
	}
	if sttMsgs[0].Text != "首次非空结果" {
		t.Errorf("expected STT text '首次非空结果', got %q", sttMsgs[0].Text)
	}
}

// TestAutoMode_StaleOldGenerationResult_Discarded 验证旧代次迟到的 ASR 最终结果被安全丢弃，不污染新轮次状态。
func TestAutoMode_StaleOldGenerationResult_Discarded(t *testing.T) {
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

	// 第 1 轮：进入 LISTENING (gen 1)
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)
	gen1 := sess.Generation()

	// 客户端发送 abort 中断第 1 轮 -> 代次递增并重置为 READY (gen 2)
	sess.PostAbort("用户打断")
	waitState(t, sess, StateReady, 2*time.Second)
	waitGeneration(t, sess, gen1+1, 2*time.Second)

	// 第 1 轮的旧代次 ASR 结果迟到投递
	sess.PostASRFinal(gen1, "第1轮迟到文本")

	time.Sleep(50 * time.Millisecond)

	// 验证状态维持在 READY，未进入 PROCESSING
	if sess.State() != StateReady {
		t.Errorf("expected state to remain READY, got %v", sess.State())
	}

	// 验证未产生任何 STT 消息
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 0 {
		t.Errorf("expected 0 STT messages from stale generation, got %d", len(sttMsgs))
	}
}

// TestAutoMode_PartialResult_NoSTTUntilFinal 验证 ASR 处于中间流式阶段（未产生最终非空结果）时，不下发 STT 且维持 LISTENING。
func TestAutoMode_PartialResult_NoSTTUntilFinal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 写入多帧音频，此时 stream 未调用 SetResult，处于收音流式传输中
	for i := 0; i < 3; i++ {
		packet := encodeSineOpusPacket(t, float64(300+i*50))
		sess.PostClientAudio(packet)
	}

	time.Sleep(50 * time.Millisecond)

	// 验证状态仍处于 LISTENING
	if sess.State() != StateListening {
		t.Errorf("expected state to remain LISTENING during streaming, got %v", sess.State())
	}

	// 验证此时未下发 STT
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != 0 {
		t.Fatalf("expected 0 STT messages before final result, got %d", len(sttMsgs))
	}

	// 最终结果到达
	stream.SetResult("完整识别结果", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 验证此时下发了 STT
	sttMsgsAfter := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgsAfter) != 1 {
		t.Fatalf("expected exactly 1 STT message after final result, got %d", len(sttMsgsAfter))
	}
	if sttMsgsAfter[0].Text != "完整识别结果" {
		t.Errorf("expected STT text '完整识别结果', got %q", sttMsgsAfter[0].Text)
	}
}

// TestAutoMode_ASRStreamCreationFailure 验证 ASR 客户端创建流失败时记录错误并进入 CLOSED 状态。
func TestAutoMode_ASRStreamCreationFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	asrClient.createErr = errors.New("cannot connect to bailian asr")

	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// listen.start -> 创建 ASR 流失败 -> 进入 CLOSED 状态
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateClosed, 2*time.Second)
}

// TestAutoMode_ASRRecognitionError 验证 ASR 识别过程中返回非取消性错误时会话安全关闭。
func TestAutoMode_ASRRecognitionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 10)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	// 模拟 ASR 识别发生服务端异常
	stream.SetResult("", errors.New("upstream asr task failed"))

	waitState(t, sess, StateClosed, 2*time.Second)
}

// TestAutoMode_MultiTurnLifeCycle 验证连续多轮 auto 模式问答生命周期中的识别与状态流转。
func TestAutoMode_MultiTurnLifeCycle(t *testing.T) {
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
		// 发送 listen.start -> LISTENING
		sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
		waitState(t, sess, StateListening, 2*time.Second)

		stream := waitForStreamCount(t, asrClient, turn, 2*time.Second)
		if stream == nil {
			t.Fatalf("turn %d: expected ASR stream", turn)
		}

		// 发送音频
		packet := encodeSineOpusPacket(t, float64(400+turn*50))
		sess.PostClientAudio(packet)

		// 识别完成
		turnText := fmt.Sprintf("这是第%d轮用户问话", turn)
		stream.SetResult(turnText, nil)

		// 进入 PROCESSING
		waitState(t, sess, StateProcessing, 2*time.Second)

		gen := sess.Generation()

		// 模拟 TTS 播报开始 -> SPEAKING
		sess.PostTTSStarted(gen)
		waitState(t, sess, StateSpeaking, 2*time.Second)

		// 模拟轮次结束 -> 回到 READY
		sess.PostTurnFinished(gen)
		waitState(t, sess, StateReady, 2*time.Second)
	}

	// 验证总共产生了 3 条 STT 消息，内容按序对应
	sttMsgs := extractSTTMessages(fakeConn.Messages())
	if len(sttMsgs) != totalTurns {
		t.Fatalf("expected %d STT messages across turns, got %d", totalTurns, len(sttMsgs))
	}
	for i := 0; i < totalTurns; i++ {
		expectedText := fmt.Sprintf("这是第%d轮用户问话", i+1)
		if sttMsgs[i].Text != expectedText {
			t.Errorf("turn %d expected text %q, got %q", i+1, expectedText, sttMsgs[i].Text)
		}
	}
}

// TestAutoMode_ConcurrentAudioAndASRFinal_Race 验证高并发音频上行与最终识别完成交织时的竞态安全。
func TestAutoMode_ConcurrentAudioAndASRFinal_Race(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithASR(ctx, asrClient, 100)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY -> LISTENING
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	stream := waitForStream(t, asrClient, 2*time.Second)

	var wg sync.WaitGroup
	const workerCount = 5
	const framesPerWorker = 30

	// 启动多个并发音频发送协程
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < framesPerWorker; i++ {
				packet := encodeSineOpusPacket(t, float64(300+workerID*20))
				sess.PostClientAudio(packet)
				time.Sleep(1 * time.Millisecond)
			}
		}(w)
	}

	// 启动并发状态与代次查询协程
	for q := 0; q < 3; q++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = sess.State()
				_ = sess.Generation()
				_ = sess.ASRQueue()
				_ = sess.SessionID()
				time.Sleep(500 * time.Microsecond)
			}
		}()
	}

	// 短暂延迟后触发最终识别结果
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		stream.SetResult("并发测试最终识别文本", nil)
	}()

	wg.Wait()

	waitState(t, sess, StateProcessing, 2*time.Second)

	if sess.State() != StateProcessing {
		t.Errorf("expected final state to be PROCESSING, got %v", sess.State())
	}
}

// createTestSessionWithPrompt 创建支持提示音开关的测试会话。
func createTestSessionWithPrompt(ctx context.Context, asrClient ai.ASRClient, promptEnabled bool) (*Session, *fakeWSConn, *Writer) {
	fakeConn := &fakeWSConn{}
	writer := NewWriter(ctx, fakeConn, 100, nil)
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:        5 * time.Second,
			MaxOpusPacketBytes:  1024,
			ListenPromptEnabled: promptEnabled,
		},
	}
	info := &ClientHeaderInfo{
		DeviceID:     "test-device",
		ClientID:     "test-client",
		SerialNumber: "test-sn",
	}
	fastTickerFactory := func(d time.Duration) Ticker {
		return &realTicker{ticker: time.NewTicker(1 * time.Millisecond)}
	}
	sess := NewSession(ctx, Options{
		Writer:        writer,
		ClientInfo:    info,
		Config:        cfg,
		ASRClient:     asrClient,
		TickerFactory: fastTickerFactory,
	})
	return sess, fakeConn, writer
}

// TestAutoMode_PromptSound_FirstListenStartFlow 验证首次 auto 模式 listen.start 下发独立提示音并在设备二次请求后进入 LISTENING。
func TestAutoMode_PromptSound_FirstListenStartFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithPrompt(ctx, asrClient, true)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 1. 发送首条 listen.start(mode=auto) -> 进入 SPEAKING 播放独立提示音
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 提示音快速播放完毕后，自动迁移回 READY
	waitState(t, sess, StateReady, 2*time.Second)

	// 断言在提示音期间未创建 ASR 流
	if asrClient.StreamCount() != 0 {
		t.Fatalf("expected 0 ASR stream created during prompt, got %d", asrClient.StreamCount())
	}

	// 验证下发的消息：首条 hello, 第二条 tts.start, 接着 N 个二进制包, 随后 tts.stop
	msgs := fakeConn.Messages()
	var textMsgs []string
	var binaryCount int
	for _, m := range msgs {
		if m.typ == websocket.MessageBinary {
			binaryCount++
		} else {
			textMsgs = append(textMsgs, string(m.payload))
		}
	}

	expectedPromptFrames, _ := audio.GetListenPromptFrameCount()
	if binaryCount != expectedPromptFrames || binaryCount == 0 {
		t.Errorf("expected %d prompt opus packets, got %d", expectedPromptFrames, binaryCount)
	}

	// 验证包含 tts.start 和 tts.stop
	var hasTTSStart, hasTTSStop bool
	for _, txt := range textMsgs {
		var base struct {
			Type  string `json:"type"`
			State string `json:"state"`
		}
		if err := json.Unmarshal([]byte(txt), &base); err == nil && base.Type == "tts" {
			if base.State == "start" {
				hasTTSStart = true
			} else if base.State == "stop" {
				hasTTSStop = true
			}
		}
	}
	if !hasTTSStart || !hasTTSStop {
		t.Errorf("expected both tts.start and tts.stop, got start=%v, stop=%v", hasTTSStart, hasTTSStop)
	}

	// 2. 模拟设备播放完提示音后发送第二条 listen.start(mode=auto) -> 直接进入 LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	// 此时正式创建了 1 个 ASR 流
	stream := waitForStream(t, asrClient, 2*time.Second)
	if stream == nil {
		t.Fatal("expected ASR stream to be created after second listen.start")
	}

	// 发送正常麦克风语音 -> ASR 返回结果 -> 进入 PROCESSING
	packet := encodeSineOpusPacket(t, 440.0)
	sess.PostClientAudio(packet)
	stream.SetResult("你好小智", nil)
	waitState(t, sess, StateProcessing, 2*time.Second)
}

// TestAutoMode_PromptSound_DiscardUplinkAudioDuringPrompt 验证在提示音播放期间上行音频被安全丢弃。
func TestAutoMode_PromptSound_DiscardUplinkAudioDuringPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, _, _ := createTestSessionWithPrompt(ctx, asrClient, true)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateSpeaking, 2*time.Second)
	waitState(t, sess, StateReady, 2*time.Second)

	// 在 READY 状态下发送上行残留音频包
	for i := 0; i < 5; i++ {
		packet := encodeSineOpusPacket(t, 440.0)
		sess.PostClientAudio(packet)
	}
	time.Sleep(20 * time.Millisecond)

	// 会话未崩溃且保持 READY 状态
	if sess.State() != StateReady {
		t.Fatalf("expected state to remain READY, got %v", sess.State())
	}
	if asrClient.StreamCount() != 0 {
		t.Fatalf("expected 0 ASR streams, got %d", asrClient.StreamCount())
	}
}

// TestAutoMode_PromptSound_AppendToTurnTail 验证问答结束时尾部追加提示音，并在下一轮无缝进入 LISTENING。
func TestAutoMode_PromptSound_AppendToTurnTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	sess, fakeConn, _ := createTestSessionWithPrompt(ctx, asrClient, true)
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// 首次 listen.start(auto) -> 提示音 -> READY -> 第二次 listen.start(auto) -> LISTENING
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateSpeaking, 2*time.Second)
	waitState(t, sess, StateReady, 2*time.Second)

	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	gen := sess.Generation()
	// ASR 识别结果 -> PROCESSING
	sess.PostASRFinal(gen, "测试问题")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 模拟播报开始 -> SPEAKING
	sess.PostTTSStarted(gen)
	waitState(t, sess, StateSpeaking, 2*time.Second)

	sess.PostTurnFinished(gen, "用户问题", "助手回答")

	// 轮次结束回到 READY
	waitState(t, sess, StateReady, 2*time.Second)

	// 收到设备的下一轮 listen.start(auto) -> 直接进入 LISTENING，不再播放独立提示音
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateListening, 2*time.Second)

	_ = fakeConn
}

// TestAutoMode_PromptSound_AbortDuringPrompting 验证在播放提示音时收到 abort 能够安全复位到 READY。
func TestAutoMode_PromptSound_AbortDuringPrompting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	asrClient := newMockSessionASRClient()
	// 使用带阻塞的 ticker 控制 prompt 停留
	manualTick := newManualTicker()
	fakeConn := &fakeWSConn{}
	writer := NewWriter(ctx, fakeConn, 100, nil)
	cfg := &config.Config{
		Session: config.SessionConfig{
			HelloTimeout:        5 * time.Second,
			MaxOpusPacketBytes:  1024,
			ListenPromptEnabled: true,
		},
	}
	sess := NewSession(ctx, Options{
		Writer:     writer,
		ClientInfo: &ClientHeaderInfo{DeviceID: "d", ClientID: "c", SerialNumber: "s"},
		Config:     cfg,
		ASRClient:  asrClient,
		TickerFactory: func(d time.Duration) Ticker {
			return manualTick
		},
	})
	go func() { _ = sess.Run() }()
	defer sess.Close()

	// 握手 -> READY
	validHello := []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHello})
	waitState(t, sess, StateReady, 2*time.Second)

	// listen.start -> 进入 SPEAKING 播放提示音
	sess.PostClientText(&ClientMessage{Kind: KindListenStart, Mode: ListenModeAuto})
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 中途 abort
	sess.PostAbort("user_cancel")
	waitState(t, sess, StateReady, 2*time.Second)

	// 验证会话回到 READY 且代次递增
	if sess.Generation() != 2 {
		t.Errorf("expected generation 2 after abort, got %d", sess.Generation())
	}
}
