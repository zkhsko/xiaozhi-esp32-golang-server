package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
)

// TestServerTool_GetCurrentTime_Format 验证 server.get_current_time 工具返回的结构及字段格式。
func TestServerTool_GetCurrentTime_Format(t *testing.T) {
	ctx := context.Background()

	resultJSON, err := executeServerTool(ctx, ServerToolGetCurrentTime, "{}")
	if err != nil {
		t.Fatalf("executeServerTool failed: %v", err)
	}

	var data struct {
		DateTime  string `json:"datetime"`
		Date      string `json:"date"`
		Time      string `json:"time"`
		Weekday   string `json:"weekday"`
		TimeZone  string `json:"timezone"`
		UTCOffset string `json:"utc_offset"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &data); err != nil {
		t.Fatalf("failed to unmarshal current time result json: %v, raw: %s", err, resultJSON)
	}

	if data.DateTime == "" || data.Date == "" || data.Time == "" || data.Weekday == "" || data.UTCOffset == "" {
		t.Errorf("missing required fields in result: %+v", data)
	}

	// 校验日期与时间格式可被解析
	if _, err := time.Parse("2006-01-02 15:04:05", data.DateTime); err != nil {
		t.Errorf("invalid datetime format %q: %v", data.DateTime, err)
	}
	if _, err := time.Parse("2006-01-02", data.Date); err != nil {
		t.Errorf("invalid date format %q: %v", data.Date, err)
	}
	if _, err := time.Parse("15:04:05", data.Time); err != nil {
		t.Errorf("invalid time format %q: %v", data.Time, err)
	}

	// 校验时区及 UTC 偏移
	now := time.Now()
	expectedZone, _ := now.Zone()
	if data.TimeZone != expectedZone {
		t.Errorf("timezone mismatch: got %q, expected %q", data.TimeZone, expectedZone)
	}
	expectedOffset := now.Format("-07:00")
	if data.UTCOffset != expectedOffset {
		t.Errorf("utc_offset mismatch: got %q, expected %q", data.UTCOffset, expectedOffset)
	}
}

// TestServerTool_GetCurrentTime_ContextCancel 验证当 Context 取消时服务端工具立即返回错误。
func TestServerTool_GetCurrentTime_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := executeServerTool(ctx, ServerToolGetCurrentTime, "")
	if err == nil {
		t.Fatalf("expected error on canceled context, got nil")
	}
}

// TestServerTool_CloseSession_Format 验证 server.close_session 工具返回的结构及字段格式。
func TestServerTool_CloseSession_Format(t *testing.T) {
	ctx := context.Background()

	resultJSON, err := executeServerTool(ctx, ServerToolCloseSession, "{}")
	if err != nil {
		t.Fatalf("executeServerTool failed: %v", err)
	}

	var data struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &data); err != nil {
		t.Fatalf("failed to unmarshal close session result json: %v, raw: %s", err, resultJSON)
	}

	if data.Status != "success" || data.Message == "" {
		t.Errorf("unexpected close session result: %+v", data)
	}
}

// TestServerTool_CloseSession_ContextCancel 验证当 Context 取消时关闭会话工具立即返回错误。
func TestServerTool_CloseSession_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := executeServerTool(ctx, ServerToolCloseSession, "")
	if err == nil {
		t.Fatalf("expected error on canceled context, got nil")
	}
}

// TestServerTool_AvailableToolsMergeRules 验证服务端工具与设备工具的合并规则与去重优先级。
func TestServerTool_AvailableToolsMergeRules(t *testing.T) {
	t.Run("only server tools when no device tools", func(t *testing.T) {
		sess := &Session{}
		tools := sess.availableTools()

		if len(tools) != 2 {
			t.Fatalf("expected 2 server tools, got %d", len(tools))
		}
		if tools[0].Name != ServerToolGetCurrentTime {
			t.Errorf("expected %s, got %s", ServerToolGetCurrentTime, tools[0].Name)
		}
		if tools[1].Name != ServerToolCloseSession {
			t.Errorf("expected %s, got %s", ServerToolCloseSession, tools[1].Name)
		}
	})

	t.Run("merge server tools and distinct device tools", func(t *testing.T) {
		sess := &Session{
			mcpTools: []ai.Tool{
				{Name: "self.lamp.turn_on", Description: "打开灯光"},
				{Name: "self.audio_speaker.set_volume", Description: "设置音量"},
			},
		}
		tools := sess.availableTools()

		if len(tools) != 4 {
			t.Fatalf("expected 4 tools, got %d", len(tools))
		}
		// 服务端工具必须排在最前面（优先）
		if tools[0].Name != ServerToolGetCurrentTime || tools[1].Name != ServerToolCloseSession {
			t.Errorf("expected server tools first, got %s, %s", tools[0].Name, tools[1].Name)
		}
		if tools[2].Name != "self.lamp.turn_on" || tools[3].Name != "self.audio_speaker.set_volume" {
			t.Errorf("device tools order mismatch: %+v", tools)
		}
	})

	t.Run("device tool with same name cannot override server tool", func(t *testing.T) {
		sess := &Session{
			mcpTools: []ai.Tool{
				{Name: ServerToolGetCurrentTime, Description: "设备冒充的时间工具"},
				{Name: ServerToolCloseSession, Description: "设备冒充的关闭工具"},
				{Name: "self.lamp.turn_on", Description: "打开灯光"},
			},
		}
		tools := sess.availableTools()

		if len(tools) != 3 {
			t.Fatalf("expected 3 tools after deduplication, got %d", len(tools))
		}
		if tools[0].Name != ServerToolGetCurrentTime || tools[0].Description != "获取服务端当前的日期、时间、星期和时区信息" {
			t.Errorf("server tool was incorrectly overridden by device tool: %+v", tools[0])
		}
		if tools[1].Name != ServerToolCloseSession || tools[1].Description != "关闭当前会话并断开连接。当用户表示想要结束对话、退下、去睡觉、断开连接、再见或不再需要交互时调用此工具" {
			t.Errorf("server close session tool was incorrectly overridden by device tool: %+v", tools[1])
		}
		if tools[2].Name != "self.lamp.turn_on" {
			t.Errorf("unexpected third tool: %+v", tools[2])
		}
	})
}

// TestServerTool_GetCurrentTime_E2E_LLM_Orchestration 完整验证用户询问时间时的流转闭环：
// 1. LLM 第 1 次返回 server.get_current_time 的 ToolCall
// 2. 服务端本地执行，绝不向 WebSocket 发送 MCP 请求
// 3. 时间结果回填给 LLM
// 4. LLM 第 2 次生成自然语言回答，TTS 正常播报。
func TestServerTool_GetCurrentTime_E2E_LLM_Orchestration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	// 第一次调用返回时间工具调用
	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_time_123",
			Name:      ServerToolGetCurrentTime,
			Arguments: `{}`,
		},
	})
	// 第二次调用返回最终自然语言回答
	stream2 := newMockMCPLLMStream([]string{"现在是北京时间", "2026年8月26日 18点整。"}, nil)

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
	sess.sessionID = "test-server-tool-time-session"
	sess.mu.Unlock()

	sess.orchestrateLLMAndTTS(ctx, 1, "现在几点")

	// 1. 验证 LLM 经历了 2 次调用
	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}

	// 2. 第一次调用带有服务端时间工具
	if len(llmClient.lastTools[0]) < 1 || llmClient.lastTools[0][0].Name != ServerToolGetCurrentTime {
		t.Errorf("first LLM call missing server time tool: %+v", llmClient.lastTools[0])
	}

	// 3. 验证未向客户端 WebSocket 下发任何 tools/call 请求
	for _, rawMsg := range conn.TextMessages() {
		var req struct {
			Type    string `json:"type"`
			Payload struct {
				Method string `json:"method"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(rawMsg, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
			t.Fatalf("unexpected mcp tools/call sent to device for server tool")
		}
	}

	// 4. 验证第二次调用包含了 RoleAssistant(ToolCall) 与 RoleTool(时间 JSON)
	secondMsgs := llmClient.lastMessages[1]
	var foundAssistantToolCall, foundToolResult bool
	for _, m := range secondMsgs {
		if m.Role == ai.RoleAssistant && len(m.ToolCalls) > 0 && m.ToolCalls[0].Name == ServerToolGetCurrentTime {
			foundAssistantToolCall = true
		}
		if m.Role == ai.RoleTool && m.ToolCallID == "call_time_123" {
			foundToolResult = true
			if !strings.Contains(m.Content, "datetime") || !strings.Contains(m.Content, "utc_offset") {
				t.Errorf("tool result content missing expected time fields: %s", m.Content)
			}
		}
	}

	if !foundAssistantToolCall {
		t.Errorf("second LLM call missing assistant tool call")
	}
	if !foundToolResult {
		t.Errorf("second LLM call missing tool result response")
	}

	// 5. 验证 TTS 接收到了最终合成文本
	sentSentences := ttsClient.sentSentences
	if len(sentSentences) == 0 {
		t.Fatalf("expected TTS sentences, got none")
	}
}

// TestServerTool_DeviceConflictDoesNotOverrideServerExecution 验证当设备上报同名工具时，依然由服务端本地直接执行，不向设备下发请求。
func TestServerTool_DeviceConflictDoesNotOverrideServerExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_conflict_1",
			Name:      ServerToolGetCurrentTime,
			Arguments: `{}`,
		},
	})
	stream2 := newMockMCPLLMStream([]string{"现在是上午10点。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
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
	sess.sessionID = "test-conflict-session"
	// 设备非法声明同名工具
	sess.mcpTools = []ai.Tool{
		{Name: ServerToolGetCurrentTime, Description: "设备试图覆盖服务端工具"},
	}
	sess.mu.Unlock()

	sess.orchestrateLLMAndTTS(ctx, 1, "现在几点了")

	// 验证未向设备 WebSocket 发送 tools/call
	for _, rawMsg := range conn.TextMessages() {
		var req struct {
			Type    string `json:"type"`
			Payload struct {
				Method string `json:"method"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(rawMsg, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
			t.Fatalf("unexpected tools/call sent to device for conflicting server tool")
		}
	}

	// 验证第二次调用正确回填了服务端执行的时间结果
	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}
	var foundToolResult bool
	for _, m := range llmClient.lastMessages[1] {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_conflict_1" {
			foundToolResult = true
			if !strings.Contains(m.Content, "datetime") {
				t.Errorf("expected server time result, got %s", m.Content)
			}
		}
	}
	if !foundToolResult {
		t.Errorf("missing tool result in second call")
	}
}

// TestServerTool_UnknownToolRejected 验证未启用的服务端工具或未授权工具被拒绝并注入错误提示。
func TestServerTool_UnknownToolRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_unknown_1",
			Name:      "server.unsupported_tool",
			Arguments: `{}`,
		},
	})
	stream2 := newMockMCPLLMStream([]string{"抱歉，不支持该操作。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
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
	sess.sessionID = "test-unknown-server-tool"
	sess.mu.Unlock()

	sess.orchestrateLLMAndTTS(ctx, 1, "执行未启用工具")

	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}

	var foundRejection bool
	for _, m := range llmClient.lastMessages[1] {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_unknown_1" {
			foundRejection = true
			if !strings.Contains(m.Content, "not authorized") {
				t.Errorf("expected unauthorized message, got %s", m.Content)
			}
		}
	}
	if !foundRejection {
		t.Errorf("missing rejection message in second LLM call")
	}
}

// TestServerTool_MixedMultiStepCalls 验证混合多步调用（第1步查服务端时间，第2步根据时间调设备夜间模式，第3步生成最终回复）。
func TestServerTool_MixedMultiStepCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	// 设备仅支持灯光控制
	deviceTools := []ai.Tool{
		{
			Name:        "self.lamp.set_night_mode",
			Description: "设置夜间模式",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	// 模拟 3 步流：
	// 1. 调用 server.get_current_time
	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_time_1",
			Name:      ServerToolGetCurrentTime,
			Arguments: `{}`,
		},
	})
	// 2. 根据当前时间为晚上，调用设备 self.lamp.set_night_mode
	stream2 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_lamp_1",
			Name:      "self.lamp.set_night_mode",
			Arguments: `{"enabled":true}`,
		},
	})
	// 3. 最终确认回答
	stream3 := newMockMCPLLMStream([]string{"现在已经是晚上，已为你开启夜间模式。"}, nil)

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
	sess.sessionID = "test-mixed-multistep"
	sess.mcpTools = deviceTools
	sess.mu.Unlock()

	// 模拟设备响应 self.lamp.set_night_mode
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
					if toolName == "self.lamp.set_night_mode" {
						resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"night mode enabled"}],"isError":false}}`, req.Payload.ID)
						sess.PostClientText(&ClientMessage{
							Kind:       KindMCP,
							MCPPayload: json.RawMessage(resp),
						})
						return
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	sess.orchestrateLLMAndTTS(ctx, 1, "根据现在的时间调整灯光模式")

	// 验证经历了 3 轮大模型调用
	if len(llmClient.lastMessages) != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(llmClient.lastMessages))
	}

	// 验证第 3 轮包含两项工具调用的请求与结果（1个服务端工具结果 + 1个设备工具结果）
	thirdMsgs := llmClient.lastMessages[2]
	var foundTimeResult, foundLampResult bool
	for _, m := range thirdMsgs {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_time_1" && strings.Contains(m.Content, "datetime") {
			foundTimeResult = true
		}
		if m.Role == ai.RoleTool && m.ToolCallID == "call_lamp_1" && strings.Contains(m.Content, "night mode enabled") {
			foundLampResult = true
		}
	}

	if !foundTimeResult {
		t.Errorf("third LLM call missing server time tool result")
	}
	if !foundLampResult {
		t.Errorf("third LLM call missing device lamp tool result")
	}

	// 验证 TTS 下发了最终播报
	if len(ttsClient.sentSentences) == 0 {
		t.Fatalf("expected TTS sentences from final round, got none")
	}
}

// TestServerTool_CloseSession_E2E_LLM_Orchestration 完整验证用户说“退下吧”触发关闭会话的流转闭环：
// 1. LLM 第 1 次返回 server.close_session 的 ToolCall；
// 2. 服务端本地直接执行，绝不向 WebSocket 发送 MCP 请求；
// 3. 执行成功回填结果后，LLM 第 2 次生成告别语（“好的，我先退下了，再见！”）；
// 4. TTS 将告别语合成为音频下发播放；
// 5. 播报完成触发 TurnFinished 事件后，Session 自动优雅关闭（状态变为 StateClosed）。
func TestServerTool_CloseSession_E2E_LLM_Orchestration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	// 第一次调用返回关闭会话工具调用
	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_close_123",
			Name:      ServerToolCloseSession,
			Arguments: `{"reason":"user_requested"}`,
		},
	})
	// 第二次调用返回告别语
	stream2 := newMockMCPLLMStream([]string{"好的，小智先退下了", "，有需要随时叫我。"}, nil)

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
	sess.sessionID = "test-close-session"
	sess.state = StateProcessing
	sess.generation = 1
	sess.mu.Unlock()

	sess.orchestrateLLMAndTTS(ctx, 1, "小智退下吧")

	// 1. 验证 LLM 经历了 2 次调用
	if len(llmClient.lastMessages) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llmClient.lastMessages))
	}

	// 2. 第一次调用带有服务端关闭工具
	var foundCloseTool bool
	for _, tool := range llmClient.lastTools[0] {
		if tool.Name == ServerToolCloseSession {
			foundCloseTool = true
			break
		}
	}
	if !foundCloseTool {
		t.Errorf("first LLM call missing server close tool: %+v", llmClient.lastTools[0])
	}

	// 3. 验证未向客户端 WebSocket 下发任何 tools/call 请求
	for _, rawMsg := range conn.TextMessages() {
		var req struct {
			Type    string `json:"type"`
			Payload struct {
				Method string `json:"method"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(rawMsg, &req); err == nil && req.Type == MessageTypeMCP && req.Payload.Method == "tools/call" {
			t.Fatalf("unexpected mcp tools/call sent to device for server close tool")
		}
	}

	// 4. 验证第二次调用包含了 RoleAssistant(ToolCall) 与 RoleTool(确认 JSON)
	secondMsgs := llmClient.lastMessages[1]
	var foundAssistantToolCall, foundToolResult bool
	for _, m := range secondMsgs {
		if m.Role == ai.RoleAssistant && len(m.ToolCalls) > 0 && m.ToolCalls[0].Name == ServerToolCloseSession {
			foundAssistantToolCall = true
		}
		if m.Role == ai.RoleTool && m.ToolCallID == "call_close_123" {
			foundToolResult = true
			if !strings.Contains(m.Content, "success") {
				t.Errorf("tool result content missing success: %s", m.Content)
			}
		}
	}

	if !foundAssistantToolCall {
		t.Errorf("second LLM call missing assistant tool call")
	}
	if !foundToolResult {
		t.Errorf("second LLM call missing tool result response")
	}

	// 5. 验证 TTS 接收到了最终告别语
	if len(ttsClient.sentSentences) == 0 {
		t.Fatalf("expected TTS sentences for goodbye speech, got none")
	}

	// 6. 验证会话在轮次播报结束后优雅关闭
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.State() == StateClosed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if sess.State() != StateClosed {
		t.Errorf("expected session state to be StateClosed after turn finished, got %s", sess.State())
	}
}

// TestServerTool_CloseSession_WithoutTTSOutput_ClosesImmediately 验证当大模型调用关闭工具后未生成任何回复文本时，
// 编排结束后会话依然能够顺利结束并自动关闭断开。
func TestServerTool_CloseSession_WithoutTTSOutput_ClosesImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_close_silence",
			Name:      ServerToolCloseSession,
			Arguments: `{}`,
		},
	})
	stream2 := newMockMCPLLMStream([]string{}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
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
	sess.sessionID = "test-close-silent"
	sess.state = StateProcessing
	sess.generation = 1
	sess.mu.Unlock()

	sess.orchestrateLLMAndTTS(ctx, 1, "退下")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.State() == StateClosed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if sess.State() != StateClosed {
		t.Errorf("expected session state to be StateClosed, got %s", sess.State())
	}
}

// TestServerTool_CloseSession_AbortCancelsPendingClose 验证在播报告别语中途若收到用户打断（Abort），
// 待关闭标记被安全清除，代次递增并复位到 READY 状态，会话不会断开。
func TestServerTool_CloseSession_AbortCancelsPendingClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := newHistoryWSConn()
	writer := NewWriter(ctx, conn, 100, slog.Default())

	frame := make([]byte, 2880)
	pauseTTS := make(chan struct{})
	ttsStream := newAbortMockTTSStream([][]byte{frame, frame}, pauseTTS)
	ttsClientWrapper := newAbortMockTTSClient(func() ai.TTSStream { return ttsStream })

	stream1 := newMockMCPLLMStream(nil, []ai.ToolCall{
		{
			ID:        "call_close_abort",
			Name:      ServerToolCloseSession,
			Arguments: `{}`,
		},
	})
	stream2 := newMockMCPLLMStream([]string{"小智退下了，有事再叫我。"}, nil)

	llmClient := &mockMCPLLMClient{
		streams: []ai.LLMStream{stream1, stream2},
	}

	sess := NewSession(ctx, Options{
		Writer:    writer,
		LLMClient: llmClient,
		TTSClient: ttsClientWrapper,
		Logger:    slog.Default(),
	})
	defer sess.Close()
	go func() { _ = sess.Run() }()

	sess.mu.Lock()
	sess.sessionID = "test-close-abort"
	sess.state = StateProcessing
	sess.generation = 1
	sess.mu.Unlock()

	go func() {
		sess.orchestrateLLMAndTTS(ctx, 1, "退下吧")
	}()

	// 等待进入 SPEAKING 状态
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.State() == StateSpeaking {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 此时用户发送打断
	sess.PostAbort("用户取消退出")

	// 释放 TTS 流
	close(pauseTTS)

	// 等待收敛
	time.Sleep(50 * time.Millisecond)

	sess.mu.RLock()
	st := sess.state
	closeFlag := sess.closeAfterTurn
	sess.mu.RUnlock()

	if st != StateReady {
		t.Errorf("expected session to return to StateReady after abort, got %s", st)
	}
	if closeFlag {
		t.Errorf("expected closeAfterTurn to be cleared on abort")
	}
}
