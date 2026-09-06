package session

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
	"xiaozhi-esp32-golang-server/internal/voice"
)

const promptToneTestHello = `{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`

// waitingASRClient 将测试停留在收音阶段，避免正文音频干扰会话提示音断言。
type waitingASRClient struct{}

func (*waitingASRClient) Recognize(ctx context.Context, _ ai.ASRRequest, _ <-chan []byte) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestSession_PromptToneGreeting(t *testing.T) {
	packets, err := audio.GetPromptOpusPackets()
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []struct {
		name         string
		message      string
		allowsPrompt bool
		wantTurnId   uint64
	}{
		{name: "auto", message: `{"type":"listen","state":"start","mode":"auto"}`, allowsPrompt: true, wantTurnId: 1},
		{name: "detect", message: `{"type":"listen","state":"detect","text":"你好小智"}`, allowsPrompt: true},
		{name: "manual", message: `{"type":"listen","state":"start","mode":"manual"}`, wantTurnId: 1},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", trigger.name, enabled), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn := &mockWSConn{}
				sess := NewSession(ctx, Options{
					PromptToneEnabled: enabled,
					ASRClient:         &waitingASRClient{},
					Outbound:          NewOutboundActor(ctx, conn, 20, time.Second, nil, nil),
				})
				go func() { _ = sess.Run() }()
				defer func() {
					sess.Close()
					<-sess.Done()
				}()

				sess.postEvent(sessionEvent{kind: eventKindClientFrame, data: []byte(promptToneTestHello)})
				if !waitForCondition(time.Second, func() bool {
					return sess.SessionId() != "" && len(conn.getMessages()) > 0
				}) {
					t.Fatal("handshake timed out")
				}
				messages := conn.getMessages()
				if len(messages) != 1 || messages[0].msgType != websocket.MessageText || !bytes.Contains(messages[0].payload, []byte(`"type":"hello"`)) {
					t.Fatalf("hello must only send ServerHello, got %v", messages)
				}

				sess.postEvent(sessionEvent{kind: eventKindClientFrame, data: []byte(trigger.message)})
				wantMessages := 1
				if enabled && trigger.allowsPrompt {
					wantMessages += len(packets) + 2
					if !waitForCondition(2*time.Second, func() bool {
						messages := conn.getMessages()
						return len(messages) == wantMessages && bytes.Contains(messages[len(messages)-1].payload, []byte(`"state":"stop"`))
					}) {
						t.Fatal("greeting prompt did not finish")
					}
				} else if waitForCondition(150*time.Millisecond, func() bool { return len(conn.getMessages()) != 1 }) {
					t.Fatalf("unexpected greeting output: %d messages", len(conn.getMessages()))
				}
				select {
				case <-sess.Done():
					t.Fatal("greeting must not close the session")
				default:
				}

				// Actor 退出后再读取私有状态，避免测试与监督循环并发访问。
				sess.Close()
				<-sess.Done()
				messages = conn.getMessages()
				if len(messages) != wantMessages {
					t.Fatalf("messages = %d, want %d", len(messages), wantMessages)
				}
				if enabled && trigger.allowsPrompt {
					first, last := messages[1], messages[len(messages)-1]
					if first.msgType != websocket.MessageText || !bytes.Contains(first.payload, []byte(`"state":"start"`)) || last.msgType != websocket.MessageText || !bytes.Contains(last.payload, []byte(`"state":"stop"`)) {
						t.Fatal("greeting must be wrapped in one tts.start/tts.stop pair")
					}
					for i, packet := range packets {
						message := messages[i+2]
						if message.msgType != websocket.MessageBinary || !bytes.Equal(message.payload, packet) {
							t.Fatalf("greeting packet %d differs", i)
						}
					}
				}
				if sess.runtime.currentTurnId != trigger.wantTurnId {
					t.Fatalf("turn Id = %d, want %d", sess.runtime.currentTurnId, trigger.wantTurnId)
				}
				if sess.runtime.history.Len() != 0 {
					t.Fatal("greeting prompt must not enter conversation history")
				}
			})
		}
	}
}

func TestSession_PromptToneTurnOptions(t *testing.T) {
	promptPCM, err := audio.GetPromptPCM()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := &mockWSConn{}
			sess := NewSession(ctx, Options{
				PromptToneEnabled: enabled,
				ASRClient:         &mockASRClient{text: "你好"},
				LLMClient:         &mockLLMClient{chunks: []ai.LLMChunk{{Text: "你好呀。"}}},
				TTSClient:         &mockTTSClient{},
				Outbound:          NewOutboundActor(ctx, conn, 20, time.Second, nil, nil),
			})
			defer sess.cleanup()
			if sess.handleHello([]byte(promptToneTestHello)) {
				t.Fatal("hello failed")
			}
			sess.startTurn("manual", nil, true)
			finished := false
			for !finished {
				select {
				case event := <-sess.events:
					if event.kind == eventKindTurnFinished {
						if event.turnResult.Status != voice.TurnCompleted {
							t.Fatalf("turn failed: %v", event.turnResult.Err)
						}
						finished = true
					}
					sess.handleEvent(event)
				case <-ctx.Done():
					t.Fatal("turn timed out")
				}
			}

			framer := audio.NewPCMFramer()
			wantFrames := len(framer.Feed(make([]byte, audio.DownlinkBytesPerFrame)))
			if enabled {
				wantFrames += len(framer.Feed(promptPCM))
			}
			wantFrames += len(framer.Flush())
			var audioFrames, starts, stops int
			for _, message := range conn.getMessages() {
				if message.msgType == websocket.MessageBinary {
					audioFrames++
				} else if bytes.Contains(message.payload, []byte(`"state":"start"`)) {
					starts++
				} else if bytes.Contains(message.payload, []byte(`"state":"stop"`)) {
					stops++
				}
			}
			if audioFrames != wantFrames || starts != 1 || stops != 1 {
				t.Fatalf("unexpected output: audio=%d (want %d), starts=%d, stops=%d", audioFrames, wantFrames, starts, stops)
			}
			if sess.runtime.history.Len() != 2 {
				t.Fatalf("history entries = %d, want 2", sess.runtime.history.Len())
			}
		})
	}
}
