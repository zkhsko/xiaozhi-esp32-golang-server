package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/agentkit"
)

func TestSession_MCPHandshakeDetection(t *testing.T) {
	// 1. features.mcp = true
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

	// 2. features = nil
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

	// 3. features.mcp = false
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

func TestSession_SendMCPPayload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := &Session{
		ctx:       ctx,
		cancel:    cancel,
		logger:    slog.Default(),
		sessionId: "test-sess-123",
	}

	conn := &mockWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	sess.writer = writer
	defer writer.Stop()

	payload := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	err := sess.SendMCPPayload(ctx, payload)
	if err != nil {
		t.Fatalf("SendMCPPayload failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	msgs := conn.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(msgs))
	}
	var downlink DownlinkMCPMessage
	if err := json.Unmarshal(msgs[0].payload, &downlink); err != nil {
		t.Fatalf("unmarshal downlink failed: %v", err)
	}

	if downlink.SessionId != "test-sess-123" {
		t.Fatalf("expected session_id test-sess-123, got %s", downlink.SessionId)
	}
	if downlink.Type != MessageTypeMCP {
		t.Fatalf("expected type mcp, got %s", downlink.Type)
	}
}

func TestSession_HandleMCPMessage_ValidationAndForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := &dummySender{}
	mcpClient := agentkit.NewDeviceMCPClient(sender)

	sess := &Session{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		sessionId:  "sess-mcp-test",
		generation: 1,
		mcpClient:  mcpClient,
	}

	// 1. 非法 session_id（不匹配）应被忽略
	mismatchMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "different-session-id",
		Payload:   json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`),
	}
	sess.handleMCPMessage(mismatchMsg)

	// 2. 空 payload 应被忽略
	emptyMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "sess-mcp-test",
		Payload:   nil,
	}
	sess.handleMCPMessage(emptyMsg)

	// 3. 非对象 payload 应被忽略
	nonObjMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "sess-mcp-test",
		Payload:   json.RawMessage(`"just a string"`),
	}
	sess.handleMCPMessage(nonObjMsg)

	// 4. 正常响应转发
	validMsg := &ClientMessage{
		Kind:      KindMCP,
		SessionId: "sess-mcp-test",
		Payload:   json.RawMessage(`{"jsonrpc":"2.0","id":100,"result":{"status":"ok"}}`),
	}
	sess.handleMCPMessage(validMsg)
}
