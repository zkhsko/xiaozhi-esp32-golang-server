package session

import (
	"encoding/json"
	"errors"
)

// 服务端文本消息类型与状态常量。
const (
	// MessageTypeSTT 服务端识别文本下发消息类型。
	MessageTypeSTT = "stt"
)

// 服务端消息编码相关的哨兵错误。
var (
	// ErrEmptySessionId 会话 Id 为空错误。
	ErrEmptySessionId = errors.New("session id cannot be empty")
)

// ServerSTTMessage 定义服务端下发的 STT 识别结果文本消息。
type ServerSTTMessage struct {
	SessionId string `json:"session_id"`
	Type      string `json:"type"`
	Text      string `json:"text"`
}

// NewServerSTTMessage 创建服务端 STT 识别文本消息对象。
func NewServerSTTMessage(sessionId, text string) ServerSTTMessage {
	return ServerSTTMessage{
		SessionId: sessionId,
		Type:      MessageTypeSTT,
		Text:      text,
	}
}

// EncodeServerHelloMessage 校验 session_id 并将服务端 hello 握手响应编码为 JSON 字节切片。
func EncodeServerHelloMessage(sessionId string) ([]byte, error) {
	if sessionId == "" {
		return nil, ErrEmptySessionId
	}
	return json.Marshal(NewServerHello(sessionId))
}

// EncodeSTTMessage 校验 session_id 并将 STT 识别文本消息编码为 JSON 字节切片。
func EncodeSTTMessage(sessionId, text string) ([]byte, error) {
	if sessionId == "" {
		return nil, ErrEmptySessionId
	}
	return json.Marshal(NewServerSTTMessage(sessionId, text))
}
