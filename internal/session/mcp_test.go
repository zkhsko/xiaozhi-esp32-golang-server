package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"

	"github.com/coder/websocket"
)

// mockMCPLLMStream 模拟支持 ToolCalls 的 LLMStream。
type mockMCPLLMStream struct {
	chunks    []string
	chunkIdx  int
	toolCalls []ai.ToolCall
	mu        sync.Mutex
	closed    bool
}

func newMockMCPLLMStream(chunks []string, toolCalls []ai.ToolCall) *mockMCPLLMStream {
	return &mockMCPLLMStream{
		chunks:    chunks,
		toolCalls: toolCalls,
	}
}

func (s *mockMCPLLMStream) Recv() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("stream closed")
	}
	if s.chunkIdx < len(s.chunks) {
		c := s.chunks[s.chunkIdx]
		s.chunkIdx++
		return c, nil
	}
	return "", io.EOF
}

func (s *mockMCPLLMStream) ToolCalls() []ai.ToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolCalls
}

func (s *mockMCPLLMStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// mockMCPLLMClient 模拟多步 LLMClient（第一步返回 ToolCall，第二步返回最终回答）。
type mockMCPLLMClient struct {
	mu           sync.Mutex
	streams      []ai.LLMStream
	callIdx      int
	lastMessages [][]ai.Message
	lastTools    [][]ai.Tool
}

func (c *mockMCPLLMClient) CreateStream(ctx context.Context, messages []ai.Message, tools []ai.Tool) (ai.LLMStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	copiedMsgs := make([]ai.Message, len(messages))
	copy(copiedMsgs, messages)
	c.lastMessages = append(c.lastMessages, copiedMsgs)

	copiedTools := make([]ai.Tool, len(tools))
	copy(copiedTools, tools)
	c.lastTools = append(c.lastTools, copiedTools)

	if c.callIdx < len(c.streams) {
		st := c.streams[c.callIdx]
		c.callIdx++
		return st, nil
	}
	return newMockMCPLLMStream([]string{"默认兜底回复。"}, nil), nil
}

// TestMCP_DiscoverToolsPagination 验证工具发现流程能够正确支持分页拉取并转换为 ai.Tool。
func TestMCP_DiscoverToolsPagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())
	sess := NewSession(ctx, Options{Writer: writer, Logger: slog.Default()})
	defer sess.Close()
	go func() { _ = sess.Run() }()

	// 模拟设备端响应：后台 goroutine 监听并响应 initialize 和两页 tools/list
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			rawMsgs := conn.TextMessages()
			if len(rawMsgs) > 0 {
				lastMsg := rawMsgs[len(rawMsgs)-1]
				var req struct {
					Type    string `json:"type"`
					Payload struct {
						JSONRPC string         `json:"jsonrpc"`
						ID      int64          `json:"id"`
						Method  string         `json:"method"`
						Params  map[string]any `json:"params"`
					} `json:"payload"`
				}
				if err := json.Unmarshal(lastMsg, &req); err == nil && req.Type == MessageTypeMCP {
					switch req.Payload.Method {
					case "initialize":
						resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"serverInfo":{"name":"test-board","version":"1.0"}}}`, req.Payload.ID)
						sess.PostClientText(&ClientMessage{
							Kind:       KindMCP,
							MCPPayload: json.RawMessage(resp),
						})
					case "tools/list":
						cursor, _ := req.Payload.Params["cursor"].(string)
						if cursor == "" {
							// 第一页，返回 tool1 并提供 nextCursor
							resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"tool1","description":"desc1","inputSchema":{"type":"object"}}],"nextCursor":"page2"}}`, req.Payload.ID)
							sess.PostClientText(&ClientMessage{
								Kind:       KindMCP,
								MCPPayload: json.RawMessage(resp),
							})
						} else if cursor == "page2" {
							// 第二页，返回 tool2 且 nextCursor 为空
							resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"tool2","description":"desc2","inputSchema":{"type":"object"}}],"nextCursor":""}}`, req.Payload.ID)
							sess.PostClientText(&ClientMessage{
								Kind:       KindMCP,
								MCPPayload: json.RawMessage(resp),
							})
						}
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	sess.discoverMCPTools(ctx)

	tools := sess.getMCPTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools discovered, got %d", len(tools))
	}
	if tools[0].Name != "tool1" || tools[1].Name != "tool2" {
		t.Errorf("discovered tools mismatch: %+v", tools)
	}
}

// TestMCP_ToolCallExecutionAndLLMFeedback 验证完整的“LLM ToolCall -> MCP 下发 -> 设备执行 -> 结果注入 -> 二次 LLM 生成”闭环。
func TestMCP_ToolCallExecutionAndLLMFeedback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	// 设置当前发现的工具
	tools := []ai.Tool{
		{
			Name:        "self.audio_speaker.set_volume",
			Description: "Set volume",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	// 第一次调用返回 ToolCall
	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_vol_1",
			Name:      "self.audio_speaker.set_volume",
			Arguments: `{"volume":80}`,
		},
	})
	// 第二次调用返回自然语言确认回答
	stream2 := newMockMCPLLMStream([]string{"已经将音量", "调整为80了。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
	}

	ttsClient := newMockTTSStream(nil)
	ttsClientWrapper := newMockTTSClient(ttsClient, nil)

	sess := NewSession(ctx, Options{
		Writer: writer,
		Config: &config.Config{
			Session: config.SessionConfig{
				SystemPrompt: "你是小智助手。",
			},
		},
		LLMClient: llmClient,
		TTSClient: ttsClientWrapper,
		Logger:    slog.Default(),
	})
	defer sess.Close()
	go func() { _ = sess.Run() }()

	sess.mu.Lock()
	sess.sessionID = "test-mcp-session"
	sess.mcpTools = tools
	sess.mu.Unlock()

	// 后台模拟设备执行 MCP Tool
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			rawMsgs := conn.TextMessages()
			for _, m := range rawMsgs {
				var req struct {
					Type    string `json:"type"`
					Payload struct {
						JSONRPC string `json:"jsonrpc"`
						ID      int64  `json:"id"`
						Method  string `json:"method"`
					} `json:"payload"`
				}
				if err := json.Unmarshal(m, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
					resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"true"}],"isError":false}}`, req.Payload.ID)
					sess.PostClientText(&ClientMessage{
						Kind:       KindMCP,
						MCPPayload: json.RawMessage(resp),
					})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	sess.orchestrateLLMAndTTS(ctx, 1, "把音量调到80")

	// 验证 LLM 一共被调用了 2 次
	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}

	// 第一次调用带有 tools
	if len(llmClient.lastTools[0]) != 1 || llmClient.lastTools[0][0].Name != "self.audio_speaker.set_volume" {
		t.Errorf("first LLM call tools mismatch: %+v", llmClient.lastTools[0])
	}

	// 第二次调用上下文包含 RoleAssistant(ToolCalls) 和 RoleTool(执行结果)
	secondMsgs := llmClient.lastMessages[1]
	var foundAssistantToolCall, foundToolResponse bool
	for _, m := range secondMsgs {
		if m.Role == ai.RoleAssistant && len(m.ToolCalls) > 0 && m.ToolCalls[0].Name == "self.audio_speaker.set_volume" {
			foundAssistantToolCall = true
		}
		if m.Role == ai.RoleTool && m.ToolCallID == "call_vol_1" && m.Content == "true" {
			foundToolResponse = true
		}
	}

	if !foundAssistantToolCall {
		t.Errorf("second LLM call missing assistant tool call message")
	}
	if !foundToolResponse {
		t.Errorf("second LLM call missing tool response message")
	}

	// 验证 TTS 下发了第二轮的自然语言确认文本
	sentSentences := ttsClient.sentSentences
	if len(sentSentences) == 0 {
		t.Fatalf("expected TTS sentences, got none")
	}
}

// TestMCP_ToolCallErrorResponse 验证设备端返回 isError: true 时能够妥善捕获错误并通知 LLM。
func TestMCP_ToolCallErrorResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	tools := []ai.Tool{
		{
			Name: "self.broken_tool",
		},
	}

	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:   "call_fail_1",
			Name: "self.broken_tool",
		},
	})
	stream2 := newMockMCPLLMStream([]string{"抱歉，设备执行失败。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
	}

	ttsClient := newMockTTSStream(nil)
	ttsClientWrapper := newMockTTSClient(ttsClient, nil)

	sess := NewSession(ctx, Options{Writer: writer, LLMClient: llmClient, TTSClient: ttsClientWrapper, Logger: slog.Default()})
	defer sess.Close()
	go func() { _ = sess.Run() }()

	sess.mu.Lock()
	sess.sessionID = "test-fail-session"
	sess.mcpTools = tools
	sess.mu.Unlock()

	// 设备返回 isError: true
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			rawMsgs := conn.TextMessages()
			for _, m := range rawMsgs {
				var req struct {
					Type    string `json:"type"`
					Payload struct {
						JSONRPC string `json:"jsonrpc"`
						ID      int64  `json:"id"`
						Method  string `json:"method"`
					} `json:"payload"`
				}
				if err := json.Unmarshal(m, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
					resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"device motor stuck"}],"isError":true}}`, req.Payload.ID)
					sess.PostClientText(&ClientMessage{
						Kind:       KindMCP,
						MCPPayload: json.RawMessage(resp),
					})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	sess.orchestrateLLMAndTTS(ctx, 1, "执行动作")

	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}

	// 验证第二轮向 LLM 传递了错误信息
	secondMsgs := llmClient.lastMessages[1]
	var foundErrorResponse bool
	for _, m := range secondMsgs {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_fail_1" {
			foundErrorResponse = true
		}
	}
	if !foundErrorResponse {
		t.Errorf("second LLM call missing tool error response message")
	}
}

// TestMCP_ConcurrentResponseAndSessionClose 验证在高并发下 MCP 响应分发与会话关闭并发触发时不发生 panic，且请求方安全退出。
func TestMCP_ConcurrentResponseAndSessionClose(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

		conn := newHistoryWSConn()
		writer := NewWriter(ctx, conn, 200, slog.Default())
		sess := NewSession(ctx, Options{Writer: writer, Logger: slog.Default()})

		const concurrentRequests = 20
		var wg sync.WaitGroup
		wg.Add(concurrentRequests)

		for i := 0; i < concurrentRequests; i++ {
			go func() {
				defer wg.Done()
				_, _ = sess.sendMCPRequest(ctx, "tools/call", map[string]any{"name": "test_tool"})
			}()
		}

		// 并发分发模拟设备返回的 MCP 响应
		go func() {
			for i := int64(1); i <= concurrentRequests; i++ {
				respJSON := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}]}}`, i)
				sess.handleIncomingMCP(&ClientMessage{
					Kind:       KindMCP,
					MCPPayload: json.RawMessage(respJSON),
				})
			}
		}()

		// 并发关闭会话
		go func() {
			sess.closeWithReason(websocket.StatusNormalClosure, "concurrent test close")
		}()

		wg.Wait()
		sess.Close()
		cancel()
	}
}

// TestMCP_HandleIncomingAndCloseDirectRace 专门验证取出通道与会话关闭清理之间的并发安全性，断言无 send on closed channel panic。
func TestMCP_HandleIncomingAndCloseDirectRace(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

		conn := newHistoryWSConn()
		writer := NewWriter(ctx, conn, 100, slog.Default())
		sess := NewSession(ctx, Options{Writer: writer, Logger: slog.Default()})

		respCh := make(chan *mcpResponse, 1)
		reqID := int64(100 + iteration)

		sess.mcpMu.Lock()
		sess.pendingMCP[reqID] = respCh
		sess.mcpMu.Unlock()

		var wg sync.WaitGroup
		wg.Add(3)

		// 协程 1: handleIncomingMCP 分发响应
		go func() {
			defer wg.Done()
			respJSON := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"done"}]}}`, reqID)
			sess.handleIncomingMCP(&ClientMessage{
				Kind:       KindMCP,
				MCPPayload: json.RawMessage(respJSON),
			})
		}()

		// 协程 2: closeWithReason 并发清空 pendingMCP
		go func() {
			defer wg.Done()
			sess.closeWithReason(websocket.StatusNormalClosure, "race test close")
		}()

		// 协程 3: 请求方安全等待响应或随上下文退出
		go func() {
			defer wg.Done()
			select {
			case <-respCh:
			case <-sess.ctx.Done():
			case <-ctx.Done():
			}
		}()

		wg.Wait()
		sess.Close()
		cancel()
	}
}

// TestMCP_ToolCallUnauthorizedRejected 验证大模型返回未在当前会话白名单中的敏感工具时，服务端拒绝下发并向模型上下文注入未授权错误说明。
func TestMCP_ToolCallUnauthorizedRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	// 会话仅声明了音量调节工具，未授权 self.reboot
	tools := []ai.Tool{
		{
			Name:        "self.audio_speaker.set_volume",
			Description: "Set volume",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	// 第一次调用返回未授权的 self.reboot 工具调用
	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_reboot_1",
			Name:      "self.reboot",
			Arguments: `{"delay":0}`,
		},
	})
	// 第二次调用根据未授权错误返回说明
	stream2 := newMockMCPLLMStream([]string{"抱歉，我没有权限重启设备。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
	}

	ttsClient := newMockTTSStream(nil)
	ttsClientWrapper := newMockTTSClient(ttsClient, nil)

	sess := NewSession(ctx, Options{
		Writer: writer,
		Config: &config.Config{
			Session: config.SessionConfig{
				SystemPrompt: "你是小智助手。",
			},
		},
		LLMClient: llmClient,
		TTSClient: ttsClientWrapper,
		Logger:    slog.Default(),
	})
	defer sess.Close()
	go func() { _ = sess.Run() }()

	sess.mu.Lock()
	sess.sessionID = "test-unauth-session"
	sess.mcpTools = tools
	sess.mu.Unlock()

	sess.orchestrateLLMAndTTS(ctx, 1, "请重启设备")

	// 验证 LLM 经历了 2 次调用
	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}

	// 验证未向客户端 WebSocket 下发任何 tools/call 请求
	for _, rawMsg := range conn.TextMessages() {
		var req struct {
			Type    string `json:"type"`
			Payload struct {
				Method string `json:"method"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(rawMsg, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
			t.Fatalf("unexpected mcp tools/call sent to device for unauthorized tool")
		}
	}

	// 验证第二次 LLM 调用接收到了未授权错误信息
	secondMsgs := llmClient.lastMessages[1]
	var foundErrorResponse bool
	for _, m := range secondMsgs {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_reboot_1" {
			foundErrorResponse = true
			if !strings.Contains(m.Content, "not authorized") {
				t.Errorf("expected tool response to contain 'not authorized', got %q", m.Content)
			}
		}
	}
	if !foundErrorResponse {
		t.Errorf("second LLM call missing unauthorized tool error message")
	}

	// 验证 TTS 下发了第二轮回复
	sentSentences := ttsClient.sentSentences
	if len(sentSentences) == 0 {
		t.Fatalf("expected TTS sentences after rejection, got none")
	}
}

// TestMCP_ToolCallMixedAuthorization 验证在大模型同时返回授权与未授权工具时，服务端仅下发授权工具并正确注入全部结果。
func TestMCP_ToolCallMixedAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	// 会话仅授权音量调节工具
	tools := []ai.Tool{
		{
			Name:        "self.audio_speaker.set_volume",
			Description: "Set volume",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	// 第一次调用返回两个工具：一个未授权(self.reboot)，一个已授权(self.audio_speaker.set_volume)
	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_reboot_1",
			Name:      "self.reboot",
			Arguments: `{}`,
		},
		{
			ID:        "call_vol_1",
			Name:      "self.audio_speaker.set_volume",
			Arguments: `{"volume":50}`,
		},
	})
	stream2 := newMockMCPLLMStream([]string{"未能重启设备，但音量已调整为50。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
	}

	ttsClient := newMockTTSStream(nil)
	ttsClientWrapper := newMockTTSClient(ttsClient, nil)

	sess := NewSession(ctx, Options{Writer: writer, LLMClient: llmClient, TTSClient: ttsClientWrapper, Logger: slog.Default()})
	defer sess.Close()
	go func() { _ = sess.Run() }()

	sess.mu.Lock()
	sess.sessionID = "test-mixed-session"
	sess.mcpTools = tools
	sess.mu.Unlock()

	// 模拟设备仅响应已授权的 tools/call 请求
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			rawMsgs := conn.TextMessages()
			for _, m := range rawMsgs {
				var req struct {
					Type    string `json:"type"`
					Payload struct {
						JSONRPC string         `json:"jsonrpc"`
						ID      int64          `json:"id"`
						Method  string         `json:"method"`
						Params  map[string]any `json:"params"`
					} `json:"payload"`
				}
				if err := json.Unmarshal(m, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
					toolName, _ := req.Payload.Params["name"].(string)
					if toolName == "self.reboot" {
						t.Errorf("device unexpectedly received tools/call for unauthorized tool self.reboot")
					}
					resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"volume set to 50"}],"isError":false}}`, req.Payload.ID)
					sess.PostClientText(&ClientMessage{
						Kind:       KindMCP,
						MCPPayload: json.RawMessage(resp),
					})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	sess.orchestrateLLMAndTTS(ctx, 1, "重启并把音量设为50")

	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}

	secondMsgs := llmClient.lastMessages[1]
	var foundRebootRejection, foundVolumeSuccess bool
	for _, m := range secondMsgs {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_reboot_1" && strings.Contains(m.Content, "not authorized") {
			foundRebootRejection = true
		}
		if m.Role == ai.RoleTool && m.ToolCallID == "call_vol_1" && strings.Contains(m.Content, "volume set to 50") {
			foundVolumeSuccess = true
		}
	}

	if !foundRebootRejection {
		t.Errorf("second LLM call missing reboot rejection message")
	}
	if !foundVolumeSuccess {
		t.Errorf("second LLM call missing volume success message")
	}
}

// TestMCP_IsMCPToolAllowedAndDirectCallValidation 验证白名单判定函数及 callMCPTool 防御校验。
func TestMCP_IsMCPToolAllowedAndDirectCallValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sess := &Session{}

	// 空白名单
	if sess.isMCPToolAllowed("self.reboot") {
		t.Errorf("expected isMCPToolAllowed to be false for empty tools")
	}

	// 注册合法工具
	sess.mcpTools = []ai.Tool{
		{Name: "self.audio_speaker.set_volume"},
	}

	if !sess.isMCPToolAllowed("self.audio_speaker.set_volume") {
		t.Errorf("expected isMCPToolAllowed to be true for registered tool")
	}
	if sess.isMCPToolAllowed("self.reboot") {
		t.Errorf("expected isMCPToolAllowed to be false for unregistered tool")
	}

	// 直接调用 callMCPTool 触发白名单拦截
	_, err := sess.callMCPTool(ctx, "self.reboot", "{}")
	if !errors.Is(err, ErrMCPToolNotFound) {
		t.Errorf("expected ErrMCPToolNotFound, got %v", err)
	}
}

// TestMCP_MultiStepToolCallVolumeControl 验证完整的多步 Agent Loop（先查设备状态后调音量再生成最终回答）。
func TestMCP_MultiStepToolCallVolumeControl(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	tools := []ai.Tool{
		{
			Name:        "self.get_device_status",
			Description: "Get device status",
			Parameters:  map[string]any{"type": "object"},
		},
		{
			Name:        "self.audio_speaker.set_volume",
			Description: "Set volume",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	// 模拟 3 步流：
	// 1. 调用 self.get_device_status
	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_status_1",
			Name:      "self.get_device_status",
			Arguments: `{}`,
		},
	})
	// 2. 根据状态 (volume=60) 调用 self.audio_speaker.set_volume(70)
	stream2 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_vol_1",
			Name:      "self.audio_speaker.set_volume",
			Arguments: `{"volume":70}`,
		},
	})
	// 3. 最终回复
	stream3 := newMockMCPLLMStream([]string{"音量已从60调大到70。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2, stream3},
	}

	ttsClient := newMockTTSStream(nil)
	ttsClientWrapper := newMockTTSClient(ttsClient, nil)

	sess := NewSession(ctx, Options{
		Writer:    writer,
		LLMClient: llmClient,
		TTSClient: ttsClientWrapper,
		Logger:    slog.Default(),
	})
	defer sess.Close()
	go func() { _ = sess.Run() }()

	sess.mu.Lock()
	sess.sessionID = "test-multistep-session"
	sess.mcpTools = tools
	sess.mu.Unlock()

	// 模拟设备响应多步 MCP 调用
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			rawMsgs := conn.TextMessages()
			for _, m := range rawMsgs {
				var req struct {
					Type    string `json:"type"`
					Payload struct {
						JSONRPC string         `json:"jsonrpc"`
						ID      int64          `json:"id"`
						Method  string         `json:"method"`
						Params  map[string]any `json:"params"`
					} `json:"payload"`
				}
				if err := json.Unmarshal(m, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
					toolName, _ := req.Payload.Params["name"].(string)
					var resp string
					if toolName == "self.get_device_status" {
						resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"audio_speaker\":{\"volume\":60}}"}],"isError":false}}`, req.Payload.ID)
					} else if toolName == "self.audio_speaker.set_volume" {
						resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"true"}],"isError":false}}`, req.Payload.ID)
					}
					if resp != "" {
						sess.PostClientText(&ClientMessage{
							Kind:       KindMCP,
							MCPPayload: json.RawMessage(resp),
						})
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	sess.orchestrateLLMAndTTS(ctx, 1, "调大一点音量")

	// 验证 LLM 一共被调用了 3 次
	if len(llmClient.lastMessages) != 3 {
		t.Fatalf("expected 3 LLM calls for multi-step loop, got %d", len(llmClient.lastMessages))
	}

	// 验证前两次调用均携带了 tools
	if len(llmClient.lastTools[0]) != 2 || len(llmClient.lastTools[1]) != 2 {
		t.Errorf("expected tools present in first and second calls, got %+v and %+v", llmClient.lastTools[0], llmClient.lastTools[1])
	}

	// 验证第三次 LLM 调用包含两次 Tool 的请求与响应
	thirdMsgs := llmClient.lastMessages[2]
	var foundStatusCall, foundStatusResp, foundVolCall, foundVolResp bool
	for _, m := range thirdMsgs {
		if m.Role == ai.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if tc.Name == "self.get_device_status" {
					foundStatusCall = true
				}
				if tc.Name == "self.audio_speaker.set_volume" {
					foundVolCall = true
				}
			}
		}
		if m.Role == ai.RoleTool {
			if m.ToolCallID == "call_status_1" && strings.Contains(m.Content, "volume") {
				foundStatusResp = true
			}
			if m.ToolCallID == "call_vol_1" && m.Content == "true" {
				foundVolResp = true
			}
		}
	}

	if !foundStatusCall || !foundStatusResp {
		t.Errorf("third LLM call missing status tool call or response")
	}
	if !foundVolCall || !foundVolResp {
		t.Errorf("third LLM call missing volume tool call or response")
	}

	// 验证最终 TTS 下发了确认回答
	if len(ttsClient.sentSentences) == 0 {
		t.Fatalf("expected TTS sentences from final round, got none")
	}
}
