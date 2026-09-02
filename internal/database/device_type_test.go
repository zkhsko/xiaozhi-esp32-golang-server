package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func createTestAgent(t *testing.T, db *Database, ctx context.Context, name string) uint64 {
	t.Helper()
	asr := &ASRConfig{
		Name:             "测试ASR-" + name,
		Provider:         "dashscope",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "qwen-asr",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	if err := db.CreateASRConfig(ctx, asr); err != nil {
		t.Fatalf("create test asr failed: %v", err)
	}

	llm := &LLMConfig{
		Name:                "测试LLM-" + name,
		Provider:            "dashscope",
		Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}
	if err := db.CreateLLMConfig(ctx, llm); err != nil {
		t.Fatalf("create test llm failed: %v", err)
	}

	tts := &TTSConfig{
		Name:              "测试TTS-" + name,
		Provider:          "dashscope",
		Endpoint:          "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:             "cosyvoice-v1",
		Voices:            `["voice1"]`,
		ConnectTimeoutMS:  5000,
		SentenceTimeoutMS: 10000,
		Enabled:           true,
	}
	if err := db.CreateTTSConfig(ctx, tts); err != nil {
		t.Fatalf("create test tts failed: %v", err)
	}

	agent := &AgentConfig{
		Name:         "测试Agent-" + name,
		ASRConfigId:  asr.Id,
		LLMConfigId:  llm.Id,
		TTSConfigId:  tts.Id,
		SystemPrompt: "你是一个助手",
		Voice:        "voice1",
		Enabled:      true,
	}
	if err := db.CreateAgentConfig(ctx, agent); err != nil {
		t.Fatalf("create test agent failed: %v", err)
	}
	return agent.Id
}

func TestDeviceTypeCRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	agent1Id := createTestAgent(t, db, ctx, "1")
	agent2Id := createTestAgent(t, db, ctx, "2")

	// 1. Create DeviceType
	dt1 := &DeviceType{
		DeviceType:    "robot-dog",
		AgentConfigId: agent1Id,
	}
	if err := db.CreateDeviceType(ctx, dt1); err != nil {
		t.Fatalf("CreateDeviceType failed: %v", err)
	}
	if dt1.Id == 0 {
		t.Errorf("expected non-zero Id, got %d", dt1.Id)
	}
	if dt1.DeviceType != "robot-dog" {
		t.Errorf("expected device_type robot-dog, got %s", dt1.DeviceType)
	}
	if dt1.AgentConfigId != agent1Id {
		t.Errorf("expected agent_config_id %d, got %d", agent1Id, dt1.AgentConfigId)
	}

	// 2. Find by Id
	foundById, err := db.FindDeviceTypeById(ctx, dt1.Id)
	if err != nil {
		t.Fatalf("FindDeviceTypeById failed: %v", err)
	}
	if foundById.Id != dt1.Id || foundById.DeviceType != "robot-dog" || foundById.AgentConfigId != agent1Id {
		t.Errorf("FindDeviceTypeById mismatch: %+v", foundById)
	}

	// 3. Find by device_type
	found, err := db.FindDeviceTypeByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("FindDeviceTypeByDeviceType failed: %v", err)
	}
	if found.Id != dt1.Id || found.AgentConfigId != agent1Id {
		t.Errorf("expected found Id=%d agent_config_id=%d, got Id=%d agent_config_id=%d",
			dt1.Id, agent1Id, found.Id, found.AgentConfigId)
	}

	// 4. Update DeviceType by Id
	dt1.AgentConfigId = agent2Id
	dt1.DeviceType = "robot-dog-updated"
	if err := db.UpdateDeviceTypeById(ctx, dt1); err != nil {
		t.Fatalf("UpdateDeviceTypeById failed: %v", err)
	}

	foundAfterUpdate, err := db.FindDeviceTypeById(ctx, dt1.Id)
	if err != nil {
		t.Fatalf("FindDeviceTypeById after update failed: %v", err)
	}
	if foundAfterUpdate.DeviceType != "robot-dog-updated" || foundAfterUpdate.AgentConfigId != agent2Id {
		t.Errorf("expected updated DeviceType robot-dog-updated and agent %d, got %s and %d",
			agent2Id, foundAfterUpdate.DeviceType, foundAfterUpdate.AgentConfigId)
	}

	// 5. Add second device type
	dt2 := &DeviceType{
		DeviceType:    "smart-speaker",
		AgentConfigId: agent2Id,
	}
	if err := db.CreateDeviceType(ctx, dt2); err != nil {
		t.Fatalf("CreateDeviceType dt2 failed: %v", err)
	}

	// 6. Find by agent_config_id
	typesByAgent, err := db.FindDeviceTypesByAgentConfigId(ctx, agent2Id)
	if err != nil {
		t.Fatalf("FindDeviceTypesByAgentConfigId failed: %v", err)
	}
	if len(typesByAgent) != 2 {
		t.Fatalf("expected 2 types for agent %d, got %d", agent2Id, len(typesByAgent))
	}

	// 7. List with filter
	list, total, err := db.ListDeviceTypes(ctx, DeviceTypeFilter{})
	if err != nil {
		t.Fatalf("ListDeviceTypes failed: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("expected 2 types in list, got total=%d len=%d", total, len(list))
	}

	// Filter by device_type keyword
	filteredList, filteredTotal, err := db.ListDeviceTypes(ctx, DeviceTypeFilter{DeviceType: "speaker"})
	if err != nil {
		t.Fatalf("ListDeviceTypes filter failed: %v", err)
	}
	if filteredTotal != 1 || len(filteredList) != 1 || filteredList[0].DeviceType != "smart-speaker" {
		t.Fatalf("unexpected filter result: total=%d, items=%+v", filteredTotal, filteredList)
	}

	// Filter by agent_config_id
	agentFiltered, agentTotal, err := db.ListDeviceTypes(ctx, DeviceTypeFilter{AgentConfigId: agent2Id})
	if err != nil {
		t.Fatalf("ListDeviceTypes by agent failed: %v", err)
	}
	if agentTotal != 2 || len(agentFiltered) != 2 {
		t.Fatalf("expected 2 for agent filter, got total=%d", agentTotal)
	}

	// 8. Delete by Id
	err = db.DeleteDeviceType(ctx, dt1.Id)
	if err != nil {
		t.Fatalf("DeleteDeviceType failed: %v", err)
	}

	_, err = db.FindDeviceTypeById(ctx, dt1.Id)
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound after delete, got %v", err)
	}

	// 9. Batch delete remaining
	err = db.BatchDeleteDeviceTypes(ctx, []uint64{dt2.Id})
	if err != nil {
		t.Fatalf("BatchDeleteDeviceTypes failed: %v", err)
	}

	_, afterBatchTotal, err := db.ListDeviceTypes(ctx, DeviceTypeFilter{})
	if err != nil {
		t.Fatalf("ListDeviceTypes after batch delete failed: %v", err)
	}
	if afterBatchTotal != 0 {
		t.Errorf("expected 0 records remaining, got %d", afterBatchTotal)
	}
}

func TestDeviceTypeUpsertCompatibility(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Initial Upsert
	dt1, err := db.UpsertDeviceType(ctx, "robot-dog", 101)
	if err != nil {
		t.Fatalf("UpsertDeviceType failed: %v", err)
	}
	if dt1.Id == 0 || dt1.DeviceType != "robot-dog" || dt1.AgentConfigId != 101 {
		t.Errorf("UpsertDeviceType mismatch: %+v", dt1)
	}

	// 2. Upsert update
	updated, err := db.UpsertDeviceType(ctx, "robot-dog", 202)
	if err != nil {
		t.Fatalf("UpsertDeviceType update failed: %v", err)
	}
	if updated.Id != dt1.Id || updated.AgentConfigId != 202 {
		t.Errorf("UpsertDeviceType update mismatch: %+v", updated)
	}

	// 3. Delete by device_type
	err = db.DeleteDeviceTypeByDeviceType(ctx, "robot-dog")
	if err != nil {
		t.Fatalf("DeleteDeviceTypeByDeviceType failed: %v", err)
	}

	_, err = db.FindDeviceTypeByDeviceType(ctx, "robot-dog")
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound, got %v", err)
	}
}

func TestDeviceTypeValidation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	agentId := createTestAgent(t, db, ctx, "val")

	// Nil struct
	err := db.CreateDeviceType(ctx, nil)
	if !errors.Is(err, ErrInvalidDeviceType) {
		t.Errorf("expected ErrInvalidDeviceType, got %v", err)
	}

	// Empty device type
	err = db.CreateDeviceType(ctx, &DeviceType{DeviceType: "   ", AgentConfigId: agentId})
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// Exceeds 32 chars
	err = db.CreateDeviceType(ctx, &DeviceType{DeviceType: strings.Repeat("a", 33), AgentConfigId: agentId})
	if !errors.Is(err, ErrInvalidDeviceTypeLength) {
		t.Errorf("expected ErrInvalidDeviceTypeLength, got %v", err)
	}

	// Zero agent config id
	err = db.CreateDeviceType(ctx, &DeviceType{DeviceType: "box", AgentConfigId: 0})
	if !errors.Is(err, ErrInvalidAgentConfigId) {
		t.Errorf("expected ErrInvalidAgentConfigId, got %v", err)
	}

	// Non-existent agent config id
	err = db.CreateDeviceType(ctx, &DeviceType{DeviceType: "box", AgentConfigId: 99999})
	if !errors.Is(err, ErrReferencedAgentNotFound) {
		t.Errorf("expected ErrReferencedAgentNotFound, got %v", err)
	}

	// Duplicate device type create
	validDt := &DeviceType{DeviceType: "box", AgentConfigId: agentId}
	if err := db.CreateDeviceType(ctx, validDt); err != nil {
		t.Fatalf("CreateDeviceType validDt failed: %v", err)
	}

	dupDt := &DeviceType{DeviceType: "box", AgentConfigId: agentId}
	err = db.CreateDeviceType(ctx, dupDt)
	if !errors.Is(err, ErrDeviceTypeAlreadyExists) {
		t.Errorf("expected ErrDeviceTypeAlreadyExists, got %v", err)
	}

	// Find by non-existent Id
	_, err = db.FindDeviceTypeById(ctx, 99999)
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound, got %v", err)
	}

	// Find by zero Id
	_, err = db.FindDeviceTypeById(ctx, 0)
	if !errors.Is(err, ErrInvalidDeviceTypeId) {
		t.Errorf("expected ErrInvalidDeviceTypeId, got %v", err)
	}

	// Update non-existent Id
	err = db.UpdateDeviceTypeById(ctx, &DeviceType{Id: 99999, DeviceType: "new-box", AgentConfigId: agentId})
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound, got %v", err)
	}

	// Delete non-existent Id
	err = db.DeleteDeviceType(ctx, 99999)
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound, got %v", err)
	}

	// Nil DB checks
	var nilDB *Database
	err = nilDB.CreateDeviceType(ctx, validDt)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, err = nilDB.FindDeviceTypeById(ctx, 1)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	err = nilDB.UpdateDeviceTypeById(ctx, validDt)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	_, _, err = nilDB.ListDeviceTypes(ctx, DeviceTypeFilter{})
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	err = nilDB.DeleteDeviceType(ctx, 1)
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	err = nilDB.BatchDeleteDeviceTypes(ctx, []uint64{1})
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}

func TestResolveAgentRuntimeSnapshotByDeviceType(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Nil DB 检查
	var nilDB *Database
	_, err := nilDB.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "robot")
	if !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}

	// 2. 空 deviceType 检查
	_, err = db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "   ")
	if !errors.Is(err, ErrEmptyDeviceType) {
		t.Errorf("expected ErrEmptyDeviceType, got %v", err)
	}

	// 3. 设备类型未找到 Fail Fast
	_, err = db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "non-existent-device")
	if !errors.Is(err, ErrDeviceTypeNotFound) {
		t.Errorf("expected ErrDeviceTypeNotFound, got %v", err)
	}

	// 4. 正常链路测试
	asr := &ASRConfig{
		Name:             "ASR-Snap",
		Provider:         "dashscope",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "qwen-asr",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	if err := db.CreateASRConfig(ctx, asr); err != nil {
		t.Fatalf("create asr: %v", err)
	}

	llm := &LLMConfig{
		Name:                "LLM-Snap",
		Provider:            "dashscope",
		Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Model:               "qwen-plus",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}
	if err := db.CreateLLMConfig(ctx, llm); err != nil {
		t.Fatalf("create llm: %v", err)
	}

	tts := &TTSConfig{
		Name:              "TTS-Snap",
		Provider:          "dashscope",
		Endpoint:          "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:             "cosyvoice-v1",
		Voices:            `["voice1"]`,
		ConnectTimeoutMS:  5000,
		SentenceTimeoutMS: 10000,
		Enabled:           true,
	}
	if err := db.CreateTTSConfig(ctx, tts); err != nil {
		t.Fatalf("create tts: %v", err)
	}

	agent := &AgentConfig{
		Name:         "Agent-Snap",
		ASRConfigId:  asr.Id,
		LLMConfigId:  llm.Id,
		TTSConfigId:  tts.Id,
		SystemPrompt: "你是专用管家",
		Voice:        "voice1",
		Enabled:      false, // 验证即使 enabled=false 只要关联正确也能根据 device_type 加载
	}
	if err := db.CreateAgentConfig(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	dt := &DeviceType{
		DeviceType:    "my-robot",
		AgentConfigId: agent.Id,
	}
	if err := db.CreateDeviceType(ctx, dt); err != nil {
		t.Fatalf("create device type: %v", err)
	}

	snapshot, err := db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "my-robot")
	if err != nil {
		t.Fatalf("ResolveAgentRuntimeSnapshotByDeviceType failed: %v", err)
	}
	if snapshot.Agent.Id != agent.Id || snapshot.Agent.SystemPrompt != "你是专用管家" || snapshot.Agent.Voice != "voice1" {
		t.Errorf("snapshot agent mismatch: %+v", snapshot.Agent)
	}
	if snapshot.ASRConfig.Id != asr.Id || snapshot.LLMConfig.Id != llm.Id || snapshot.TTSConfig.Id != tts.Id {
		t.Errorf("snapshot component IDs mismatch: ASR=%d, LLM=%d, TTS=%d",
			snapshot.ASRConfig.Id, snapshot.LLMConfig.Id, snapshot.TTSConfig.Id)
	}

	// 5. ASR 被禁用 Fail Fast
	asr.Enabled = false
	if err := db.UpdateASRConfigById(ctx, asr); err != nil {
		t.Fatalf("disable asr: %v", err)
	}
	_, err = db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "my-robot")
	if !errors.Is(err, ErrReferencedASRDisabled) {
		t.Errorf("expected ErrReferencedASRDisabled, got %v", err)
	}
	asr.Enabled = true
	_ = db.UpdateASRConfigById(ctx, asr)

	// 6. LLM 被禁用 Fail Fast
	llm.Enabled = false
	if err := db.UpdateLLMConfigById(ctx, llm); err != nil {
		t.Fatalf("disable llm: %v", err)
	}
	_, err = db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "my-robot")
	if !errors.Is(err, ErrReferencedLLMDisabled) {
		t.Errorf("expected ErrReferencedLLMDisabled, got %v", err)
	}
	llm.Enabled = true
	_ = db.UpdateLLMConfigById(ctx, llm)

	// 7. TTS 被禁用 Fail Fast
	tts.Enabled = false
	if err := db.UpdateTTSConfigById(ctx, tts); err != nil {
		t.Fatalf("disable tts: %v", err)
	}
	_, err = db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "my-robot")
	if !errors.Is(err, ErrReferencedTTSDisabled) {
		t.Errorf("expected ErrReferencedTTSDisabled, got %v", err)
	}
	tts.Enabled = true
	_ = db.UpdateTTSConfigById(ctx, tts)

	// 8. 智能体被删除后点查 Fail Fast
	if err := db.DeleteAgentConfig(ctx, agent.Id); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	_, err = db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "my-robot")
	if !errors.Is(err, ErrAgentConfigNotFound) {
		t.Errorf("expected ErrAgentConfigNotFound, got %v", err)
	}
}
