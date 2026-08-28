package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLogger_SystemPrompt_NotRedactedAndNotTruncated(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelInfo)

	// 构造一个超过 1024 字符的超长提示词
	longPrompt := "你是一个智能语音管家。" + strings.Repeat("请根据用户的需求调用对应的设备工具。", 100)
	if len([]rune(longPrompt)) <= 1024 {
		t.Fatalf("expected longPrompt length > 1024, got %d", len([]rune(longPrompt)))
	}

	l.Info("llm system prompt for turn",
		"session_id", "sess-test-12345",
		"system_prompt", longPrompt,
		"api_key", "sk-secret-key-123456",
	)

	var logMap map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
		t.Fatalf("failed to unmarshal log json: %v\ncontent: %s", err, buf.String())
	}

	// 1. 验证 system_prompt 未被脱敏且未被截断（保持完整长度）
	loggedPrompt, ok := logMap["system_prompt"].(string)
	if !ok {
		t.Fatalf("expected system_prompt in log output, got %v", logMap["system_prompt"])
	}
	if loggedPrompt != longPrompt {
		t.Fatalf("expected full system_prompt without truncation, expected len=%d, got len=%d", len(longPrompt), len(loggedPrompt))
	}
	if strings.Contains(loggedPrompt, RedactedValue) {
		t.Fatalf("system_prompt must not contain redacted value %s", RedactedValue)
	}

	// 2. 验证敏感字段 api_key 依然被正确脱敏
	loggedAPIKey, ok := logMap["api_key"].(string)
	if !ok || loggedAPIKey != RedactedValue {
		t.Fatalf("expected api_key to be redacted as %q, got %q", RedactedValue, loggedAPIKey)
	}
}

func TestLogger_PromptVariants_NoTruncation(t *testing.T) {
	testKeys := []string{
		"prompt",
		"system_prompt",
		"full_prompt",
		"user_prompt",
		"raw_prompt",
		"custom_agent_prompt",
		"tools_json",
		"tools",
	}

	for _, key := range testKeys {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			l := New(&buf, slog.LevelInfo)
			longContent := "Prompt content: " + strings.Repeat("ABCD ", 300)

			l.Info("test prompt key", key, longContent)

			var logMap map[string]any
			if err := json.Unmarshal(buf.Bytes(), &logMap); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}

			val, ok := logMap[key].(string)
			if !ok {
				t.Fatalf("expected key %s in log, got %v", key, logMap[key])
			}
			if val != longContent {
				t.Fatalf("key %s was unexpectedly truncated: got len %d, expected len %d", key, len(val), len(longContent))
			}
		})
	}
}
