package session

import (
	"encoding/json"
	"errors"
)

// 服务端文本消息类型与状态常量。
const (
	// MessageTypeSTT 服务端识别文本下发消息类型。
	MessageTypeSTT = "stt"

	// MessageTypeTTS 服务端语音合成状态与文本下发消息类型。
	MessageTypeTTS = "tts"

	// TTSStateStart TTS 开始合成/播放状态。
	TTSStateStart = "start"

	// TTSStateSentenceStart TTS 单句开始播报与文本下发状态。
	TTSStateSentenceStart = "sentence_start"

	// TTSStateStop TTS 停止合成/播报状态。
	TTSStateStop = "stop"
)

// 服务端消息编码相关的哨兵错误。
var (
	// ErrEmptySessionID 会话 ID 为空错误。
	ErrEmptySessionID = errors.New("session id cannot be empty")
)

// ServerSTTMessage 定义服务端下发的 STT 识别结果文本消息。
type ServerSTTMessage struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Text      string `json:"text"`
}

// ServerTTSStartMessage 定义服务端下发的 TTS start 状态消息。
type ServerTTSStartMessage struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	State     string `json:"state"`
}

// ServerTTSSentenceStartMessage 定义服务端下发的 TTS sentence_start 状态及对应单句文本消息。
type ServerTTSSentenceStartMessage struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Text      string `json:"text"`
}

// ServerTTSStopMessage 定义服务端下发的 TTS stop 状态消息。
type ServerTTSStopMessage struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	State     string `json:"state"`
}

// NewServerSTTMessage 创建服务端 STT 识别文本消息对象。
func NewServerSTTMessage(sessionID, text string) ServerSTTMessage {
	return ServerSTTMessage{
		SessionID: sessionID,
		Type:      MessageTypeSTT,
		Text:      text,
	}
}

// NewServerTTSStartMessage 创建服务端 TTS start 状态消息对象。
func NewServerTTSStartMessage(sessionID string) ServerTTSStartMessage {
	return ServerTTSStartMessage{
		SessionID: sessionID,
		Type:      MessageTypeTTS,
		State:     TTSStateStart,
	}
}

// NewServerTTSSentenceStartMessage 创建服务端 TTS sentence_start 状态消息对象。
func NewServerTTSSentenceStartMessage(sessionID, text string) ServerTTSSentenceStartMessage {
	return ServerTTSSentenceStartMessage{
		SessionID: sessionID,
		Type:      MessageTypeTTS,
		State:     TTSStateSentenceStart,
		Text:      text,
	}
}

// NewServerTTSStopMessage 创建服务端 TTS stop 状态消息对象。
func NewServerTTSStopMessage(sessionID string) ServerTTSStopMessage {
	return ServerTTSStopMessage{
		SessionID: sessionID,
		Type:      MessageTypeTTS,
		State:     TTSStateStop,
	}
}

// EncodeServerHelloMessage 校验 session_id 并将服务端 hello 握手响应编码为 JSON 字节切片。
func EncodeServerHelloMessage(sessionID string) ([]byte, error) {
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	return json.Marshal(NewServerHello(sessionID))
}

// EncodeSTTMessage 校验 session_id 并将 STT 识别文本消息编码为 JSON 字节切片。
func EncodeSTTMessage(sessionID, text string) ([]byte, error) {
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	return json.Marshal(NewServerSTTMessage(sessionID, text))
}

// EncodeTTSStartMessage 校验 session_id 并将 TTS start 状态消息编码为 JSON 字节切片。
func EncodeTTSStartMessage(sessionID string) ([]byte, error) {
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	return json.Marshal(NewServerTTSStartMessage(sessionID))
}

// EncodeTTSSentenceStartMessage 校验 session_id 并将 TTS sentence_start 状态与文本消息编码为 JSON 字节切片。
func EncodeTTSSentenceStartMessage(sessionID, text string) ([]byte, error) {
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	return json.Marshal(NewServerTTSSentenceStartMessage(sessionID, text))
}

// EncodeTTSStopMessage 校验 session_id 并将 TTS stop 状态消息编码为 JSON 字节切片。
func EncodeTTSStopMessage(sessionID string) ([]byte, error) {
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	return json.Marshal(NewServerTTSStopMessage(sessionID))
}
