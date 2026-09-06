package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/database"
)

type promptToneResolver struct {
	*mockDeviceAgentResolver
	enabled atomic.Bool
}

func (r *promptToneResolver) ResolveAgentRuntimeSnapshotByDeviceType(ctx context.Context, deviceType string) (*database.AgentRuntimeSnapshot, error) {
	snapshot, err := r.mockDeviceAgentResolver.ResolveAgentRuntimeSnapshotByDeviceType(ctx, deviceType)
	if err != nil {
		return nil, err
	}
	copy := *snapshot
	copy.Agent.PromptToneEnabled = r.enabled.Load()
	return &copy, nil
}

func TestHandler_PromptToneNewSessionsOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resolver := &promptToneResolver{mockDeviceAgentResolver: &mockDeviceAgentResolver{
		tokens: map[string]*database.DeviceAccessToken{
			"old-token": {SerialNumber: "old-device", AccessToken: "old-token", DeviceType: "speaker"},
			"new-token": {SerialNumber: "new-device", AccessToken: "new-token", DeviceType: "speaker"},
		},
		snapshots: map[string]*database.AgentRuntimeSnapshot{
			"speaker": {
				Agent:     database.AgentSnapshot{Id: 1, Name: "prompt-agent", SystemPrompt: "你好", Voice: "voice1"},
				ASRConfig: database.ASRConfig{Provider: "dashscope", Endpoint: "wss://example.invalid/asr", APIKey: "test", Model: "asr"},
				LLMConfig: database.LLMConfig{Provider: "dashscope", Endpoint: "https://example.invalid/v1", APIKey: "test", Model: "llm"},
				TTSConfig: database.TTSConfig{Provider: "dashscope", Endpoint: "wss://example.invalid/tts", APIKey: "test", Model: "tts"},
			},
		},
	}}
	resolver.enabled.Store(true)
	handler := NewHandler(HandlerOptions{DB: resolver, Limiter: NewSessionLimiter(2)})
	server := httptest.NewServer(handler)
	var connections []*websocket.Conn
	defer func() {
		for _, conn := range connections {
			_ = conn.CloseNow()
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := handler.Close(shutdownCtx); err != nil {
			t.Errorf("close handler: %v", err)
		}
		server.Close()
	}()

	connect := func(token, serialNumber string) *Session {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http://", "ws://", 1)+WebSocketPath, &websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization":    {"Bearer " + token},
				"Protocol-Version": {"1"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		if err := conn.Write(ctx, websocket.MessageText, []byte(promptToneTestHello)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatal(err)
		}
		sess := handler.Registry().GetBySerial(serialNumber)
		if sess == nil {
			t.Fatal("session was not registered")
		}
		return sess
	}

	original := connect("old-token", "old-device")
	if !original.promptToneEnabled {
		t.Fatal("initial session did not receive the enabled prompt tone")
	}
	resolver.enabled.Store(false)
	latest := connect("new-token", "new-device")
	if latest.promptToneEnabled {
		t.Fatal("new session did not receive the disabled prompt tone")
	}
	if !original.promptToneEnabled {
		t.Fatal("configuration changes must not modify an existing session")
	}
}
