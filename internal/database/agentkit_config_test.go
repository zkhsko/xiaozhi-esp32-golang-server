package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAgentKitConfig_CRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	_ = db.gormDB.Exec("DELETE FROM agentkit_config")

	// 1. Create AgentKitConfig
	cfg := &AgentKitConfig{
		ToolName:   "server.get_current_weather",
		ToolConfig: `{"api_key":"test_key","location":"WX4SUCU47R3T"}`,
		Enabled:    true,
	}

	err := db.CreateAgentKitConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateAgentKitConfig failed: %v", err)
	}
	if cfg.Id == 0 {
		t.Fatal("expected non-zero Id after creation")
	}

	// 2. Find by Id
	found, err := db.FindAgentKitConfigById(ctx, cfg.Id)
	if err != nil {
		t.Fatalf("FindAgentKitConfigById failed: %v", err)
	}
	if found.ToolName != "server.get_current_weather" {
		t.Fatalf("expected tool_name %q, got %q", "server.get_current_weather", found.ToolName)
	}
	if found.ToolConfig != `{"api_key":"test_key","location":"WX4SUCU47R3T"}` {
		t.Fatalf("expected tool_config %q, got %q", `{"api_key":"test_key","location":"WX4SUCU47R3T"}`, found.ToolConfig)
	}
	if !found.Enabled {
		t.Fatal("expected enabled to be true")
	}

	// 3. Find by ToolName
	foundByName, err := db.FindAgentKitConfigByToolName(ctx, "server.get_current_weather")
	if err != nil {
		t.Fatalf("FindAgentKitConfigByToolName failed: %v", err)
	}
	if foundByName.Id != cfg.Id {
		t.Fatalf("expected Id %d, got %d", cfg.Id, foundByName.Id)
	}

	// 4. Update AgentKitConfig
	found.ToolConfig = `{"api_key":"new_key","location":"101010100"}`
	found.Enabled = false
	err = db.UpdateAgentKitConfigById(ctx, found)
	if err != nil {
		t.Fatalf("UpdateAgentKitConfigById failed: %v", err)
	}

	updated, err := db.FindAgentKitConfigById(ctx, cfg.Id)
	if err != nil {
		t.Fatalf("FindAgentKitConfigById after update failed: %v", err)
	}
	if updated.ToolConfig != `{"api_key":"new_key","location":"101010100"}` {
		t.Fatalf("expected updated config, got %q", updated.ToolConfig)
	}
	if updated.Enabled {
		t.Fatal("expected enabled to be false after update")
	}

	// 5. Delete AgentKitConfig
	err = db.DeleteAgentKitConfig(ctx, cfg.Id)
	if err != nil {
		t.Fatalf("DeleteAgentKitConfig failed: %v", err)
	}

	_, err = db.FindAgentKitConfigById(ctx, cfg.Id)
	if !errors.Is(err, ErrAgentKitConfigNotFound) {
		t.Fatalf("expected ErrAgentKitConfigNotFound, got %v", err)
	}
}

func TestAgentKitConfig_Validation(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *AgentKitConfig
		expectedErr error
	}{
		{
			name:        "NilConfig",
			cfg:         nil,
			expectedErr: ErrInvalidAgentKitConfig,
		},
		{
			name: "EmptyToolName",
			cfg: &AgentKitConfig{
				ToolName:   "   ",
				ToolConfig: `{"key":"val"}`,
			},
			expectedErr: ErrEmptyAgentKitToolName,
		},
		{
			name: "ToolNameTooLong",
			cfg: &AgentKitConfig{
				ToolName:   strings.Repeat("a", 129),
				ToolConfig: `{"key":"val"}`,
			},
			expectedErr: ErrInvalidAgentKitToolNameLength,
		},
		{
			name: "EmptyToolConfig",
			cfg: &AgentKitConfig{
				ToolName:   "server.tool",
				ToolConfig: "   ",
			},
			expectedErr: ErrEmptyAgentKitToolConfig,
		},
		{
			name: "InvalidJSONToolConfig",
			cfg: &AgentKitConfig{
				ToolName:   "server.tool",
				ToolConfig: `not-a-json`,
			},
			expectedErr: ErrInvalidAgentKitToolConfigJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if !errors.Is(err, tt.expectedErr) {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}
		})
	}
}

func TestAgentKitConfig_DuplicateToolName(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	_ = db.gormDB.Exec("DELETE FROM agentkit_config")

	cfg1 := &AgentKitConfig{
		ToolName:   "server.duplicate",
		ToolConfig: `{"k":"v1"}`,
		Enabled:    true,
	}
	if err := db.CreateAgentKitConfig(ctx, cfg1); err != nil {
		t.Fatalf("CreateAgentKitConfig cfg1 failed: %v", err)
	}

	cfg2 := &AgentKitConfig{
		ToolName:   "server.duplicate",
		ToolConfig: `{"k":"v2"}`,
		Enabled:    true,
	}
	err := db.CreateAgentKitConfig(ctx, cfg2)
	if !errors.Is(err, ErrAgentKitToolNameDuplicate) {
		t.Fatalf("expected ErrAgentKitToolNameDuplicate, got %v", err)
	}
}

func TestAgentKitConfig_ListAndFilter(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	_ = db.gormDB.Exec("DELETE FROM agentkit_config")

	cfg1 := &AgentKitConfig{ToolName: "server.weather", ToolConfig: `{"k":"1"}`, Enabled: true}
	cfg2 := &AgentKitConfig{ToolName: "server.search", ToolConfig: `{"k":"2"}`, Enabled: false}
	cfg3 := &AgentKitConfig{ToolName: "server.time", ToolConfig: `{"k":"3"}`, Enabled: true}

	if err := db.CreateAgentKitConfig(ctx, cfg1); err != nil {
		t.Fatalf("create cfg1 failed: %v", err)
	}
	if err := db.CreateAgentKitConfig(ctx, cfg2); err != nil {
		t.Fatalf("create cfg2 failed: %v", err)
	}
	if err := db.CreateAgentKitConfig(ctx, cfg3); err != nil {
		t.Fatalf("create cfg3 failed: %v", err)
	}

	// 1. ListEnabledAgentKitConfigs
	enabledList, err := db.ListEnabledAgentKitConfigs(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAgentKitConfigs failed: %v", err)
	}
	if len(enabledList) != 2 {
		t.Fatalf("expected 2 enabled configs, got %d", len(enabledList))
	}

	// 2. Filter by tool_name
	list, total, err := db.ListAgentKitConfigs(ctx, AgentKitConfigFilter{ToolName: "weather"})
	if err != nil {
		t.Fatalf("ListAgentKitConfigs with name filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected 1 result, got %d (total: %d)", len(list), total)
	}

	// 3. BatchDelete
	err = db.BatchDeleteAgentKitConfigs(ctx, []uint64{cfg1.Id, cfg2.Id, cfg3.Id})
	if err != nil {
		t.Fatalf("BatchDeleteAgentKitConfigs failed: %v", err)
	}
	afterDelete, err := db.ListEnabledAgentKitConfigs(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAgentKitConfigs after delete failed: %v", err)
	}
	if len(afterDelete) != 0 {
		t.Fatalf("expected 0 configs after delete, got %d", len(afterDelete))
	}
}
