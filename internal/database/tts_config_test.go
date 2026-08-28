package database

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTTSConfig_CRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Create TTSConfig
	cfg := &TTSConfig{
		Name:                "百炼语音合成",
		Provider:            "bailian",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:              "sk-test-tts-api-key-123456",
		Model:               "cosyvoice-v1",
		Voices:              `["longanlingxi","longxiaochun","longxiaoxia","longwanwan"]`,
		ProxyURL:            "http://127.0.0.1:7890",
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}

	err := db.CreateTTSConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateTTSConfig failed: %v", err)
	}
	if cfg.ID == 0 {
		t.Fatalf("expected non-zero ID after create")
	}

	// 2. Find by ID
	found, err := db.FindTTSConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("FindTTSConfigByID failed: %v", err)
	}
	if found.Name != "百炼语音合成" {
		t.Errorf("expected name %q, got %q", "百炼语音合成", found.Name)
	}
	if found.Provider != "bailian" {
		t.Errorf("expected provider %q, got %q", "bailian", found.Provider)
	}
	if found.Endpoint != "wss://dashscope.aliyuncs.com/api-v1/ws" {
		t.Errorf("expected endpoint %q, got %q", "wss://dashscope.aliyuncs.com/api-v1/ws", found.Endpoint)
	}
	if found.APIKey != "sk-test-tts-api-key-123456" {
		t.Errorf("expected api_key %q, got %q", "sk-test-tts-api-key-123456", found.APIKey)
	}
	if found.Model != "cosyvoice-v1" {
		t.Errorf("expected model %q, got %q", "cosyvoice-v1", found.Model)
	}
	if found.Voices != `["longanlingxi","longxiaochun","longxiaoxia","longwanwan"]` {
		t.Errorf("expected voices %q, got %q", `["longanlingxi","longxiaochun","longxiaoxia","longwanwan"]`, found.Voices)
	}
	if found.ProxyURL != "http://127.0.0.1:7890" {
		t.Errorf("expected proxy_url %q, got %q", "http://127.0.0.1:7890", found.ProxyURL)
	}
	if found.ConnectTimeoutMS != 5000 {
		t.Errorf("expected connect_timeout_ms 5000, got %d", found.ConnectTimeoutMS)
	}
	if found.FirstAudioTimeoutMS != 5000 {
		t.Errorf("expected first_audio_timeout_ms 5000, got %d", found.FirstAudioTimeoutMS)
	}
	if found.SentenceTimeoutMS != 10000 {
		t.Errorf("expected sentence_timeout_ms 10000, got %d", found.SentenceTimeoutMS)
	}
	if !found.Enabled {
		t.Errorf("expected enabled true, got false")
	}

	// 3. Update by ID
	found.Name = "百炼语音合成-更新版"
	found.Provider = "volcengine"
	found.Endpoint = "ws://localhost:9000/tts"
	found.APIKey = "sk-new-tts-key-654321"
	found.Model = "cosyvoice-v2"
	found.Voices = `["longanlingxi","longxiaochun","new_voice_custom"]`
	found.ProxyURL = "socks5://127.0.0.1:1080"
	found.ConnectTimeoutMS = 8000
	found.FirstAudioTimeoutMS = 7000
	found.SentenceTimeoutMS = 15000
	found.Enabled = false

	err = db.UpdateTTSConfigByID(ctx, found)
	if err != nil {
		t.Fatalf("UpdateTTSConfigByID failed: %v", err)
	}

	// 4. Verify Update
	updated, err := db.FindTTSConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("FindTTSConfigByID after update failed: %v", err)
	}
	if updated.Name != "百炼语音合成-更新版" {
		t.Errorf("expected updated name %q, got %q", "百炼语音合成-更新版", updated.Name)
	}
	if updated.Provider != "volcengine" {
		t.Errorf("expected updated provider %q, got %q", "volcengine", updated.Provider)
	}
	if updated.Endpoint != "ws://localhost:9000/tts" {
		t.Errorf("expected updated endpoint %q, got %q", "ws://localhost:9000/tts", updated.Endpoint)
	}
	if updated.APIKey != "sk-new-tts-key-654321" {
		t.Errorf("expected updated api_key %q, got %q", "sk-new-tts-key-654321", updated.APIKey)
	}
	if updated.Model != "cosyvoice-v2" {
		t.Errorf("expected updated model %q, got %q", "cosyvoice-v2", updated.Model)
	}
	if updated.Voices != `["longanlingxi","longxiaochun","new_voice_custom"]` {
		t.Errorf("expected updated voices %q, got %q", `["longanlingxi","longxiaochun","new_voice_custom"]`, updated.Voices)
	}
	if updated.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Errorf("expected updated proxy_url %q, got %q", "socks5://127.0.0.1:1080", updated.ProxyURL)
	}
	if updated.ConnectTimeoutMS != 8000 {
		t.Errorf("expected updated connect_timeout_ms 8000, got %d", updated.ConnectTimeoutMS)
	}
	if updated.FirstAudioTimeoutMS != 7000 {
		t.Errorf("expected updated first_audio_timeout_ms 7000, got %d", updated.FirstAudioTimeoutMS)
	}
	if updated.SentenceTimeoutMS != 15000 {
		t.Errorf("expected updated sentence_timeout_ms 15000, got %d", updated.SentenceTimeoutMS)
	}
	if updated.Enabled != false {
		t.Errorf("expected updated enabled false, got %v", updated.Enabled)
	}

	// 5. Update non-existent ID
	nonExistent := &TTSConfig{
		ID:                  999999,
		Name:                "不存在的TTS配置",
		Endpoint:            "wss://example.com/tts",
		Model:               "model-v1",
		Voices:              "[]",
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}
	err = db.UpdateTTSConfigByID(ctx, nonExistent)
	if !errors.Is(err, ErrTTSConfigNotFound) {
		t.Fatalf("expected ErrTTSConfigNotFound, got %v", err)
	}

	// 6. Find non-existent ID
	_, err = db.FindTTSConfigByID(ctx, 999999)
	if !errors.Is(err, ErrTTSConfigNotFound) {
		t.Fatalf("expected ErrTTSConfigNotFound, got %v", err)
	}

	// 7. Delete TTSConfig
	err = db.DeleteTTSConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("DeleteTTSConfig failed: %v", err)
	}

	// 8. Verify Deleted
	_, err = db.FindTTSConfigByID(ctx, cfg.ID)
	if !errors.Is(err, ErrTTSConfigNotFound) {
		t.Fatalf("expected ErrTTSConfigNotFound after delete, got %v", err)
	}

	// 9. Delete non-existent ID
	err = db.DeleteTTSConfig(ctx, 999999)
	if !errors.Is(err, ErrTTSConfigNotFound) {
		t.Fatalf("expected ErrTTSConfigNotFound for non-existent delete, got %v", err)
	}
}

func TestTTSConfig_BatchDelete(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	cfg1 := &TTSConfig{
		Name:                "TTS-Batch-1",
		Endpoint:            "wss://example.com/tts1",
		Model:               "m1",
		Voices:              "[]",
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}
	cfg2 := &TTSConfig{
		Name:                "TTS-Batch-2",
		Endpoint:            "wss://example.com/tts2",
		Model:               "m2",
		Voices:              "[]",
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}

	if err := db.CreateTTSConfig(ctx, cfg1); err != nil {
		t.Fatalf("failed to create cfg1: %v", err)
	}
	if err := db.CreateTTSConfig(ctx, cfg2); err != nil {
		t.Fatalf("failed to create cfg2: %v", err)
	}

	list, total, _ := db.ListTTSConfigs(ctx, TTSConfigFilter{})
	if total != 2 || len(list) != 2 {
		t.Fatalf("expected 2 configs before batch delete, got total %d", total)
	}

	err := db.BatchDeleteTTSConfigs(ctx, []uint64{cfg1.ID, cfg2.ID})
	if err != nil {
		t.Fatalf("BatchDeleteTTSConfigs failed: %v", err)
	}

	listAfter, totalAfter, _ := db.ListTTSConfigs(ctx, TTSConfigFilter{})
	if totalAfter != 0 || len(listAfter) != 0 {
		t.Fatalf("expected 0 configs after batch delete, got total %d", totalAfter)
	}
}


func TestTTSConfig_LargeVoices(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 构造 50KB 大容量合法 JSON 数组
	voiceList := make([]string, 2500)
	for i := 0; i < 2500; i++ {
		voiceList[i] = "custom_voice_item_0123456789"
	}
	largeVoicesBytes, _ := json.Marshal(voiceList)
	largeVoices := string(largeVoicesBytes)

	cfg := &TTSConfig{
		Name:                "大容量音色列表配置",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:               "cosyvoice-v1",
		Voices:              largeVoices,
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}

	if err := db.CreateTTSConfig(ctx, cfg); err != nil {
		t.Fatalf("CreateTTSConfig with large voices failed: %v", err)
	}

	found, err := db.FindTTSConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("FindTTSConfigByID failed: %v", err)
	}
	if found.Voices != largeVoices {
		t.Fatalf("expected voices length %d, got %d", len(largeVoices), len(found.Voices))
	}
}

func TestTTSConfig_EmptyVoicesDefault(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	cfg := &TTSConfig{
		Name:                "空音色列表配置",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:               "cosyvoice-v1",
		Voices:              "", // 留空
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}

	if err := db.CreateTTSConfig(ctx, cfg); err != nil {
		t.Fatalf("CreateTTSConfig with empty voices failed: %v", err)
	}

	found, err := db.FindTTSConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("FindTTSConfigByID failed: %v", err)
	}
	if found.Voices != "[]" {
		t.Errorf("expected default voices '[]', got %q", found.Voices)
	}
}

func TestTTSConfig_DuplicateNameAllowed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	cfg1 := &TTSConfig{
		Name:                "同名TTS配置",
		Provider:            "",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		Model:               "model-1",
		Voices:              `["voice1"]`,
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}
	cfg2 := &TTSConfig{
		Name:                "同名TTS配置",
		Provider:            "",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v2/ws",
		Model:               "model-2",
		Voices:              `["voice2"]`,
		ConnectTimeoutMS:    6000,
		FirstAudioTimeoutMS: 6000,
		SentenceTimeoutMS:   12000,
		Enabled:             false,
	}

	if err := db.CreateTTSConfig(ctx, cfg1); err != nil {
		t.Fatalf("failed to create first config: %v", err)
	}
	if err := db.CreateTTSConfig(ctx, cfg2); err != nil {
		t.Fatalf("failed to create second config with same name: %v", err)
	}
	if cfg1.ID == cfg2.ID {
		t.Fatalf("expected distinct IDs for duplicate names, got %d and %d", cfg1.ID, cfg2.ID)
	}
	if cfg1.Provider != "" || cfg2.Provider != "" {
		t.Fatalf("expected empty default provider, got %q and %q", cfg1.Provider, cfg2.Provider)
	}
}

func TestTTSConfig_ListAndFilter(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	items := []*TTSConfig{
		{Name: "TTS-Alpha", Provider: "bailian", Endpoint: "wss://alpha.example.com/tts", Model: "m1", Voices: "[]", ConnectTimeoutMS: 5000, FirstAudioTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: true},
		{Name: "TTS-Beta", Provider: "volcengine", Endpoint: "wss://beta.example.com/tts", Model: "m2", Voices: "[]", ConnectTimeoutMS: 5000, FirstAudioTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: true},
		{Name: "TTS-Gamma", Provider: "openai", Endpoint: "wss://gamma.example.com/tts", Model: "m3", Voices: "[]", ConnectTimeoutMS: 5000, FirstAudioTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: false},
	}

	for _, item := range items {
		if err := db.CreateTTSConfig(ctx, item); err != nil {
			t.Fatalf("failed to create item: %v", err)
		}
	}

	// 1. List all
	list, total, err := db.ListTTSConfigs(ctx, TTSConfigFilter{})
	if err != nil {
		t.Fatalf("ListTTSConfigs failed: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Errorf("expected total 3 and len 3, got total %d, len %d", total, len(list))
	}

	// 2. Filter by Name
	list, total, err = db.ListTTSConfigs(ctx, TTSConfigFilter{Name: "Alpha"})
	if err != nil {
		t.Fatalf("ListTTSConfigs with name filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "TTS-Alpha" {
		t.Errorf("unexpected name filter result: total %d, items %v", total, list)
	}

	// 2.1 Filter by Provider
	list, total, err = db.ListTTSConfigs(ctx, TTSConfigFilter{Provider: "volcengine"})
	if err != nil {
		t.Fatalf("ListTTSConfigs with provider filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Provider != "volcengine" {
		t.Errorf("unexpected provider filter result: total %d, items %v", total, list)
	}

	// 3. Filter by Enabled = true
	enabledTrue := true
	list, total, err = db.ListTTSConfigs(ctx, TTSConfigFilter{Enabled: &enabledTrue})
	if err != nil {
		t.Fatalf("ListTTSConfigs with enabled filter failed: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Errorf("expected 2 enabled items, got total %d, len %d", total, len(list))
	}

	// 4. Filter by Enabled = false
	enabledFalse := false
	list, total, err = db.ListTTSConfigs(ctx, TTSConfigFilter{Enabled: &enabledFalse})
	if err != nil {
		t.Fatalf("ListTTSConfigs with enabled=false filter failed: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "TTS-Gamma" {
		t.Errorf("expected 1 disabled item, got total %d, len %d", total, len(list))
	}

	// 5. Pagination
	list, total, err = db.ListTTSConfigs(ctx, TTSConfigFilter{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListTTSConfigs with pagination failed: %v", err)
	}
	if total != 3 || len(list) != 2 {
		t.Errorf("expected total 3 and page size 2, got total %d, len %d", total, len(list))
	}
}

func TestTTSConfig_Validation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name        string
		cfg         *TTSConfig
		expectedErr error
	}{
		{
			name:        "nil config",
			cfg:         nil,
			expectedErr: ErrInvalidTTSConfig,
		},
		{
			name: "empty name",
			cfg: &TTSConfig{
				Name:                "   ",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrEmptyTTSConfigName,
		},
		{
			name: "name exceeds 128 bytes",
			cfg: &TTSConfig{
				Name:                strings.Repeat("a", 129),
				Provider:            "bailian",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSConfigNameLength,
		},
		{
			name: "provider exceeds 64 bytes",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Provider:            strings.Repeat("p", 65),
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSProviderLength,
		},
		{
			name: "empty endpoint",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "  ",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrEmptyTTSEndpoint,
		},
		{
			name: "endpoint exceeds 1024 bytes",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/" + strings.Repeat("x", 1020),
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSEndpointLength,
		},
		{
			name: "endpoint invalid scheme http",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "http://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSEndpointScheme,
		},
		{
			name: "endpoint invalid scheme https",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "https://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSEndpointScheme,
		},
		{
			name: "endpoint invalid url without host",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSEndpointScheme,
		},
		{
			name: "api_key exceeds 1024 bytes",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				APIKey:              strings.Repeat("k", 1025),
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSAPIKeyLength,
		},
		{
			name: "empty model",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "  ",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrEmptyTTSModel,
		},
		{
			name: "model exceeds 255 bytes",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               strings.Repeat("m", 256),
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSModelLength,
		},
		{
			name: "connect_timeout_ms below 3000",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    2999,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSConnectTimeout,
		},
		{
			name: "connect_timeout_ms above 30000",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    30001,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSConnectTimeout,
		},
		{
			name: "first_audio_timeout_ms below 3000",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 2999,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSFirstAudioTimeout,
		},
		{
			name: "first_audio_timeout_ms above 30000",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 30001,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSFirstAudioTimeout,
		},
		{
			name: "sentence_timeout_ms below 5000",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   4999,
			},
			expectedErr: ErrInvalidTTSSentenceTimeout,
		},
		{
			name: "sentence_timeout_ms above 60000",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   60001,
			},
			expectedErr: ErrInvalidTTSSentenceTimeout,
		},
		{
			name: "voices invalid plain text non-json",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				Voices:              "longanlingxi,longxiaochun",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSVoicesJSON,
		},
		{
			name: "voices invalid malformed json",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				Voices:              `{"key": invalid}`,
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSVoicesJSON,
		},
		{
			name: "voices exceeds 1MB",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				Voices:              strings.Repeat("a", 1024*1024+1),
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSVoicesLength,
		},
		{
			name: "proxy_url exceeds 1024 bytes",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ProxyURL:            "http://example.com/" + strings.Repeat("p", 1024),
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSProxyURLLength,
		},
		{
			name: "proxy_url invalid scheme ftp",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ProxyURL:            "ftp://127.0.0.1:21",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: ErrInvalidTTSProxyURLScheme,
		},
		{
			name: "proxy_url valid socks5h",
			cfg: &TTSConfig{
				Name:                "valid-name",
				Endpoint:            "wss://example.com/tts",
				Model:               "model-1",
				ProxyURL:            "socks5h://127.0.0.1:1080",
				ConnectTimeoutMS:    5000,
				FirstAudioTimeoutMS: 5000,
				SentenceTimeoutMS:   10000,
			},
			expectedErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.CreateTTSConfig(ctx, tt.cfg)
			if !errors.Is(err, tt.expectedErr) {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}
		})
	}
}

func TestTTSConfig_NilDB(t *testing.T) {
	var nilDB *Database
	ctx := context.Background()

	if err := nilDB.CreateTTSConfig(ctx, &TTSConfig{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, err := nilDB.FindTTSConfigByID(ctx, 1); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if err := nilDB.UpdateTTSConfigByID(ctx, &TTSConfig{ID: 1}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
	if _, _, err := nilDB.ListTTSConfigs(ctx, TTSConfigFilter{}); !errors.Is(err, ErrDatabaseInstanceRequired) {
		t.Fatalf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}
