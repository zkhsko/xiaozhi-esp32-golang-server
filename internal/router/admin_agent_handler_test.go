package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestAdminAgentConfig_PromptTone(t *testing.T) {
	db := setupTestRouterDB(t)
	ctx := context.Background()
	asr := &database.ASRConfig{Name: "ASR", Provider: "dashscope", Endpoint: "wss://asr.example.com", Model: "asr", ConnectTimeoutMS: 5000, Enabled: true}
	llm := &database.LLMConfig{Name: "LLM", Provider: "dashscope", Endpoint: "https://llm.example.com", Model: "llm", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	tts := &database.TTSConfig{Name: "TTS", Provider: "dashscope", Endpoint: "wss://tts.example.com", Model: "tts", Voices: `["voice1"]`, ConnectTimeoutMS: 5000, Enabled: true}
	if err := db.CreateASRConfig(ctx, asr); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLLMConfig(ctx, llm); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTTSConfig(ctx, tts); err != nil {
		t.Fatal(err)
	}
	routes := NewAdminHandler(&config.Config{}, db, nil).Routes()

	for _, tc := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "omitted", want: true},
		{name: "on", value: true, want: true},
		{name: "off", value: false, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			save := func(body map[string]any, want bool) AgentConfigItem {
				t.Helper()
				data, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, "/agent-config/save", bytes.NewReader(data))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				routes.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("save failed: status=%d, body=%s", w.Code, w.Body.String())
				}
				var response struct {
					Data AgentConfigItem `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(w.Body.Bytes(), []byte(`"prompt_tone_enabled"`)) || response.Data.PromptToneEnabled != want {
					t.Fatalf("response prompt tone = %v, want %v: %s", response.Data.PromptToneEnabled, want, w.Body.String())
				}
				stored, err := db.FindAgentConfigById(ctx, response.Data.Id)
				if err != nil {
					t.Fatal(err)
				}
				if stored.PromptToneEnabled != want {
					t.Fatalf("stored prompt tone = %v, want %v", stored.PromptToneEnabled, want)
				}
				return response.Data
			}

			body := map[string]any{
				"name":          "prompt-tone-" + tc.name,
				"asr_config_id": asr.Id, "llm_config_id": llm.Id, "tts_config_id": tts.Id,
				"system_prompt": "你好", "voice": "voice1", "enabled": true,
			}
			if tc.value != nil {
				body["prompt_tone_enabled"] = tc.value
			}
			agent := save(body, tc.want)
			save(map[string]any{"id": agent.Id, "voice": "voice2"}, tc.want)
			save(map[string]any{"id": agent.Id, "prompt_tone_enabled": !tc.want}, !tc.want)
			save(map[string]any{"id": agent.Id, "enabled": false}, !tc.want)

			w := httptest.NewRecorder()
			routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/agent-config?name=prompt-tone-"+tc.name, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("list failed: status=%d, body=%s", w.Code, w.Body.String())
			}
			var response struct {
				Data AgentConfigListData `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Data.Items) != 1 || response.Data.Items[0].PromptToneEnabled != !tc.want {
				t.Fatalf("unexpected list: %s", w.Body.String())
			}
		})
	}
}
