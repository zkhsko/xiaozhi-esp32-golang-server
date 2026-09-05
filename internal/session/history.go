package session

import (
	"sync"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// ConversationHistory 负责维护单会话上下文历史消息，按轮次原子存储并保证 Tool Call 轨迹的完整性。
type ConversationHistory struct {
	mu       sync.RWMutex
	maxTurns int
	turns    [][]ai.Message
}

// NewConversationHistory 创建指定最大轮数的会话历史管理器。
func NewConversationHistory(maxTurns int) *ConversationHistory {
	if maxTurns <= 0 {
		maxTurns = DefaultMaxHistoryTurns
	}
	return &ConversationHistory{
		maxTurns: maxTurns,
		turns:    make([][]ai.Message, 0, maxTurns),
	}
}

// AppendTurn 将一轮成功的用户文本、助手回复及本轮消息轨迹原子追加至历史，并按上限滚动淘汰最旧轮次。
func (h *ConversationHistory) AppendTurn(userText, assistantText string, turnMessages []ai.Message) {
	if userText == "" {
		return
	}

	turn := make([]ai.Message, 0, 1+len(turnMessages))
	turn = append(turn, ai.Message{Role: ai.RoleUser, Content: userText})

	if len(turnMessages) > 0 {
		turn = append(turn, turnMessages...)
	} else if assistantText != "" {
		turn = append(turn, ai.Message{Role: ai.RoleAssistant, Content: assistantText})
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.turns = append(h.turns, turn)
	if len(h.turns) > h.maxTurns {
		trimmed := make([][]ai.Message, h.maxTurns)
		copy(trimmed, h.turns[len(h.turns)-h.maxTurns:])
		h.turns = trimmed
	}
}

// MessagesSnapshot 返回当前历史消息切片的不可变独立副本。
func (h *ConversationHistory) MessagesSnapshot() []ai.Message {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.turns) == 0 {
		return nil
	}

	var total int
	for _, t := range h.turns {
		total += len(t)
	}

	result := make([]ai.Message, 0, total)
	for _, t := range h.turns {
		result = append(result, t...)
	}
	return result
}

// Clear 清空会话历史。
func (h *ConversationHistory) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.turns = h.turns[:0]
}

// Len 返回当前历史消息总条数。
func (h *ConversationHistory) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var total int
	for _, t := range h.turns {
		total += len(t)
	}
	return total
}
