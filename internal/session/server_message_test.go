package session

import (
	"encoding/json"
	"testing"
)

func TestEncodeServerHelloMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sessionId := "sess-hello-123"
		data, err := EncodeServerHelloMessage(sessionId)
		if err != nil {
			t.Fatalf("EncodeServerHelloMessage unexpected error: %v", err)
		}

		var msg ServerHelloMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}

		if msg.Type != MessageTypeHello {
			t.Errorf("expected type %q, got %q", MessageTypeHello, msg.Type)
		}
		if msg.Transport != TransportWebSocket {
			t.Errorf("expected transport %q, got %q", TransportWebSocket, msg.Transport)
		}
		if msg.SessionId != sessionId {
			t.Errorf("expected session_id %q, got %q", sessionId, msg.SessionId)
		}
		if msg.AudioParams.Format != ServerAudioFormat {
			t.Errorf("expected format %q, got %q", ServerAudioFormat, msg.AudioParams.Format)
		}
		if msg.AudioParams.SampleRate != ServerSampleRate {
			t.Errorf("expected sample_rate %d, got %d", ServerSampleRate, msg.AudioParams.SampleRate)
		}
		if msg.AudioParams.Channels != ServerChannels {
			t.Errorf("expected channels %d, got %d", ServerChannels, msg.AudioParams.Channels)
		}
		if msg.AudioParams.FrameDuration != ServerFrameDuration {
			t.Errorf("expected frame_duration %d, got %d", ServerFrameDuration, msg.AudioParams.FrameDuration)
		}
	})

	t.Run("empty sessionId", func(t *testing.T) {
		_, err := EncodeServerHelloMessage("")
		if err != ErrEmptySessionId {
			t.Fatalf("expected ErrEmptySessionId, got %v", err)
		}
	})
}

func TestEncodeSTTMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sessionId := "sess-stt-123"
		text := "你好，小智"
		data, err := EncodeSTTMessage(sessionId, text)
		if err != nil {
			t.Fatalf("EncodeSTTMessage unexpected error: %v", err)
		}

		expectedJSON := `{"session_id":"sess-stt-123","type":"stt","text":"你好，小智"}`
		if string(data) != expectedJSON {
			t.Errorf("expected JSON %s, got %s", expectedJSON, string(data))
		}

		var msg ServerSTTMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if msg.SessionId != sessionId {
			t.Errorf("expected session_id %q, got %q", sessionId, msg.SessionId)
		}
		if msg.Type != MessageTypeSTT {
			t.Errorf("expected type %q, got %q", MessageTypeSTT, msg.Type)
		}
		if msg.Text != text {
			t.Errorf("expected text %q, got %q", text, msg.Text)
		}
	})

	t.Run("empty sessionId", func(t *testing.T) {
		_, err := EncodeSTTMessage("", "text")
		if err != ErrEmptySessionId {
			t.Fatalf("expected ErrEmptySessionId, got %v", err)
		}
	})
}

func TestEncodeTTSStartMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sessionId := "sess-tts-start-123"
		data, err := EncodeTTSStartMessage(sessionId)
		if err != nil {
			t.Fatalf("EncodeTTSStartMessage unexpected error: %v", err)
		}

		expectedJSON := `{"session_id":"sess-tts-start-123","type":"tts","state":"start"}`
		if string(data) != expectedJSON {
			t.Errorf("expected JSON %s, got %s", expectedJSON, string(data))
		}

		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			t.Fatalf("unmarshal raw error: %v", err)
		}
		if _, exists := rawMap["text"]; exists {
			t.Errorf("expected text field to be omitted, but found in json: %v", rawMap["text"])
		}

		var msg ServerTTSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal typed error: %v", err)
		}
		if msg.SessionId != sessionId {
			t.Errorf("expected session_id %q, got %q", sessionId, msg.SessionId)
		}
		if msg.Type != MessageTypeTTS {
			t.Errorf("expected type %q, got %q", MessageTypeTTS, msg.Type)
		}
		if msg.State != TTSStateStart {
			t.Errorf("expected state %q, got %q", TTSStateStart, msg.State)
		}
		if msg.Text != "" {
			t.Errorf("expected empty text, got %q", msg.Text)
		}
	})

	t.Run("empty sessionId", func(t *testing.T) {
		_, err := EncodeTTSStartMessage("")
		if err != ErrEmptySessionId {
			t.Fatalf("expected ErrEmptySessionId, got %v", err)
		}
	})
}

func TestEncodeTTSSentenceStartMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sessionId := "sess-tts-sentence-123"
		text := "今天天气真好，我们一起去公园散步吧！"
		data, err := EncodeTTSSentenceStartMessage(sessionId, text)
		if err != nil {
			t.Fatalf("EncodeTTSSentenceStartMessage unexpected error: %v", err)
		}

		expectedJSON := `{"session_id":"sess-tts-sentence-123","type":"tts","state":"sentence_start","text":"今天天气真好，我们一起去公园散步吧！"}`
		if string(data) != expectedJSON {
			t.Errorf("expected JSON %s, got %s", expectedJSON, string(data))
		}

		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			t.Fatalf("unmarshal raw error: %v", err)
		}
		if textVal, ok := rawMap["text"].(string); !ok || textVal != text {
			t.Errorf("expected text field %q, got %v", text, rawMap["text"])
		}

		var msg ServerTTSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal typed error: %v", err)
		}
		if msg.SessionId != sessionId {
			t.Errorf("expected session_id %q, got %q", sessionId, msg.SessionId)
		}
		if msg.Type != MessageTypeTTS {
			t.Errorf("expected type %q, got %q", MessageTypeTTS, msg.Type)
		}
		if msg.State != TTSStateSentenceStart {
			t.Errorf("expected state %q, got %q", TTSStateSentenceStart, msg.State)
		}
		if msg.Text != text {
			t.Errorf("expected text %q, got %q", text, msg.Text)
		}
	})

	t.Run("empty sessionId", func(t *testing.T) {
		_, err := EncodeTTSSentenceStartMessage("", "待朗读文本")
		if err != ErrEmptySessionId {
			t.Fatalf("expected ErrEmptySessionId, got %v", err)
		}
	})
}

func TestEncodeTTSStopMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sessionId := "sess-tts-stop-123"
		data, err := EncodeTTSStopMessage(sessionId)
		if err != nil {
			t.Fatalf("EncodeTTSStopMessage unexpected error: %v", err)
		}

		expectedJSON := `{"session_id":"sess-tts-stop-123","type":"tts","state":"stop"}`
		if string(data) != expectedJSON {
			t.Errorf("expected JSON %s, got %s", expectedJSON, string(data))
		}

		var rawMap map[string]any
		if err := json.Unmarshal(data, &rawMap); err != nil {
			t.Fatalf("unmarshal raw error: %v", err)
		}
		if _, exists := rawMap["text"]; exists {
			t.Errorf("expected text field to be omitted, but found in json: %v", rawMap["text"])
		}

		var msg ServerTTSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal typed error: %v", err)
		}
		if msg.SessionId != sessionId {
			t.Errorf("expected session_id %q, got %q", sessionId, msg.SessionId)
		}
		if msg.Type != MessageTypeTTS {
			t.Errorf("expected type %q, got %q", MessageTypeTTS, msg.Type)
		}
		if msg.State != TTSStateStop {
			t.Errorf("expected state %q, got %q", TTSStateStop, msg.State)
		}
		if msg.Text != "" {
			t.Errorf("expected empty text, got %q", msg.Text)
		}
	})

	t.Run("empty sessionId", func(t *testing.T) {
		_, err := EncodeTTSStopMessage("")
		if err != ErrEmptySessionId {
			t.Fatalf("expected ErrEmptySessionId, got %v", err)
		}
	})
}

func TestNewServerTTSMessages(t *testing.T) {
	sessionId := "sess-constructor-test"

	startMsg := NewServerTTSStartMessage(sessionId)
	if startMsg.SessionId != sessionId || startMsg.Type != MessageTypeTTS || startMsg.State != TTSStateStart || startMsg.Text != "" {
		t.Errorf("unexpected start message: %+v", startMsg)
	}

	sentenceMsg := NewServerTTSSentenceStartMessage(sessionId, "测试文本")
	if sentenceMsg.SessionId != sessionId || sentenceMsg.Type != MessageTypeTTS || sentenceMsg.State != TTSStateSentenceStart || sentenceMsg.Text != "测试文本" {
		t.Errorf("unexpected sentence start message: %+v", sentenceMsg)
	}

	stopMsg := NewServerTTSStopMessage(sessionId)
	if stopMsg.SessionId != sessionId || stopMsg.Type != MessageTypeTTS || stopMsg.State != TTSStateStop || stopMsg.Text != "" {
		t.Errorf("unexpected stop message: %+v", stopMsg)
	}
}
