package session

import (
	"sync"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// ConversationHistory 负责维护单会话上下文历史消息。
type ConversationHistory struct {
	mu       sync.RWMutex
	maxTurns int
	messages []ai.Message
}

// NewConversationHistory 创建指定最大轮数的会话历史管理器。
func NewConversationHistory(maxTurns int) *ConversationHistory {
	if maxTurns <= 0 {
		maxTurns = DefaultMaxHistoryTurns
	}
	return &ConversationHistory{
		maxTurns: maxTurns,
		messages: make([]ai.Message, 0, maxTurns*2),
	}
}

// AppendTurn 将一轮成功的用户文本与助手完整回复追加至历史，并按上限滚动淘汰最旧轮次。
func (h *ConversationHistory) AppendTurn(userText, assistantText string) {
	if userText == "" || assistantText == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.messages = append(h.messages,
		ai.Message{Role: ai.RoleUser, Content: userText},
		ai.Message{Role: ai.RoleAssistant, Content: assistantText},
	)

	maxMessages := h.maxTurns * 2
	if len(h.messages) > maxMessages {
		trimmed := make([]ai.Message, maxMessages)
		copy(trimmed, h.messages[len(h.messages)-maxMessages:])
		h.messages = trimmed
	}
}

// MessagesSnapshot 返回当前历史消息切片的不可变独立副本。
func (h *ConversationHistory) MessagesSnapshot() []ai.Message {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.messages) == 0 {
		return nil
	}
	cp := make([]ai.Message, len(h.messages))
	copy(cp, h.messages)
	return cp
}

// Clear 清空会话历史。
func (h *ConversationHistory) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.messages = h.messages[:0]
}

// Len 返回当前历史消息总条数。
func (h *ConversationHistory) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.messages)
}

