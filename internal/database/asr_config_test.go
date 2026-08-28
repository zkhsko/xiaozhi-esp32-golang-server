package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestASRConfig_CRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Create ASRConfig
	cfg := &ASRConfig{
		Name:             "百炼语音识别",
		Provider:         "bailian",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "sk-test-asr-api-key-123456",
		Model:            "qwen-audio-3.0-asr-flash-streaming",
		Hotwords:         `["小智","智能音箱","ESP32"]`,
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}

	err := db.CreateASRConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateASRConfig failed: %v", err)
	}
	if cfg.ID == 0 {
		t.Fatalf("expected non-zero ID after create")
	}

	// 2. Find by ID
	found, err := db.FindASRConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("FindASRConfigByID failed: %v", err)
	}
	if found.Name != "百炼语音识别" {
		t.Errorf("expected name %q, got %q", "百炼语音识别", found.Name)
	}
	if found.Provider != "bailian" {
		t.Errorf("expected provider %q, got %q", "bailian", found.Provider)
	}
	if found.Endpoint != "wss://dashscope.aliyuncs.com/api-v1/ws" {
		t.Errorf("expected endpoint %q, got %q", "wss://dashscope.aliyuncs.com/api-v1/ws", found.Endpoint)
	}
	if found.APIKey != "sk-test-asr-api-key-123456" {
		t.Errorf("expected api_key %q, got %q", "sk-test-asr-api-key-123456", found.APIKey)
	}
	if found.Model != "qwen-audio-3.0-asr-flash-streaming" {
		t.Errorf("expected model %q, got %q", "qwen-audio-3.0-asr-flash-streaming", found.Model)
	}
	if found.Hotwords != `["小智","智能音箱","ESP32"]` {
		t.Errorf("expected hotwords %q, got %q", `["小智","智能音箱","ESP32"]`, found.Hotwords)
	}
	if found.ConnectTimeoutMS != 5000 {
		t.Errorf("expected connect_timeout_ms 5000, got %d", found.ConnectTimeoutMS)
	}
	if !found.Enabled {
		t.Errorf("expected enabled true, got false")
	}

	// 3. Update by ID
	found.Name = "百炼语音识别-更新版"
	found.Provider = "volcengine"
	found.Endpoint = "ws://localhost:9000/asr"
	found.APIKey = "sk-new-key-654321"
	found.Model = "qwen-audio-asr-v2"
	found.Hotwords = `["小智二代","新热词","ESP32-S3"]`
	found.ConnectTimeoutMS = 10000
	found.Enabled = false

	err = db.UpdateASRConfigByID(ctx, found)
	if err != nil {
		t.Fatalf("UpdateASRConfigByID failed: %v", err)
	}

	// 4. Verify Update
	updated, err := db.FindASRConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("FindASRConfigByID after update failed: %v", err)
	}
	if updated.Name != "百炼语音识别-更新版" {
		t.Errorf("expected updated name %q, got %q", "百炼语音识别-更新版", updated.Name)
	}
	if updated.Provider != "volcengine" {
		t.Errorf("expected updated provider %q, got %q", "volcengine", updated.Provider)
	}
	if updated.Endpoint != "ws://localhost:9000/asr" {
		t.Errorf("expected updated endpoint %q, got %q", "ws://localhost:9000/asr", updated.Endpoint)
	}
	if updated.APIKey != "sk-new-key-654321" {
		t.Errorf("expected updated api_key %q, got %q", "sk-new-key-654321", updated.APIKey)
	}
	if updated.Model != "qwen-audio-asr-v2" {
		t.Errorf("expected updated model %q, got %q", "qwen-audio-asr-v2", updated.Model)
	}
	if updated.Hotwords != `["小智二代","新热词","ESP32-S3"]` {
		t.Errorf("expected updated hotwords %q, got %q", `["小智二代","新热词","ESP32-S3"]`, updated.Hotwords)
	}
	if updated.ConnectTimeoutMS != 10000 {
		t.Errorf("expected updated connect_timeout_ms 10000, got %d", updated.ConnectTimeoutMS)
	}
	if updated.Enabled != false {
		t.Errorf("expected updated enabled false, got %v", updated.Enabled)
	}

	// 5. Update non-existent ID
	nonExistent := &ASRConfig{
		ID:               999999,
		Name:             "不存在的配置",
		Endpoint:         "wss://example.com/asr",
		Model:            "model-v1",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	err = db.UpdateASRConfigByID(ctx, nonExistent)
	if !errors.Is(err, ErrASRConfigNotFound) {
		t.Fatalf("expected ErrASRConfigNotFound, got %v", err)
	}

	// 6. Find non-existent ID
	_, err = db.FindASRConfigByID(ctx, 999999)
	if !errors.Is(err, ErrASRConfigNotFound) {
		t.Fatalf("expected ErrASRConfigNotFound, got %v", err)
	}
}

func TestASRConfig_LargeHotwords(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 构造 50KB+ 大文本 JSON 热词数组
	elements := make([]string, 2500)
	for i := 0; i < 2500; i++ {
		elements[i] = `"自定义专业热词词汇项"`
	}
	largeHotwords := "[" + strings.Join(elements, ",") + "]" // 约 75KB+ JSON

	cfg := &ASRConfig{
		Name:             "大容量热词配置",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "qwen-audio-3.0-asr-flash-streaming",
		Hotwords:         largeHotwords,
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}

	if err := db.CreateASRConfig(ctx, cfg); err != nil {
		t.Fatalf("CreateASRConfig with large hotwords failed: %v", err)
	}

	found, err := db.FindASRConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("FindASRConfigByID failed: %v", err)
	}
	if found.Hotwords != largeHotwords {
		t.Fatalf("expected hotwords length %d, got %d", len(largeHotwords), len(found.Hotwords))
	}
}

func TestASRConfig_DuplicateNameAllowed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	cfg1 := &ASRConfig{
		Name:             "同名配置",
		Provider:         "",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "model-1",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	cfg2 := &ASRConfig{
		Name:             "同名配置",
		Provider:         "",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v2/ws",
		Model:            "model-2",
		ConnectTimeoutMS: 6000,
		Enabled:          false,
	}

	if err := db.CreateASRConfig(ctx, cfg1); err != nil {
		t.Fatalf("failed to create first config: %v", err)
	}
	if err := db.CreateASRConfig(ctx, cfg2); err != nil {
		t.Fatalf("failed to create second config with same name: %v", err)
	}
	if cfg1.ID == cfg2.ID {
		t.Fatalf("expected distinct IDs for duplicate names, got %d and %d", cfg1.ID, cfg2.ID)
	}
	if cfg1.Provider != "" || cfg2.Provider != "" {
		t.Fatalf("expected empty default provider, got %q and %q", cfg1.Provider, cfg2.Provider)
	}
}

func TestASRConfig_ListAndFilter(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	items := []*ASRConfig{
		{Name: "ASR-Alpha", Provider: "bailian", Endpoint: "wss://alpha.example.com/asr", Model: "m1", ConnectTimeoutMS: 5000, Enabled: true},
		{Name: "ASR-Beta", Provider: "volcengine", Endpoint: "wss://beta.example.com/asr", Model: "m2", ConnectTimeoutMS: 5000, Enabled: true},
		{Name: "ASR-Gamma", Provider: "openai", Endpoint: "wss://gamma.example.com/asr", Model: "m3", ConnectTimeoutMS: 5000, Enabled: false},
	}

	for _, item := range items {
		if err := db.CreateASRConfig(ctx, item); err != nil {
			t.Fatalf("failed to create item: %v", err)
		}
	}

	// 1. List all
	list, total, err := db.ListASRConfigs(ctx, ASRConfigFilter{})
	if err != nil {
		t.Fatalf("ListASRConfigs failed: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Errorf("expected total 3 and len 3, got total %d, len %d", total, len(list))
	}

	// 2. Filter by Name
	list, total, err = db.ListASRConfigs(ctx, ASRConfigFilter{Name: "Alpha"})
	if err != nil {
		t.Fatalf("ListASRConfigs with name filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "ASR-Alpha" {
		t.Errorf("unexpected name filter result: total %d, items %v", total, list)
	}

	// 2.1 Filter by Provider
	list, total, err = db.ListASRConfigs(ctx, ASRConfigFilter{Provider: "volcengine"})
	if err != nil {
		t.Fatalf("ListASRConfigs with provider filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Provider != "volcengine" {
		t.Errorf("unexpected provider filter result: total %d, items %v", total, list)
	}

	// 3. Filter by Enabled = true
	enabledTrue := true
	list, total, err = db.ListASRConfigs(ctx, ASRConfigFilter{Enabled: &enabledTrue})
	if err != nil {
		t.Fatalf("ListASRConfigs with enabled filter failed: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Errorf("expected 2 enabled items, got total %d, len %d", total, len(list))
	}

	// 4. Filter by Enabled = false
	enabledFalse := false
	list, total, err = db.ListASRConfigs(ctx, ASRConfigFilter{Enabled: &enabledFalse})
	if err != nil {
		t.Fatalf("ListASRConfigs with enabled=false filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "ASR-Gamma" {
		t.Errorf("expected 1 disabled item, got total %d, len %d", total, len(list))
	}

	// 5. Pagination
	list, total, err = db.ListASRConfigs(ctx, ASRConfigFilter{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListASRConfigs with pagination failed: %v", err)
	}
	if total != 3 || len(list) != 2 {
		t.Errorf("expected total 3 and page size 2, got total %d, len %d", total, len(list))
	}
}

func TestASRConfig_Validation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name        string
		cfg         *ASRConfig
		expectedErr error
	}{
		{
			name:        "nil config",
			cfg:         nil,
			expectedErr: ErrInvalidASRConfig,
		},
		{
			name: "empty name",
			cfg: &ASRConfig{
				Name:             "   ",
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrEmptyASRConfigName,
		},
		{
			name: "name exceeds 128 bytes",
			cfg: &ASRConfig{
				Name:             strings.Repeat("a", 129),
				Provider:         "bailian",
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASRConfigNameLength,
		},
		{
			name: "provider exceeds 64 bytes",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Provider:         strings.Repeat("p", 65),
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASRProviderLength,
		},
		{
			name: "empty endpoint",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "  ",
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrEmptyASREndpoint,
		},
		{
			name: "endpoint exceeds 1024 bytes",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/" + strings.Repeat("x", 1020),
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASREndpointLength,
		},
		{
			name: "endpoint invalid scheme http",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "http://example.com/asr",
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASREndpointScheme,
		},
		{
			name: "endpoint invalid scheme https",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "https://example.com/asr",
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASREndpointScheme,
		},
		{
			name: "endpoint invalid url without host",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://",
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASREndpointScheme,
		},
		{
			name: "api_key exceeds 1024 bytes",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				APIKey:           strings.Repeat("k", 1025),
				Model:            "model-1",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASRAPIKeyLength,
		},
		{
			name: "empty model",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				Model:            "  ",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrEmptyASRModel,
		},
		{
			name: "model exceeds 255 bytes",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				Model:            strings.Repeat("m", 256),
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASRModelLength,
		},
		{
			name: "connect_timeout_ms below 3000",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				ConnectTimeoutMS: 2999,
			},
			expectedErr: ErrInvalidASRConnectTimeout,
		},
		{
			name: "connect_timeout_ms above 30000",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				ConnectTimeoutMS: 30001,
			},
			expectedErr: ErrInvalidASRConnectTimeout,
		},
		{
			name: "invalid hotwords json format",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				Hotwords:         "not-valid-json-string",
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASRHotwordsJSON,
		},
		{
			name: "valid hotwords json object format",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				Hotwords:         `{"words":["小智","智能音箱"],"weight":10}`,
				ConnectTimeoutMS: 5000,
			},
			expectedErr: nil,
		},
		{
			name: "hotwords exceeds 1MB",
			cfg: &ASRConfig{
				Name:             "valid-name",
				Endpoint:         "wss://example.com/asr",
				Model:            "model-1",
				Hotwords:         strings.Repeat("a", 1024*1024+1),
				ConnectTimeoutMS: 5000,
			},
			expectedErr: ErrInvalidASRHotwordsLength,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.CreateASRConfig(ctx, tt.cfg)
			if !errors.Is(err, tt.expectedErr) {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}
		})
	}
}

func TestASRConfig_NilDB(t *testing.T) {
	var nilDB *Database
	ctx := context.Background()

	if err := nilDB.CreateASRConfig(ctx, &ASRConfig{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, err := nilDB.FindASRConfigByID(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.UpdateASRConfigByID(ctx, &ASRConfig{ID: 1}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, _, err := nilDB.ListASRConfigs(ctx, ASRConfigFilter{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.DeleteASRConfig(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.BatchDeleteASRConfigs(ctx, []uint64{1}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}

func TestASRConfig_DeleteAndBatchDelete(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	cfg1 := &ASRConfig{
		Name:             "待删除配置1",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "model-del-1",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	cfg2 := &ASRConfig{
		Name:             "待删除配置2",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "model-del-2",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	cfg3 := &ASRConfig{
		Name:             "待删除配置3",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:            "model-del-3",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}

	_ = db.CreateASRConfig(ctx, cfg1)
	_ = db.CreateASRConfig(ctx, cfg2)
	_ = db.CreateASRConfig(ctx, cfg3)

	// 1. Delete single config
	if err := db.DeleteASRConfig(ctx, cfg1.ID); err != nil {
		t.Fatalf("DeleteASRConfig failed: %v", err)
	}

	// Verify deleted
	_, err := db.FindASRConfigByID(ctx, cfg1.ID)
	if !errors.Is(err, ErrASRConfigNotFound) {
		t.Fatalf("expected ErrASRConfigNotFound after delete, got %v", err)
	}

	// Delete non-existent
	if err := db.DeleteASRConfig(ctx, 99999); !errors.Is(err, ErrASRConfigNotFound) {
		t.Fatalf("expected ErrASRConfigNotFound for non-existent ID, got %v", err)
	}

	// 2. Batch delete
	if err := db.BatchDeleteASRConfigs(ctx, []uint64{cfg2.ID, cfg3.ID}); err != nil {
		t.Fatalf("BatchDeleteASRConfigs failed: %v", err)
	}

	_, total, err := db.ListASRConfigs(ctx, ASRConfigFilter{})
	if err != nil {
		t.Fatalf("ListASRConfigs failed: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected total 0 after batch delete, got %d", total)
	}
}

