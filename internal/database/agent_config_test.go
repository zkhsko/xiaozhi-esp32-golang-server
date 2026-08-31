package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// helper 创建一组测试用的基础 ASR、LLM、TTS 组件。
func createTestComponents(t *testing.T, db *Database, ctx context.Context) (asrId, llmId, ttsId uint64) {
	t.Helper()

	asr := &ASRConfig{
		Name:             "测试ASR",
		Provider:         "bailian",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "sk-test-asr-key",
		Model:            "qwen-audio-3.0-asr-flash-streaming",
		Hotwords:         `["小智"]`,
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	if err := db.CreateASRConfig(ctx, asr); err != nil {
		t.Fatalf("create test asr failed: %v", err)
	}

	llm := &LLMConfig{
		Name:                "测试LLM",
		Provider:            "dashscope",
		Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:              "sk-test-llm-key",
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}
	if err := db.CreateLLMConfig(ctx, llm); err != nil {
		t.Fatalf("create test llm failed: %v", err)
	}

	tts := &TTSConfig{
		Name:                "测试TTS",
		Provider:            "bailian",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:              "sk-test-tts-key",
		Model:               "cosyvoice-v1",
		Voices:              `["longanlingxi","longxiaochun"]`,
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}
	if err := db.CreateTTSConfig(ctx, tts); err != nil {
		t.Fatalf("create test tts failed: %v", err)
	}

	return asr.Id, llm.Id, tts.Id
}

func TestAgentConfig_CRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	// 1. Create AgentConfig
	cfg := &AgentConfig{
		Name:         "默认助手",
		ASRConfigId:  asrId,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: "你是一个智能语音助手。",
		Voice:        "longanlingxi",
		Enabled:      false,
	}

	err := db.CreateAgentConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateAgentConfig failed: %v", err)
	}
	if cfg.Id == 0 {
		t.Fatalf("expected non-zero Id after create")
	}

	// 2. Find by Id
	found, err := db.FindAgentConfigById(ctx, cfg.Id)
	if err != nil {
		t.Fatalf("FindAgentConfigById failed: %v", err)
	}
	if found.Name != "默认助手" {
		t.Errorf("expected name %q, got %q", "默认助手", found.Name)
	}
	if found.ASRConfigId != asrId {
		t.Errorf("expected asr_config_id %d, got %d", asrId, found.ASRConfigId)
	}
	if found.LLMConfigId != llmId {
		t.Errorf("expected llm_config_id %d, got %d", llmId, found.LLMConfigId)
	}
	if found.TTSConfigId != ttsId {
		t.Errorf("expected tts_config_id %d, got %d", ttsId, found.TTSConfigId)
	}
	if found.SystemPrompt != "你是一个智能语音助手。" {
		t.Errorf("expected system_prompt %q, got %q", "你是一个智能语音助手。", found.SystemPrompt)
	}
	if found.Voice != "longanlingxi" {
		t.Errorf("expected voice %q, got %q", "longanlingxi", found.Voice)
	}
	if found.Enabled {
		t.Errorf("expected enabled false, got true")
	}

	// 3. Update by Id
	found.Name = "儿童学习助手"
	found.SystemPrompt = "你是一个专为儿童设计的学习助手。"
	found.Voice = "longxiaochun"

	err = db.UpdateAgentConfigById(ctx, found)
	if err != nil {
		t.Fatalf("UpdateAgentConfigById failed: %v", err)
	}

	// 4. Verify Update
	updated, err := db.FindAgentConfigById(ctx, cfg.Id)
	if err != nil {
		t.Fatalf("FindAgentConfigById after update failed: %v", err)
	}
	if updated.Name != "儿童学习助手" {
		t.Errorf("expected updated name %q, got %q", "儿童学习助手", updated.Name)
	}
	if updated.SystemPrompt != "你是一个专为儿童设计的学习助手。" {
		t.Errorf("expected updated system_prompt %q, got %q", "你是一个专为儿童设计的学习助手。", updated.SystemPrompt)
	}
	if updated.Voice != "longxiaochun" {
		t.Errorf("expected updated voice %q, got %q", "longxiaochun", updated.Voice)
	}

	// 5. Update non-existent Id
	nonExistent := &AgentConfig{
		Id:           999999,
		Name:         "不存在的Agent",
		ASRConfigId:  asrId,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: "提示词",
		Voice:        "v1",
	}
	err = db.UpdateAgentConfigById(ctx, nonExistent)
	if !errors.Is(err, ErrAgentConfigNotFound) {
		t.Fatalf("expected ErrAgentConfigNotFound, got %v", err)
	}

	// 6. Find non-existent Id
	_, err = db.FindAgentConfigById(ctx, 999999)
	if !errors.Is(err, ErrAgentConfigNotFound) {
		t.Fatalf("expected ErrAgentConfigNotFound, got %v", err)
	}
}

func TestAgentConfig_DuplicateNameAllowed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	cfg1 := &AgentConfig{
		Name:         "同名助手",
		ASRConfigId:  asrId,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: "提示词1",
		Voice:        "v1",
	}
	cfg2 := &AgentConfig{
		Name:         "同名助手",
		ASRConfigId:  asrId,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: "提示词2",
		Voice:        "v2",
	}

	if err := db.CreateAgentConfig(ctx, cfg1); err != nil {
		t.Fatalf("failed to create first agent config: %v", err)
	}
	if err := db.CreateAgentConfig(ctx, cfg2); err != nil {
		t.Fatalf("failed to create second agent config with same name: %v", err)
	}
	if cfg1.Id == cfg2.Id {
		t.Fatalf("expected distinct Ids for duplicate names, got %d and %d", cfg1.Id, cfg2.Id)
	}
}

func TestAgentConfig_ListAndFilter(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	items := []*AgentConfig{
		{Name: "Agent-Alpha", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p1", Voice: "v1", Enabled: true},
		{Name: "Agent-Beta", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p2", Voice: "v2", Enabled: false},
		{Name: "Agent-Gamma", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p3", Voice: "v3", Enabled: false},
	}

	for _, item := range items {
		if err := db.CreateAgentConfig(ctx, item); err != nil {
			t.Fatalf("failed to create item: %v", err)
		}
	}

	// 1. List all
	list, total, err := db.ListAgentConfigs(ctx, AgentConfigFilter{})
	if err != nil {
		t.Fatalf("ListAgentConfigs failed: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Errorf("expected total 3 and len 3, got total %d, len %d", total, len(list))
	}

	// 2. Filter by Name
	list, total, err = db.ListAgentConfigs(ctx, AgentConfigFilter{Name: "Alpha"})
	if err != nil {
		t.Fatalf("ListAgentConfigs with name filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "Agent-Alpha" {
		t.Errorf("unexpected name filter result: total %d, items %v", total, list)
	}

	// 3. Filter by Enabled = true
	enabledTrue := true
	list, total, err = db.ListAgentConfigs(ctx, AgentConfigFilter{Enabled: &enabledTrue})
	if err != nil {
		t.Fatalf("ListAgentConfigs with enabled=true failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "Agent-Alpha" {
		t.Errorf("expected 1 enabled item, got total %d, len %d", total, len(list))
	}

	// 4. Filter by Enabled = false
	enabledFalse := false
	list, total, err = db.ListAgentConfigs(ctx, AgentConfigFilter{Enabled: &enabledFalse})
	if err != nil {
		t.Fatalf("ListAgentConfigs with enabled=false failed: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Errorf("expected 2 disabled items, got total %d, len %d", total, len(list))
	}

	// 5. Pagination
	list, total, err = db.ListAgentConfigs(ctx, AgentConfigFilter{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListAgentConfigs with pagination failed: %v", err)
	}
	if total != 3 || len(list) != 2 {
		t.Errorf("expected total 3 and page size 2, got total %d, len %d", total, len(list))
	}
}

func TestAgentConfig_Validation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	tests := []struct {
		name        string
		cfg         *AgentConfig
		expectedErr error
	}{
		{
			name:        "nil config",
			cfg:         nil,
			expectedErr: ErrInvalidAgentConfig,
		},
		{
			name: "empty name",
			cfg: &AgentConfig{
				Name:         "   ",
				ASRConfigId:  asrId,
				LLMConfigId:  llmId,
				TTSConfigId:  ttsId,
				SystemPrompt: "提示词",
				Voice:        "v1",
			},
			expectedErr: ErrEmptyAgentConfigName,
		},
		{
			name: "name exceeds 128 bytes",
			cfg: &AgentConfig{
				Name:         strings.Repeat("a", 129),
				ASRConfigId:  asrId,
				LLMConfigId:  llmId,
				TTSConfigId:  ttsId,
				SystemPrompt: "提示词",
				Voice:        "v1",
			},
			expectedErr: ErrInvalidAgentConfigNameLength,
		},
		{
			name: "zero asr_config_id",
			cfg: &AgentConfig{
				Name:         "valid-name",
				ASRConfigId:  0,
				LLMConfigId:  llmId,
				TTSConfigId:  ttsId,
				SystemPrompt: "提示词",
				Voice:        "v1",
			},
			expectedErr: ErrInvalidASRConfigReference,
		},
		{
			name: "zero llm_config_id",
			cfg: &AgentConfig{
				Name:         "valid-name",
				ASRConfigId:  asrId,
				LLMConfigId:  0,
				TTSConfigId:  ttsId,
				SystemPrompt: "提示词",
				Voice:        "v1",
			},
			expectedErr: ErrInvalidLLMConfigReference,
		},
		{
			name: "zero tts_config_id",
			cfg: &AgentConfig{
				Name:         "valid-name",
				ASRConfigId:  asrId,
				LLMConfigId:  llmId,
				TTSConfigId:  0,
				SystemPrompt: "提示词",
				Voice:        "v1",
			},
			expectedErr: ErrInvalidTTSConfigReference,
		},
		{
			name: "empty system_prompt",
			cfg: &AgentConfig{
				Name:         "valid-name",
				ASRConfigId:  asrId,
				LLMConfigId:  llmId,
				TTSConfigId:  ttsId,
				SystemPrompt: "   ",
				Voice:        "v1",
			},
			expectedErr: ErrEmptySystemPrompt,
		},
		{
			name: "system_prompt exceeds 16384 bytes",
			cfg: &AgentConfig{
				Name:         "valid-name",
				ASRConfigId:  asrId,
				LLMConfigId:  llmId,
				TTSConfigId:  ttsId,
				SystemPrompt: strings.Repeat("p", 16385),
				Voice:        "v1",
			},
			expectedErr: ErrInvalidSystemPromptLength,
		},
		{
			name: "empty voice",
			cfg: &AgentConfig{
				Name:         "valid-name",
				ASRConfigId:  asrId,
				LLMConfigId:  llmId,
				TTSConfigId:  ttsId,
				SystemPrompt: "提示词",
				Voice:        "   ",
			},
			expectedErr: ErrEmptyVoice,
		},
		{
			name: "voice exceeds 128 bytes",
			cfg: &AgentConfig{
				Name:         "valid-name",
				ASRConfigId:  asrId,
				LLMConfigId:  llmId,
				TTSConfigId:  ttsId,
				SystemPrompt: "提示词",
				Voice:        strings.Repeat("v", 129),
			},
			expectedErr: ErrInvalidVoiceLength,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.CreateAgentConfig(ctx, tt.cfg)
			if !errors.Is(err, tt.expectedErr) {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}
		})
	}
}

func TestAgentConfig_ComponentReferencesValidation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	// 1. ASR 不存在
	err := db.CreateAgentConfig(ctx, &AgentConfig{
		Name:         "Agent",
		ASRConfigId:  9999,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: "提示词",
		Voice:        "v1",
	})
	if !errors.Is(err, ErrReferencedASRNotFound) {
		t.Errorf("expected ErrReferencedASRNotFound, got %v", err)
	}

	// 2. LLM 不存在
	err = db.CreateAgentConfig(ctx, &AgentConfig{
		Name:         "Agent",
		ASRConfigId:  asrId,
		LLMConfigId:  9999,
		TTSConfigId:  ttsId,
		SystemPrompt: "提示词",
		Voice:        "v1",
	})
	if !errors.Is(err, ErrReferencedLLMNotFound) {
		t.Errorf("expected ErrReferencedLLMNotFound, got %v", err)
	}

	// 3. TTS 不存在
	err = db.CreateAgentConfig(ctx, &AgentConfig{
		Name:         "Agent",
		ASRConfigId:  asrId,
		LLMConfigId:  llmId,
		TTSConfigId:  9999,
		SystemPrompt: "提示词",
		Voice:        "v1",
	})
	if !errors.Is(err, ErrReferencedTTSNotFound) {
		t.Errorf("expected ErrReferencedTTSNotFound, got %v", err)
	}

	// 4. 组件处于禁用状态
	disabledASR := &ASRConfig{
		Name:             "禁用ASR",
		Provider:         "bailian",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "m1",
		ConnectTimeoutMS: 5000,
		Enabled:          false,
	}
	_ = db.CreateASRConfig(ctx, disabledASR)

	err = db.CreateAgentConfig(ctx, &AgentConfig{
		Name:         "Agent",
		ASRConfigId:  disabledASR.Id,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: "提示词",
		Voice:        "v1",
	})
	if !errors.Is(err, ErrReferencedASRDisabled) {
		t.Errorf("expected ErrReferencedASRDisabled, got %v", err)
	}
}

func TestAgentConfig_ActivateAgent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	agent1 := &AgentConfig{Name: "Agent-1", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p1", Voice: "v1", Enabled: false}
	agent2 := &AgentConfig{Name: "Agent-2", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p2", Voice: "v2", Enabled: false}

	_ = db.CreateAgentConfig(ctx, agent1)
	_ = db.CreateAgentConfig(ctx, agent2)

	// 1. 激活 Agent 1
	if err := db.ActivateAgent(ctx, agent1.Id); err != nil {
		t.Fatalf("ActivateAgent 1 failed: %v", err)
	}

	a1, _ := db.FindAgentConfigById(ctx, agent1.Id)
	a2, _ := db.FindAgentConfigById(ctx, agent2.Id)
	if !a1.Enabled {
		t.Errorf("expected agent 1 enabled=true")
	}
	if a2.Enabled {
		t.Errorf("expected agent 2 enabled=false")
	}

	// 2. 激活 Agent 2
	if err := db.ActivateAgent(ctx, agent2.Id); err != nil {
		t.Fatalf("ActivateAgent 2 failed: %v", err)
	}

	a1, _ = db.FindAgentConfigById(ctx, agent1.Id)
	a2, _ = db.FindAgentConfigById(ctx, agent2.Id)
	if a1.Enabled {
		t.Errorf("expected agent 1 enabled=false after switching")
	}
	if !a2.Enabled {
		t.Errorf("expected agent 2 enabled=true after switching")
	}

	// 3. 激活不存在的 Agent
	err := db.ActivateAgent(ctx, 99999)
	if !errors.Is(err, ErrAgentConfigNotFound) {
		t.Errorf("expected ErrAgentConfigNotFound, got %v", err)
	}

	// 4. 激活参数为 0
	err = db.ActivateAgent(ctx, 0)
	if !errors.Is(err, ErrInvalidAgentConfigId) {
		t.Errorf("expected ErrInvalidAgentConfigId, got %v", err)
	}
}

func TestAgentConfig_FindActiveAgentRuntimeSnapshot(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	// 1. 无 enabled Agent 时查询报错
	_, err := db.FindActiveAgentRuntimeSnapshot(ctx)
	if !errors.Is(err, ErrActiveAgentStateInvalid) {
		t.Fatalf("expected ErrActiveAgentStateInvalid when no active agent, got %v", err)
	}

	// 2. 创建并激活 Agent
	agent := &AgentConfig{
		Name:         "活跃助手",
		ASRConfigId:  asrId,
		LLMConfigId:  llmId,
		TTSConfigId:  ttsId,
		SystemPrompt: "系统提示词内容",
		Voice:        "longxiaochun",
		Enabled:      false,
	}
	if err := db.CreateAgentConfig(ctx, agent); err != nil {
		t.Fatalf("CreateAgentConfig failed: %v", err)
	}
	if err := db.ActivateAgent(ctx, agent.Id); err != nil {
		t.Fatalf("ActivateAgent failed: %v", err)
	}

	// 3. 成功获取快照
	snapshot, err := db.FindActiveAgentRuntimeSnapshot(ctx)
	if err != nil {
		t.Fatalf("FindActiveAgentRuntimeSnapshot failed: %v", err)
	}

	if snapshot.Agent.Id != agent.Id {
		t.Errorf("expected snapshot Agent Id %d, got %d", agent.Id, snapshot.Agent.Id)
	}
	if snapshot.Agent.Name != "活跃助手" {
		t.Errorf("expected snapshot Agent Name %q, got %q", "活跃助手", snapshot.Agent.Name)
	}
	if snapshot.Agent.SystemPrompt != "系统提示词内容" {
		t.Errorf("expected snapshot Agent SystemPrompt %q, got %q", "系统提示词内容", snapshot.Agent.SystemPrompt)
	}
	if snapshot.Agent.Voice != "longxiaochun" {
		t.Errorf("expected snapshot Agent Voice %q, got %q", "longxiaochun", snapshot.Agent.Voice)
	}
	if !snapshot.Agent.Enabled {
		t.Errorf("expected snapshot Agent Enabled true, got false")
	}

	// 验证 ASR 快照
	if snapshot.ASRConfig.Id != asrId || snapshot.ASRConfig.Model != "qwen-audio-3.0-asr-flash-streaming" || snapshot.ASRConfig.Provider != "bailian" {
		t.Errorf("unexpected ASR snapshot: %+v", snapshot.ASRConfig)
	}
	// 验证 LLM 快照
	if snapshot.LLMConfig.Id != llmId || snapshot.LLMConfig.Model != "qwen-plus" || snapshot.LLMConfig.Provider != "dashscope" {
		t.Errorf("unexpected LLM snapshot: %+v", snapshot.LLMConfig)
	}
	// 验证 TTS 快照
	if snapshot.TTSConfig.Id != ttsId || snapshot.TTSConfig.Model != "cosyvoice-v1" || snapshot.TTSConfig.Provider != "bailian" {
		t.Errorf("unexpected TTS snapshot: %+v", snapshot.TTSConfig)
	}

	// 4. FindAgentRuntimeSnapshotById
	byIdSnapshot, err := db.FindAgentRuntimeSnapshotById(ctx, agent.Id)
	if err != nil {
		t.Fatalf("FindAgentRuntimeSnapshotById failed: %v", err)
	}
	if byIdSnapshot.Agent.Id != agent.Id {
		t.Errorf("expected Agent Id %d, got %d", agent.Id, byIdSnapshot.Agent.Id)
	}

	// 查询不存在的 Id
	_, err = db.FindAgentRuntimeSnapshotById(ctx, 99999)
	if !errors.Is(err, ErrAgentConfigNotFound) {
		t.Errorf("expected ErrAgentConfigNotFound, got %v", err)
	}
}

func TestAgentConfig_DeleteAndBatchDelete(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	asrId, llmId, ttsId := createTestComponents(t, db, ctx)

	agent1 := &AgentConfig{Name: "Del-1", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p1", Voice: "v1"}
	agent2 := &AgentConfig{Name: "Del-2", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p2", Voice: "v2"}
	agent3 := &AgentConfig{Name: "Del-3", ASRConfigId: asrId, LLMConfigId: llmId, TTSConfigId: ttsId, SystemPrompt: "p3", Voice: "v3"}

	_ = db.CreateAgentConfig(ctx, agent1)
	_ = db.CreateAgentConfig(ctx, agent2)
	_ = db.CreateAgentConfig(ctx, agent3)

	// 1. 单条删除
	if err := db.DeleteAgentConfig(ctx, agent1.Id); err != nil {
		t.Fatalf("DeleteAgentConfig failed: %v", err)
	}
	_, err := db.FindAgentConfigById(ctx, agent1.Id)
	if !errors.Is(err, ErrAgentConfigNotFound) {
		t.Errorf("expected ErrAgentConfigNotFound, got %v", err)
	}

	// 删除不存在的 Id
	if err := db.DeleteAgentConfig(ctx, 99999); !errors.Is(err, ErrAgentConfigNotFound) {
		t.Errorf("expected ErrAgentConfigNotFound for non-existent Id, got %v", err)
	}

	// 2. 批量删除
	if err := db.BatchDeleteAgentConfigs(ctx, []uint64{agent2.Id, agent3.Id}); err != nil {
		t.Fatalf("BatchDeleteAgentConfigs failed: %v", err)
	}

	list, total, err := db.ListAgentConfigs(ctx, AgentConfigFilter{})
	if err != nil {
		t.Fatalf("ListAgentConfigs failed: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("expected 0 configs, got %d", total)
	}
}

func TestAgentConfig_NilDB(t *testing.T) {
	var nilDB *Database
	ctx := context.Background()

	if err := nilDB.CreateAgentConfig(ctx, &AgentConfig{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, err := nilDB.FindAgentConfigById(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.UpdateAgentConfigById(ctx, &AgentConfig{Id: 1}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, _, err := nilDB.ListAgentConfigs(ctx, AgentConfigFilter{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.DeleteAgentConfig(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.BatchDeleteAgentConfigs(ctx, []uint64{1}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.ActivateAgent(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, err := nilDB.FindActiveAgentRuntimeSnapshot(ctx); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, err := nilDB.FindAgentRuntimeSnapshotById(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}
