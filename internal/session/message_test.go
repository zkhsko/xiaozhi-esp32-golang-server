package session

import (
	"errors"
	"strings"
	"testing"
)

func TestParseClientMessage_ValidMessages(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantKind    MessageKind
		wantSessID  string
		wantMode    string
		wantDetect  string
		wantAbort   string
		wantRawType string
		wantExt     bool
	}{
		{
			name:     "listen.start auto without session_id",
			input:    `{"type":"listen","state":"start","mode":"auto"}`,
			wantKind: KindListenStart,
			wantMode: "auto",
			wantExt:  false,
		},
		{
			name:       "listen.start auto with session_id",
			input:      `{"session_id":"sess-auto-1","type":"listen","state":"start","mode":"auto"}`,
			wantKind:   KindListenStart,
			wantSessID: "sess-auto-1",
			wantMode:   "auto",
			wantExt:    false,
		},
		{
			name:     "listen.start manual without session_id",
			input:    `{"type":"listen","state":"start","mode":"manual"}`,
			wantKind: KindListenStart,
			wantMode: "manual",
			wantExt:  false,
		},
		{
			name:       "listen.start manual with session_id",
			input:      `{"session_id":"sess-manual-2","type":"listen","state":"start","mode":"manual"}`,
			wantKind:   KindListenStart,
			wantSessID: "sess-manual-2",
			wantMode:   "manual",
			wantExt:    false,
		},
		{
			name:     "listen.stop without session_id",
			input:    `{"type":"listen","state":"stop"}`,
			wantKind: KindListenStop,
			wantExt:  false,
		},
		{
			name:       "listen.stop with session_id",
			input:      `{"session_id":"sess-stop-3","type":"listen","state":"stop"}`,
			wantKind:   KindListenStop,
			wantSessID: "sess-stop-3",
			wantExt:    false,
		},
		{
			name:     "listen.detect without text",
			input:    `{"type":"listen","state":"detect"}`,
			wantKind: KindListenDetect,
			wantExt:  false,
		},
		{
			name:       "listen.detect with text",
			input:      `{"type":"listen","state":"detect","text":"小智小智"}`,
			wantKind:   KindListenDetect,
			wantDetect: "小智小智",
			wantExt:    false,
		},
		{
			name:       "listen.detect with session_id and text",
			input:      `{"session_id":"sess-detect-4","type":"listen","state":"detect","text":"hi_xiaozhi"}`,
			wantKind:   KindListenDetect,
			wantSessID: "sess-detect-4",
			wantDetect: "hi_xiaozhi",
			wantExt:    false,
		},
		{
			name:     "abort without reason",
			input:    `{"type":"abort"}`,
			wantKind: KindAbort,
			wantExt:  false,
		},
		{
			name:      "abort with wake_word_detected reason",
			input:     `{"type":"abort","reason":"wake_word_detected"}`,
			wantKind:  KindAbort,
			wantAbort: "wake_word_detected",
			wantExt:   false,
		},
		{
			name:       "abort with session_id and reason",
			input:      `{"session_id":"sess-abort-5","type":"abort","reason":"button_pressed"}`,
			wantKind:   KindAbort,
			wantSessID: "sess-abort-5",
			wantAbort:  "button_pressed",
			wantExt:    false,
		},
		{
			name:     "hello message recognized",
			input:    `{"type":"hello","version":1,"transport":"websocket"}`,
			wantKind: KindHello,
			wantExt:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ParseClientMessage([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msg.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", msg.Kind, tt.wantKind)
			}
			if msg.SessionID != tt.wantSessID {
				t.Errorf("SessionID = %q, want %q", msg.SessionID, tt.wantSessID)
			}
			if msg.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", msg.Mode, tt.wantMode)
			}
			if msg.DetectText != tt.wantDetect {
				t.Errorf("DetectText = %q, want %q", msg.DetectText, tt.wantDetect)
			}
			if msg.AbortReason != tt.wantAbort {
				t.Errorf("AbortReason = %q, want %q", msg.AbortReason, tt.wantAbort)
			}
			if msg.IsExtension() != tt.wantExt {
				t.Errorf("IsExtension() = %v, want %v", msg.IsExtension(), tt.wantExt)
			}
		})
	}
}

func TestParseClientMessage_RejectRealtimeMode(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "listen.start realtime mode rejected without session_id",
			input: `{"type":"listen","state":"start","mode":"realtime"}`,
		},
		{
			name:  "listen.start realtime mode rejected with session_id",
			input: `{"session_id":"s-100","type":"listen","state":"start","mode":"realtime"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ParseClientMessage([]byte(tt.input))
			if err == nil {
				t.Fatalf("expected error for realtime mode, got msg: %+v", msg)
			}
			if !errors.Is(err, ErrUnsupportedListenMode) {
				t.Errorf("expected ErrUnsupportedListenMode, got: %v", err)
			}
		})
	}
}

func TestParseClientMessage_UnknownExtensions(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantRawType string
		wantSessID  string
	}{
		{
			name:        "mcp extension message",
			input:       `{"type":"mcp","payload":{"method":"tools/call","params":{"name":"weather"}}}`,
			wantRawType: "mcp",
		},
		{
			name:        "custom extension message with session_id",
			input:       `{"session_id":"ext-sess-1","type":"custom","action":"ping","extra":123}`,
			wantRawType: "custom",
			wantSessID:  "ext-sess-1",
		},
		{
			name:        "system extension message",
			input:       `{"type":"system","command":"reboot"}`,
			wantRawType: "system",
		},
		{
			name:        "glyph_push extension message",
			input:       `{"type":"glyph_push","pattern":"smile","duration_ms":3000}`,
			wantRawType: "glyph_push",
		},
		{
			name:        "iot extension message",
			input:       `{"type":"iot","endpoint":"light","state":"off"}`,
			wantRawType: "iot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ParseClientMessage([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected error for extension message: %v", err)
			}
			if msg.Kind != KindUnknownExtension {
				t.Errorf("Kind = %q, want %q", msg.Kind, KindUnknownExtension)
			}
			if !msg.IsExtension() {
				t.Errorf("IsExtension() = false, want true")
			}
			if msg.RawType != tt.wantRawType {
				t.Errorf("RawType = %q, want %q", msg.RawType, tt.wantRawType)
			}
			if msg.SessionID != tt.wantSessID {
				t.Errorf("SessionID = %q, want %q", msg.SessionID, tt.wantSessID)
			}
			if len(msg.RawPayload) == 0 {
				t.Errorf("RawPayload is empty, expected raw message bytes")
			}
		})
	}
}

func TestParseClientMessage_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "empty json object missing type",
			input: `{}`,
		},
		{
			name:  "only session_id missing type",
			input: `{"session_id":"s-1"}`,
		},
		{
			name:  "empty string type",
			input: `{"type":""}`,
		},
		{
			name:  "listen missing state",
			input: `{"type":"listen"}`,
		},
		{
			name:  "listen empty state",
			input: `{"type":"listen","state":""}`,
		},
		{
			name:  "listen.start missing mode",
			input: `{"type":"listen","state":"start"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseClientMessage([]byte(tt.input))
			if err == nil {
				t.Fatalf("expected error for missing required field, got nil")
			}
			if !errors.Is(err, ErrMissingRequiredField) {
				t.Errorf("expected ErrMissingRequiredField, got: %v", err)
			}
		})
	}
}

func TestParseClientMessage_InvalidValuesAndStates(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{
			name:    "listen unknown state pause",
			input:   `{"type":"listen","state":"pause"}`,
			wantErr: ErrInvalidListenState,
		},
		{
			name:    "listen unknown state resume",
			input:   `{"type":"listen","state":"resume"}`,
			wantErr: ErrInvalidListenState,
		},
		{
			name:    "listen.start invalid mode unknown_mode",
			input:   `{"type":"listen","state":"start","mode":"unknown_mode"}`,
			wantErr: ErrInvalidListenMode,
		},
		{
			name:    "listen.start invalid empty mode",
			input:   `{"type":"listen","state":"start","mode":""}`,
			wantErr: ErrInvalidListenMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseClientMessage([]byte(tt.input))
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error wrapping %v, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestParseClientMessage_InvalidFieldTypes(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "type is number",
			input: `{"type":123}`,
		},
		{
			name:  "type is boolean",
			input: `{"type":true}`,
		},
		{
			name:  "type is array",
			input: `{"type":["listen"]}`,
		},
		{
			name:  "session_id is number",
			input: `{"session_id":123,"type":"abort"}`,
		},
		{
			name:  "session_id is object",
			input: `{"session_id":{},"type":"abort"}`,
		},
		{
			name:  "listen state is number",
			input: `{"type":"listen","state":123}`,
		},
		{
			name:  "listen state is array",
			input: `{"type":"listen","state":["start"]}`,
		},
		{
			name:  "listen.start mode is number",
			input: `{"type":"listen","state":"start","mode":123}`,
		},
		{
			name:  "listen.start mode is boolean",
			input: `{"type":"listen","state":"start","mode":false}`,
		},
		{
			name:  "listen.detect text is number",
			input: `{"type":"listen","state":"detect","text":123}`,
		},
		{
			name:  "listen.detect text is object",
			input: `{"type":"listen","state":"detect","text":{"nested":"val"}}`,
		},
		{
			name:  "abort reason is number",
			input: `{"type":"abort","reason":123}`,
		},
		{
			name:  "abort reason is array",
			input: `{"type":"abort","reason":["err"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseClientMessage([]byte(tt.input))
			if err == nil {
				t.Fatalf("expected error for invalid field type, got nil")
			}
			if !errors.Is(err, ErrInvalidFieldType) {
				t.Errorf("expected ErrInvalidFieldType, got: %v", err)
			}
		})
	}
}

func TestParseClientMessage_MalformedAndNonObject(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantErr error
	}{
		{
			name:    "nil data",
			input:   nil,
			wantErr: ErrEmptyMessage,
		},
		{
			name:    "empty byte slice",
			input:   []byte{},
			wantErr: ErrEmptyMessage,
		},
		{
			name:    "malformed syntax",
			input:   []byte(`{"type":"listen",`),
			wantErr: ErrInvalidJSON,
		},
		{
			name:    "not json at all",
			input:   []byte(`not a json string`),
			wantErr: ErrInvalidJSON,
		},
		{
			name:    "top level json array",
			input:   []byte(`[1, 2, 3]`),
			wantErr: ErrInvalidMessageFormat,
		},
		{
			name:    "top level string",
			input:   []byte(`"simple string"`),
			wantErr: ErrInvalidMessageFormat,
		},
		{
			name:    "top level number",
			input:   []byte(`12345`),
			wantErr: ErrInvalidMessageFormat,
		},
		{
			name:    "top level boolean",
			input:   []byte(`true`),
			wantErr: ErrInvalidMessageFormat,
		},
		{
			name:    "top level null",
			input:   []byte(`null`),
			wantErr: ErrInvalidMessageFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseClientMessage(tt.input)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error wrapping %v, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestParseClientMessage_LimitsAndTooLarge(t *testing.T) {
	t.Run("message size exceeds default limit", func(t *testing.T) {
		// Default limit is 32 KiB (32768 bytes)
		oversized := make([]byte, MaxClientTextMessageBytes+1)
		for i := range oversized {
			oversized[i] = ' '
		}
		copy(oversized, `{"type":"abort"}`)

		_, err := ParseClientMessage(oversized)
		if err == nil {
			t.Fatalf("expected ErrMessageTooLarge, got nil")
		}
		if !errors.Is(err, ErrMessageTooLarge) {
			t.Errorf("expected ErrMessageTooLarge, got: %v", err)
		}
	})

	t.Run("custom max bytes limit exceeded", func(t *testing.T) {
		data := []byte(`{"type":"abort"}`) // len = 16
		_, err := ParseClientMessageWithLimit(data, 10)
		if err == nil {
			t.Fatalf("expected ErrMessageTooLarge, got nil")
		}
		if !errors.Is(err, ErrMessageTooLarge) {
			t.Errorf("expected ErrMessageTooLarge, got: %v", err)
		}
	})

	t.Run("session_id length exactly max allowed is accepted", func(t *testing.T) {
		exactSessID := strings.Repeat("s", MaxTextFieldLength)
		input := `{"session_id":"` + exactSessID + `","type":"abort"}`
		msg, err := ParseClientMessage([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for max length session_id: %v", err)
		}
		if msg.SessionID != exactSessID {
			t.Errorf("SessionID length = %d, want %d", len(msg.SessionID), MaxTextFieldLength)
		}
	})

	t.Run("session_id length exceeds limit", func(t *testing.T) {
		tooLongSessID := strings.Repeat("s", MaxTextFieldLength+1)
		input := `{"session_id":"` + tooLongSessID + `","type":"abort"}`
		_, err := ParseClientMessage([]byte(input))
		if err == nil {
			t.Fatalf("expected ErrFieldTooLong, got nil")
		}
		if !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("expected ErrFieldTooLong, got: %v", err)
		}
	})

	t.Run("type length exceeds limit", func(t *testing.T) {
		tooLongType := strings.Repeat("t", MaxTextFieldLength+1)
		input := `{"type":"` + tooLongType + `"}`
		_, err := ParseClientMessage([]byte(input))
		if err == nil {
			t.Fatalf("expected ErrFieldTooLong, got nil")
		}
		if !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("expected ErrFieldTooLong, got: %v", err)
		}
	})

	t.Run("listen state length exceeds limit", func(t *testing.T) {
		tooLongState := strings.Repeat("s", MaxTextFieldLength+1)
		input := `{"type":"listen","state":"` + tooLongState + `"}`
		_, err := ParseClientMessage([]byte(input))
		if err == nil {
			t.Fatalf("expected ErrFieldTooLong, got nil")
		}
		if !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("expected ErrFieldTooLong, got: %v", err)
		}
	})

	t.Run("detect text length exactly max allowed is accepted", func(t *testing.T) {
		exactText := strings.Repeat("a", MaxTextFieldLength)
		input := `{"type":"listen","state":"detect","text":"` + exactText + `"}`
		msg, err := ParseClientMessage([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for max length detect text: %v", err)
		}
		if msg.DetectText != exactText {
			t.Errorf("DetectText length = %d, want %d", len(msg.DetectText), MaxTextFieldLength)
		}
	})

	t.Run("detect text length exceeds limit", func(t *testing.T) {
		tooLongText := strings.Repeat("a", MaxTextFieldLength+1)
		input := `{"type":"listen","state":"detect","text":"` + tooLongText + `"}`
		_, err := ParseClientMessage([]byte(input))
		if err == nil {
			t.Fatalf("expected ErrFieldTooLong, got nil")
		}
		if !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("expected ErrFieldTooLong, got: %v", err)
		}
	})

	t.Run("abort reason length exactly max allowed is accepted", func(t *testing.T) {
		exactReason := strings.Repeat("r", MaxTextFieldLength)
		input := `{"type":"abort","reason":"` + exactReason + `"}`
		msg, err := ParseClientMessage([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for max length abort reason: %v", err)
		}
		if msg.AbortReason != exactReason {
			t.Errorf("AbortReason length = %d, want %d", len(msg.AbortReason), MaxTextFieldLength)
		}
	})

	t.Run("abort reason length exceeds limit", func(t *testing.T) {
		tooLongReason := strings.Repeat("r", MaxTextFieldLength+1)
		input := `{"type":"abort","reason":"` + tooLongReason + `"}`
		_, err := ParseClientMessage([]byte(input))
		if err == nil {
			t.Fatalf("expected ErrFieldTooLong, got nil")
		}
		if !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("expected ErrFieldTooLong, got: %v", err)
		}
	})

	t.Run("listen.start mode length exceeds limit", func(t *testing.T) {
		tooLongMode := strings.Repeat("m", MaxTextFieldLength+1)
		input := `{"type":"listen","state":"start","mode":"` + tooLongMode + `"}`
		_, err := ParseClientMessage([]byte(input))
		if err == nil {
			t.Fatalf("expected ErrFieldTooLong, got nil")
		}
		if !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("expected ErrFieldTooLong, got: %v", err)
		}
	})

	t.Run("unicode chinese text length exactly max allowed is accepted", func(t *testing.T) {
		exactChinese := strings.Repeat("测", MaxTextFieldLength)
		input := `{"type":"listen","state":"detect","text":"` + exactChinese + `"}`
		msg, err := ParseClientMessage([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for max rune length chinese text: %v", err)
		}
		if msg.DetectText != exactChinese {
			t.Errorf("DetectText rune count mismatch")
		}
	})

	t.Run("unicode chinese text length exceeds limit", func(t *testing.T) {
		tooLongChinese := strings.Repeat("测", MaxTextFieldLength+1)
		input := `{"type":"listen","state":"detect","text":"` + tooLongChinese + `"}`
		_, err := ParseClientMessage([]byte(input))
		if err == nil {
			t.Fatalf("expected ErrFieldTooLong, got nil")
		}
		if !errors.Is(err, ErrFieldTooLong) {
			t.Errorf("expected ErrFieldTooLong, got: %v", err)
		}
	})

	t.Run("json with whitespaces and newlines accepted", func(t *testing.T) {
		input := "  {\n    \"type\": \"listen\",\n    \"state\": \"start\",\n    \"mode\": \"auto\"\n  }  "
		msg, err := ParseClientMessage([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for formatted json: %v", err)
		}
		if msg.Kind != KindListenStart || msg.Mode != "auto" {
			t.Errorf("unexpected parsed message: %+v", msg)
		}
	})
}
