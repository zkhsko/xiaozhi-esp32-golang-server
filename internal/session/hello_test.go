package session

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

// validClientHello 返回一个满足所有校验要求的合法客户端 hello 消息。
func validClientHello() *ClientHelloMessage {
	return &ClientHelloMessage{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: ClientAudioParams{
			Format:        "opus",
			SampleRate:    16000,
			Channels:      1,
			FrameDuration: 60,
		},
	}
}

// TestGenerateSessionID 验证生成的会话 ID 格式、长度、合法性与唯一性。
func TestGenerateSessionID(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := GenerateSessionID()
		if err != nil {
			t.Fatalf("unexpected error generating session id: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("expected session id length 32, got %d (value: %s)", len(id), id)
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("session id is not valid hex: %v", err)
		}
		if ids[id] {
			t.Fatalf("duplicate session id generated: %s", id)
		}
		ids[id] = true
	}
}

// TestValidateClientHello 验证客户端 hello 消息各项字段的严格校验逻辑。
func TestValidateClientHello(t *testing.T) {
	tests := []struct {
		name        string
		modify      func(m *ClientHelloMessage)
		expectedErr error
	}{
		{
			name:        "合法客户端 hello 消息",
			modify:      func(m *ClientHelloMessage) {},
			expectedErr: nil,
		},
		{
			name: "消息类型错误",
			modify: func(m *ClientHelloMessage) {
				m.Type = "invalid_type"
			},
			expectedErr: ErrInvalidMessageType,
		},
		{
			name: "协议版本号错误",
			modify: func(m *ClientHelloMessage) {
				m.Version = 2
			},
			expectedErr: ErrInvalidProtocolVer,
		},
		{
			name: "协议版本号缺失为零值",
			modify: func(m *ClientHelloMessage) {
				m.Version = 0
			},
			expectedErr: ErrInvalidProtocolVer,
		},
		{
			name: "传输层协议错误",
			modify: func(m *ClientHelloMessage) {
				m.Transport = "mqtt"
			},
			expectedErr: ErrInvalidTransport,
		},
		{
			name: "音频格式错误",
			modify: func(m *ClientHelloMessage) {
				m.AudioParams.Format = "pcm"
			},
			expectedErr: ErrInvalidAudioFormat,
		},
		{
			name: "音频采样率错误（8000 Hz）",
			modify: func(m *ClientHelloMessage) {
				m.AudioParams.SampleRate = 8000
			},
			expectedErr: ErrInvalidSampleRate,
		},
		{
			name: "音频采样率错误（24000 Hz）",
			modify: func(m *ClientHelloMessage) {
				m.AudioParams.SampleRate = 24000
			},
			expectedErr: ErrInvalidSampleRate,
		},
		{
			name: "音频声道数错误（双声道）",
			modify: func(m *ClientHelloMessage) {
				m.AudioParams.Channels = 2
			},
			expectedErr: ErrInvalidChannels,
		},
		{
			name: "音频帧时长错误（20 ms）",
			modify: func(m *ClientHelloMessage) {
				m.AudioParams.FrameDuration = 20
			},
			expectedErr: ErrInvalidFrameDuration,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := validClientHello()
			tt.modify(msg)
			err := ValidateClientHello(msg)
			if tt.expectedErr == nil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			} else {
				if !errors.Is(err, tt.expectedErr) {
					t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
				}
			}
		})
	}

	t.Run("nil 消息校验返回错误", func(t *testing.T) {
		err := ValidateClientHello(nil)
		if err == nil {
			t.Fatal("expected error for nil message, got nil")
		}
	})
}

// TestNewServerHello 验证服务端 hello 消息的构建与序列化结果。
func TestNewServerHello(t *testing.T) {
	sessionID := "0123456789abcdef0123456789abcdef"
	msg := NewServerHello(sessionID)

	if msg.Type != "hello" {
		t.Errorf("expected type hello, got %s", msg.Type)
	}
	if msg.Transport != "websocket" {
		t.Errorf("expected transport websocket, got %s", msg.Transport)
	}
	if msg.SessionID != sessionID {
		t.Errorf("expected session_id %s, got %s", sessionID, msg.SessionID)
	}
	if msg.AudioParams.Format != "opus" {
		t.Errorf("expected format opus, got %s", msg.AudioParams.Format)
	}
	if msg.AudioParams.SampleRate != 24000 {
		t.Errorf("expected sample_rate 24000, got %d", msg.AudioParams.SampleRate)
	}
	if msg.AudioParams.Channels != 1 {
		t.Errorf("expected channels 1, got %d", msg.AudioParams.Channels)
	}
	if msg.AudioParams.FrameDuration != 60 {
		t.Errorf("expected frame_duration 60, got %d", msg.AudioParams.FrameDuration)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal server hello: %v", err)
	}

	var rawMap map[string]any
	if err := json.Unmarshal(data, &rawMap); err != nil {
		t.Fatalf("failed to unmarshal json to map: %v", err)
	}

	if rawMap["type"] != "hello" {
		t.Errorf("expected type hello in json, got %v", rawMap["type"])
	}
	if rawMap["transport"] != "websocket" {
		t.Errorf("expected transport websocket in json, got %v", rawMap["transport"])
	}
	if rawMap["session_id"] != sessionID {
		t.Errorf("expected session_id %s in json, got %v", sessionID, rawMap["session_id"])
	}

	audioParams, ok := rawMap["audio_params"].(map[string]any)
	if !ok {
		t.Fatalf("expected audio_params object in json, got %v", rawMap["audio_params"])
	}
	if audioParams["format"] != "opus" {
		t.Errorf("expected audio_params.format opus, got %v", audioParams["format"])
	}
	if audioParams["sample_rate"] != float64(24000) {
		t.Errorf("expected audio_params.sample_rate 24000, got %v", audioParams["sample_rate"])
	}
	if audioParams["channels"] != float64(1) {
		t.Errorf("expected audio_params.channels 1, got %v", audioParams["channels"])
	}
	if audioParams["frame_duration"] != float64(60) {
		t.Errorf("expected audio_params.frame_duration 60, got %v", audioParams["frame_duration"])
	}
}
