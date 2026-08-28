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
	Id      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// mcpResponse 定义设备返回的 JSON-RPC 2.0 响应对象。
type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Id      int64           `json:"id"`
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

	reqId := s.mcpSeq.Add(1)
	respCh := make(chan *mcpResponse, 1)

	s.mcpMu.Lock()
	if s.pendingMCP == nil {
		s.pendingMCP = make(map[int64]chan *mcpResponse)
	}
	s.pendingMCP[reqId] = respCh
	s.mcpMu.Unlock()

	defer func() {
		s.mcpMu.Lock()
		delete(s.pendingMCP, reqId)
		s.mcpMu.Unlock()
	}()

	rpcReq := mcpRequest{
		JSONRPC: "2.0",
		Id:      reqId,
		Method:  method,
		Params:  params,
	}

	payloadBytes, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshal mcp payload: %w", err)
	}

	wrapper := struct {
		SessionId string          `json:"session_id,omitempty"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}{
		SessionId: s.SessionId(),
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
		return nil, fmt.Errorf("%w (%s, id=%d, timeout=%v)", ErrMCPTimeout, method, reqId, timeout)
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
			"session_id", s.SessionId(),
			"error", err,
		)
		return
	}

	if resp.Id == 0 {
		// 可能是 notification 或无 id 消息，忽略
		return
	}

	s.mcpMu.Lock()
	ch, ok := s.pendingMCP[resp.Id]
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
	s.logger.Info("starting mcp tools discovery", "session_id", s.SessionId())

	// 1. 发送 initialize 请求
	initParams := map[string]any{
		"capabilities": map[string]any{},
	}
	if _, err := s.sendMCPRequest(ctx, "initialize", initParams); err != nil {
		s.logger.Warn("mcp initialize failed",
			"session_id", s.SessionId(),
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
				"session_id", s.SessionId(),
				"page", page,
				"error", err,
			)
			break
		}

		var listResult mcpToolsListResult
		if err := json.Unmarshal(resp.Result, &listResult); err != nil {
			s.logger.Warn("failed to unmarshal mcp tools/list result",
				"session_id", s.SessionId(),
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
		"session_id", s.SessionId(),
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
		"session_id", s.SessionId(),
		"tool_name", name,
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
		"session_id", s.SessionId(),
		"tool_name", name,
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

// DeviceControlInstruction 相关控制提示词模板常量。
const (
	// DeviceControlInstructionHeader 控制提示词引导头。
	DeviceControlInstructionHeader = `【重要工具调用规范】
你拥有直接控制设备硬件、查询实时状态与调用服务端能力的工具权限。
当用户的请求或意图与可用工具的功能相匹配时，你必须坚决直接发起工具调用（Tool Call），严禁拒绝，严禁仅以纯文本口头假装执行。`

	// DeviceControlInstructionFooter 控制提示词反馈指导尾。
	DeviceControlInstructionFooter = `【工具调用执行准则】
1. 意图匹配即调用：凡用户请求涉及控制外设、调节参数（如音量/亮度）、查询实时状态或数据（如时间/电量/设备信息）、结束退出对话等工具支持的场景，必须坚决调用对应的工具，不得只用文字应答。
2. 先查询后控制：若调节操作需要基于设备当前状态（如在当前音量基础上调高/调低），且当前数值未知，第1步必须先调用对应的查询/状态工具获取实时值，待返回结果后再发起控制工具调用。
3. 真实反馈与简洁口语：严禁在文本中向用户输出工具调用的 JSON 格式或函数名称。工具执行完成后，根据工具实际返回的结果，用简短、自然、亲切的中文口语向用户反馈执行结果。`
)

// FormatDeviceToolsPrompt 将控制提示词和工具列表整理为清晰易懂的提示词段落。
func FormatDeviceToolsPrompt(tools []ai.Tool) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(DeviceControlInstructionHeader)
	sb.WriteString("\n\n【可用工具列表】\n")
	for _, t := range tools {
		sb.WriteString("- ")
		sb.WriteString(t.Name)
		if t.Description != "" {
			sb.WriteString(": ")
			sb.WriteString(t.Description)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(DeviceControlInstructionFooter)

	return sb.String()
}

// buildSystemPromptLocked 在持有锁的前提下计算当前会话实际生效的系统提示词。
// 将控制提示词与工具列表（设备 MCP 工具在前，服务端工具拼接在后）整理为段落追加到基础系统提示词最后。
func (s *Session) buildSystemPromptLocked() string {
	basePrompt := s.systemPrompt

	serverTools := DefaultServerTools()
	deviceTools := s.mcpTools

	totalLen := len(deviceTools) + len(serverTools)
	if totalLen == 0 {
		return basePrompt
	}

	allTools := make([]ai.Tool, 0, totalLen)
	seen := make(map[string]struct{}, totalLen)

	// 1. 设备 MCP 工具在前
	for _, t := range deviceTools {
		if _, exists := seen[t.Name]; !exists {
			seen[t.Name] = struct{}{}
			allTools = append(allTools, t)
		}
	}

	// 2. 服务端工具拼接在 MCP 工具之后
	for _, t := range serverTools {
		if _, exists := seen[t.Name]; !exists {
			seen[t.Name] = struct{}{}
			allTools = append(allTools, t)
		}
	}

	toolsPrompt := FormatDeviceToolsPrompt(allTools)
	if basePrompt == "" {
		return toolsPrompt
	}
	return basePrompt + "\n\n" + toolsPrompt
}

// SystemPrompt 返回当前会话实际生效的完整系统提示词（含根据设备上报工具追加的控制指令）。
func (s *Session) SystemPrompt() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.buildSystemPromptLocked()
}
