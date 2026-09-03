package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
)

type mockSessionSender struct {
	mu   sync.Mutex
	sent [][]byte
}

func (m *mockSessionSender) SendTextSession(ctx context.Context, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, append([]byte(nil), payload...))
	return nil
}

func TestSession_MCPHandshakeDetection(t *testing.T) {
	helloTrue := &ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		Features: &ClientFeatures{
			MCP: true,
		},
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	if !helloTrue.SupportsMCP() {
		t.Fatal("expected SupportsMCP to be true")
	}

	helloNil := &ClientHelloMessage{
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
	if helloNil.SupportsMCP() {
		t.Fatal("expected SupportsMCP to be false when features is nil")
	}

	helloFalse := &ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		Features: &ClientFeatures{
			MCP: false,
		},
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
	if helloFalse.SupportsMCP() {
		t.Fatal("expected SupportsMCP to be false when features.mcp is false")
	}
}

func TestMCPBridge_SendMCPPayload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := &mockSessionSender{}
	bridge := NewMCPBridge(slog.Default(), nil)
	bridge.Enable(ctx, "test-sess-123", sender)

	payload := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	err := bridge.SendMCPPayload(ctx, payload)
	if err != nil {
		t.Fatalf("SendMCPPayload failed: %v", err)
	}

	sender.mu.Lock()
	msgs := sender.sent
	sender.mu.Unlock()

	if len(msgs) == 0 {
		t.Fatal("expected at least 1 message sent")
	}
	lastMsg := msgs[len(msgs)-1]
	var downlink DownlinkMCPMessage
	if err := json.Unmarshal(lastMsg, &downlink); err != nil {
		t.Fatalf("unmarshal downlink failed: %v", err)
	}

	if downlink.SessionId != "test-sess-123" {
		t.Fatalf("expected session_id test-sess-123, got %s", downlink.SessionId)
	}
	if downlink.Type != MessageTypeMCP {
		t.Fatalf("expected type mcp, got %s", downlink.Type)
	}
}

func TestMCPBridge_HandleInbound_ValidationAndForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := &mockSessionSender{}
	bridge := NewMCPBridge(slog.Default(), nil)
	bridge.Enable(ctx, "sess-mcp-test", sender)

	// 1. 非法 session_id（不匹配）应被忽略
	mismatchMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "different-session-id",
		Payload:   json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`),
	}
	bridge.HandleInbound("sess-mcp-test", mismatchMsg)

	// 2. 空 payload 应被忽略
	emptyMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "sess-mcp-test",
		Payload:   nil,
	}
	bridge.HandleInbound("sess-mcp-test", emptyMsg)

	// 3. 非合法 json payload 应被忽略
	invalidJSONMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "sess-mcp-test",
		Payload:   json.RawMessage(`invalid-json`),
	}
	bridge.HandleInbound("sess-mcp-test", invalidJSONMsg)
}
