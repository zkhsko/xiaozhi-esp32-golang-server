package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestEncodeServerHelloMessage 验证服务端 hello 消息编码与字段精确断言。
func TestEncodeServerHelloMessage(t *testing.T) {
	t.Run("valid session id", func(t *testing.T) {
		sessionID := "test-session-hello-001"
		data, err := EncodeServerHelloMessage(sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			t.Fatalf("failed to unmarshal encoded json: %v", err)
		}

		// 断言顶层字段白名单（仅包含 type, transport, session_id, audio_params）
		expectedTopKeys := []string{"audio_params", "session_id", "transport", "type"}
		var actualTopKeys []string
		for k := range rawMap {
			actualTopKeys = append(actualTopKeys, k)
		}
		sort.Strings(actualTopKeys)
		if !reflect.DeepEqual(actualTopKeys, expectedTopKeys) {
			t.Fatalf("top keys mismatch: got %v, want %v", actualTopKeys, expectedTopKeys)
		}

		// 逐字段断言顶层值
		if rawMap["type"] != MessageTypeHello {
			t.Errorf("type mismatch: got %v, want %s", rawMap["type"], MessageTypeHello)
		}
		if rawMap["transport"] != TransportWebSocket {
			t.Errorf("transport mismatch: got %v, want %s", rawMap["transport"], TransportWebSocket)
		}
		if rawMap["session_id"] != sessionID {
			t.Errorf("session_id mismatch: got %v, want %s", rawMap["session_id"], sessionID)
		}

		// 断言 audio_params 嵌套字段与类型
		audioParams, ok := rawMap["audio_params"].(map[string]any)
		if !ok {
			t.Fatalf("audio_params is not a json object: %T", rawMap["audio_params"])
		}
		expectedAudioKeys := []string{"channels", "format", "frame_duration", "sample_rate"}
		var actualAudioKeys []string
		for k := range audioParams {
			actualAudioKeys = append(actualAudioKeys, k)
		}
		sort.Strings(actualAudioKeys)
		if !reflect.DeepEqual(actualAudioKeys, expectedAudioKeys) {
			t.Fatalf("audio_params keys mismatch: got %v, want %v", actualAudioKeys, expectedAudioKeys)
		}

		if audioParams["format"] != ServerAudioFormat {
			t.Errorf("audio_params.format mismatch: got %v, want %s", audioParams["format"], ServerAudioFormat)
		}
		if audioParams["sample_rate"] != float64(ServerSampleRate) {
			t.Errorf("audio_params.sample_rate mismatch: got %v, want %d", audioParams["sample_rate"], ServerSampleRate)
		}
		if audioParams["channels"] != float64(ServerChannels) {
			t.Errorf("audio_params.channels mismatch: got %v, want %d", audioParams["channels"], ServerChannels)
		}
		if audioParams["frame_duration"] != float64(ServerFrameDuration) {
			t.Errorf("audio_params.frame_duration mismatch: got %v, want %d", audioParams["frame_duration"], ServerFrameDuration)
		}

		// 强类型反序列化校验
		var msg ServerHelloMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("failed to unmarshal to ServerHelloMessage: %v", err)
		}
		if msg.SessionID != sessionID || msg.Type != MessageTypeHello || msg.Transport != TransportWebSocket {
			t.Errorf("unmarshaled struct mismatch: %+v", msg)
		}
		if msg.AudioParams.Format != ServerAudioFormat || msg.AudioParams.SampleRate != ServerSampleRate ||
			msg.AudioParams.Channels != ServerChannels || msg.AudioParams.FrameDuration != ServerFrameDuration {
			t.Errorf("unmarshaled audio params mismatch: %+v", msg.AudioParams)
		}
	})

	t.Run("empty session id error", func(t *testing.T) {
		_, err := EncodeServerHelloMessage("")
		if !errors.Is(err, ErrEmptySessionID) {
			t.Fatalf("expected ErrEmptySessionID, got: %v", err)
		}
	})
}

// TestEncodeSTTMessage 验证 STT 消息编码、多语种文本与边界断言。
func TestEncodeSTTMessage(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		text      string
	}{
		{
			name:      "chinese text with punctuation",
			sessionID: "sess-stt-001",
			text:      "你好，请问今天杭州的天气怎么样？",
		},
		{
			name:      "english and numbers",
			sessionID: "sess-stt-002",
			text:      "The temperature is 26 degrees Celsius in 2026.",
		},
		{
			name:      "unicode symbols and emoji",
			sessionID: "sess-stt-003",
			text:      "小智🤖：天气晴朗 ☀️！温度 25℃，风速 3.5m/s。",
		},
		{
			name:      "special escape characters",
			sessionID: "sess-stt-004",
			text:      "第一行\n第二行\t带有引号\"和反斜杠\\以及标签<tag>",
		},
		{
			name:      "empty text",
			sessionID: "sess-stt-005",
			text:      "",
		},
		{
			name:      "long text boundary",
			sessionID: "sess-stt-006",
			text:      strings.Repeat("长文本测试", 200),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := EncodeSTTMessage(tt.sessionID, tt.text)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var rawMap map[string]any
			if err := json.Unmarshal(data, &rawMap); err != nil {
				t.Fatalf("failed to unmarshal JSON: %v", err)
			}

			// 严格断言顶层字段白名单（必须且仅有 session_id, type, text 3个字段）
			expectedKeys := []string{"session_id", "text", "type"}
			var actualKeys []string
			for k := range rawMap {
				actualKeys = append(actualKeys, k)
			}
			sort.Strings(actualKeys)
			if !reflect.DeepEqual(actualKeys, expectedKeys) {
				t.Fatalf("keys mismatch: got %v, want %v", actualKeys, expectedKeys)
			}

			// 逐字段断言
			if rawMap["session_id"] != tt.sessionID {
				t.Errorf("session_id mismatch: got %v, want %s", rawMap["session_id"], tt.sessionID)
			}
			if rawMap["type"] != MessageTypeSTT {
				t.Errorf("type mismatch: got %v, want %s", rawMap["type"], MessageTypeSTT)
			}
			if rawMap["text"] != tt.text {
				t.Errorf("text mismatch: got %v, want %s", rawMap["text"], tt.text)
			}

			// 结构体反序列化验证
			var msg ServerSTTMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("failed to unmarshal to ServerSTTMessage: %v", err)
			}
			if msg.SessionID != tt.sessionID || msg.Type != MessageTypeSTT || msg.Text != tt.text {
				t.Errorf("unmarshaled struct mismatch: %+v", msg)
			}
		})
	}

	t.Run("empty session id error", func(t *testing.T) {
		_, err := EncodeSTTMessage("", "text")
		if !errors.Is(err, ErrEmptySessionID) {
			t.Fatalf("expected ErrEmptySessionID, got: %v", err)
		}
	})
}

// TestEncodeTTSStartMessage 验证 TTS start 消息编码与字段精确断言。
func TestEncodeTTSStartMessage(t *testing.T) {
	t.Run("valid session id", func(t *testing.T) {
		sessionID := "sess-tts-start-001"
		data, err := EncodeTTSStartMessage(sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}

		// 严格断言顶层字段白名单（仅 session_id, state, type）
		expectedKeys := []string{"session_id", "state", "type"}
		var actualKeys []string
		for k := range rawMap {
			actualKeys = append(actualKeys, k)
		}
		sort.Strings(actualKeys)
		if !reflect.DeepEqual(actualKeys, expectedKeys) {
			t.Fatalf("keys mismatch: got %v, want %v", actualKeys, expectedKeys)
		}

		// 逐字段断言
		if rawMap["session_id"] != sessionID {
			t.Errorf("session_id mismatch: got %v, want %s", rawMap["session_id"], sessionID)
		}
		if rawMap["type"] != MessageTypeTTS {
			t.Errorf("type mismatch: got %v, want %s", rawMap["type"], MessageTypeTTS)
		}
		if rawMap["state"] != TTSStateStart {
			t.Errorf("state mismatch: got %v, want %s", rawMap["state"], TTSStateStart)
		}

		// 结构体反序列化验证
		var msg ServerTTSStartMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("failed to unmarshal to ServerTTSStartMessage: %v", err)
		}
		if msg.SessionID != sessionID || msg.Type != MessageTypeTTS || msg.State != TTSStateStart {
			t.Errorf("unmarshaled struct mismatch: %+v", msg)
		}
	})

	t.Run("empty session id error", func(t *testing.T) {
		_, err := EncodeTTSStartMessage("")
		if !errors.Is(err, ErrEmptySessionID) {
			t.Fatalf("expected ErrEmptySessionID, got: %v", err)
		}
	})
}

// TestEncodeTTSSentenceStartMessage 验证 TTS sentence_start 消息编码、单句文本与边界断言。
func TestEncodeTTSSentenceStartMessage(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		text      string
	}{
		{
			name:      "chinese sentence with comma and period",
			sessionID: "sess-sent-001",
			text:      "今天天气真不错，适合出门散步。",
		},
		{
			name:      "english sentence with punctuation",
			sessionID: "sess-sent-002",
			text:      "Hello world! It's a fine day, isn't it?",
		},
		{
			name:      "sentence with quotes and symbols",
			sessionID: "sess-sent-003",
			text:      "他说：“你好！” 包含特殊字符: <>&\"'\\/\n\t",
		},
		{
			name:      "empty sentence text",
			sessionID: "sess-sent-004",
			text:      "",
		},
		{
			name:      "single character sentence",
			sessionID: "sess-sent-005",
			text:      "好",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := EncodeTTSSentenceStartMessage(tt.sessionID, tt.text)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var rawMap map[string]any
			if err := json.Unmarshal(data, &rawMap); err != nil {
				t.Fatalf("failed to unmarshal JSON: %v", err)
			}

			// 严格断言顶层字段白名单（必须且仅有 session_id, state, text, type 4个字段）
			expectedKeys := []string{"session_id", "state", "text", "type"}
			var actualKeys []string
			for k := range rawMap {
				actualKeys = append(actualKeys, k)
			}
			sort.Strings(actualKeys)
			if !reflect.DeepEqual(actualKeys, expectedKeys) {
				t.Fatalf("keys mismatch: got %v, want %v", actualKeys, expectedKeys)
			}

			// 逐字段断言
			if rawMap["session_id"] != tt.sessionID {
				t.Errorf("session_id mismatch: got %v, want %s", rawMap["session_id"], tt.sessionID)
			}
			if rawMap["type"] != MessageTypeTTS {
				t.Errorf("type mismatch: got %v, want %s", rawMap["type"], MessageTypeTTS)
			}
			if rawMap["state"] != TTSStateSentenceStart {
				t.Errorf("state mismatch: got %v, want %s", rawMap["state"], TTSStateSentenceStart)
			}
			if rawMap["text"] != tt.text {
				t.Errorf("text mismatch: got %v, want %s", rawMap["text"], tt.text)
			}

			// 结构体反序列化验证
			var msg ServerTTSSentenceStartMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("failed to unmarshal to ServerTTSSentenceStartMessage: %v", err)
			}
			if msg.SessionID != tt.sessionID || msg.Type != MessageTypeTTS ||
				msg.State != TTSStateSentenceStart || msg.Text != tt.text {
				t.Errorf("unmarshaled struct mismatch: %+v", msg)
			}
		})
	}

	t.Run("empty session id error", func(t *testing.T) {
		_, err := EncodeTTSSentenceStartMessage("", "sentence")
		if !errors.Is(err, ErrEmptySessionID) {
			t.Fatalf("expected ErrEmptySessionID, got: %v", err)
		}
	})
}

// TestEncodeTTSStopMessage 验证 TTS stop 消息编码与字段精确断言。
func TestEncodeTTSStopMessage(t *testing.T) {
	t.Run("valid session id", func(t *testing.T) {
		sessionID := "sess-tts-stop-001"
		data, err := EncodeTTSStopMessage(sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}

		// 严格断言顶层字段白名单（仅 session_id, state, type）
		expectedKeys := []string{"session_id", "state", "type"}
		var actualKeys []string
		for k := range rawMap {
			actualKeys = append(actualKeys, k)
		}
		sort.Strings(actualKeys)
		if !reflect.DeepEqual(actualKeys, expectedKeys) {
			t.Fatalf("keys mismatch: got %v, want %v", actualKeys, expectedKeys)
		}

		// 逐字段断言
		if rawMap["session_id"] != sessionID {
			t.Errorf("session_id mismatch: got %v, want %s", rawMap["session_id"], sessionID)
		}
		if rawMap["type"] != MessageTypeTTS {
			t.Errorf("type mismatch: got %v, want %s", rawMap["type"], MessageTypeTTS)
		}
		if rawMap["state"] != TTSStateStop {
			t.Errorf("state mismatch: got %v, want %s", rawMap["state"], TTSStateStop)
		}

		// 结构体反序列化验证
		var msg ServerTTSStopMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("failed to unmarshal to ServerTTSStopMessage: %v", err)
		}
		if msg.SessionID != sessionID || msg.Type != MessageTypeTTS || msg.State != TTSStateStop {
			t.Errorf("unmarshaled struct mismatch: %+v", msg)
		}
	})

	t.Run("empty session id error", func(t *testing.T) {
		_, err := EncodeTTSStopMessage("")
		if !errors.Is(err, ErrEmptySessionID) {
			t.Fatalf("expected ErrEmptySessionID, got: %v", err)
		}
	})
}

// TestStrictNoDisallowedFields 严格断言所有下发消息绝不存在白名单之外的字段（如 llm 表情、mcp、system、custom、glyph_push 等）。
func TestStrictNoDisallowedFields(t *testing.T) {
	disallowedFields := []string{
		"llm", "emotion", "mcp", "system", "custom", "glyph_push", "iot",
		"mode", "reason", "error", "code", "data", "status", "params",
		"audio", "payload", "extra", "format", "channels", "sample_rate",
	}

	sessionID := "strict-check-session-999"

	messages := []struct {
		name       string
		encoded    []byte
		allowedMap map[string]bool
	}{
		{
			name: "hello message",
			encoded: func() []byte {
				b, _ := EncodeServerHelloMessage(sessionID)
				return b
			}(),
			allowedMap: map[string]bool{
				"type": true, "transport": true, "session_id": true, "audio_params": true,
			},
		},
		{
			name: "stt message",
			encoded: func() []byte {
				b, _ := EncodeSTTMessage(sessionID, "识别结果")
				return b
			}(),
			allowedMap: map[string]bool{
				"session_id": true, "type": true, "text": true,
			},
		},
		{
			name: "tts.start message",
			encoded: func() []byte {
				b, _ := EncodeTTSStartMessage(sessionID)
				return b
			}(),
			allowedMap: map[string]bool{
				"session_id": true, "type": true, "state": true,
			},
		},
		{
			name: "tts.sentence_start message",
			encoded: func() []byte {
				b, _ := EncodeTTSSentenceStartMessage(sessionID, "单句播报内容")
				return b
			}(),
			allowedMap: map[string]bool{
				"session_id": true, "type": true, "state": true, "text": true,
			},
		},
		{
			name: "tts.stop message",
			encoded: func() []byte {
				b, _ := EncodeTTSStopMessage(sessionID)
				return b
			}(),
			allowedMap: map[string]bool{
				"session_id": true, "type": true, "state": true,
			},
		},
	}

	for _, msg := range messages {
		t.Run(msg.name, func(t *testing.T) {
			var rawMap map[string]any
			if err := json.Unmarshal(msg.encoded, &rawMap); err != nil {
				t.Fatalf("failed to unmarshal JSON: %v", err)
			}

			// 1. 检查白名单：存在的所有字段必须在 allowedMap 中
			for key := range rawMap {
				if !msg.allowedMap[key] {
					t.Errorf("found unallowed field %q in %s", key, msg.name)
				}
			}

			// 2. 显式断言禁用字段列表不存在于顶层
			for _, disallowed := range disallowedFields {
				if !msg.allowedMap[disallowed] {
					if _, exists := rawMap[disallowed]; exists {
						t.Errorf("disallowed field %q exists in %s", disallowed, msg.name)
					}
				}
			}
		})
	}
}

// TestConstructors 验证各消息结构体构造函数的字段初始化。
func TestConstructors(t *testing.T) {
	sessionID := "session-ctor-test"

	stt := NewServerSTTMessage(sessionID, "识别文本")
	if stt.SessionID != sessionID || stt.Type != MessageTypeSTT || stt.Text != "识别文本" {
		t.Errorf("NewServerSTTMessage result unexpected: %+v", stt)
	}

	ttsStart := NewServerTTSStartMessage(sessionID)
	if ttsStart.SessionID != sessionID || ttsStart.Type != MessageTypeTTS || ttsStart.State != TTSStateStart {
		t.Errorf("NewServerTTSStartMessage result unexpected: %+v", ttsStart)
	}

	ttsSentence := NewServerTTSSentenceStartMessage(sessionID, "句子")
	if ttsSentence.SessionID != sessionID || ttsSentence.Type != MessageTypeTTS ||
		ttsSentence.State != TTSStateSentenceStart || ttsSentence.Text != "句子" {
		t.Errorf("NewServerTTSSentenceStartMessage result unexpected: %+v", ttsSentence)
	}

	ttsStop := NewServerTTSStopMessage(sessionID)
	if ttsStop.SessionID != sessionID || ttsStop.Type != MessageTypeTTS || ttsStop.State != TTSStateStop {
		t.Errorf("NewServerTTSStopMessage result unexpected: %+v", ttsStop)
	}
}
