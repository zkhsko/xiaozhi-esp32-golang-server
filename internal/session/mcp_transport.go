package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// DownlinkMCPMessage 定义服务端下发给设备的 MCP WebSocket 传输外层消息。
// 由于 ESP32 运行在 NAT 后且复用单个 WebSocket 连接，底层 MCP JSON-RPC 载荷必须通过包含 session_id
// 和 type: "mcp" 的智子协议帧下发。
type DownlinkMCPMessage struct {
	SessionId string          `json:"session_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// SendMCPPayload 实现 agentkit.MCPPayloadSender 接口，负责将 MCP JSON-RPC 载荷封装并下发给设备。
// 该方法实现了 AgentKit 业务层与 WebSocket 传输层的解耦，AgentKit 只需关心纯净的 JSON-RPC 协议。
func (s *Session) SendMCPPayload(ctx context.Context, payload json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return errors.New("session is closed")
	}

	sessionId := s.SessionId()
	if sessionId == "" {
		return errors.New("session id is empty")
	}

	msg := DownlinkMCPMessage{
		SessionId: sessionId,
		Type:      MessageTypeMCP,
		Payload:   payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal downlink mcp message: %w", err)
	}

	return s.sendTextMessage(data)
}

// handleMCPMessage 处理客户端上行的 MCP 消息：
// 1. 校验设备是否已开启 MCP；
// 2. 校验 session_id 与当前会话是否一致；
// 3. 校验 payload 为合法非空 JSON 对象；
// 4. 防御性设计：MCP 协议层的单次格式错误或迟到响应仅丢弃并限频记录，绝不导致核心语音会话断开。
func (s *Session) handleMCPMessage(msg *ClientMessage) {
	if msg == nil {
		return
	}

	s.mu.RLock()
	client := s.mcpClient
	sessionId := s.sessionId
	s.mu.RUnlock()

	if client == nil {
		s.logDiag("mcp message ignored: device mcp not enabled for this session")
		return
	}

	// 校验 session_id 是否匹配
	if msg.SessionId != "" && msg.SessionId != sessionId {
		s.logDiag("mcp message ignored: session_id mismatch",
			"expected", sessionId,
			"actual", msg.SessionId,
		)
		return
	}

	// 校验 payload 必须是非空 JSON 对象
	if len(msg.Payload) == 0 {
		s.logDiag("mcp message ignored: empty payload")
		return
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(msg.Payload, &obj); err != nil || obj == nil {
		s.logDiag("mcp message ignored: payload is not a valid json object")
		return
	}

	// 转交 DeviceMCPClient 唤醒匹配的 pending JSON-RPC 请求
	client.HandlePayload(msg.Payload)
}
