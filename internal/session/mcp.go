package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// MCP 协议相关的常量定义。
const (
	// DefaultMCPRequestTimeout 默认 MCP 单次请求超时时间。
	DefaultMCPRequestTimeout = 10 * time.Second

	// DefaultMCPMaxListPages 防止死循环拉取 tools/list 的最大分页数限制。
	DefaultMCPMaxListPages = 10

	// DefaultMaxToolCallIterations 单轮对话中大模型最多连续调用工具的迭代次数。
	DefaultMaxToolCallIterations = 5
)

// 哨兵错误。
var (
	ErrMCPTimeout      = errors.New("mcp request timeout")
	ErrMCPToolNotFound = errors.New("mcp tool not found")
	ErrMCPCallFailed   = errors.New("mcp tool call failed")
)

// MCPTool 表示客户端设备上报的工具定义。
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpRequest 定义下发给设备的 JSON-RPC 2.0 请求对象。
type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// mcpResponse 定义设备返回的 JSON-RPC 2.0 响应对象。
type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

// mcpError 定义 JSON-RPC 2.0 错误结构。
type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpToolsListResult 定义 tools/list 返回的 result 结构。
type mcpToolsListResult struct {
	Tools      []MCPTool `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// mcpToolCallContentItem 定义 tools/call 执行结果内容项。
type mcpToolCallContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// mcpToolCallResult 定义 tools/call 返回的 result 结构。
type mcpToolCallResult struct {
	Content []mcpToolCallContentItem `json:"content"`
	IsError bool                     `json:"isError"`
}

// sendMCPRequest 异步发送 MCP 请求并同步等待设备的响应。
func (s *Session) sendMCPRequest(ctx context.Context, method string, params any) (*mcpResponse, error) {
	if s.writer == nil {
		return nil, errors.New("nil session writer")
	}

	reqID := s.mcpSeq.Add(1)
	respCh := make(chan *mcpResponse, 1)

	s.mcpMu.Lock()
	if s.pendingMCP == nil {
		s.pendingMCP = make(map[int64]chan *mcpResponse)
	}
	s.pendingMCP[reqID] = respCh
	s.mcpMu.Unlock()

	defer func() {
		s.mcpMu.Lock()
		delete(s.pendingMCP, reqID)
		s.mcpMu.Unlock()
	}()

	rpcReq := mcpRequest{
		JSONRPC: "2.0",
		ID:      reqID,
		Method:  method,
		Params:  params,
	}

	payloadBytes, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshal mcp payload: %w", err)
	}

	wrapper := struct {
		SessionID string          `json:"session_id,omitempty"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}{
		SessionID: s.SessionID(),
		Type:      MessageTypeMCP,
		Payload:   payloadBytes,
	}

	msgBytes, err := json.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal mcp message: %w", err)
	}

	if err := s.sendTextMessage(msgBytes); err != nil {
		return nil, fmt.Errorf("write mcp text: %w", err)
	}

	timeout := DefaultMCPRequestTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		if resp == nil {
			return nil, errors.New("nil mcp response received")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp rpc error (code=%d): %s", resp.Error.Code, resp.Error.Message)
		}
		return resp, nil
	case <-timer.C:
		return nil, fmt.Errorf("%w (%s, id=%d, timeout=%v)", ErrMCPTimeout, method, reqID, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

// handleIncomingMCP 解析客户端发来的 MCP 消息并分发给等待中的请求。
func (s *Session) handleIncomingMCP(msg *ClientMessage) {
	if msg == nil || len(msg.MCPPayload) == 0 {
		return
	}

	var resp mcpResponse
	if err := json.Unmarshal(msg.MCPPayload, &resp); err != nil {
		s.logger.Warn("failed to parse incoming mcp payload",
			"session_id", s.SessionID(),
			"error", err,
		)
		return
	}

	if resp.ID == 0 {
		// 可能是 notification 或无 id 消息，忽略
		return
	}

	s.mcpMu.Lock()
	ch, ok := s.pendingMCP[resp.ID]
	s.mcpMu.Unlock()

	if ok && ch != nil {
		select {
		case ch <- &resp:
		default:
		}
	}
}

// discoverMCPTools 在会话建立后探测并拉取设备支持的所有 MCP Tools。
func (s *Session) discoverMCPTools(ctx context.Context) {
	s.logger.Info("starting mcp tools discovery", "session_id", s.SessionID())

	// 1. 发送 initialize 请求
	initParams := map[string]any{
		"capabilities": map[string]any{},
	}
	if _, err := s.sendMCPRequest(ctx, "initialize", initParams); err != nil {
		s.logger.Warn("mcp initialize failed",
			"session_id", s.SessionID(),
			"error", err,
		)
		return
	}

	// 2. 分页拉取 tools/list
	var allTools []MCPTool
	cursor := ""
	for page := 0; page < DefaultMCPMaxListPages; page++ {
		listParams := map[string]any{
			"cursor": cursor,
		}
		resp, err := s.sendMCPRequest(ctx, "tools/list", listParams)
		if err != nil {
			s.logger.Warn("mcp tools/list failed",
				"session_id", s.SessionID(),
				"page", page,
				"error", err,
			)
			break
		}

		var listResult mcpToolsListResult
		if err := json.Unmarshal(resp.Result, &listResult); err != nil {
			s.logger.Warn("failed to unmarshal mcp tools/list result",
				"session_id", s.SessionID(),
				"error", err,
			)
			break
		}

		allTools = append(allTools, listResult.Tools...)
		if listResult.NextCursor == "" {
			break
		}
		cursor = listResult.NextCursor
	}

	// 3. 转换并缓存为 ai.Tool 列表
	aiTools := make([]ai.Tool, 0, len(allTools))
	toolNames := make([]string, 0, len(allTools))
	for _, t := range allTools {
		aiTools = append(aiTools, ai.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
		toolNames = append(toolNames, t.Name)
	}

	s.mu.Lock()
	s.mcpTools = aiTools
	s.mu.Unlock()

	s.logger.Info("mcp tools discovery completed",
		"session_id", s.SessionID(),
		"tool_count", len(aiTools),
		"tools", strings.Join(toolNames, ", "),
	)
}

// isMCPToolAllowed 检查指定名称的工具是否在当前会话已发现并授权的白名单中。
func (s *Session) isMCPToolAllowed(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.mcpTools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// callMCPTool 向设备下发 tools/call 请求并返回结果文本。
func (s *Session) callMCPTool(ctx context.Context, name string, argumentsJSON string) (string, error) {
	if !s.isMCPToolAllowed(name) {
		return "", fmt.Errorf("%w: %s", ErrMCPToolNotFound, name)
	}

	var args any
	if argumentsJSON != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(argumentsJSON), &parsed); err == nil {
			args = parsed
		} else {
			args = argumentsJSON
		}
	} else {
		args = map[string]any{}
	}

	callParams := map[string]any{
		"name":      name,
		"arguments": args,
	}

	s.logger.Info("executing mcp tool call",
		"session_id", s.SessionID(),
		"tool_name", name,
		"arguments", argumentsJSON,
	)

	resp, err := s.sendMCPRequest(ctx, "tools/call", callParams)
	if err != nil {
		return "", fmt.Errorf("call mcp tool %q: %w", name, err)
	}

	var callResult mcpToolCallResult
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		// 若返回不是标准 result 结构，直接返回原始字符串
		return string(resp.Result), nil
	}

	if callResult.IsError {
		errMsg := "tool returned error"
		if len(callResult.Content) > 0 && callResult.Content[0].Text != "" {
			errMsg = callResult.Content[0].Text
		}
		return "", fmt.Errorf("%w: %s", ErrMCPCallFailed, errMsg)
	}

	var sb strings.Builder
	for i, item := range callResult.Content {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(item.Text)
	}

	resultText := sb.String()
	if resultText == "" {
		resultText = "success"
	}

	s.logger.Info("mcp tool call executed successfully",
		"session_id", s.SessionID(),
		"tool_name", name,
		"result", resultText,
	)

	return resultText, nil
}

// getMCPTools 返回当前会话已发现的 MCP Tools 列表副本。
func (s *Session) getMCPTools() []ai.Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.mcpTools) == 0 {
		return nil
	}
	copied := make([]ai.Tool, len(s.mcpTools))
	copy(copied, s.mcpTools)
	return copied
}
