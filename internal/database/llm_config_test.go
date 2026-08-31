package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLLMConfig_CRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Create LLMConfig
	cfg := &LLMConfig{
		Name:                "DashScope大语言模型",
		Provider:            "dashscope",
		Endpoint:            "https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation",
		APIKey:              "sk-test-llm-api-key-123456",
		Model:               "qwen-max",
		ProxyURL:            "http://127.0.0.1:7890",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}

	err := db.CreateLLMConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateLLMConfig failed: %v", err)
	}
	if cfg.Id == 0 {
		t.Fatalf("expected non-zero Id after create")
	}

	// 2. Find by Id
	found, err := db.FindLLMConfigById(ctx, cfg.Id)
	if err != nil {
		t.Fatalf("FindLLMConfigById failed: %v", err)
	}
	if found.Name != "DashScope大语言模型" {
		t.Errorf("expected name %q, got %q", "DashScope大语言模型", found.Name)
	}
	if found.Provider != "dashscope" {
		t.Errorf("expected provider %q, got %q", "dashscope", found.Provider)
	}
	if found.Endpoint != "https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation" {
		t.Errorf("expected endpoint %q, got %q", "https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation", found.Endpoint)
	}
	if found.APIKey != "sk-test-llm-api-key-123456" {
		t.Errorf("expected api_key %q, got %q", "sk-test-llm-api-key-123456", found.APIKey)
	}
	if found.Model != "qwen-max" {
		t.Errorf("expected model %q, got %q", "qwen-max", found.Model)
	}
	if found.ProxyURL != "http://127.0.0.1:7890" {
		t.Errorf("expected proxy_url %q, got %q", "http://127.0.0.1:7890", found.ProxyURL)
	}
	if found.FirstTokenTimeoutMS != 5000 {
		t.Errorf("expected first_token_timeout_ms 5000, got %d", found.FirstTokenTimeoutMS)
	}
	if found.OverallTimeoutMS != 30000 {
		t.Errorf("expected overall_timeout_ms 30000, got %d", found.OverallTimeoutMS)
	}
	if !found.Enabled {
		t.Errorf("expected enabled true, got false")
	}

	// 3. Update by Id
	found.Name = "百炼大语言模型-更新版"
	found.Provider = "openai"
	found.Endpoint = "http://localhost:8000/v1/chat/completions"
	found.APIKey = "sk-new-llm-key-654321"
	found.Model = "qwen-plus"
	found.ProxyURL = "socks5://127.0.0.1:1080"
	found.FirstTokenTimeoutMS = 8000
	found.OverallTimeoutMS = 45000
	found.Enabled = false

	err = db.UpdateLLMConfigById(ctx, found)
	if err != nil {
		t.Fatalf("UpdateLLMConfigById failed: %v", err)
	}

	// 4. Verify Update
	updated, err := db.FindLLMConfigById(ctx, cfg.Id)
	if err != nil {
		t.Fatalf("FindLLMConfigById after update failed: %v", err)
	}
	if updated.Name != "百炼大语言模型-更新版" {
		t.Errorf("expected updated name %q, got %q", "百炼大语言模型-更新版", updated.Name)
	}
	if updated.Provider != "openai" {
		t.Errorf("expected updated provider %q, got %q", "openai", updated.Provider)
	}
	if updated.Endpoint != "http://localhost:8000/v1/chat/completions" {
		t.Errorf("expected updated endpoint %q, got %q", "http://localhost:8000/v1/chat/completions", updated.Endpoint)
	}
	if updated.APIKey != "sk-new-llm-key-654321" {
		t.Errorf("expected updated api_key %q, got %q", "sk-new-llm-key-654321", updated.APIKey)
	}
	if updated.Model != "qwen-plus" {
		t.Errorf("expected updated model %q, got %q", "qwen-plus", updated.Model)
	}
	if updated.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Errorf("expected updated proxy_url %q, got %q", "socks5://127.0.0.1:1080", updated.ProxyURL)
	}
	if updated.FirstTokenTimeoutMS != 8000 {
		t.Errorf("expected updated first_token_timeout_ms 8000, got %d", updated.FirstTokenTimeoutMS)
	}
	if updated.OverallTimeoutMS != 45000 {
		t.Errorf("expected updated overall_timeout_ms 45000, got %d", updated.OverallTimeoutMS)
	}
	if updated.Enabled != false {
		t.Errorf("expected updated enabled false, got %v", updated.Enabled)
	}

	// 5. Update non-existent Id
	nonExistent := &LLMConfig{
		Id:                  999999,
		Name:                "不存在的LLM配置",
		Endpoint:            "https://example.com/llm",
		Model:               "model-v1",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}
	err = db.UpdateLLMConfigById(ctx, nonExistent)
	if !errors.Is(err, ErrLLMConfigNotFound) {
		t.Fatalf("expected ErrLLMConfigNotFound, got %v", err)
	}

	// 6. Find non-existent Id
	_, err = db.FindLLMConfigById(ctx, 999999)
	if !errors.Is(err, ErrLLMConfigNotFound) {
		t.Fatalf("expected ErrLLMConfigNotFound, got %v", err)
	}
}

func TestLLMConfig_DuplicateNameAllowed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	cfg1 := &LLMConfig{
		Name:                "同名LLM配置",
		Provider:            "",
		Endpoint:            "https://dashscope.aliyuncs.com/api/v1/chat",
		Model:               "model-1",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}
	cfg2 := &LLMConfig{
		Name:                "同名LLM配置",
		Provider:            "",
		Endpoint:            "https://dashscope.aliyuncs.com/api/v2/chat",
		Model:               "model-2",
		FirstTokenTimeoutMS: 6000,
		OverallTimeoutMS:    35000,
		Enabled:             false,
	}

	if err := db.CreateLLMConfig(ctx, cfg1); err != nil {
		t.Fatalf("failed to create first config: %v", err)
	}
	if err := db.CreateLLMConfig(ctx, cfg2); err != nil {
		t.Fatalf("failed to create second config with same name: %v", err)
	}
	if cfg1.Id == cfg2.Id {
		t.Fatalf("expected distinct Ids for duplicate names, got %d and %d", cfg1.Id, cfg2.Id)
	}
	if cfg1.Provider != "" || cfg2.Provider != "" {
		t.Fatalf("expected empty default provider, got %q and %q", cfg1.Provider, cfg2.Provider)
	}
}

func TestLLMConfig_ListAndFilter(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	items := []*LLMConfig{
		{Name: "LLM-Alpha", Provider: "dashscope", Endpoint: "https://alpha.example.com/llm", Model: "m1", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true},
		{Name: "LLM-Beta", Provider: "openai", Endpoint: "https://beta.example.com/llm", Model: "m2", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true},
		{Name: "LLM-Gamma", Provider: "deepseek", Endpoint: "https://gamma.example.com/llm", Model: "m3", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: false},
	}

	for _, item := range items {
		if err := db.CreateLLMConfig(ctx, item); err != nil {
			t.Fatalf("failed to create item: %v", err)
		}
	}

	// 1. List all
	list, total, err := db.ListLLMConfigs(ctx, LLMConfigFilter{})
	if err != nil {
		t.Fatalf("ListLLMConfigs failed: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Errorf("expected total 3 and len 3, got total %d, len %d", total, len(list))
	}

	// 2. Filter by Name
	list, total, err = db.ListLLMConfigs(ctx, LLMConfigFilter{Name: "Alpha"})
	if err != nil {
		t.Fatalf("ListLLMConfigs with name filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "LLM-Alpha" {
		t.Errorf("unexpected name filter result: total %d, items %v", total, list)
	}

	// 2.1 Filter by Provider
	list, total, err = db.ListLLMConfigs(ctx, LLMConfigFilter{Provider: "openai"})
	if err != nil {
		t.Fatalf("ListLLMConfigs with provider filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Provider != "openai" {
		t.Errorf("unexpected provider filter result: total %d, items %v", total, list)
	}

	// 3. Filter by Enabled = true
	enabledTrue := true
	list, total, err = db.ListLLMConfigs(ctx, LLMConfigFilter{Enabled: &enabledTrue})
	if err != nil {
		t.Fatalf("ListLLMConfigs with enabled filter failed: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Errorf("expected 2 enabled items, got total %d, len %d", total, len(list))
	}

	// 4. Filter by Enabled = false
	enabledFalse := false
	list, total, err = db.ListLLMConfigs(ctx, LLMConfigFilter{Enabled: &enabledFalse})
	if err != nil {
		t.Fatalf("ListLLMConfigs with enabled=false filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "LLM-Gamma" {
		t.Errorf("expected 1 disabled item, got total %d, len %d", total, len(list))
	}

	// 5. Pagination
	list, total, err = db.ListLLMConfigs(ctx, LLMConfigFilter{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListLLMConfigs with pagination failed: %v", err)
	}
	if total != 3 || len(list) != 2 {
		t.Errorf("expected total 3 and page size 2, got total %d, len %d", total, len(list))
	}
}

func TestLLMConfig_Validation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name        string
		cfg         *LLMConfig
		expectedErr error
	}{
		{
			name:        "nil config",
			cfg:         nil,
			expectedErr: ErrInvalidLLMConfig,
		},
		{
			name: "empty name",
			cfg: &LLMConfig{
				Name:                "   ",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrEmptyLLMConfigName,
		},
		{
			name: "name exceeds 128 bytes",
			cfg: &LLMConfig{
				Name:                strings.Repeat("a", 129),
				Provider:            "dashscope",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMConfigNameLength,
		},
		{
			name: "provider exceeds 64 bytes",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Provider:            strings.Repeat("p", 65),
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMProviderLength,
		},
		{
			name: "empty endpoint",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "  ",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrEmptyLLMEndpoint,
		},
		{
			name: "endpoint exceeds 1024 bytes",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/" + strings.Repeat("x", 1020),
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMEndpointLength,
		},
		{
			name: "endpoint invalid scheme ws",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "ws://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMEndpointScheme,
		},
		{
			name: "endpoint invalid scheme wss",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMEndpointScheme,
		},
		{
			name: "endpoint invalid url without host",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMEndpointScheme,
		},
		{
			name: "api_key exceeds 1024 bytes",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				APIKey:              strings.Repeat("k", 1025),
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMAPIKeyLength,
		},
		{
			name: "empty model",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "  ",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrEmptyLLMModel,
		},
		{
			name: "model exceeds 255 bytes",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               strings.Repeat("m", 256),
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMModelLength,
		},
		{
			name: "first_token_timeout_ms below 3000",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 2999,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMFirstTokenTimeout,
		},
		{
			name: "first_token_timeout_ms above 30000",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 30001,
				OverallTimeoutMS:    40000,
			},
			expectedErr: ErrInvalidLLMFirstTokenTimeout,
		},
		{
			name: "overall_timeout_ms below 10000",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 3000,
				OverallTimeoutMS:    9999,
			},
			expectedErr: ErrInvalidLLMOverallTimeout,
		},
		{
			name: "overall_timeout_ms above 180000",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    180001,
			},
			expectedErr: ErrInvalidLLMOverallTimeout,
		},
		{
			name: "overall_timeout_ms equals first_token_timeout_ms",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 15000,
				OverallTimeoutMS:    15000,
			},
			expectedErr: ErrLLMOverallTimeoutMustExceedFirstToken,
		},
		{
			name: "overall_timeout_ms less than first_token_timeout_ms",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				FirstTokenTimeoutMS: 20000,
				OverallTimeoutMS:    15000,
			},
			expectedErr: ErrLLMOverallTimeoutMustExceedFirstToken,
		},
		{
			name: "proxy_url exceeds 1024 bytes",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				ProxyURL:            "http://example.com/" + strings.Repeat("p", 1024),
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMProxyURLLength,
		},
		{
			name: "proxy_url invalid scheme ftp",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				ProxyURL:            "ftp://127.0.0.1:21",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: ErrInvalidLLMProxyURLScheme,
		},
		{
			name: "proxy_url valid socks5h",
			cfg: &LLMConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/llm",
				Model:               "model-1",
				ProxyURL:            "socks5h://127.0.0.1:1080",
				FirstTokenTimeoutMS: 5000,
				OverallTimeoutMS:    30000,
			},
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.CreateLLMConfig(ctx, tt.cfg)
			if !errors.Is(err, tt.expectedErr) {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}
		})
	}
}

func TestLLMConfig_NilDB(t *testing.T) {
	var nilDB *Database
	ctx := context.Background()

	if err := nilDB.CreateLLMConfig(ctx, &LLMConfig{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, err := nilDB.FindLLMConfigById(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.UpdateLLMConfigById(ctx, &LLMConfig{Id: 1}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, _, err := nilDB.ListLLMConfigs(ctx, LLMConfigFilter{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.DeleteLLMConfig(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.BatchDeleteLLMConfigs(ctx, []uint64{1}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}

func TestLLMConfig_Delete(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Delete invalid Id
	if err := db.DeleteLLMConfig(ctx, 0); !errors.Is(err, ErrInvalidLLMConfigId) {
		t.Fatalf("expected ErrInvalidLLMConfigId, got %v", err)
	}

	// 2. Delete non-existent Id
	if err := db.DeleteLLMConfig(ctx, 99999); !errors.Is(err, ErrLLMConfigNotFound) {
		t.Fatalf("expected ErrLLMConfigNotFound, got %v", err)
	}

	// 3. Create and delete
	cfg := &LLMConfig{
		Name:                "待删除LLM",
		Endpoint:            "https://example.com/llm",
		Model:               "test-model",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}
	if err := db.CreateLLMConfig(ctx, cfg); err != nil {
		t.Fatalf("CreateLLMConfig failed: %v", err)
	}

	if err := db.DeleteLLMConfig(ctx, cfg.Id); err != nil {
		t.Fatalf("DeleteLLMConfig failed: %v", err)
	}

	// 4. Verify deleted
	_, err := db.FindLLMConfigById(ctx, cfg.Id)
	if !errors.Is(err, ErrLLMConfigNotFound) {
		t.Fatalf("expected ErrLLMConfigNotFound after delete, got %v", err)
	}

	// 5. Batch Delete
	cfgA := &LLMConfig{Name: "LLM-A", Endpoint: "https://example.com/a", Model: "m1", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	cfgB := &LLMConfig{Name: "LLM-B", Endpoint: "https://example.com/b", Model: "m2", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	_ = db.CreateLLMConfig(ctx, cfgA)
	_ = db.CreateLLMConfig(ctx, cfgB)

	// empty Ids should be no-op
	if err := db.BatchDeleteLLMConfigs(ctx, []uint64{}); err != nil {
		t.Fatalf("BatchDelete with empty list failed: %v", err)
	}
	if err := db.BatchDeleteLLMConfigs(ctx, []uint64{0}); err != nil {
		t.Fatalf("BatchDelete with zero Id failed: %v", err)
	}

	if err := db.BatchDeleteLLMConfigs(ctx, []uint64{cfgA.Id, cfgB.Id}); err != nil {
		t.Fatalf("BatchDeleteLLMConfigs failed: %v", err)
	}

	_, err = db.FindLLMConfigById(ctx, cfgA.Id)
	if !errors.Is(err, ErrLLMConfigNotFound) {
		t.Fatalf("expected ErrLLMConfigNotFound for cfgA, got %v", err)
	}
	_, err = db.FindLLMConfigById(ctx, cfgB.Id)
	if !errors.Is(err, ErrLLMConfigNotFound) {
		t.Fatalf("expected ErrLLMConfigNotFound for cfgB, got %v", err)
	}
}
