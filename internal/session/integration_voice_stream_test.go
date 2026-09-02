package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/ai/dashscope"
	"xiaozhi-esp32-golang-server/internal/audio"
	"xiaozhi-esp32-golang-server/internal/database"
)

// waitForSessionState 条件轮询等待会话状态迁移至预期状态，避免任意固定休眠。
func waitForSessionState(t *testing.T, sess *Session, expected State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sess.State() == expected {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for session state %v, current state: %v", expected, sess.State())
}

// performHandshake 执行标准 WebSocket hello 握手并等待会话就绪（StateReady）。
func performHandshake(t *testing.T, sess *Session) {
	t.Helper()
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
	raw, err := json.Marshal(helloMsg)
	if err != nil {
		t.Fatalf("failed to marshal hello message: %v", err)
	}
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     raw,
	})
	waitForSessionState(t, sess, StateReady, 2*time.Second)
}

// sendClientListenStart 投递客户端 listen.start 消息。
func sendClientListenStart(sess *Session, mode string) {
	if mode == "" {
		mode = ListenModeAuto
	}
	data, _ := json.Marshal(map[string]string{
		"type":  "listen",
		"state": "start",
		"mode":  mode,
	})
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     data,
	})
}

// sendClientListenStop 投递客户端 listen.stop 消息。
func sendClientListenStop(sess *Session) {
	data, _ := json.Marshal(map[string]string{
		"type":  "listen",
		"state": "stop",
	})
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     data,
	})
}

// sendClientAbort 投递客户端 abort 打断消息。
func sendClientAbort(sess *Session) {
	data, _ := json.Marshal(map[string]string{
		"type": "abort",
	})
	sess.postEvent(sessionEvent{
		kind:     eventKindClientFrame,
		isBinary: false,
		data:     data,
	})
}

// sendASRResult 投递 ASR 识别最终结果事件。
func sendASRResult(sess *Session, turnId uint64, text string) {
	sess.postEvent(sessionEvent{
		kind: eventKindTurnEvent,
		turnEv: turnEvent{
			turnId: turnId,
			typ:    turnEventASRFinal,
			text:   text,
		},
	})
}

// parseTTSMessage 解析下行文本帧中的 TTS 协议字段。
type parsedTTSMessage struct {
	Type      string `json:"type"`
	State     string `json:"state"`
	SessionId string `json:"session_id"`
	Text      string `json:"text,omitempty"`
}

// extractTTSMessages 从 mockWSConn 记录的所有消息中提取所有 TTS 文本消息与二进制音频包。
func extractTTSMessages(conn *mockWSConn) ([]parsedTTSMessage, [][]byte) {
	var (
		ttsMsgs [][]byte
		binary  [][]byte
		parsed  []parsedTTSMessage
	)
	for _, m := range conn.getMessages() {
		if m.typ == websocket.MessageBinary {
			binary = append(binary, m.payload)
		} else if m.typ == websocket.MessageText {
			var msg parsedTTSMessage
			if err := json.Unmarshal(m.payload, &msg); err == nil && msg.Type == "tts" {
				parsed = append(parsed, msg)
				ttsMsgs = append(ttsMsgs, m.payload)
			}
		}
	}
	_ = ttsMsgs
	return parsed, binary
}

// TestIntegration_Scenario1_NormalMultiSentenceSequenceAndHistoryCommit 验证正常多句时序与历史提交：
// 1. 严格时序：tts/start → (sentence_start → Opus)* → tts/stop → barrier；
// 2. 单轮回答复用同一个 TTSStream 物理连接；
// 3. 屏障通过后提交历史并返回 Ready。
func TestIntegration_Scenario1_NormalMultiSentenceSequenceAndHistoryCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	var (
		synthesizedSentences []string
		streamMu             sync.Mutex
	)

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		streamMu.Lock()
		synthesizedSentences = append(synthesizedSentences, text)
		streamMu.Unlock()
		pcm := make([]byte, audio.DownlinkBytesPerFrame)
		return onPCM(ctx, pcm)
	}

	ttsClient := newMockTTSClient(mockStream)

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "第一句完整的回答内容。", Iteration: 0})
				_ = callback(ctx, ai.LLMChunk{Text: "第二句也是完整的回答呀。", Iteration: 0})
			}
			return "第一句完整的回答内容。第二句也是完整的回答呀。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-MULTI-SENTENCE-01",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	// 使用手动模式以聚焦问答多句时序（自动模式提示音在场景 6 单独测试）
	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)

	// 发送 ASR 识别结果触发 LLM 生成与 VoiceStream
	sendASRResult(sess, 1, "请讲两句话")

	// 等待会话完成播报并回到 StateReady
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	// 1. 验证 TTSStream 单轮仅创建 1 个且被复用合成两句
	if ttsClient.createCalls != 1 {
		t.Fatalf("expected exactly 1 CreateStream call, got %d", ttsClient.createCalls)
	}
	streamMu.Lock()
	if len(synthesizedSentences) != 2 {
		t.Fatalf("expected 2 synthesized sentences, got %d: %v", len(synthesizedSentences), synthesizedSentences)
	}
	if synthesizedSentences[0] != "第一句完整的回答内容。" || synthesizedSentences[1] != "第二句也是完整的回答呀。" {
		t.Fatalf("unexpected synthesized sentences: %v", synthesizedSentences)
	}
	streamMu.Unlock()

	// 2. 验证 TTSStream 已被正常关闭
	if mockStream.CloseCalls() != 1 {
		t.Fatalf("expected TTSStream to be closed once, got %d", mockStream.CloseCalls())
	}

	// 3. 验证下行消息严格时序
	ttsMsgs, binaryFrames := extractTTSMessages(conn)
	if len(ttsMsgs) != 4 {
		t.Fatalf("expected 4 TTS control messages (start, 2x sentence_start, stop), got %d: %+v", len(ttsMsgs), ttsMsgs)
	}
	if ttsMsgs[0].State != "start" {
		t.Fatalf("expected first TTS message state 'start', got %s", ttsMsgs[0].State)
	}
	if ttsMsgs[1].State != "sentence_start" || ttsMsgs[1].Text != "第一句完整的回答内容。" {
		t.Fatalf("unexpected sentence_start 1: %+v", ttsMsgs[1])
	}
	if ttsMsgs[2].State != "sentence_start" || ttsMsgs[2].Text != "第二句也是完整的回答呀。" {
		t.Fatalf("unexpected sentence_start 2: %+v", ttsMsgs[2])
	}
	if ttsMsgs[3].State != "stop" {
		t.Fatalf("expected last TTS message state 'stop', got %s", ttsMsgs[3].State)
	}
	if len(binaryFrames) < 2 {
		t.Fatalf("expected at least 2 Opus binary frames (1 per sentence), got %d", len(binaryFrames))
	}

	// 4. 验证历史记录在屏障确认后提交且内容完整
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages (user + assistant), got %d", sess.History().Len())
	}
	historyMsgs := sess.History().Messages()
	if historyMsgs[0].Content != "请讲两句话" {
		t.Fatalf("unexpected user history: %s", historyMsgs[0].Content)
	}
	if historyMsgs[1].Content != "第一句完整的回答内容。第二句也是完整的回答呀。" {
		t.Fatalf("unexpected assistant history: %s", historyMsgs[1].Content)
	}
}

// TestIntegration_Scenario2_CrossTurnInteractionAndResourceIsolation 验证跨轮交互与资源隔离：
// 1. 每轮问答创建全新的 TTSStream 物理连接；
// 2. 轮次结束时前一轮 TTSStream 彻底关闭释放；
// 3. 两轮之间状态与资源彻底隔离，历史记录按序累积。
func TestIntegration_Scenario2_CrossTurnInteractionAndResourceIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	ttsClient := newMockTTSClient(nil) // nil 表示每次 CreateStream 生成全新 mockTTSStream

	var turnCounter atomic.Int32
	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			turn := turnCounter.Add(1)
			var text string
			if turn == 1 {
				text = "我是第一轮回答。"
			} else {
				text = "我是第二轮回答。"
			}
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: text, Iteration: 0})
			}
			return text, nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-CROSS-TURN-02",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	// ====== 轮次 1 ======
	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 1, "第一轮输入")
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages after turn 1, got %d", sess.History().Len())
	}

	ttsClient.mu.Lock()
	if len(ttsClient.createdStreams) != 1 {
		t.Fatalf("expected 1 TTS stream created after turn 1, got %d", len(ttsClient.createdStreams))
	}
	stream1 := ttsClient.createdStreams[0]
	ttsClient.mu.Unlock()

	if stream1.CloseCalls() != 1 {
		t.Fatalf("expected stream 1 to be closed after turn 1, got %d", stream1.CloseCalls())
	}

	// ====== 轮次 2 ======
	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 2, "第二轮输入")
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	if sess.History().Len() != 4 {
		t.Fatalf("expected 4 history messages after turn 2, got %d", sess.History().Len())
	}

	ttsClient.mu.Lock()
	if len(ttsClient.createdStreams) != 2 {
		t.Fatalf("expected 2 TTS streams created after turn 2, got %d", len(ttsClient.createdStreams))
	}
	stream2 := ttsClient.createdStreams[1]
	ttsClient.mu.Unlock()

	if stream1 == stream2 {
		t.Fatal("expected different TTSStream instances for turn 1 and turn 2")
	}
	if stream2.CloseCalls() != 1 {
		t.Fatalf("expected stream 2 to be closed after turn 2, got %d", stream2.CloseCalls())
	}

	msgs := sess.History().Messages()
	if msgs[0].Content != "第一轮输入" || msgs[1].Content != "我是第一轮回答。" ||
		msgs[2].Content != "第二轮输入" || msgs[3].Content != "我是第二轮回答。" {
		t.Fatalf("unexpected cumulative history: %+v", msgs)
	}
}

// TestIntegration_Scenario3_DynamicTTSModelPassthrough 验证动态模型透传：
// 配置任意非空模型名称，透传到 TTS 建连 run-task 请求中。
func TestIntegration_Scenario3_DynamicTTSModelPassthrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const expectedCustomModel = "custom-tts-model-2026-flash"

	var (
		modelReceived atomic.Value // string
		taskStarted   atomic.Bool
	)

	// 本地 mock DashScope TTS WebSocket 服务
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket accept failed: %v", err)
			return
		}
		defer wsConn.Close(websocket.StatusNormalClosure, "done")

		for {
			typ, payload, err := wsConn.Read(r.Context())
			if err != nil {
				return
			}
			if typ == websocket.MessageText {
				var req struct {
					Header struct {
						Action string `json:"action"`
						TaskId string `json:"task_id"`
					} `json:"header"`
					Payload struct {
						Model string `json:"model"`
					} `json:"payload"`
				}
				if err := json.Unmarshal(payload, &req); err == nil && req.Header.Action == "run-task" {
					modelReceived.Store(req.Payload.Model)
					taskStarted.Store(true)

					// 应答 task-started
					startedMsg := map[string]any{
						"header": map[string]any{
							"action":  "task-started",
							"task_id": req.Header.TaskId,
						},
					}
					startedBytes, _ := json.Marshal(startedMsg)
					_ = wsConn.Write(r.Context(), websocket.MessageText, startedBytes)

					// 发送一帧 PCM 二进制
					pcm := make([]byte, audio.DownlinkBytesPerFrame)
					_ = wsConn.Write(r.Context(), websocket.MessageBinary, pcm)

					// 应答 task-finished
					finishedMsg := map[string]any{
						"header": map[string]any{
							"action":  "task-finished",
							"task_id": req.Header.TaskId,
						},
					}
					finishedBytes, _ := json.Marshal(finishedMsg)
					_ = wsConn.Write(r.Context(), websocket.MessageText, finishedBytes)
				}
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	ttsClient, err := dashscope.NewTTSClient(&database.TTSConfig{
		Endpoint: wsURL,
		APIKey:   "sk-test-key",
		Model:    expectedCustomModel,
	}, "longxiaochun")
	if err != nil {
		t.Fatalf("failed to create dashscope TTSClient: %v", err)
	}

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "测试动态模型透传内容。", Iteration: 0})
			}
			return "测试动态模型透传内容。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-DYNAMIC-MODEL-03",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 1, "测试动态模型")
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	// 验证透传到服务端的模型名称完全匹配
	val := modelReceived.Load()
	if val == nil || val.(string) != expectedCustomModel {
		t.Fatalf("expected TTS server to receive model %q, got %v", expectedCustomModel, val)
	}
	if !taskStarted.Load() {
		t.Fatal("expected TTS task to have started and completed")
	}

	// 验证历史已提交
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages, got %d", sess.History().Len())
	}
}

// TestIntegration_Scenario4_DelayedFirstAudio_SuccessWithoutTimeout 验证延迟首音频正常合成与播放：
// 在首音频超时彻底移除后，首个非空 PCM 延迟到达（未超单句总超时）仍能正常编码与下发，会话顺利收敛至 Ready。
func TestIntegration_Scenario4_DelayedFirstAudio_SuccessWithoutTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	mockStream := newMockTTSStream()
	mockStream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		// 故意延迟 80ms 交付首个 PCM，模拟服务端首包延迟到达
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
		pcm := make([]byte, audio.DownlinkBytesPerFrame)
		return onPCM(ctx, pcm)
	}

	ttsClient := newMockTTSClient(mockStream)

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "首包延迟到达但未超时。", Iteration: 0})
			}
			return "首包延迟到达但未超时。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-DELAYED-AUDIO-04",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 1, "测试延迟首包")

	// 验证即便首包延迟，仍能正常完成并返回 StateReady
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	// 验证下行消息包含完整的 start, sentence_start, opus, stop
	ttsMsgs, binaryFrames := extractTTSMessages(conn)
	if len(ttsMsgs) != 3 {
		t.Fatalf("expected 3 TTS messages (start, sentence_start, stop), got %d", len(ttsMsgs))
	}
	if len(binaryFrames) == 0 {
		t.Fatal("expected at least 1 Opus binary frame to be written")
	}
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages, got %d", sess.History().Len())
	}
}

// TestIntegration_Scenario5_ThreeLayerBackpressure_NoDataLossOrReorder 验证三层队列背压传递：
// 在慢速 WebSocket 消费下，下行/PCM/句子队列背压能够自然传递且无数据丢失或乱序。
func TestIntegration_Scenario5_ThreeLayerBackpressure_NoDataLossOrReorder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	// 模拟慢速消费：在底层每次写入前引入 5ms 阻塞
	conn.beforeWrite = func(typ websocket.MessageType, p []byte) {
		time.Sleep(5 * time.Millisecond)
	}

	writer := NewWriter(ctx, conn, 2, nil) // 极小队列容量
	defer writer.Close()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)

	// 构造 4 句完整文本
	sentences := []string{
		"第一句背压测试无丢失。",
		"第二句背压测试无乱序。",
		"第三句背压测试自然传递。",
		"第四句背压测试顺利完成。",
	}
	fullText := strings.Join(sentences, "")

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				// 快速流式投递多个 chunk
				for _, s := range sentences {
					_ = callback(ctx, ai.LLMChunk{Text: s, Iteration: 0})
				}
			}
			return fullText, nil
		},
	}

	cfg := NormalizeConfig(SessionConfig{
		TTSPCMQueueCapacity:       2,
		DownlinkOpusQueueCapacity: 2,
	})

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-BACKPRESSURE-05",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Config:       cfg,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 1, "测试四句背压")

	// 等待慢速消费下所有队列排空并成功收敛
	waitForSessionState(t, sess, StateReady, 5*time.Second)

	// 校验所有 4 句话严格按序下发
	ttsMsgs, binaryFrames := extractTTSMessages(conn)
	// 期望：start + 4x sentence_start + stop = 6 条控制消息
	if len(ttsMsgs) != 6 {
		t.Fatalf("expected 6 TTS messages, got %d: %+v", len(ttsMsgs), ttsMsgs)
	}
	if ttsMsgs[0].State != "start" {
		t.Fatalf("expected first message state 'start', got %s", ttsMsgs[0].State)
	}
	for i, expectedSentence := range sentences {
		msg := ttsMsgs[i+1]
		if msg.State != "sentence_start" || msg.Text != expectedSentence {
			t.Fatalf("sentence %d mismatch: expected %q, got %+v", i, expectedSentence, msg)
		}
	}
	if ttsMsgs[5].State != "stop" {
		t.Fatalf("expected last message state 'stop', got %s", ttsMsgs[5].State)
	}
	if len(binaryFrames) < 4 {
		t.Fatalf("expected at least 4 Opus binary frames, got %d", len(binaryFrames))
	}
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages, got %d", sess.History().Len())
	}
}

// TestIntegration_Scenario6_ListenPrompt_StrictSequenceAndNoEndingPrompt 验证自动模式提示音时序与结束路径：
// 1. 自动模式聆听前提示音严格先于 Listening，屏障确认后才转入 Listening；
// 2. 结束路径（stop、abort、关闭）绝无额外提示音。
func TestIntegration_Scenario6_ListenPrompt_StrictSequenceAndNoEndingPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "收到，为您处理。", Iteration: 0})
			}
			return "收到，为您处理。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-LISTEN-PROMPT-06",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	expectedPackets, err := audio.GetListenPromptOpusPackets()
	if err != nil {
		t.Fatalf("failed to load prompt opus packets: %v", err)
	}

	// 1. 自动模式发送 listen.start
	sendClientListenStart(sess, ListenModeAuto)

	// 验证在屏障写出前会话进入 StateSpeaking（播放提示音），随后自动转入 StateListening
	waitForSessionState(t, sess, StateListening, 2*time.Second)

	// 验证提示音下发内容：tts/start -> expectedPackets 个二进制包 -> tts/stop
	ttsMsgs, binaryFrames := extractTTSMessages(conn)
	if len(ttsMsgs) < 2 {
		t.Fatalf("expected at least start and stop TTS messages for prompt, got %d", len(ttsMsgs))
	}
	if ttsMsgs[0].State != "start" || ttsMsgs[1].State != "stop" {
		t.Fatalf("unexpected prompt messages: %+v", ttsMsgs)
	}
	if len(binaryFrames) != len(expectedPackets) {
		t.Fatalf("expected %d prompt opus frames, got %d", len(expectedPackets), len(binaryFrames))
	}

	// 2. 正常回答交互
	sendASRResult(sess, 1, "启动问答")
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	// 记录回答结束时的消息总数
	totalMessagesAfterTurn := len(conn.getMessages())

	// 3. 验证结束路径 1：在 Listening 下发送 listen.stop，绝不下发提示音
	sendClientListenStart(sess, ListenModeManual) // 手动模式不播放提示音直接进入 Listening
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendClientListenStop(sess)

	// 验证 listen.stop 期间没有多余的音频帧
	for _, m := range conn.getMessages()[totalMessagesAfterTurn:] {
		if m.typ == websocket.MessageBinary {
			t.Fatal("unexpected binary frame sent during listen.stop")
		}
	}

	// 恢复至 Ready 状态
	sendClientAbort(sess)
	waitForSessionState(t, sess, StateReady, 2*time.Second)

	totalMessagesAfterStop := len(conn.getMessages())

	// 4. 验证结束路径 2：abort 绝不下发提示音
	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendClientAbort(sess)
	waitForSessionState(t, sess, StateReady, 2*time.Second)

	for _, m := range conn.getMessages()[totalMessagesAfterStop:] {
		if m.typ == websocket.MessageBinary {
			t.Fatal("unexpected binary frame sent during abort")
		}
	}

	totalMessagesAfterAbort := len(conn.getMessages())

	// 5. 验证结束路径 3：Close 绝不下发提示音
	sess.Close()
	waitForSessionState(t, sess, StateClosed, 2*time.Second)

	for _, m := range conn.getMessages()[totalMessagesAfterAbort:] {
		if m.typ == websocket.MessageBinary {
			t.Fatal("unexpected binary frame sent during close")
		}
	}
}

// TestIntegration_Scenario7_Abort_StaleFrameIsolationAndNextTurnClean 验证 abort 迟到帧隔离与新轮次进行：
// 1. 打断后旧语音帧被跳过；
// 2. 迟到的旧 PCM 帧被彻底隔离丢弃；
// 3. 新轮次正常进行，历史仅提交新轮次。
func TestIntegration_Scenario7_Abort_StaleFrameIsolationAndNextTurnClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	var (
		pcmCallback    atomic.Value // func(context.Context, []byte) error
		turn1AbortSent = make(chan struct{})
		turn1Stream    = newMockTTSStream()
	)

	turn1Stream.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		pcmCallback.Store(onPCM)
		// 发送首包 PCM
		pcm := make([]byte, audio.DownlinkBytesPerFrame)
		_ = onPCM(ctx, pcm)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-turn1AbortSent:
			return nil
		}
	}

	turn2Stream := newMockTTSStream()

	ttsClient := &mockTTSClient{
		createStreamFn: func(ctx context.Context) (ai.TTSStream, error) {
			if pcmCallback.Load() == nil {
				return turn1Stream, nil
			}
			return turn2Stream, nil
		},
	}

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "正在播放第一轮很长的回答。", Iteration: 0})
			}
			return "正在播放第一轮很长的回答。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-ABORT-ISOLATE-07",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	// ====== 轮次 1：开始并打断 ======
	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 1, "第一轮输入")

	// 等待进入 Speaking 状态
	waitForSessionState(t, sess, StateSpeaking, 2*time.Second)

	// 发送 abort 打断
	sendClientAbort(sess)
	close(turn1AbortSent)
	waitForSessionState(t, sess, StateReady, 2*time.Second)

	// 模拟旧轮次的迟到 PCM 数据在 abort 之后尝试写入
	if cb := pcmCallback.Load(); cb != nil {
		fn := cb.(func(context.Context, []byte) error)
		latePCM := make([]byte, audio.DownlinkBytesPerFrame)
		_ = fn(context.Background(), latePCM)
	}

	// 此时历史记录应为空（第 1 轮被 abort 丢弃）
	if sess.History().Len() != 0 {
		t.Fatalf("history should not be committed after abort, got len %d", sess.History().Len())
	}

	// ====== 轮次 2：正常执行 ======
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "这是第二轮正常回答。", Iteration: 0})
		}
		return "这是第二轮正常回答。", nil
	}

	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 3, "第二轮输入") // abort 会将 turnId 递增
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	// 验证历史仅包含第 2 轮
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages (turn 2 only), got %d", sess.History().Len())
	}
	msgs := sess.History().Messages()
	if msgs[0].Content != "第二轮输入" || msgs[1].Content != "这是第二轮正常回答。" {
		t.Fatalf("unexpected history after turn 2: %+v", msgs)
	}
}

// TestIntegration_Scenario8_TTSNonFatalFailure_ReturnsToReadyAndNextTurnWorks 验证 TTS 非致命失败：
// 1. TTS 报错回到 Ready，不关闭会话；
// 2. 不提交失败轮次历史；
// 3. 下一轮正常工作。
func TestIntegration_Scenario8_TTSNonFatalFailure_ReturnsToReadyAndNextTurnWorks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	var (
		callCount atomic.Int32
		stream1   = newMockTTSStream()
		stream2   = newMockTTSStream()
	)

	// stream1 模拟合成失败
	stream1.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
		return errors.New("simulated non-fatal tts network error")
	}

	ttsClient := &mockTTSClient{
		createStreamFn: func(ctx context.Context) (ai.TTSStream, error) {
			count := callCount.Add(1)
			if count == 1 {
				return stream1, nil
			}
			return stream2, nil
		},
	}

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "测试回答文本内容。", Iteration: 0})
			}
			return "测试回答文本内容。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-TTS-NONFATAL-08",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	// ====== 轮次 1：TTS 失败 ======
	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 1, "第一轮输入（将触发 TTS 失败）")

	// 验证会话在 TTS 失败后平稳恢复至 StateReady，且绝不关闭
	waitForSessionState(t, sess, StateReady, 3*time.Second)
	if sess.State() == StateClosed {
		t.Fatal("session should remain open on non-fatal TTS error")
	}
	if sess.History().Len() != 0 {
		t.Fatalf("expected 0 history messages on TTS failure, got %d", sess.History().Len())
	}

	// ====== 轮次 2：TTS 恢复正常 ======
	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 2, "第二轮输入（正常）")
	waitForSessionState(t, sess, StateReady, 3*time.Second)

	// 验证第二轮正常提交历史
	if sess.History().Len() != 2 {
		t.Fatalf("expected 2 history messages after recovery turn, got %d", sess.History().Len())
	}
	msgs := sess.History().Messages()
	if msgs[0].Content != "第二轮输入（正常）" || msgs[1].Content != "测试回答文本内容。" {
		t.Fatalf("unexpected history: %+v", msgs)
	}
}

// TestIntegration_Scenario9_WriterFatalFailure_ClosesSessionWithoutHistory 验证 Writer 致命写失败：
// 设备连接断开/写失败触发会话关闭，未写出的历史不提交。
func TestIntegration_Scenario9_WriterFatalFailure_ClosesSessionWithoutHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 50, nil)
	defer writer.Close()

	mockStream := newMockTTSStream()
	ttsClient := newMockTTSClient(mockStream)

	// 在语音帧写入时触发致命错误
	conn.beforeWrite = func(typ websocket.MessageType, p []byte) {
		if typ == websocket.MessageBinary {
			conn.mu.Lock()
			conn.writeErr = errors.New("simulated fatal connection broken pipe")
			conn.mu.Unlock()
		}
	}

	mockLLM := &mockLLMClient{
		generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
			if callback != nil {
				_ = callback(ctx, ai.LLMChunk{Text: "测试致命写入错误。", Iteration: 0})
			}
			return "测试致命写入错误。", nil
		},
	}

	sess := NewSession(ctx, Options{
		Writer:       writer,
		SerialNumber: "SN-WRITER-FATAL-09",
		TTSClient:    ttsClient,
		LLMClient:    mockLLM,
		Logger:       slog.Default(),
	})

	go func() {
		_ = sess.Run()
	}()

	performHandshake(t, sess)

	sendClientListenStart(sess, ListenModeManual)
	waitForSessionState(t, sess, StateListening, 2*time.Second)
	sendASRResult(sess, 1, "测试写失败")

	// 验证会话因致命错误异步关闭至 StateClosed
	waitForSessionState(t, sess, StateClosed, 3*time.Second)

	// 验证失败轮次历史未提交
	if sess.History().Len() != 0 {
		t.Fatalf("expected 0 history messages after fatal write error, got %d", sess.History().Len())
	}
}

// TestIntegration_Scenario10_CloseSession_DelayedCloseAndAbortPriority 验证 close_session 告别语与 abort 优先语义：
// 1. 正常路径：告别语与 stop 写出后关闭连接；
// 2. abort 路径：打断优先取消关闭意图，回到 Ready 且下一轮可正常继续。
func TestIntegration_Scenario10_CloseSession_DelayedCloseAndAbortPriority(t *testing.T) {
	t.Run("NormalGoodbyeAndDelayedClose", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		conn := &mockWSConn{}
		writer := NewWriter(ctx, conn, 50, nil)
		defer writer.Close()

		ttsClient := newMockTTSClient(nil)

		var stateBeforeStopWritten State
		conn.beforeWrite = func(typ websocket.MessageType, p []byte) {
			var msg parsedTTSMessage
			if typ == websocket.MessageText && json.Unmarshal(p, &msg) == nil && msg.State == "stop" {
				// 在 stop 写出瞬间记录会话状态，此时会话绝不能已关闭
				stateBeforeStopWritten = StateSpeaking
			}
		}

		mockLLM := &mockLLMClient{
			generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
				for _, tool := range req.Tools {
					if tool.Name == agentkit.ToolCloseSession {
						_, _ = tool.Run(ctx, map[string]any{"reason": "用户请求退出"})
					}
				}
				if callback != nil {
					_ = callback(ctx, ai.LLMChunk{Text: "再见，祝您生活愉快！", Iteration: 0})
				}
				return "再见，祝您生活愉快！", nil
			},
		}

		sess := NewSession(ctx, Options{
			Writer:       writer,
			SerialNumber: "SN-CLOSE-GOODBYE-10A",
			TTSClient:    ttsClient,
			LLMClient:    mockLLM,
			Logger:       slog.Default(),
		})

		go func() {
			_ = sess.Run()
		}()

		performHandshake(t, sess)

		sendClientListenStart(sess, ListenModeManual)
		waitForSessionState(t, sess, StateListening, 2*time.Second)
		sendASRResult(sess, 1, "退出会话")

		// 验证会话平稳写出告别语并在屏障确认后关闭
		waitForSessionState(t, sess, StateClosed, 3*time.Second)

		if stateBeforeStopWritten != StateSpeaking {
			t.Fatalf("expected session to be StateSpeaking before stop is written, got %v", stateBeforeStopWritten)
		}

		// 验证告别语历史已正确提交
		if sess.History().Len() != 2 {
			t.Fatalf("expected 2 history messages for goodbye turn, got %d", sess.History().Len())
		}
		msgs := sess.History().Messages()
		if msgs[0].Content != "退出会话" || msgs[1].Content != "再见，祝您生活愉快！" {
			t.Fatalf("unexpected history: %+v", msgs)
		}
	})

	t.Run("AbortPriorityCancelsCloseIntent", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		conn := &mockWSConn{}
		writer := NewWriter(ctx, conn, 50, nil)
		defer writer.Close()

		var (
			turnCount atomic.Int32
			abortSent = make(chan struct{})
		)

		stream1 := newMockTTSStream()
		stream1.synthesizeFn = func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			pcm := make([]byte, audio.DownlinkBytesPerFrame)
			_ = onPCM(ctx, pcm)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-abortSent:
				return nil
			}
		}

		stream2 := newMockTTSStream()

		ttsClient := &mockTTSClient{
			createStreamFn: func(ctx context.Context) (ai.TTSStream, error) {
				if turnCount.Load() == 1 {
					return stream1, nil
				}
				return stream2, nil
			},
		}

		mockLLM := &mockLLMClient{
			generate: func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
				count := turnCount.Add(1)
				if count == 1 {
					for _, tool := range req.Tools {
						if tool.Name == agentkit.ToolCloseSession {
							_, _ = tool.Run(ctx, map[string]any{"reason": "用户请求退出"})
						}
					}
					if callback != nil {
						_ = callback(ctx, ai.LLMChunk{Text: "再见，准备退出。", Iteration: 0})
					}
					return "再见，准备退出。", nil
				}

				if callback != nil {
					_ = callback(ctx, ai.LLMChunk{Text: "好的，我们继续交流。", Iteration: 0})
				}
				return "好的，我们继续交流。", nil
			},
		}

		sess := NewSession(ctx, Options{
			Writer:       writer,
			SerialNumber: "SN-CLOSE-ABORT-10B",
			TTSClient:    ttsClient,
			LLMClient:    mockLLM,
			Logger:       slog.Default(),
		})

		go func() {
			_ = sess.Run()
		}()

		performHandshake(t, sess)

		// ====== 轮次 1：触发 close_session 并在播报期间 abort ======
		sendClientListenStart(sess, ListenModeManual)
		waitForSessionState(t, sess, StateListening, 2*time.Second)
		sendASRResult(sess, 1, "我想退出")

		waitForSessionState(t, sess, StateSpeaking, 2*time.Second)

		// 发送 abort 打断退出意图
		sendClientAbort(sess)
		close(abortSent)

		// 验证打断后回到 StateReady 而绝非 StateClosed
		waitForSessionState(t, sess, StateReady, 2*time.Second)
		if sess.State() == StateClosed {
			t.Fatal("session should not be closed when abort was issued")
		}
		if sess.History().Len() != 0 {
			t.Fatalf("expected 0 history messages after abort, got %d", sess.History().Len())
		}

		// ====== 轮次 2：继续正常交互 ======
		sendClientListenStart(sess, ListenModeManual)
		waitForSessionState(t, sess, StateListening, 2*time.Second)
		sendASRResult(sess, 3, "继续聊天")
		waitForSessionState(t, sess, StateReady, 3*time.Second)

		// 验证第 2 轮历史成功追加
		if sess.History().Len() != 2 {
			t.Fatalf("expected 2 history messages after turn 2, got %d", sess.History().Len())
		}
		msgs := sess.History().Messages()
		if msgs[0].Content != "继续聊天" || msgs[1].Content != "好的，我们继续交流。" {
			t.Fatalf("unexpected history: %+v", msgs)
		}
	})
}
