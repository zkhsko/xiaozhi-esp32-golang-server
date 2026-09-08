package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hraban/opus"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/protocol/ws"
)

var testProtocolVersions = []ws.Version{ws.Version1, ws.Version2}

func protocolHello(t *testing.T, version ws.Version) []byte {
	t.Helper()
	var hello ClientHelloMessage
	if err := json.Unmarshal([]byte(promptToneTestHello), &hello); err != nil {
		t.Fatal(err)
	}
	hello.Version = int(version)
	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func deviceAudioPacket(t *testing.T, version ws.Version, payload []byte) []byte {
	t.Helper()
	if version == ws.Version1 {
		return bytes.Clone(payload)
	}
	header := struct {
		Version   uint16
		Type      uint16
		Reserved  uint32
		Timestamp uint32
		Size      uint32
	}{Version: 2, Size: uint32(len(payload))}
	var packet bytes.Buffer
	if err := binary.Write(&packet, binary.BigEndian, header); err != nil {
		t.Fatal(err)
	}
	packet.Write(payload)
	return packet.Bytes()
}

func serverAudioPayload(t *testing.T, version ws.Version, packet []byte) []byte {
	t.Helper()
	if version == ws.Version1 {
		return packet
	}
	if len(packet) <= 16 {
		t.Fatalf("short v2 packet: %x", packet)
	}
	payload := packet[16:]
	if !bytes.Equal(packet, deviceAudioPacket(t, version, payload)) {
		t.Fatalf("invalid v2 downlink header: %x", packet[:16])
	}
	return payload
}

func TestProtocolVoiceCycle(t *testing.T) {
	for _, version := range testProtocolVersions {
		for _, mode := range []string{"auto", "manual"} {
			t.Run(fmt.Sprintf("%d/%s", version, mode), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn, peer := newSessionTestConnection(t, ctx, version, SessionConfig{})
				pcm := make(chan []byte, 1)
				sess := NewSession(ctx, Options{
					Conn:      conn,
					ASRClient: &mockASRClient{text: "test", onFeed: func(data []byte) { pcm <- bytes.Clone(data) }},
					LLMClient: &mockLLMClient{chunks: []ai.LLMChunk{{Text: "Hello."}}},
					TTSClient: &mockTTSClient{},
				})
				go func() { _ = sess.Run() }()
				defer func() { sess.Close(); <-sess.Done() }()
				if err := peer.Write(ctx, websocket.MessageText, protocolHello(t, version)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := peer.Read(ctx); err != nil {
					t.Fatal(err)
				}
				listen, err := json.Marshal(map[string]string{"type": "listen", "state": "start", "mode": mode})
				if err != nil {
					t.Fatal(err)
				}
				if err := peer.Write(ctx, websocket.MessageText, listen); err != nil {
					t.Fatal(err)
				}
				packet := createValid16kOpusPacket()
				if len(packet) == 0 {
					t.Fatal("could not encode uplink audio")
				}
				if err := peer.Write(ctx, websocket.MessageBinary, deviceAudioPacket(t, version, packet)); err != nil {
					t.Fatal(err)
				}
				if mode == "manual" {
					if err := peer.Write(ctx, websocket.MessageText, []byte(`{"type":"listen","state":"stop"}`)); err != nil {
						t.Fatal(err)
					}
				}
				decoder, err := opus.NewDecoder(24000, 1)
				if err != nil {
					t.Fatal(err)
				}
				var frames, starts, stops, transcripts int
				for stops == 0 {
					typ, data, err := peer.Read(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if typ == websocket.MessageBinary {
						n, err := decoder.Decode(serverAudioPayload(t, version, data), make([]int16, 1440))
						if err != nil || n != 1440 {
							t.Fatalf("downlink Opus: samples=%d, err=%v", n, err)
						}
						frames++
						continue
					}
					var message ServerTTSMessage
					if err := json.Unmarshal(data, &message); err != nil {
						t.Fatal(err)
					}
					if message.Type == MessageTypeSTT {
						transcripts++
					}
					switch message.State {
					case TTSStateStart:
						starts++
					case TTSStateStop:
						stops++
					}
				}
				if frames != 1 || starts != 1 || transcripts != 1 {
					t.Fatalf("frames=%d starts=%d transcripts=%d", frames, starts, transcripts)
				}
				select {
				case data := <-pcm:
					if len(data) != 1920 {
						t.Fatalf("uplink PCM length = %d", len(data))
					}
				default:
					t.Fatal("ASR did not receive decoded uplink audio")
				}
				if !waitForCondition(time.Second, func() bool { return sess.runtime.history.Len() == 2 }) {
					t.Fatal("turn history was not committed")
				}
			})
		}
	}
}

func TestHandlerProtocolVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resolver := &mockDeviceAgentResolver{
		tokens: map[string]*database.DeviceAccessToken{},
		snapshots: map[string]*database.AgentRuntimeSnapshot{
			"speaker": {
				Agent:     database.AgentSnapshot{Id: 1, Voice: "voice1", PromptToneEnabled: true},
				ASRConfig: database.ASRConfig{Provider: "dashscope", Endpoint: "wss://example.invalid/asr", APIKey: "test", Model: "asr"},
				LLMConfig: database.LLMConfig{Provider: "dashscope", Endpoint: "https://example.invalid/v1", APIKey: "test", Model: "llm"},
				TTSConfig: database.TTSConfig{Provider: "dashscope", Endpoint: "wss://example.invalid/tts", APIKey: "test", Model: "tts"},
			},
		},
	}
	for _, version := range testProtocolVersions {
		token := fmt.Sprintf("protocol-%d", version)
		resolver.tokens[token] = &database.DeviceAccessToken{SerialNumber: token, AccessToken: token, DeviceType: "speaker"}
	}
	resolver.tokens["mismatch"] = &database.DeviceAccessToken{SerialNumber: "mismatch", AccessToken: "mismatch", DeviceType: "speaker"}
	handler := NewHandler(HandlerOptions{
		DB:      resolver,
		Config:  &config.Config{Session: config.SessionConfig{MaxOpusPacketBytes: 128}},
		Limiter: NewSessionLimiter(len(testProtocolVersions) + 1),
	})
	server := httptest.NewServer(handler)
	var peers []*websocket.Conn
	t.Cleanup(func() {
		for _, peer := range peers {
			_ = peer.CloseNow()
		}
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := handler.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		server.Close()
	})
	dial := func(version ws.Version, token string) *websocket.Conn {
		t.Helper()
		peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+WebSocketPath, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}, "Protocol-Version": {fmt.Sprint(version)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, peer)
		return peer
	}
	for _, version := range testProtocolVersions {
		peer := dial(version, fmt.Sprintf("protocol-%d", version))
		if err := peer.Write(ctx, websocket.MessageText, protocolHello(t, version)); err != nil {
			t.Fatal(err)
		}
		_, data, err := peer.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var hello ServerHelloMessage
		if err := json.Unmarshal(data, &hello); err != nil || hello.SessionId == "" || hello.AudioParams.SampleRate != 24000 {
			t.Fatalf("invalid server hello: %s, %v", data, err)
		}
	}
	packets, err := audio.GetPromptOpusPackets()
	if err != nil {
		t.Fatal(err)
	}
	for i, version := range testProtocolVersions {
		peer := peers[i]
		if err := peer.Write(ctx, websocket.MessageText, []byte(`{"type":"listen","state":"detect","text":"hello"}`)); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < len(packets)+2; j++ {
			typ, data, err := peer.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if j > 0 && j <= len(packets) {
				if typ != websocket.MessageBinary || !bytes.Equal(serverAudioPayload(t, version, data), packets[j-1]) {
					t.Fatalf("v%d greeting packet %d differs", version, j)
				}
				continue
			}
			var message ServerTTSMessage
			if err := json.Unmarshal(data, &message); err != nil {
				t.Fatal(err)
			}
			want := TTSStateStart
			if j > 0 {
				want = TTSStateStop
			}
			if message.Type != MessageTypeTTS || message.State != want {
				t.Fatalf("greeting marker = %+v", message)
			}
		}
	}
	for _, headerVersion := range testProtocolVersions {
		for _, helloVersion := range testProtocolVersions {
			if headerVersion == helloVersion {
				continue
			}
			peer := dial(headerVersion, "mismatch")
			if err := peer.Write(ctx, websocket.MessageText, protocolHello(t, helloVersion)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := peer.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("version mismatch was not rejected: %v", err)
			}
			if !waitForCondition(time.Second, func() bool { return handler.registry.ActiveCount() == len(testProtocolVersions) }) {
				t.Fatal("mismatched connection retained a session slot")
			}
		}
	}
}
