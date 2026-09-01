package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// 文本消息解析相关的上限常量。
const (
	// MaxClientTextMessageBytes 客户端 WebSocket 文本消息最大允许字节数（32 KiB）。
	MaxClientTextMessageBytes = 32768

	// MaxTextFieldLength 文本字段（如 session_id, text, reason 等）的最大字符数限制。
	MaxTextFieldLength = 1024
)

// 客户端文本消息分类与枚举常量。
const (
	// MessageTypeListen 监听相关消息类型。
	MessageTypeListen = "listen"

	// MessageTypeAbort 中断相关消息类型。
	MessageTypeAbort = "abort"

	// MessageTypeMCP 设备 MCP 消息类型。
	MessageTypeMCP = "mcp"

	// ListenStateStart listen 开始状态。
	ListenStateStart = "start"

	// ListenStateStop listen 结束状态。
	ListenStateStop = "stop"

	// ListenStateDetect listen 唤醒词检测状态。
	ListenStateDetect = "detect"

	// ListenModeAuto 自动检测收音模式。
	ListenModeAuto = "auto"

	// ListenModeManual 手动按键收音模式。
	ListenModeManual = "manual"

	// ListenModeRealtime 实时全双工模式（首期明确拒绝）。
	ListenModeRealtime = "realtime"
)

// MessageKind 表示解析并分类后的客户端消息类型。
type MessageKind string

const (
	// KindHello 握手 hello 消息。
	KindHello MessageKind = "hello"

	// KindListenStart 开始收音消息。
	KindListenStart MessageKind = "listen.start"

	// KindListenStop 停止收音消息。
	KindListenStop MessageKind = "listen.stop"

	// KindListenDetect 唤醒词检测诊断消息。
	KindListenDetect MessageKind = "listen.detect"

	// KindAbort 显式中断当前处理与播报消息。
	KindAbort MessageKind = "abort"

	// KindMCP 设备 MCP 交互消息。
	KindMCP MessageKind = "mcp"

	// KindUnknownExtension 未知扩展消息（合法的非核心扩展类型）。
	KindUnknownExtension MessageKind = "unknown_extension"
)

// 客户端消息解析相关的哨兵错误定义。
var (
	// ErrEmptyMessage 消息为空字节。
	ErrEmptyMessage = errors.New("empty client message")

	// ErrMessageTooLarge 消息大小超出限制。
	ErrMessageTooLarge = errors.New("client message exceeds maximum allowed size")

	// ErrInvalidJSON JSON 语法非法。
	ErrInvalidJSON = errors.New("invalid json format")

	// ErrInvalidMessageFormat 消息顶层结构非法（非 JSON 对象）。
	ErrInvalidMessageFormat = errors.New("invalid message format: expected json object")

	// ErrMissingRequiredField 缺少必填字段。
	ErrMissingRequiredField = errors.New("missing required field")

	// ErrInvalidFieldType 字段数据类型错误。
	ErrInvalidFieldType = errors.New("invalid field type")

	// ErrInvalidListenState 非法的 listen 状态。
	ErrInvalidListenState = errors.New("invalid listen state")

	// ErrUnsupportedListenMode 不支持的收音模式（如 realtime）。
	ErrUnsupportedListenMode = errors.New("unsupported listen mode: realtime is rejected")

	// ErrInvalidListenMode 非法的收音模式值。
	ErrInvalidListenMode = errors.New("invalid listen mode")

	// ErrFieldTooLong 字段文本长度超出上限。
	ErrFieldTooLong = errors.New("field value exceeds maximum length limit")
)

// ClientMessage 表示强类型分类后的客户端上行文本消息。
type ClientMessage struct {
	// Kind 消息分类。
	Kind MessageKind `json:"kind"`

	// SessionId 客户端携带的会话标识（可选）。
	SessionId string `json:"session_id,omitempty"`

	// Mode 收音模式（仅在 Kind == KindListenStart 时有效，为 auto 或 manual）。
	Mode string `json:"mode,omitempty"`

	// DetectText 唤醒词文本（仅在 Kind == KindListenDetect 时有效）。
	DetectText string `json:"text,omitempty"`

	// AbortReason 中断原因（仅在 Kind == KindAbort 时有效）。
	AbortReason string `json:"reason,omitempty"`

	// RawType 未知扩展消息的原始 type 字段。
	RawType string `json:"raw_type,omitempty"`

	// RawPayload 未知扩展消息的原始 JSON 载荷（供诊断日志等使用）。
	RawPayload json.RawMessage `json:"raw_payload,omitempty"`

	// Payload 设备 MCP 消息的 JSON 载荷。
	Payload json.RawMessage `json:"payload,omitempty"`
}

// IsExtension 判断当前消息是否为未知扩展消息。
func (m *ClientMessage) IsExtension() bool {
	return m != nil && m.Kind == KindUnknownExtension
}

// ParseClientMessage 解析客户端上行文本消息，并执行协议字段校验与强类型分类。
// 消息总长度受 MaxClientTextMessageBytes 限制。
func ParseClientMessage(data []byte) (*ClientMessage, error) {
	return ParseClientMessageWithLimit(data, MaxClientTextMessageBytes)
}

// ParseClientMessageWithLimit 解析客户端上行文本消息并指定最大允许字节数。
func ParseClientMessageWithLimit(data []byte, maxBytes int) (*ClientMessage, error) {
	if len(data) == 0 {
		return nil, ErrEmptyMessage
	}
	if maxBytes <= 0 {
		maxBytes = MaxClientTextMessageBytes
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("%w: size %d exceeds limit %d", ErrMessageTooLarge, len(data), maxBytes)
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("%w: expected json object but got %s", ErrInvalidMessageFormat, typeErr.Value)
		}
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, syntaxErr)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	if rawMap == nil {
		return nil, fmt.Errorf("%w: null json object", ErrInvalidMessageFormat)
	}

	msg := &ClientMessage{}

	// 1. 解析可选的 session_id 字段
	if rawSessionId, ok := rawMap["session_id"]; ok {
		var sessionId string
		if err := json.Unmarshal(rawSessionId, &sessionId); err != nil {
			return nil, fmt.Errorf("%w: 'session_id' must be string", ErrInvalidFieldType)
		}
		if utf8.RuneCountInString(sessionId) > MaxTextFieldLength {
			return nil, fmt.Errorf("%w: 'session_id' length %d exceeds max %d", ErrFieldTooLong, utf8.RuneCountInString(sessionId), MaxTextFieldLength)
		}
		msg.SessionId = sessionId
	}

	// 2. 解析必填的 type 字段
	rawType, ok := rawMap["type"]
	if !ok {
		return nil, fmt.Errorf("%w: missing 'type' field", ErrMissingRequiredField)
	}
	var msgType string
	if err := json.Unmarshal(rawType, &msgType); err != nil {
		return nil, fmt.Errorf("%w: 'type' must be string", ErrInvalidFieldType)
	}
	if msgType == "" {
		return nil, fmt.Errorf("%w: 'type' cannot be empty", ErrMissingRequiredField)
	}
	if utf8.RuneCountInString(msgType) > MaxTextFieldLength {
		return nil, fmt.Errorf("%w: 'type' length %d exceeds max %d", ErrFieldTooLong, utf8.RuneCountInString(msgType), MaxTextFieldLength)
	}

	// 3. 根据 type 分流处理
	switch msgType {
	case MessageTypeHello:
		msg.Kind = KindHello
		return msg, nil

	case MessageTypeListen:
		return parseListenMessage(rawMap, msg)

	case MessageTypeAbort:
		return parseAbortMessage(rawMap, msg)

	case MessageTypeMCP:
		msg.Kind = KindMCP
		if rawPayload, ok := rawMap["payload"]; ok {
			msg.Payload = rawPayload
		}
		return msg, nil

	default:
		msg.Kind = KindUnknownExtension
		msg.RawType = msgType
		msg.RawPayload = data
		return msg, nil
	}
}

// parseListenMessage 解析并校验 listen 相关状态消息。
func parseListenMessage(rawMap map[string]json.RawMessage, msg *ClientMessage) (*ClientMessage, error) {
	rawState, ok := rawMap["state"]
	if !ok {
		return nil, fmt.Errorf("%w: listen message missing 'state' field", ErrMissingRequiredField)
	}
	var state string
	if err := json.Unmarshal(rawState, &state); err != nil {
		return nil, fmt.Errorf("%w: listen 'state' must be string", ErrInvalidFieldType)
	}
	if state == "" {
		return nil, fmt.Errorf("%w: listen 'state' cannot be empty", ErrMissingRequiredField)
	}
	if utf8.RuneCountInString(state) > MaxTextFieldLength {
		return nil, fmt.Errorf("%w: listen 'state' length exceeds limit", ErrFieldTooLong)
	}

	switch state {
	case ListenStateStart:
		rawMode, ok := rawMap["mode"]
		if !ok {
			return nil, fmt.Errorf("%w: listen.start missing 'mode' field", ErrMissingRequiredField)
		}
		var mode string
		if err := json.Unmarshal(rawMode, &mode); err != nil {
			return nil, fmt.Errorf("%w: listen.start 'mode' must be string", ErrInvalidFieldType)
		}
		if utf8.RuneCountInString(mode) > MaxTextFieldLength {
			return nil, fmt.Errorf("%w: listen.start 'mode' length exceeds limit", ErrFieldTooLong)
		}
		switch mode {
		case ListenModeRealtime:
			return nil, fmt.Errorf("%w: realtime mode is not supported", ErrUnsupportedListenMode)
		case ListenModeAuto:
			msg.Kind = KindListenStart
			msg.Mode = ListenModeAuto
			return msg, nil
		case ListenModeManual:
			msg.Kind = KindListenStart
			msg.Mode = ListenModeManual
			return msg, nil
		default:
			return nil, fmt.Errorf("%w: unknown mode %q, expected 'auto' or 'manual'", ErrInvalidListenMode, mode)
		}

	case ListenStateStop:
		msg.Kind = KindListenStop
		return msg, nil

	case ListenStateDetect:
		msg.Kind = KindListenDetect
		if rawText, ok := rawMap["text"]; ok {
			var text string
			if err := json.Unmarshal(rawText, &text); err != nil {
				return nil, fmt.Errorf("%w: listen.detect 'text' must be string", ErrInvalidFieldType)
			}
			if utf8.RuneCountInString(text) > MaxTextFieldLength {
				return nil, fmt.Errorf("%w: listen.detect 'text' length %d exceeds max %d", ErrFieldTooLong, utf8.RuneCountInString(text), MaxTextFieldLength)
			}
			msg.DetectText = text
		}
		return msg, nil

	default:
		return nil, fmt.Errorf("%w: unknown listen state %q", ErrInvalidListenState, state)
	}
}

// parseAbortMessage 解析并校验 abort 中断消息。
func parseAbortMessage(rawMap map[string]json.RawMessage, msg *ClientMessage) (*ClientMessage, error) {
	msg.Kind = KindAbort
	if rawReason, ok := rawMap["reason"]; ok {
		var reason string
		if err := json.Unmarshal(rawReason, &reason); err != nil {
			return nil, fmt.Errorf("%w: abort 'reason' must be string", ErrInvalidFieldType)
		}
		if utf8.RuneCountInString(reason) > MaxTextFieldLength {
			return nil, fmt.Errorf("%w: abort 'reason' length %d exceeds max %d", ErrFieldTooLong, utf8.RuneCountInString(reason), MaxTextFieldLength)
		}
		msg.AbortReason = reason
	}
	return msg, nil
}
