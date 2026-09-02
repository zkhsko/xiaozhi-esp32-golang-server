package session

import (
	"encoding/json"
	"errors"
)

// 服务端文本消息类型与状态常量。
const (
	// MessageTypeSTT 服务端识别文本下发消息类型。
	MessageTypeSTT = "stt"
	// MessageTypeTTS 服务端语音播报消息类型。
	MessageTypeTTS = "tts"

	// TTSStateStart 服务端开始语音播报状态。
	TTSStateStart = "start"
	// TTSStateSentenceStart 服务端开始朗读单句状态。
	TTSStateSentenceStart = "sentence_start"
	// TTSStateStop 服务端停止语音播报状态。
	TTSStateStop = "stop"
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

// ServerTTSMessage 定义服务端下发的 TTS 状态与文本控制消息。
type ServerTTSMessage struct {
	SessionId string `json:"session_id"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Text      string `json:"text,omitempty"`
}

// NewServerTTSStartMessage 创建服务端 TTS 开始播放消息对象。
func NewServerTTSStartMessage(sessionId string) ServerTTSMessage {
	return ServerTTSMessage{
		SessionId: sessionId,
		Type:      MessageTypeTTS,
		State:     TTSStateStart,
	}
}

// NewServerTTSSentenceStartMessage 创建服务端 TTS 单句开始消息对象。
func NewServerTTSSentenceStartMessage(sessionId, text string) ServerTTSMessage {
	return ServerTTSMessage{
		SessionId: sessionId,
		Type:      MessageTypeTTS,
		State:     TTSStateSentenceStart,
		Text:      text,
	}
}

// NewServerTTSStopMessage 创建服务端 TTS 停止播放消息对象。
func NewServerTTSStopMessage(sessionId string) ServerTTSMessage {
	return ServerTTSMessage{
		SessionId: sessionId,
		Type:      MessageTypeTTS,
		State:     TTSStateStop,
	}
}

// EncodeTTSStartMessage 校验 session_id 并将 TTS start 消息编码为 JSON 字节切片。
func EncodeTTSStartMessage(sessionId string) ([]byte, error) {
	if sessionId == "" {
		return nil, ErrEmptySessionId
	}
	return json.Marshal(NewServerTTSStartMessage(sessionId))
}

// EncodeTTSSentenceStartMessage 校验 session_id 并将 TTS sentence_start 消息编码为 JSON 字节切片。
func EncodeTTSSentenceStartMessage(sessionId, text string) ([]byte, error) {
	if sessionId == "" {
		return nil, ErrEmptySessionId
	}
	return json.Marshal(NewServerTTSSentenceStartMessage(sessionId, text))
}

// EncodeTTSStopMessage 校验 session_id 并将 TTS stop 消息编码为 JSON 字节切片。
func EncodeTTSStopMessage(sessionId string) ([]byte, error) {
	if sessionId == "" {
		return nil, ErrEmptySessionId
	}
	return json.Marshal(NewServerTTSStopMessage(sessionId))
}
