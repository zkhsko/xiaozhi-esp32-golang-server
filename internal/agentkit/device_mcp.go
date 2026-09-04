package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// MCP 协议与设备工具调用相关常量定义。
const (
	MCPProtocolVersion      = "2024-11-05"
	DefaultDiscoveryTimeout = 5 * time.Second
	DefaultToolCallTimeout  = 15 * time.Second
	MaxDiscoveryPages       = 16
	MaxDeviceTools          = 64
	MaxPendingRequests      = 32
	MaxToolNameLength       = 256
	MaxToolDescLength       = 4096
)

// 设备 MCP 相关的哨兵错误。
var (
	ErrMCPDisabled        = errors.New("device mcp is disabled")
	ErrMCPClientClosed    = errors.New("device mcp client closed")
	ErrMCPDiscoveryFailed = errors.New("device mcp discovery failed")
	ErrMCPTooManyPending  = errors.New("too many pending mcp requests")
	ErrMCPUnknownTool     = errors.New("device mcp tool not found or not authorized")
	ErrMCPCallTimedOut    = errors.New("device mcp tool call timed out")
	ErrMCPInvalidResponse = errors.New("invalid mcp response")
	ErrMCPInvalidSchema   = errors.New("invalid mcp tool input schema")
	ErrMCPVersionMismatch = errors.New("unsupported mcp protocol version")
)

// MCPPayloadSender 定义发送 JSON-RPC 消息载荷的最小接口。
type MCPPayloadSender interface {
	SendMCPPayload(ctx context.Context, payload json.RawMessage) error
}

// jsonRPCRequest 定义标准的 JSON-RPC 2.0 请求。
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Id      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse 定义标准的 JSON-RPC 2.0 响应。
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Id      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError 定义可选 code 的 JSON-RPC 错误结构。
type jsonRPCError struct {
	Code    *int64 `json:"code,omitempty"`
	Message string `json:"message"`
}

// mcpInitializeResult 定义 initialize 方法的响应体。
type mcpInitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    any    `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// mcpToolsListParams 定义 tools/list 分页请求参数。
type mcpToolsListParams struct {
	Cursor        string `json:"cursor"`
	WithUserTools bool   `json:"withUserTools"`
}

// mcpToolsListResult 定义 tools/list 方法的响应体。
type mcpToolsListResult struct {
	Tools      []mcpToolDefinition `json:"tools"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// mcpToolDefinition 定义固件上报的单个 MCP 工具元信息。
type mcpToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations *mcpAnnotations `json:"annotations,omitempty"`
}

// mcpAnnotations 定义工具的受众注解信息。
type mcpAnnotations struct {
	Audience []string `json:"audience,omitempty"`
}

// mcpToolsCallParams 定义 tools/call 请求参数。
type mcpToolsCallParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// mcpToolsCallResult 定义 tools/call 方法的响应体。
type mcpToolsCallResult struct {
	Content []any `json:"content"`
	IsError bool  `json:"isError,omitempty"`
}

// DeviceMCPClient 负责管理与单个设备之间的 MCP 协议交互、工具发现与串行调用。
type DeviceMCPClient struct {
	sender MCPPayloadSender

	ctx    context.Context
	cancel context.CancelFunc

	reqIdSeq atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan *jsonRPCResponse
	closed   bool
	disabled atomic.Bool

	callMu sync.Mutex

	toolCallTimeout time.Duration

	discoveryOnce sync.Once
	readyCh       chan struct{}
	discoveryErr  error
	tools         []ai.Tool
	toolsByName   map[string]ai.Tool
}

// NewDeviceMCPClient 创建 DeviceMCPClient 实例。
func NewDeviceMCPClient(sender MCPPayloadSender) *DeviceMCPClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &DeviceMCPClient{
		sender:      sender,
		ctx:         ctx,
		cancel:      cancel,
		pending:     make(map[int64]chan *jsonRPCResponse),
		readyCh:     make(chan struct{}),
		toolsByName: make(map[string]ai.Tool),
	}
}

// IsDisabled 返回当前设备 MCP 是否已被永久停用。
func (c *DeviceMCPClient) IsDisabled() bool {
	return c.disabled.Load()
}

// WaitReady 等待工具发现就绪或上下文取消。
func (c *DeviceMCPClient) WaitReady(ctx context.Context) error {
	select {
	case <-c.readyCh:
		return c.discoveryErr
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return ErrMCPClientClosed
	}
}

// Discover 执行 initialize 握手与分页 tools/list 发现。
func (c *DeviceMCPClient) Discover(ctx context.Context) error {
	var runErr error
	c.discoveryOnce.Do(func() {
		defer close(c.readyCh)

		if c.disabled.Load() {
			c.discoveryErr = ErrMCPDisabled
			runErr = c.discoveryErr
			return
		}

		discCtx, cancel := context.WithTimeout(ctx, DefaultDiscoveryTimeout)
		defer cancel()

		if err := c.runDiscovery(discCtx); err != nil {
			c.discoveryErr = err
			runErr = err
			c.mu.Lock()
			c.tools = nil
			c.toolsByName = make(map[string]ai.Tool)
			c.mu.Unlock()
			return
		}
	})

	if runErr != nil {
		return runErr
	}
	return c.discoveryErr
}

// runDiscovery 内部执行发现流程。
func (c *DeviceMCPClient) runDiscovery(ctx context.Context) error {
	// 1. 发送 initialize 请求
	var initResp mcpInitializeResult
	if err := c.callRPC(ctx, "initialize", map[string]any{"capabilities": map[string]any{}}, &initResp); err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	if initResp.ProtocolVersion != MCPProtocolVersion {
		return fmt.Errorf("%w: expected %s, got %s", ErrMCPVersionMismatch, MCPProtocolVersion, initResp.ProtocolVersion)
	}

	// 2. 分页拉取 tools/list
	discoveredTools := make([]ai.Tool, 0)
	toolMap := make(map[string]ai.Tool)
	seenCursors := make(map[string]bool)
	cursor := ""

	for page := 0; page < MaxDiscoveryPages; page++ {
		if cursor != "" {
			if seenCursors[cursor] {
				return fmt.Errorf("%w: loop detected on cursor %q", ErrMCPDiscoveryFailed, cursor)
			}
			seenCursors[cursor] = true
		}

		var listResp mcpToolsListResult
		params := mcpToolsListParams{
			Cursor:        cursor,
			WithUserTools: false,
		}

		if err := c.callRPC(ctx, "tools/list", params, &listResp); err != nil {
			return fmt.Errorf("tools/list failed at page %d: %w", page, err)
		}

		for _, item := range listResp.Tools {
			tool, ok := c.validateAndConvertTool(item)
			if !ok {
				continue
			}

			// 同名工具以第一页（先到的）为准
			if _, exists := toolMap[tool.Name]; exists {
				continue
			}

			if len(discoveredTools) >= MaxDeviceTools {
				return fmt.Errorf("%w: device tools exceeded max limit %d", ErrMCPDiscoveryFailed, MaxDeviceTools)
			}

			toolMap[tool.Name] = tool
			discoveredTools = append(discoveredTools, tool)
		}

		if listResp.NextCursor == "" {
			break
		}
		if listResp.NextCursor == cursor {
			return fmt.Errorf("%w: loop detected on duplicate cursor %q", ErrMCPDiscoveryFailed, listResp.NextCursor)
		}
		cursor = listResp.NextCursor

		if page == MaxDiscoveryPages-1 {
			return fmt.Errorf("%w: tools/list exceeded max page limit %d", ErrMCPDiscoveryFailed, MaxDiscoveryPages)
		}
	}

	c.mu.Lock()
	c.tools = discoveredTools
	c.toolsByName = toolMap
	c.mu.Unlock()

	return nil
}

// validateAndConvertTool 校验单个工具定义并转换为 ai.Tool。
func (c *DeviceMCPClient) validateAndConvertTool(item mcpToolDefinition) (ai.Tool, bool) {
	name := item.Name
	if name == "" || utf8.RuneCountInString(name) > MaxToolNameLength {
		return ai.Tool{}, false
	}
	if utf8.RuneCountInString(item.Description) > MaxToolDescLength {
		return ai.Tool{}, false
	}

	// 过滤 user-only 工具
	if item.Annotations != nil {
		for _, aud := range item.Annotations.Audience {
			if aud == "user" {
				return ai.Tool{}, false
			}
		}
	}

	// 规范化与校验 inputSchema，自适应固件扁平属性与标准 JSON Schema
	parameters, ok := normalizeInputSchema(item.InputSchema)
	if !ok {
		return ai.Tool{}, false
	}

	toolName := name
	tool := ai.Tool{
		Name:        toolName,
		Description: item.Description,
		Parameters:  parameters,
		Run: func(runCtx context.Context, input any) (any, error) {
			return c.CallTool(runCtx, toolName, input)
		},
	}

	return tool, true
}

// normalizeInputSchema 将固件或客户端上报的 inputSchema 规范化为标准 JSON Schema 对象。
func normalizeInputSchema(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}, true
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(raw, &schemaMap); err != nil {
		return nil, false
	}
	if schemaMap == nil || len(schemaMap) == 0 {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}, true
	}

	// 标准 JSON Schema 对象（顶层声明 type）
	if t, ok := schemaMap["type"].(string); ok {
		if t != "object" {
			return nil, false
		}
		if _, hasProps := schemaMap["properties"]; !hasProps {
			schemaMap["properties"] = map[string]any{}
		}
		return schemaMap, true
	}

	// 固件扁平属性字典，包裹为标准 JSON Schema 对象
	return map[string]any{
		"type":       "object",
		"properties": schemaMap,
	}, true
}

// Tools 返回当前已发现设备工具的不可变快照副本。
func (c *DeviceMCPClient) Tools() []ai.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.disabled.Load() || len(c.tools) == 0 {
		return nil
	}

	snapshot := make([]ai.Tool, len(c.tools))
	for i, t := range c.tools {
		toolName := t.Name
		snapshot[i] = ai.Tool{
			Name:        toolName,
			Description: t.Description,
			Parameters:  t.Parameters,
			Run: func(runCtx context.Context, input any) (any, error) {
				return c.CallTool(runCtx, toolName, input)
			},
		}
	}
	return snapshot
}

// CallTool 执行单个设备工具调用：
// 1. 权限校验：只允许调用已通过发现阶段白名单认证的工具；
// 2. 硬件保护：使用 callMu 保证设备端调用严格单并发执行（防止大模型并发 ToolCall 冲垮 MCU）；
// 3. 超时机制：单次调用最多等待 15 秒，若超时判定设备已无响应，自动将当前会话 MCP 永久停用；
// 4. 容错转换：设备明确返回的业务或 JSON-RPC 错误转换为结构化工具错误回填大模型，不中断语音通话。
func (c *DeviceMCPClient) CallTool(ctx context.Context, name string, input any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.disabled.Load() {
		return nil, ErrMCPDisabled
	}

	c.mu.Lock()
	_, authorized := c.toolsByName[name]
	c.mu.Unlock()

	if !authorized {
		return map[string]any{
			"isError": true,
			"content": []any{
				map[string]any{
					"type": "text",
					"text": fmt.Sprintf("tool %q not found or not authorized", name),
				},
			},
		}, nil
	}

	// 规范化入参为 JSON 对象
	arguments := normalizeArguments(input)

	// 严格单并发执行设备调用（互斥锁）
	c.callMu.Lock()
	defer c.callMu.Unlock()

	if c.disabled.Load() {
		return nil, ErrMCPDisabled
	}

	timeout := c.toolCallTimeout
	if timeout <= 0 {
		timeout = DefaultToolCallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	params := mcpToolsCallParams{
		Name:      name,
		Arguments: arguments,
	}

	var callResult mcpToolsCallResult
	err := c.callRPC(callCtx, "tools/call", params, &callResult)
	if err != nil {
		// 检查是否单次 15 秒超时：超时后永久停用本会话 MCP，返回未知状态
		if errors.Is(err, context.DeadlineExceeded) || (callCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil) {
			c.disabled.Store(true)
			return map[string]any{
				"isError": true,
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "tool execution result unknown: timeout after 15s",
					},
				},
			}, nil
		}

		// 上层 Context 取消或 Client 关闭，直接抛出 Go error
		if errors.Is(err, context.Canceled) || errors.Is(err, ErrMCPClientClosed) || ctx.Err() != nil {
			return nil, err
		}

		// 设备业务或 RPC 错误，以结构化错误回填给大模型
		return map[string]any{
			"isError": true,
			"content": []any{
				map[string]any{
					"type": "text",
					"text": err.Error(),
				},
			},
		}, nil
	}

	// 返回结构化结果
	if len(callResult.Content) == 0 {
		return map[string]any{
			"isError": callResult.IsError,
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "success",
				},
			},
		}, nil
	}

	return map[string]any{
		"isError": callResult.IsError,
		"content": callResult.Content,
	}, nil
}

// normalizeArguments 将入参转换为 map[string]any。
func normalizeArguments(input any) any {
	if input == nil {
		return map[string]any{}
	}
	switch v := input.(type) {
	case map[string]any:
		return v
	case string:
		if v == "" {
			return map[string]any{}
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err == nil && m != nil {
			return m
		}
		return map[string]any{}
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return map[string]any{}
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err == nil && m != nil {
			return m
		}
		return map[string]any{}
	}
}

// callRPC 执行底层的 JSON-RPC 调用并等待响应。
func (c *DeviceMCPClient) callRPC(ctx context.Context, method string, params any, resultDst any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.sender == nil {
		return errors.New("mcp payload sender is nil")
	}

	reqId := c.reqIdSeq.Add(1)

	respCh := make(chan *jsonRPCResponse, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrMCPClientClosed
	}
	if len(c.pending) >= MaxPendingRequests {
		c.mu.Unlock()
		return ErrMCPTooManyPending
	}
	c.pending[reqId] = respCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, reqId)
		c.mu.Unlock()
	}()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Id:      reqId,
		Method:  method,
		Params:  params,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal jsonrpc request: %w", err)
	}

	if err := c.sender.SendMCPPayload(ctx, reqBytes); err != nil {
		return fmt.Errorf("send mcp payload: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return ErrMCPClientClosed
	case resp, ok := <-respCh:
		if !ok || resp == nil {
			return ErrMCPClientClosed
		}

		if resp.Error != nil {
			if resp.Error.Message != "" {
				return errors.New(resp.Error.Message)
			}
			return errors.New("mcp rpc error")
		}

		if resultDst != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, resultDst); err != nil {
				return fmt.Errorf("%w: unmarshal result: %v", ErrMCPInvalidResponse, err)
			}
		}
		return nil
	}
}

// HandlePayload 接收来自设备的 JSON-RPC 响应载荷并分发给等待中的 pending 请求。
func (c *DeviceMCPClient) HandlePayload(payload json.RawMessage) {
	if len(payload) == 0 {
		return
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return
	}

	if resp.Id == nil {
		return
	}

	reqId := *resp.Id

	c.mu.Lock()
	ch, exists := c.pending[reqId]
	if exists {
		delete(c.pending, reqId)
	}
	c.mu.Unlock()

	if exists && ch != nil {
		select {
		case ch <- &resp:
		default:
		}
	}
}

// Close 关闭客户端并清理所有挂起的等待请求。
func (c *DeviceMCPClient) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pendingCopy := c.pending
	c.pending = make(map[int64]chan *jsonRPCResponse)
	c.mu.Unlock()

	c.cancel()

	for _, ch := range pendingCopy {
		close(ch)
	}
}
