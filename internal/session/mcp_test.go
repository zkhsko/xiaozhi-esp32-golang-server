package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	sess := NewSessionWithWriter(ctx, nil, writer, nil, nil, nil, nil, nil, slog.Default())
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

	sess := NewSessionWithWriter(ctx, nil, writer, nil, &config.Config{
		Session: config.SessionConfig{
			SystemPrompt: "你是小智助手。",
		},
	}, nil, llmClient, ttsClientWrapper, slog.Default())
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

	sess := NewSessionWithWriter(ctx, nil, writer, nil, nil, nil, llmClient, ttsClientWrapper, slog.Default())
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
		sess := NewSessionWithWriter(ctx, nil, writer, nil, nil, nil, nil, nil, slog.Default())

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
		sess := NewSessionWithWriter(ctx, nil, writer, nil, nil, nil, nil, nil, slog.Default())

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
