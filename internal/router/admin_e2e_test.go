package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestAdminCredentialEndToEnd(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	r := NewRouter(Options{
		Admin: adminHandler,
	})

	// 1. 批量生成 3 条凭证
	genBody := []byte(`{"count": 3, "device_type": "esp32-s3-box"}`)
	reqGen := httptest.NewRequest(http.MethodPost, "/admin-api/device-hmac-credential/generate", bytes.NewReader(genBody))
	reqGen.Header.Set("Content-Type", "application/json")
	wGen := httptest.NewRecorder()
	r.ServeHTTP(wGen, reqGen)

	if wGen.Code != http.StatusOK {
		t.Fatalf("generate failed, code=%d, body=%s", wGen.Code, wGen.Body.String())
	}

	var genResp GenerateCredentialResponse
	if err := json.Unmarshal(wGen.Body.Bytes(), &genResp); err != nil {
		t.Fatalf("unmarshal genResp failed: %v", err)
	}
	if len(genResp.Items) != 3 {
		t.Fatalf("expected 3 items generated, got %d", len(genResp.Items))
	}

	firstSN := genResp.Items[0].SerialNumber
	firstId := genResp.Items[0].Id

	// 2. 分页查询列表，校验第一条
	reqList := httptest.NewRequest(http.MethodGet, "/admin-api/device-hmac-credential?page=1&page_size=10", nil)
	wList := httptest.NewRecorder()
	r.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list failed, code=%d, body=%s", wList.Code, wList.Body.String())
	}

	var listResp struct {
		Success bool                     `json:"success"`
		Data    DeviceCredentialListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp failed: %v", err)
	}
	if listResp.Data.Total != 3 {
		t.Fatalf("expected total 3, got %d", listResp.Data.Total)
	}

	// 3. 过滤查询特定 SN
	reqFilter := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/admin-api/device-hmac-credential?serial_number=%s", firstSN), nil)
	wFilter := httptest.NewRecorder()
	r.ServeHTTP(wFilter, reqFilter)

	if wFilter.Code != http.StatusOK {
		t.Fatalf("filter failed, code=%d", wFilter.Code)
	}
	var filterResp struct {
		Success bool                     `json:"success"`
		Data    DeviceCredentialListData `json:"data"`
	}
	_ = json.Unmarshal(wFilter.Body.Bytes(), &filterResp)
	if filterResp.Data.Total != 1 || filterResp.Data.Items[0].SerialNumber != firstSN {
		t.Fatalf("filter result mismatch: %+v", filterResp.Data)
	}

	// 4. 更新第一条凭证的状态为 blocked (via POST /admin-api/device-hmac-credential/update)
	updateBody, _ := json.Marshal(UpdateCredentialRequest{
		Id:               firstId,
		CredentialStatus: "blocked",
		DeviceType:       "esp32-custom",
	})
	reqUpdate := httptest.NewRequest(http.MethodPost, "/admin-api/device-hmac-credential/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	r.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update failed, code=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	// 5. 校验更新结果
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), firstSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != "blocked" || cred.DeviceType != "esp32-custom" {
		t.Fatalf("cred update mismatch: %+v", cred)
	}

	// 6. 单条删除 (via POST /admin-api/device-hmac-credential/delete)
	deleteBody, _ := json.Marshal(DeleteCredentialRequest{
		Id: firstId,
	})
	reqDel := httptest.NewRequest(http.MethodPost, "/admin-api/device-hmac-credential/delete", bytes.NewReader(deleteBody))
	reqDel.Header.Set("Content-Type", "application/json")
	wDel := httptest.NewRecorder()
	r.ServeHTTP(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("delete failed, code=%d, body=%s", wDel.Code, wDel.Body.String())
	}

	// 7. 访问 /admin/ 及 /admin/device-credentials 确保静态 SPA 正常加载
	reqSPA := httptest.NewRequest(http.MethodGet, "/admin/device-credentials", nil)
	wSPA := httptest.NewRecorder()
	r.ServeHTTP(wSPA, reqSPA)

	if wSPA.Code != http.StatusOK {
		t.Fatalf("SPA route failed, code=%d", wSPA.Code)
	}
	if !bytes.Contains(wSPA.Body.Bytes(), []byte("<!DOCTYPE html>")) && !bytes.Contains(wSPA.Body.Bytes(), []byte("<html")) {
		t.Fatalf("expected index.html response for SPA route")
	}

	// 8. 测试 device_activation 初始数据及列表查询
	initialAct, err := db.ActivateDeviceBySerialNumber(context.Background(), "e2e-sn-act-001", "11:22:33:44:55:66", "e2e-client-001")
	if err != nil {
		t.Fatalf("activate device failed: %v", err)
	}
	createdActId := initialAct.Id

	// 9. 查询设备激活列表
	reqActList := httptest.NewRequest(http.MethodGet, "/admin-api/device-activation?serial_number=e2e-sn", nil)
	wActList := httptest.NewRecorder()
	r.ServeHTTP(wActList, reqActList)

	if wActList.Code != http.StatusOK {
		t.Fatalf("list activation failed: %s", wActList.Body.String())
	}
	var actListResp struct {
		Success bool                     `json:"success"`
		Data    DeviceActivationListData `json:"data"`
	}
	_ = json.Unmarshal(wActList.Body.Bytes(), &actListResp)
	if actListResp.Data.Total != 1 {
		t.Fatalf("expected 1 activation item, got %d", actListResp.Data.Total)
	}

	// 10. 更新激活状态为 frozen
	updateActBody, _ := json.Marshal(UpdateActivationRequest{
		Id:               createdActId,
		ActivationStatus: "frozen",
	})
	reqUpdateAct := httptest.NewRequest(http.MethodPost, "/admin-api/device-activation/update", bytes.NewReader(updateActBody))
	reqUpdateAct.Header.Set("Content-Type", "application/json")
	wUpdateAct := httptest.NewRecorder()
	r.ServeHTTP(wUpdateAct, reqUpdateAct)

	if wUpdateAct.Code != http.StatusOK {
		t.Fatalf("update activation failed: %s", wUpdateAct.Body.String())
	}

	// 11. 删除激活记录
	delActBody, _ := json.Marshal(DeleteActivationRequest{
		Id: createdActId,
	})
	reqDelAct := httptest.NewRequest(http.MethodPost, "/admin-api/device-activation/delete", bytes.NewReader(delActBody))
	reqDelAct.Header.Set("Content-Type", "application/json")
	wDelAct := httptest.NewRecorder()
	r.ServeHTTP(wDelAct, reqDelAct)

	if wDelAct.Code != http.StatusOK {
		t.Fatalf("delete activation failed: %s", wDelAct.Body.String())
	}

	// 12. 访问 /admin/device-activations 确保静态 SPA 路由可 fallback 到 index.html
	reqSPA2 := httptest.NewRequest(http.MethodGet, "/admin/device-activations", nil)
	wSPA2 := httptest.NewRecorder()
	r.ServeHTTP(wSPA2, reqSPA2)

	if wSPA2.Code != http.StatusOK {
		t.Fatalf("SPA route for activations failed, code=%d", wSPA2.Code)
	}

	// 13. ASR 配置 E2E 创建
	createASRBody, _ := json.Marshal(SaveASRConfigRequest{
		Name:             "E2E 百炼 ASR",
		Provider:         "dashscope",
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "sk-e2e-asr-key",
		Model:            "qwen-audio-3.0-asr-flash-streaming",
		Hotwords:         `["小智", "测试热词"]`,
		ProxyURL:         "http://127.0.0.1:7890",
		ConnectTimeoutMS: 5000,
	})
	reqASRCreate := httptest.NewRequest(http.MethodPost, "/admin-api/asr-config/save", bytes.NewReader(createASRBody))
	reqASRCreate.Header.Set("Content-Type", "application/json")
	wASRCreate := httptest.NewRecorder()
	r.ServeHTTP(wASRCreate, reqASRCreate)

	if wASRCreate.Code != http.StatusOK {
		t.Fatalf("create asr config failed: %s", wASRCreate.Body.String())
	}
	var asrCreateResp struct {
		Success bool          `json:"success"`
		Data    ASRConfigItem `json:"data"`
	}
	_ = json.Unmarshal(wASRCreate.Body.Bytes(), &asrCreateResp)
	if !asrCreateResp.Success || asrCreateResp.Data.Id == 0 || !asrCreateResp.Data.HasAPIKey || asrCreateResp.Data.Provider != "dashscope" || asrCreateResp.Data.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected asr create resp: %+v", asrCreateResp)
	}
	createdASRId := asrCreateResp.Data.Id

	// 14. ASR 配置列表查询
	reqASRList := httptest.NewRequest(http.MethodGet, "/admin-api/asr-config?name=E2E", nil)
	wASRList := httptest.NewRecorder()
	r.ServeHTTP(wASRList, reqASRList)

	if wASRList.Code != http.StatusOK {
		t.Fatalf("list asr config failed: %s", wASRList.Body.String())
	}
	var asrListResp struct {
		Success bool              `json:"success"`
		Data    ASRConfigListData `json:"data"`
	}
	_ = json.Unmarshal(wASRList.Body.Bytes(), &asrListResp)
	if asrListResp.Data.Total != 1 || len(asrListResp.Data.Items) != 1 || asrListResp.Data.Items[0].Provider != "dashscope" || asrListResp.Data.Items[0].ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("expected 1 ASR config item with provider dashscope and proxy_url, got %v", asrListResp.Data.Items)
	}

	// 15. ASR 配置单条删除
	delASRBody, _ := json.Marshal(DeleteASRConfigRequest{Id: createdASRId})
	reqDelASR := httptest.NewRequest(http.MethodPost, "/admin-api/asr-config/delete", bytes.NewReader(delASRBody))
	reqDelASR.Header.Set("Content-Type", "application/json")
	wDelASR := httptest.NewRecorder()
	r.ServeHTTP(wDelASR, reqDelASR)

	if wDelASR.Code != http.StatusOK {
		t.Fatalf("delete asr config failed: %s", wDelASR.Body.String())
	}

	// 16. 访问 /admin/asr-configs 确保静态 SPA 路由可 fallback 到 index.html
	reqSPA3 := httptest.NewRequest(http.MethodGet, "/admin/asr-configs", nil)
	wSPA3 := httptest.NewRecorder()
	r.ServeHTTP(wSPA3, reqSPA3)

	if wSPA3.Code != http.StatusOK {
		t.Fatalf("SPA route for asr-configs failed, code=%d", wSPA3.Code)
	}

	// 17. LLM 配置 E2E 创建
	createLLMBody, _ := json.Marshal(SaveLLMConfigRequest{
		Name:                "E2E DashScope LLM",
		Provider:            "dashscope",
		Endpoint:            "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:              "sk-e2e-llm-key",
		Model:               "qwen-max",
		ProxyURL:            "http://127.0.0.1:7890",
		FirstTokenTimeoutMS: 5000,
		OverallTimeoutMS:    30000,
	})
	reqLLMCreate := httptest.NewRequest(http.MethodPost, "/admin-api/llm-config/save", bytes.NewReader(createLLMBody))
	reqLLMCreate.Header.Set("Content-Type", "application/json")
	wLLMCreate := httptest.NewRecorder()
	r.ServeHTTP(wLLMCreate, reqLLMCreate)

	if wLLMCreate.Code != http.StatusOK {
		t.Fatalf("create llm config failed: %s", wLLMCreate.Body.String())
	}
	var llmCreateResp struct {
		Success bool          `json:"success"`
		Data    LLMConfigItem `json:"data"`
	}
	_ = json.Unmarshal(wLLMCreate.Body.Bytes(), &llmCreateResp)
	if !llmCreateResp.Success || llmCreateResp.Data.Id == 0 || !llmCreateResp.Data.HasAPIKey || llmCreateResp.Data.Provider != "dashscope" || llmCreateResp.Data.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected llm create resp: %+v", llmCreateResp)
	}
	createdLLMId := llmCreateResp.Data.Id

	// 18. LLM 配置列表查询
	reqLLMList := httptest.NewRequest(http.MethodGet, "/admin-api/llm-config?name=E2E", nil)
	wLLMList := httptest.NewRecorder()
	r.ServeHTTP(wLLMList, reqLLMList)

	if wLLMList.Code != http.StatusOK {
		t.Fatalf("list llm config failed: %s", wLLMList.Body.String())
	}
	var llmListResp struct {
		Success bool              `json:"success"`
		Data    LLMConfigListData `json:"data"`
	}
	_ = json.Unmarshal(wLLMList.Body.Bytes(), &llmListResp)
	if llmListResp.Data.Total != 1 || len(llmListResp.Data.Items) != 1 || llmListResp.Data.Items[0].Provider != "dashscope" || llmListResp.Data.Items[0].ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("expected 1 LLM config item with provider dashscope and proxy_url, got %v", llmListResp.Data.Items)
	}

	// 19. LLM 配置单条删除
	delLLMBody, _ := json.Marshal(DeleteLLMConfigRequest{Id: createdLLMId})
	reqDelLLM := httptest.NewRequest(http.MethodPost, "/admin-api/llm-config/delete", bytes.NewReader(delLLMBody))
	reqDelLLM.Header.Set("Content-Type", "application/json")
	wDelLLM := httptest.NewRecorder()
	r.ServeHTTP(wDelLLM, reqDelLLM)

	if wDelLLM.Code != http.StatusOK {
		t.Fatalf("delete llm config failed: %s", wDelLLM.Body.String())
	}

	// 20. 访问 /admin/llm-configs 确保静态 SPA 路由可 fallback 到 index.html
	reqSPA4 := httptest.NewRequest(http.MethodGet, "/admin/llm-configs", nil)
	wSPA4 := httptest.NewRecorder()
	r.ServeHTTP(wSPA4, reqSPA4)

	if wSPA4.Code != http.StatusOK {
		t.Fatalf("SPA route for llm-configs failed, code=%d", wSPA4.Code)
	}

	// 21. TTS 配置 E2E 创建
	createTTSBody, _ := json.Marshal(SaveTTSConfigRequest{
		Name:              "E2E 百炼 TTS",
		Provider:          "dashscope",
		Endpoint:          "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:            "sk-e2e-tts-key",
		Model:             "cosyvoice-v1",
		Voices:            `["longanlingxi", "longxiaochun"]`,
		ProxyURL:          "http://127.0.0.1:7890",
		ConnectTimeoutMS:  5000,
		SentenceTimeoutMS: 10000,
	})
	reqTTSCreate := httptest.NewRequest(http.MethodPost, "/admin-api/tts-config/save", bytes.NewReader(createTTSBody))
	reqTTSCreate.Header.Set("Content-Type", "application/json")
	wTTSCreate := httptest.NewRecorder()
	r.ServeHTTP(wTTSCreate, reqTTSCreate)

	if wTTSCreate.Code != http.StatusOK {
		t.Fatalf("create tts config failed: %s", wTTSCreate.Body.String())
	}
	var ttsCreateResp struct {
		Success bool          `json:"success"`
		Data    TTSConfigItem `json:"data"`
	}
	_ = json.Unmarshal(wTTSCreate.Body.Bytes(), &ttsCreateResp)
	if !ttsCreateResp.Success || ttsCreateResp.Data.Id == 0 || !ttsCreateResp.Data.HasAPIKey || ttsCreateResp.Data.Provider != "dashscope" || ttsCreateResp.Data.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected tts create resp: %+v", ttsCreateResp)
	}
	createdTTSId := ttsCreateResp.Data.Id

	// 22. TTS 配置列表查询
	reqTTSList := httptest.NewRequest(http.MethodGet, "/admin-api/tts-config?name=E2E", nil)
	wTTSList := httptest.NewRecorder()
	r.ServeHTTP(wTTSList, reqTTSList)

	if wTTSList.Code != http.StatusOK {
		t.Fatalf("list tts config failed: %s", wTTSList.Body.String())
	}
	var ttsListResp struct {
		Success bool              `json:"success"`
		Data    TTSConfigListData `json:"data"`
	}
	_ = json.Unmarshal(wTTSList.Body.Bytes(), &ttsListResp)
	if ttsListResp.Data.Total != 1 || len(ttsListResp.Data.Items) != 1 || ttsListResp.Data.Items[0].Provider != "dashscope" || ttsListResp.Data.Items[0].ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("expected 1 TTS config item with provider dashscope and proxy_url, got %v", ttsListResp.Data.Items)
	}

	// 23. TTS 配置单条删除
	delTTSBody, _ := json.Marshal(DeleteTTSConfigRequest{Id: createdTTSId})
	reqDelTTS := httptest.NewRequest(http.MethodPost, "/admin-api/tts-config/delete", bytes.NewReader(delTTSBody))
	reqDelTTS.Header.Set("Content-Type", "application/json")
	wDelTTS := httptest.NewRecorder()
	r.ServeHTTP(wDelTTS, reqDelTTS)

	if wDelTTS.Code != http.StatusOK {
		t.Fatalf("delete tts config failed: %s", wDelTTS.Body.String())
	}

	// 24. 访问 /admin/tts-configs 确保静态 SPA 路由可 fallback 到 index.html
	reqSPA5 := httptest.NewRequest(http.MethodGet, "/admin/tts-configs", nil)
	wSPA5 := httptest.NewRecorder()
	r.ServeHTTP(wSPA5, reqSPA5)

	if wSPA5.Code != http.StatusOK {
		t.Fatalf("SPA route for tts-configs failed, code=%d", wSPA5.Code)
	}

	// 25. Agent 配置 E2E 测试准备基础依赖 ASR, LLM, TTS
	asrCfg := &database.ASRConfig{Name: "E2E-Agent-ASR", Provider: "dashscope", Endpoint: "wss://asr.com", Model: "m1", ConnectTimeoutMS: 5000, Enabled: true}
	_ = db.CreateASRConfig(context.Background(), asrCfg)
	llmCfg := &database.LLMConfig{Name: "E2E-Agent-LLM", Provider: "dashscope", Endpoint: "https://llm.com", Model: "m2", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	_ = db.CreateLLMConfig(context.Background(), llmCfg)
	ttsCfg := &database.TTSConfig{Name: "E2E-Agent-TTS", Provider: "dashscope", Endpoint: "wss://tts.com", Model: "m3", Voices: "[]", ConnectTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: true}
	_ = db.CreateTTSConfig(context.Background(), ttsCfg)

	// 26. Agent 配置 E2E 创建
	createAgentBody, _ := json.Marshal(SaveAgentConfigRequest{
		Name:         "E2E 智能助手",
		ASRConfigId:  asrCfg.Id,
		LLMConfigId:  llmCfg.Id,
		TTSConfigId:  ttsCfg.Id,
		SystemPrompt: "你是一个测试助手。",
		Voice:        "default-voice",
		Enabled:      func(b bool) *bool { return &b }(true),
	})
	reqAgentCreate := httptest.NewRequest(http.MethodPost, "/admin-api/agent-config/save", bytes.NewReader(createAgentBody))
	reqAgentCreate.Header.Set("Content-Type", "application/json")
	wAgentCreate := httptest.NewRecorder()
	r.ServeHTTP(wAgentCreate, reqAgentCreate)

	if wAgentCreate.Code != http.StatusOK {
		t.Fatalf("create agent config failed: %s", wAgentCreate.Body.String())
	}
	var agentCreateResp struct {
		Success bool            `json:"success"`
		Data    AgentConfigItem `json:"data"`
	}
	_ = json.Unmarshal(wAgentCreate.Body.Bytes(), &agentCreateResp)
	if !agentCreateResp.Success || agentCreateResp.Data.Id == 0 || agentCreateResp.Data.Name != "E2E 智能助手" {
		t.Fatalf("unexpected agent create resp: %+v", agentCreateResp)
	}
	createdAgentId := agentCreateResp.Data.Id

	// 27. Agent 配置列表查询
	reqAgentList := httptest.NewRequest(http.MethodGet, "/admin-api/agent-config?name=E2E", nil)
	wAgentList := httptest.NewRecorder()
	r.ServeHTTP(wAgentList, reqAgentList)

	if wAgentList.Code != http.StatusOK {
		t.Fatalf("list agent config failed: %s", wAgentList.Body.String())
	}
	var agentListResp struct {
		Success bool                `json:"success"`
		Data    AgentConfigListData `json:"data"`
	}
	_ = json.Unmarshal(wAgentList.Body.Bytes(), &agentListResp)
	if agentListResp.Data.Total != 1 || len(agentListResp.Data.Items) != 1 || agentListResp.Data.Items[0].Name != "E2E 智能助手" {
		t.Fatalf("expected 1 Agent config item, got %v", agentListResp.Data.Items)
	}

	// 28. Agent 配置单条删除
	delAgentBody, _ := json.Marshal(DeleteAgentConfigRequest{Id: createdAgentId})
	reqDelAgent := httptest.NewRequest(http.MethodPost, "/admin-api/agent-config/delete", bytes.NewReader(delAgentBody))
	reqDelAgent.Header.Set("Content-Type", "application/json")
	wDelAgent := httptest.NewRecorder()
	r.ServeHTTP(wDelAgent, reqDelAgent)

	if wDelAgent.Code != http.StatusOK {
		t.Fatalf("delete agent config failed: %s", wDelAgent.Body.String())
	}

	// 29. 访问 /admin/agent-configs 确保静态 SPA 路由可 fallback 到 index.html
	reqSPA6 := httptest.NewRequest(http.MethodGet, "/admin/agent-configs", nil)
	wSPA6 := httptest.NewRecorder()
	r.ServeHTTP(wSPA6, reqSPA6)

	if wSPA6.Code != http.StatusOK {
		t.Fatalf("SPA route for agent-configs failed, code=%d", wSPA6.Code)
	}

	// 30. 创建测试 Agent 用于 DeviceType
	dtAgent := &database.AgentConfig{Name: "DT-Agent", ASRConfigId: asrCfg.Id, LLMConfigId: llmCfg.Id, TTSConfigId: ttsCfg.Id, SystemPrompt: "prompt", Voice: "v", Enabled: true}
	_ = db.CreateAgentConfig(context.Background(), dtAgent)

	// 31. DeviceType 创建
	createDTBody, _ := json.Marshal(SaveDeviceTypeRequest{
		DeviceType:    "e2e-robot-type",
		AgentConfigId: dtAgent.Id,
	})
	reqDTCreate := httptest.NewRequest(http.MethodPost, "/admin-api/device-type/save", bytes.NewReader(createDTBody))
	reqDTCreate.Header.Set("Content-Type", "application/json")
	wDTCreate := httptest.NewRecorder()
	r.ServeHTTP(wDTCreate, reqDTCreate)

	if wDTCreate.Code != http.StatusOK {
		t.Fatalf("create device type failed: %s", wDTCreate.Body.String())
	}
	var dtCreateResp struct {
		Success bool           `json:"success"`
		Data    DeviceTypeItem `json:"data"`
	}
	_ = json.Unmarshal(wDTCreate.Body.Bytes(), &dtCreateResp)
	if !dtCreateResp.Success || dtCreateResp.Data.Id == 0 || dtCreateResp.Data.DeviceType != "e2e-robot-type" || dtCreateResp.Data.AgentName != "DT-Agent" {
		t.Fatalf("unexpected device type create resp: %+v", dtCreateResp)
	}
	createdDTId := dtCreateResp.Data.Id

	// 32. DeviceType 列表查询
	reqDTList := httptest.NewRequest(http.MethodGet, "/admin-api/device-type?device_type=e2e-robot", nil)
	wDTList := httptest.NewRecorder()
	r.ServeHTTP(wDTList, reqDTList)

	if wDTList.Code != http.StatusOK {
		t.Fatalf("list device type failed: %s", wDTList.Body.String())
	}
	var dtListResp struct {
		Success bool               `json:"success"`
		Data    DeviceTypeListData `json:"data"`
	}
	_ = json.Unmarshal(wDTList.Body.Bytes(), &dtListResp)
	if dtListResp.Data.Total != 1 || len(dtListResp.Data.Items) != 1 || dtListResp.Data.Items[0].DeviceType != "e2e-robot-type" {
		t.Fatalf("expected 1 DeviceType item, got %v", dtListResp.Data.Items)
	}

	// 33. DeviceType 更新
	updateDTBody, _ := json.Marshal(SaveDeviceTypeRequest{
		Id:            createdDTId,
		DeviceType:    "e2e-robot-pro",
		AgentConfigId: dtAgent.Id,
	})
	reqDTUpdate := httptest.NewRequest(http.MethodPost, "/admin-api/device-type/update", bytes.NewReader(updateDTBody))
	reqDTUpdate.Header.Set("Content-Type", "application/json")
	wDTUpdate := httptest.NewRecorder()
	r.ServeHTTP(wDTUpdate, reqDTUpdate)

	if wDTUpdate.Code != http.StatusOK {
		t.Fatalf("update device type failed: %s", wDTUpdate.Body.String())
	}

	// 34. DeviceType 单条删除
	delDTBody, _ := json.Marshal(DeleteDeviceTypeRequest{Id: createdDTId})
	reqDelDT := httptest.NewRequest(http.MethodPost, "/admin-api/device-type/delete", bytes.NewReader(delDTBody))
	reqDelDT.Header.Set("Content-Type", "application/json")
	wDelDT := httptest.NewRecorder()
	r.ServeHTTP(wDelDT, reqDelDT)

	if wDelDT.Code != http.StatusOK {
		t.Fatalf("delete device type failed: %s", wDelDT.Body.String())
	}

	// 35. 访问 /admin/device-types 确保静态 SPA 路由可 fallback 到 index.html
	reqSPA7 := httptest.NewRequest(http.MethodGet, "/admin/device-types", nil)
	wSPA7 := httptest.NewRecorder()
	r.ServeHTTP(wSPA7, reqSPA7)

	if wSPA7.Code != http.StatusOK {
		t.Fatalf("SPA route for device-types failed, code=%d", wSPA7.Code)
	}

	// 36. AgentKit 工具配置创建
	createAKBody, _ := json.Marshal(SaveAgentKitConfigRequest{
		ToolName:   "server.get_current_weather",
		ToolConfig: `{"api_key":"test-key","location":"beijing"}`,
		Enabled:    func(b bool) *bool { return &b }(true),
	})
	reqAKCreate := httptest.NewRequest(http.MethodPost, "/admin-api/agentkit-config/save", bytes.NewReader(createAKBody))
	reqAKCreate.Header.Set("Content-Type", "application/json")
	wAKCreate := httptest.NewRecorder()
	r.ServeHTTP(wAKCreate, reqAKCreate)

	if wAKCreate.Code != http.StatusOK {
		t.Fatalf("create agentkit config failed: %s", wAKCreate.Body.String())
	}
	var akCreateResp struct {
		Success bool               `json:"success"`
		Data    AgentKitConfigItem `json:"data"`
	}
	_ = json.Unmarshal(wAKCreate.Body.Bytes(), &akCreateResp)
	if !akCreateResp.Success || akCreateResp.Data.Id == 0 || akCreateResp.Data.ToolName != "server.get_current_weather" {
		t.Fatalf("unexpected agentkit create resp: %+v", akCreateResp)
	}
	createdAKId := akCreateResp.Data.Id

	// 37. AgentKit 工具配置列表查询
	reqAKList := httptest.NewRequest(http.MethodGet, "/admin-api/agentkit-config?tool_name=weather", nil)
	wAKList := httptest.NewRecorder()
	r.ServeHTTP(wAKList, reqAKList)

	if wAKList.Code != http.StatusOK {
		t.Fatalf("list agentkit config failed: %s", wAKList.Body.String())
	}
	var akListResp struct {
		Success bool                   `json:"success"`
		Data    AgentKitConfigListData `json:"data"`
	}
	_ = json.Unmarshal(wAKList.Body.Bytes(), &akListResp)
	if akListResp.Data.Total != 1 || len(akListResp.Data.Items) != 1 || akListResp.Data.Items[0].ToolName != "server.get_current_weather" {
		t.Fatalf("expected 1 AgentKit config item, got %v", akListResp.Data.Items)
	}

	// 38. AgentKit 工具配置单条删除
	delAKBody, _ := json.Marshal(DeleteAgentKitConfigRequest{Id: createdAKId})
	reqDelAK := httptest.NewRequest(http.MethodPost, "/admin-api/agentkit-config/delete", bytes.NewReader(delAKBody))
	reqDelAK.Header.Set("Content-Type", "application/json")
	wDelAK := httptest.NewRecorder()
	r.ServeHTTP(wDelAK, reqDelAK)

	if wDelAK.Code != http.StatusOK {
		t.Fatalf("delete agentkit config failed: %s", wDelAK.Body.String())
	}

	// 39. 访问 /admin/agentkit-configs 确保静态 SPA 路由可 fallback 到 index.html
	reqSPA8 := httptest.NewRequest(http.MethodGet, "/admin/agentkit-configs", nil)
	wSPA8 := httptest.NewRecorder()
	r.ServeHTTP(wSPA8, reqSPA8)

	if wSPA8.Code != http.StatusOK {
		t.Fatalf("SPA route for agentkit-configs failed, code=%d", wSPA8.Code)
	}
}
