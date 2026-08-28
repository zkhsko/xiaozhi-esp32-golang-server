package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/session"
)

func TestWebSocketDynamicLoading_EndToEnd(t *testing.T) {
	db := setupTestRouterDB(t)
	ctx := context.Background()

	// 1. 创建两组不同的 ASR, LLM, TTS 和 Agent 配置
	// Agent A: 语音助手
	asrA := &database.ASRConfig{
		Name:             "ASR-A",
		Provider:         "bailian",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "key-a",
		Model:            "qwen-asr-a",
		ConnectTimeoutMS: 5000,
		Enabled:          true,
	}
	if err := db.CreateASRConfig(ctx, asrA); err != nil {
		t.Fatalf("create asrA: %v", err)
	}

	llmA := &database.LLMConfig{
		Name:                "LLM-A",
		Provider:            "bailian",
		Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:              "key-a",
		Model:               "qwen-turbo",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
		Enabled:             true,
	}
	if err := db.CreateLLMConfig(ctx, llmA); err != nil {
		t.Fatalf("create llmA: %v", err)
	}

	ttsA := &database.TTSConfig{
		Name:                "TTS-A",
		Provider:            "bailian",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:              "key-a",
		Model:               "cosyvoice-v1",
		Voices:              `["voice-a"]`,
		ConnectTimeoutMS:    5000,
		FirstAudioTimeoutMS: 5000,
		SentenceTimeoutMS:   10000,
		Enabled:             true,
	}
	if err := db.CreateTTSConfig(ctx, ttsA); err != nil {
		t.Fatalf("create ttsA: %v", err)
	}

	agentA := &database.AgentConfig{
		Name:         "Agent-A",
		ASRConfigId:  asrA.Id,
		LLMConfigId:  llmA.Id,
		TTSConfigId:  ttsA.Id,
		SystemPrompt: "你是客服助手A",
		Voice:        "voice-a",
		Enabled:      true,
	}
	if err := db.CreateAgentConfig(ctx, agentA); err != nil {
		t.Fatalf("create agentA: %v", err)
	}

	// Agent B: 故事助手
	asrB := &database.ASRConfig{
		Name:             "ASR-B",
		Provider:         "bailian",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "key-b",
		Model:            "qwen-asr-b",
		ConnectTimeoutMS: 6000,
		Enabled:          true,
	}
	if err := db.CreateASRConfig(ctx, asrB); err != nil {
		t.Fatalf("create asrB: %v", err)
	}

	llmB := &database.LLMConfig{
		Name:                "LLM-B",
		Provider:            "bailian",
		Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:              "key-b",
		Model:               "qwen-max",
		FirstTokenTimeoutMS: 6000,
		OverallTimeoutMS:    35000,
		Enabled:             true,
	}
	if err := db.CreateLLMConfig(ctx, llmB); err != nil {
		t.Fatalf("create llmB: %v", err)
	}

	ttsB := &database.TTSConfig{
		Name:                "TTS-B",
		Provider:            "bailian",
		Endpoint:            "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:              "key-b",
		Model:               "cosyvoice-v2",
		Voices:              `["voice-b"]`,
		ConnectTimeoutMS:    6000,
		FirstAudioTimeoutMS: 6000,
		SentenceTimeoutMS:   12000,
		Enabled:             true,
	}
	if err := db.CreateTTSConfig(ctx, ttsB); err != nil {
		t.Fatalf("create ttsB: %v", err)
	}

	agentB := &database.AgentConfig{
		Name:         "Agent-B",
		ASRConfigId:  asrB.Id,
		LLMConfigId:  llmB.Id,
		TTSConfigId:  ttsB.Id,
		SystemPrompt: "你是故事机B",
		Voice:        "voice-b",
		Enabled:      false,
	}
	if err := db.CreateAgentConfig(ctx, agentB); err != nil {
		t.Fatalf("create agentB: %v", err)
	}

	// 2. 绑定两种不同的设备类型
	_, err := db.UpsertDeviceType(ctx, "robot-service", agentA.Id)
	if err != nil {
		t.Fatalf("upsert device type robot-service: %v", err)
	}
	_, err = db.UpsertDeviceType(ctx, "story-toy", agentB.Id)
	if err != nil {
		t.Fatalf("upsert device type story-toy: %v", err)
	}

	// 3. 注册两台设备的 Access Token
	tokenA := "access-token-device-service-001"
	err = db.UpsertDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: "SN-ROBOT-001",
		AccessToken:  tokenA,
		DeviceType:   "robot-service",
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert token A: %v", err)
	}

	tokenB := "access-token-device-story-002"
	err = db.UpsertDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: "SN-STORY-002",
		AccessToken:  tokenB,
		DeviceType:   "story-toy",
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("upsert token B: %v", err)
	}

	// 4. 构建 Server Router
	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:            ":8080",
			WebSocketURL:          "ws://localhost:8080/xiaozhi/v1/",
			MaxConcurrentSessions: 10,
		},
	}
	sessionLimiter := session.NewSessionLimiter(10)
	websocketSessionHandler := session.NewHandler(session.HandlerOptions{
		Config:  cfg,
		DB:      db,
		Limiter: sessionLimiter,
	})

	r := NewRouter(Options{
		WebsocketSession: websocketSessionHandler,
	})

	// 5. 验证单表分步点查正确加载
	snapA, err := db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "robot-service")
	if err != nil {
		t.Fatalf("ResolveAgentRuntimeSnapshotByDeviceType A failed: %v", err)
	}
	if snapA.Agent.SystemPrompt != "你是客服助手A" || snapA.Agent.Voice != "voice-a" {
		t.Errorf("unexpected snapA: %+v", snapA)
	}
	if snapA.ASRConfig.Model != "qwen-asr-a" || snapA.LLMConfig.Model != "qwen-turbo" || snapA.TTSConfig.Model != "cosyvoice-v1" {
		t.Errorf("unexpected snapA models: ASR=%s, LLM=%s, TTS=%s", snapA.ASRConfig.Model, snapA.LLMConfig.Model, snapA.TTSConfig.Model)
	}

	snapB, err := db.ResolveAgentRuntimeSnapshotByDeviceType(ctx, "story-toy")
	if err != nil {
		t.Fatalf("ResolveAgentRuntimeSnapshotByDeviceType B failed: %v", err)
	}
	if snapB.Agent.SystemPrompt != "你是故事机B" || snapB.Agent.Voice != "voice-b" {
		t.Errorf("unexpected snapB: %+v", snapB)
	}
	if snapB.ASRConfig.Model != "qwen-asr-b" || snapB.LLMConfig.Model != "qwen-max" || snapB.TTSConfig.Model != "cosyvoice-v2" {
		t.Errorf("unexpected snapB models: ASR=%s, LLM=%s, TTS=%s", snapB.ASRConfig.Model, snapB.LLMConfig.Model, snapB.TTSConfig.Model)
	}

	// 6. Fail Fast 测试：未配置的设备类型发起建连 -> HTTP 400
	tokenUnconfigured := "access-token-unconfigured-type"
	_ = db.UpsertDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: "SN-UNCONFIGURED",
		AccessToken:  tokenUnconfigured,
		DeviceType:   "unknown-type",
		IssuedAt:     time.Now(),
	})

	reqUnconf := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	reqUnconf.Header.Set("Authorization", "Bearer "+tokenUnconfigured)
	reqUnconf.Header.Set("Protocol-Version", "1")
	wUnconf := httptest.NewRecorder()
	r.ServeHTTP(wUnconf, reqUnconf)

	if wUnconf.Code != http.StatusBadRequest {
		t.Errorf("expected HTTP 400 for unconfigured device type, got %d", wUnconf.Code)
	}

	// 7. Fail Fast 测试：ASR 被禁用 -> HTTP 500
	asrB.Enabled = false
	if err := db.UpdateASRConfigById(ctx, asrB); err != nil {
		t.Fatalf("disable asrB: %v", err)
	}

	reqDisabled := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	reqDisabled.Header.Set("Authorization", "Bearer "+tokenB)
	reqDisabled.Header.Set("Protocol-Version", "1")
	wDisabled := httptest.NewRecorder()
	r.ServeHTTP(wDisabled, reqDisabled)

	if wDisabled.Code != http.StatusInternalServerError {
		t.Errorf("expected HTTP 500 for disabled component, got %d", wDisabled.Code)
	}
}
