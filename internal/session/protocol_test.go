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

var testProtocolVersions = []ws.Version{ws.Version1, ws.Version2, ws.Version3}

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
	var packet bytes.Buffer
	if version == ws.Version2 {
		header := struct {
			Version   uint16
			Type      uint16
			Reserved  uint32
			Timestamp uint32
			Size      uint32
		}{Version: 2, Size: uint32(len(payload))}
		if err := binary.Write(&packet, binary.BigEndian, header); err != nil {
			t.Fatal(err)
		}
	} else {
		header := struct {
			Type     uint8
			Reserved uint8
			Size     uint16
		}{Size: uint16(len(payload))}
		if err := binary.Write(&packet, binary.BigEndian, header); err != nil {
			t.Fatal(err)
		}
	}
	packet.Write(payload)
	return packet.Bytes()
}

func serverAudioPayload(t *testing.T, version ws.Version, packet []byte) []byte {
	t.Helper()
	if version == ws.Version1 {
		return packet
	}
	headerSize := 16
	if version == ws.Version3 {
		headerSize = 4
	}
	if len(packet) <= headerSize {
		t.Fatalf("short v%d packet: %x", version, packet)
	}
	payload := packet[headerSize:]
	if !bytes.Equal(packet, deviceAudioPacket(t, version, payload)) {
		t.Fatalf("invalid v%d downlink header: %x", version, packet[:headerSize])
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
					Conn:              conn,
					PromptToneEnabled: mode == "manual",
					ASRClient:         &mockASRClient{text: "test", onFeed: func(data []byte) { pcm <- bytes.Clone(data) }},
					LLMClient:         &mockLLMClient{chunks: []ai.LLMChunk{{Text: "Hello."}}},
					TTSClient:         &mockTTSClient{},
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
				wantFrames := 1
				if mode == "manual" {
					promptPCM, err := audio.GetPromptPCM()
					if err != nil {
						t.Fatal(err)
					}
					wantFrames += (len(promptPCM) + audio.DownlinkBytesPerFrame - 1) / audio.DownlinkBytesPerFrame
				}
				if frames != wantFrames || starts != 1 || transcripts != 1 {
					t.Fatalf("frames=%d (want %d) starts=%d transcripts=%d", frames, wantFrames, starts, transcripts)
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

func TestProtocolMCPDiscovery(t *testing.T) {
	for _, version := range testProtocolVersions {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, peer := newSessionTestConnection(t, ctx, version, SessionConfig{})
			sess := NewSession(ctx, Options{Conn: conn})
			go func() { _ = sess.Run() }()
			defer func() { sess.Close(); <-sess.Done() }()
			var hello ClientHelloMessage
			if err := json.Unmarshal(protocolHello(t, version), &hello); err != nil {
				t.Fatal(err)
			}
			hello.Features = &ClientFeatures{MCP: true}
			data, err := json.Marshal(hello)
			if err != nil {
				t.Fatal(err)
			}
			if err := peer.Write(ctx, websocket.MessageText, data); err != nil {
				t.Fatal(err)
			}
			typ, data, err := peer.Read(ctx)
			if err != nil || typ != websocket.MessageText {
				t.Fatalf("server hello: type=%v err=%v", typ, err)
			}
			var serverHello ServerHelloMessage
			if err := json.Unmarshal(data, &serverHello); err != nil || serverHello.SessionId == "" {
				t.Fatalf("invalid server hello: %s, %v", data, err)
			}
			for _, step := range []struct {
				method string
				result json.RawMessage
			}{
				{"initialize", json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`)},
				{"tools/list", json.RawMessage(`{"tools":[{"name":"self.test","description":"Test device tool","inputSchema":{"type":"object","properties":{}}}]}`)},
			} {
				typ, data, err := peer.Read(ctx)
				if err != nil || typ != websocket.MessageText {
					t.Fatalf("MCP %s: type=%v err=%v", step.method, typ, err)
				}
				var message DownlinkMCPMessage
				if err := json.Unmarshal(data, &message); err != nil || message.Type != MessageTypeMCP || message.SessionId != serverHello.SessionId {
					t.Fatalf("invalid MCP envelope: %s, %v", data, err)
				}
				var request struct {
					Id     int64  `json:"id"`
					Method string `json:"method"`
				}
				if err := json.Unmarshal(message.Payload, &request); err != nil || request.Method != step.method || request.Id <= 0 {
					t.Fatalf("invalid MCP request: %s, %v", message.Payload, err)
				}
				response, err := json.Marshal(map[string]any{
					"type":       "mcp",
					"session_id": serverHello.SessionId,
					"payload":    map[string]any{"jsonrpc": "2.0", "id": request.Id, "result": step.result},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := peer.Write(ctx, websocket.MessageText, response); err != nil {
					t.Fatal(err)
				}
			}
			if err := sess.mcpBridge.WaitReady(ctx); err != nil {
				t.Fatal(err)
			}
			if tools := sess.mcpBridge.Tools(); len(tools) != 1 || tools[0].Name != "self.test" {
				t.Fatalf("discovered tools = %+v", tools)
			}
		})
	}
}

func TestProtocolInvalidAudio(t *testing.T) {
	for _, version := range testProtocolVersions {
		for _, kind := range []string{"empty", "oversized", "invalid_type"} {
			if version == ws.Version1 && kind == "invalid_type" {
				continue
			}
			t.Run(fmt.Sprintf("%d/%s", version, kind), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cfg := SessionConfig{MaxOpusPacketBytes: 128}
				conn, peer := newSessionTestConnection(t, ctx, version, cfg)
				sess := NewSession(ctx, Options{Conn: conn, Config: cfg})
				go func() { _ = sess.Run() }()
				defer func() { sess.Close(); <-sess.Done() }()
				if err := peer.Write(ctx, websocket.MessageText, protocolHello(t, version)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := peer.Read(ctx); err != nil {
					t.Fatal(err)
				}
				wire := deviceAudioPacket(t, version, nil)
				want := websocket.StatusPolicyViolation
				switch kind {
				case "oversized":
					wire = deviceAudioPacket(t, version, make([]byte, 129))
					want = websocket.StatusMessageTooBig
				case "invalid_type":
					wire = deviceAudioPacket(t, version, []byte{0x11})
					if version == ws.Version2 {
						wire[3] = 1
					} else {
						wire[0] = 1
					}
				}
				if err := peer.Write(ctx, websocket.MessageBinary, wire); err != nil {
					t.Fatal(err)
				}
				if _, _, err := peer.Read(ctx); websocket.CloseStatus(err) != want {
					t.Fatalf("close = %v, want %v", err, want)
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
		DB: resolver,
		Config: &config.Config{
			Server:  config.ServerConfig{WebSocketVersion: ws.Version2},
			Session: config.SessionConfig{MaxOpusPacketBytes: 128},
		},
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
