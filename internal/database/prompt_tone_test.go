package database

import (
	"context"
	"strconv"
	"testing"
)

func TestAgentConfig_PromptToneSnapshot(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			db := setupTestDB(t)
			ctx := context.Background()
			asrId, llmId, ttsId := createTestComponents(t, db, ctx)
			agent := &AgentConfig{
				Name:              "prompt-tone",
				ASRConfigId:       asrId,
				LLMConfigId:       llmId,
				TTSConfigId:       ttsId,
				SystemPrompt:      "你好",
				Voice:             "voice1",
				PromptToneEnabled: enabled,
				Enabled:           true,
			}
			if err := db.CreateAgentConfig(ctx, agent); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateDeviceType(ctx, &DeviceType{DeviceType: "prompt-speaker", AgentConfigId: agent.Id}); err != nil {
				t.Fatal(err)
			}

			original, err := db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "prompt-speaker")
			if err != nil {
				t.Fatal(err)
			}
			if original.Agent.PromptToneEnabled != enabled {
				t.Fatalf("created prompt tone = %v, want %v", original.Agent.PromptToneEnabled, enabled)
			}

			agent.PromptToneEnabled = !enabled
			if err := db.UpdateAgentConfigById(ctx, agent); err != nil {
				t.Fatal(err)
			}
			updated, err := db.FindAgentConfigById(ctx, agent.Id)
			if err != nil {
				t.Fatal(err)
			}
			if updated.PromptToneEnabled != !enabled {
				t.Fatalf("updated prompt tone = %v, want %v", updated.PromptToneEnabled, !enabled)
			}
			latest, err := db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "prompt-speaker")
			if err != nil {
				t.Fatal(err)
			}
			if latest.Agent.PromptToneEnabled != !enabled || original.Agent.PromptToneEnabled != enabled {
				t.Fatalf("snapshot isolation failed: original=%v, latest=%v", original.Agent.PromptToneEnabled, latest.Agent.PromptToneEnabled)
			}
		})
	}
}

func TestAgentConfig_PromptToneSQLDefault(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	asrId, llmId, ttsId := createTestComponents(t, db, ctx)
	if err := db.gormDB.WithContext(ctx).Exec(
		`INSERT INTO agent_config (name, asr_config_id, llm_config_id, tts_config_id, system_prompt, voice) VALUES ('prompt-tone-default', ?, ?, ?, '你好', 'voice1')`,
		asrId, llmId, ttsId,
	).Error; err != nil {
		t.Fatal(err)
	}

	agents, total, err := db.ListAgentConfigs(ctx, AgentConfigFilter{Name: "prompt-tone-default"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(agents) != 1 || !agents[0].PromptToneEnabled {
		t.Fatalf("SQL inserts that omit prompt tone must default to true: total=%d, agents=%+v", total, agents)
	}
}
