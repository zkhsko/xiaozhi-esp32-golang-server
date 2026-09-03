package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// SessionTextSender 定义发送会话级文本下行消息的接口。
type SessionTextSender interface {
	SendTextSession(ctx context.Context, payload []byte) error
}

// DownlinkMCPMessage 定义服务端下发给设备的 MCP WebSocket 传输外层消息。
type DownlinkMCPMessage struct {
	SessionId string          `json:"session_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// MCPBridge 负责管理单个会话的设备 MCP 客户端生命周期、下行消息封包与上行响应分发。
type MCPBridge struct {
	mu          sync.RWMutex
	sessionId   string
	sender      SessionTextSender
	client      *agentkit.DeviceMCPClient
	logger      *slog.Logger
	diagLimiter *logger.RateLimiter
}

// NewMCPBridge 创建配置就绪的 MCPBridge 实例。
func NewMCPBridge(l *slog.Logger, diagLimiter *logger.RateLimiter) *MCPBridge {
	if l == nil {
		l = slog.Default()
	}
	return &MCPBridge{
		logger:      l,
		diagLimiter: diagLimiter,
	}
}

// Enable 在握手成功后初始化 DeviceMCPClient 并使用会话 Context 启动后台发现。
func (b *MCPBridge) Enable(sessCtx context.Context, sessionId string, sender SessionTextSender) {
	if sessCtx == nil {
		sessCtx = context.Background()
	}

	b.mu.Lock()
	b.sessionId = sessionId
	b.sender = sender
	client := agentkit.NewDeviceMCPClient(b)
	b.client = client
	b.mu.Unlock()

	go func() {
		discCtx, cancel := context.WithTimeout(sessCtx, 5*time.Second)
		defer cancel()
		if err := client.Discover(discCtx); err != nil {
			b.logger.Warn("device mcp discovery failed, continuing with builtin tools only",
				"session_id", sessionId,
				"error", err,
			)
		}
	}()
}

// SendMCPPayload 实现 agentkit.MCPPayloadSender 接口，将 MCP JSON-RPC 载荷封装并下发给设备。
func (b *MCPBridge) SendMCPPayload(ctx context.Context, payload json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	b.mu.RLock()
	sessionId := b.sessionId
	sender := b.sender
	b.mu.RUnlock()

	if sender == nil {
		return errors.New("mcp sender is not available")
	}
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

	return sender.SendTextSession(ctx, data)
}

// HandleInbound 处理客户端上行的 MCP 消息。
func (b *MCPBridge) HandleInbound(sessionId string, msg *ClientMessage) {
	if msg == nil {
		return
	}

	b.mu.RLock()
	client := b.client
	currentSessionId := b.sessionId
	b.mu.RUnlock()

	if client == nil {
		b.logDiag("mcp message ignored: device mcp not enabled for this session", sessionId)
		return
	}

	if msg.SessionId != "" && msg.SessionId != currentSessionId {
		b.logDiag("mcp message ignored: session_id mismatch", sessionId,
			"expected", currentSessionId,
			"actual", msg.SessionId,
		)
		return
	}

	if len(msg.Payload) == 0 {
		b.logDiag("mcp message ignored: empty payload", sessionId)
		return
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(msg.Payload, &obj); err != nil || obj == nil {
		b.logDiag("mcp message ignored: payload is not a valid json object", sessionId)
		return
	}

	client.HandlePayload(msg.Payload)
}

// WaitReady 等待设备 MCP 发现完成或超时。
func (b *MCPBridge) WaitReady(ctx context.Context) error {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	if client == nil || client.IsDisabled() {
		return agentkit.ErrMCPDisabled
	}
	return client.WaitReady(ctx)
}

// Tools 获取当前设备已就绪的 MCP 工具列表。
func (b *MCPBridge) Tools() []ai.Tool {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	if client == nil || client.IsDisabled() {
		return nil
	}
	return client.Tools()
}

// CallTool 执行设备 MCP 工具调用。
func (b *MCPBridge) CallTool(ctx context.Context, name string, input any) (any, error) {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	if client == nil || client.IsDisabled() {
		return nil, agentkit.ErrMCPDisabled
	}
	return client.CallTool(ctx, name, input)
}

// IsEnabled 返回当前会话是否启用了设备 MCP。
func (b *MCPBridge) IsEnabled() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.client != nil && !b.client.IsDisabled()
}

// Close 安全关闭 MCP 客户端并唤醒所有等待中的请求。
func (b *MCPBridge) Close() {
	b.mu.Lock()
	client := b.client
	b.client = nil
	b.sender = nil
	b.mu.Unlock()

	if client != nil {
		client.Close()
	}
}

// logDiag 限频记录诊断日志。
func (b *MCPBridge) logDiag(msg string, sessionId string, args ...any) {
	if b.diagLimiter != nil && !b.diagLimiter.Allow() {
		return
	}
	allArgs := append([]any{"session_id", sessionId}, args...)
	b.logger.Warn(msg, allArgs...)
}
