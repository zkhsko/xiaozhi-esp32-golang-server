package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

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

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Stop()

	bridge := NewMCPBridge(slog.Default(), nil)
	bridge.Enable("test-sess-123", writer)

	payload := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	err := bridge.SendMCPPayload(ctx, payload)
	if err != nil {
		t.Fatalf("SendMCPPayload failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	msgs := conn.getMessages()
	if len(msgs) == 0 {
		t.Fatal("expected at least 1 message sent")
	}
	lastMsg := msgs[len(msgs)-1]
	var downlink DownlinkMCPMessage
	if err := json.Unmarshal(lastMsg.payload, &downlink); err != nil {
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

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Stop()

	bridge := NewMCPBridge(slog.Default(), nil)
	bridge.Enable("sess-mcp-test", writer)

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

	// 3. 非对象 payload 应被忽略
	nonObjMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "sess-mcp-test",
		Payload:   json.RawMessage(`"just a string"`),
	}
	bridge.HandleInbound("sess-mcp-test", nonObjMsg)

	// 4. 正常响应转发
	validMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "sess-mcp-test",
		Payload:   json.RawMessage(`{"jsonrpc":"2.0","id":100,"result":{"status":"ok"}}`),
	}
	bridge.HandleInbound("sess-mcp-test", validMsg)
}
