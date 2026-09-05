package session

import (
	"sync"
	"testing"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func TestConversationHistory_BasicAppendAndSnapshot(t *testing.T) {
	h := NewConversationHistory(3)

	if h.Len() != 0 {
		t.Fatalf("expected 0 messages initially, got %d", h.Len())
	}
	if snap := h.MessagesSnapshot(); snap != nil {
		t.Fatalf("expected nil snapshot when empty, got %v", snap)
	}

	// 1. 追加普通轮次（无 tool call）
	h.AppendTurn("你好", "你好！很高兴为您服务。", nil)
	if h.Len() != 2 {
		t.Fatalf("expected 2 messages, got %d", h.Len())
	}

	snap1 := h.MessagesSnapshot()
	if len(snap1) != 2 {
		t.Fatalf("expected 2 snapshot messages, got %d", len(snap1))
	}
	if snap1[0].Role != ai.RoleUser || snap1[0].Content != "你好" {
		t.Errorf("unexpected msg0: %+v", snap1[0])
	}
	if snap1[1].Role != ai.RoleAssistant || snap1[1].Content != "你好！很高兴为您服务。" {
		t.Errorf("unexpected msg1: %+v", snap1[1])
	}

	// 2. 追加带有 Tool Call 轨迹的轮次
	toolTurnMessages := []ai.Message{
		{
			Role: ai.RoleAssistant,
			ToolCalls: []ai.ToolCall{
				{
					Id:        "call_vol_1",
					Name:      "self.audio_speaker.set_volume",
					Arguments: map[string]any{"volume": 50},
				},
			},
		},
		{
			Role:       ai.RoleTool,
			Content:    "true",
			ToolCallId: "call_vol_1",
			ToolName:   "self.audio_speaker.set_volume",
		},
		{
			Role:    ai.RoleAssistant,
			Content: "已为您将音量调大至 50%",
		},
	}

	h.AppendTurn("调大音量", "已为您将音量调大至 50%", toolTurnMessages)
	// 第1轮 2条 + 第2轮 4条 (1 user + 3 turnMessages) = 6条
	if h.Len() != 6 {
		t.Fatalf("expected 6 messages, got %d", h.Len())
	}

	snap2 := h.MessagesSnapshot()
	if len(snap2) != 6 {
		t.Fatalf("expected 6 messages in snapshot, got %d", len(snap2))
	}
	if snap2[2].Role != ai.RoleUser || snap2[2].Content != "调大音量" {
		t.Errorf("unexpected msg2: %+v", snap2[2])
	}
	if snap2[3].Role != ai.RoleAssistant || len(snap2[3].ToolCalls) != 1 {
		t.Errorf("unexpected msg3 tool call: %+v", snap2[3])
	}
	if snap2[4].Role != ai.RoleTool || snap2[4].ToolCallId != "call_vol_1" {
		t.Errorf("unexpected msg4 tool response: %+v", snap2[4])
	}
	if snap2[5].Role != ai.RoleAssistant || snap2[5].Content != "已为您将音量调大至 50%" {
		t.Errorf("unexpected msg5 text: %+v", snap2[5])
	}
}

func TestConversationHistory_AtomicTurnRolling(t *testing.T) {
	// 限制最多 2 轮历史
	h := NewConversationHistory(2)

	// Turn 1: 普通问答 (2 条)
	h.AppendTurn("问题1", "回答1", nil)

	// Turn 2: 带工具调用问答 (4 条)
	h.AppendTurn("问题2", "回答2", []ai.Message{
		{
			Role: ai.RoleAssistant,
			ToolCalls: []ai.ToolCall{
				{Id: "c1", Name: "t1", Arguments: map[string]any{}},
			},
		},
		{Role: ai.RoleTool, Content: "ok", ToolCallId: "c1", ToolName: "t1"},
		{Role: ai.RoleAssistant, Content: "回答2"},
	})

	if h.Len() != 6 {
		t.Fatalf("expected 6 messages before rolling, got %d", h.Len())
	}

	// Turn 3: 普通问答 (2 条) -> 此时应淘汰 Turn 1，保留完整的 Turn 2 和 Turn 3
	h.AppendTurn("问题3", "回答3", nil)

	snap := h.MessagesSnapshot()
	// Turn 2 (4条) + Turn 3 (2条) = 6条
	if len(snap) != 6 {
		t.Fatalf("expected 6 messages after rolling (Turn 2 + Turn 3), got %d", len(snap))
	}

	// 验证 Turn 1 已经被完整淘汰
	if snap[0].Role != ai.RoleUser || snap[0].Content != "问题2" {
		t.Errorf("expected first remaining message to be Turn 2 user query, got %+v", snap[0])
	}
	// 验证 Turn 2 的 Tool Call 关系保持原子完整，未被拆散
	if snap[1].Role != ai.RoleAssistant || len(snap[1].ToolCalls) != 1 {
		t.Errorf("expected Turn 2 assistant tool call, got %+v", snap[1])
	}
	if snap[2].Role != ai.RoleTool || snap[2].ToolCallId != "c1" {
		t.Errorf("expected Turn 2 tool result, got %+v", snap[2])
	}
	if snap[3].Role != ai.RoleAssistant || snap[3].Content != "回答2" {
		t.Errorf("expected Turn 2 assistant text, got %+v", snap[3])
	}
	if snap[4].Role != ai.RoleUser || snap[4].Content != "问题3" {
		t.Errorf("expected Turn 3 user query, got %+v", snap[4])
	}
	if snap[5].Role != ai.RoleAssistant || snap[5].Content != "回答3" {
		t.Errorf("expected Turn 3 assistant text, got %+v", snap[5])
	}
}

func TestConversationHistory_EmptyAndClear(t *testing.T) {
	h := NewConversationHistory(5)

	// 空字符串不追加
	h.AppendTurn("", "回答", nil)
	if h.Len() != 0 {
		t.Fatalf("expected 0 messages for empty userText, got %d", h.Len())
	}

	h.AppendTurn("用户输入", "助手回答", nil)
	if h.Len() != 2 {
		t.Fatalf("expected 2 messages, got %d", h.Len())
	}

	h.Clear()
	if h.Len() != 0 {
		t.Fatalf("expected 0 messages after clear, got %d", h.Len())
	}
	if snap := h.MessagesSnapshot(); snap != nil {
		t.Fatalf("expected nil snapshot after clear, got %v", snap)
	}
}

func TestConversationHistory_Concurrency(t *testing.T) {
	h := NewConversationHistory(10)
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			h.AppendTurn("query", "reply", []ai.Message{
				{Role: ai.RoleAssistant, Content: "reply"},
			})
		}(i)

		go func() {
			defer wg.Done()
			_ = h.MessagesSnapshot()
			_ = h.Len()
		}()
	}

	wg.Wait()
	if h.Len() == 0 {
		t.Fatal("expected non-empty history after concurrent appends")
	}
}
