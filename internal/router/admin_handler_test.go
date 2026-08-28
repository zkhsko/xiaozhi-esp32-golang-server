package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

func setupTestRouterDB(t *testing.T) *database.Database {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := config.DatabaseConfig{
		Driver:       "sqlite",
		DSN:          "file::memory:?cache=shared",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	}

	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func TestAdminGenerateCredential(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)

	reqBody := []byte(`{"count": 2, "device_type": "smart-speaker"}`)
	req := httptest.NewRequest(http.MethodPost, "/device-hmac-credential/generate", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	adminHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp GenerateCredentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("expected success true")
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resp.Items))
	}

	for _, item := range resp.Items {
		if len(item.HMACKey) != 64 {
			t.Errorf("expected 64-char hex HMACKey, got %q (len %d)", item.HMACKey, len(item.HMACKey))
		}
		if item.SerialNumber == "" {
			t.Errorf("expected non-empty serial number")
		}
		if item.DeviceType != "smart-speaker" {
			t.Errorf("expected DeviceType %q, got %q", "smart-speaker", item.DeviceType)
		}

		cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), item.SerialNumber)
		if err != nil {
			t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
		}
		if cred.HMACKeyCiphertext != item.HMACKey {
			t.Errorf("db ciphertext %q mismatch returned hex key %q", cred.HMACKeyCiphertext, item.HMACKey)
		}
		if cred.CredentialStatus != database.CredentialStatusEnabled {
			t.Errorf("expected enabled status, got %q", cred.CredentialStatus)
		}
		if cred.DeviceType != "smart-speaker" {
			t.Errorf("expected db DeviceType %q, got %q", "smart-speaker", cred.DeviceType)
		}
	}
}

func TestAdminGenerateCredential_DefaultDeviceType(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)

	reqBody := []byte(`{"count": 1}`)
	req := httptest.NewRequest(http.MethodPost, "/device-hmac-credential/generate", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	adminHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp GenerateCredentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}

	if resp.Items[0].DeviceType != "default" {
		t.Errorf("expected default DeviceType %q, got %q", "default", resp.Items[0].DeviceType)
	}

	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), resp.Items[0].SerialNumber)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.DeviceType != "default" {
		t.Errorf("expected db default DeviceType %q, got %q", "default", cred.DeviceType)
	}
}

func TestAdminCredentialCRUD(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()

	// 1. 生成初始凭证
	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      "test-sn-001",
			HMACKeyCiphertext: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			DeviceType:        "desk-robot",
			CredentialStatus:  database.CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("batch create failed: %v", err)
	}

	foundCred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), "test-sn-001")
	if err != nil {
		t.Fatalf("find credential failed: %v", err)
	}
	targetID := foundCred.ID

	// 2. List credentials
	reqList := httptest.NewRequest(http.MethodGet, "/device-hmac-credential?serial_number=test-sn", nil)
	wList := httptest.NewRecorder()
	routes.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list failed, status=%d, body=%s", wList.Code, wList.Body.String())
	}

	var listResp struct {
		Success bool                     `json:"success"`
		Data    DeviceCredentialListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list resp failed: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("expected 1 item in list, got total=%d len=%d", listResp.Data.Total, len(listResp.Data.Items))
	}

	// 3. Update credential (via POST /device-hmac-credential/update)
	updateBody, _ := json.Marshal(UpdateCredentialRequest{
		ID:               targetID,
		DeviceType:       "desk-robot-v2",
		CredentialStatus: "blocked",
	})
	reqUpdate := httptest.NewRequest(http.MethodPost, "/device-hmac-credential/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	routes.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update failed, status=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), "test-sn-001")
	if err != nil {
		t.Fatalf("find credential failed: %v", err)
	}
	if cred.DeviceType != "desk-robot-v2" || cred.CredentialStatus != database.CredentialStatusBlocked {
		t.Fatalf("update mismatch: type=%s, status=%s", cred.DeviceType, cred.CredentialStatus)
	}

	// 4. Delete credential (via POST /device-hmac-credential/delete)
	deleteBody, _ := json.Marshal(DeleteCredentialRequest{
		ID: targetID,
	})
	reqDelete := httptest.NewRequest(http.MethodPost, "/device-hmac-credential/delete", bytes.NewReader(deleteBody))
	reqDelete.Header.Set("Content-Type", "application/json")
	wDelete := httptest.NewRecorder()
	routes.ServeHTTP(wDelete, reqDelete)

	if wDelete.Code != http.StatusOK {
		t.Fatalf("delete failed, status=%d, body=%s", wDelete.Code, wDelete.Body.String())
	}

	// Verify deleted
	_, err = db.FindDeviceHmacCredentialBySerialNumber(context.Background(), "test-sn-001")
	if !errors.Is(err, database.ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound after delete, got %v", err)
	}
}

func TestAdminCredentialBatchDelete(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()

	// Insert 2 records via BatchCreateDeviceHmacCredentials
	_ = db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      "sn-del-1",
			HMACKeyCiphertext: "1111111111111111111111111111111111111111111111111111111111111111",
		},
		{
			SerialNumber:      "sn-del-2",
			HMACKeyCiphertext: "2222222222222222222222222222222222222222222222222222222222222222",
		},
	})

	list, _, _ := db.ListDeviceHmacCredentials(context.Background(), database.DeviceHmacCredentialFilter{})
	if len(list) != 2 {
		t.Fatalf("expected 2 items, got %d", len(list))
	}

	batchDelBody, _ := json.Marshal(BatchDeleteCredentialRequest{
		IDs: []uint64{list[0].ID, list[1].ID},
	})
	req := httptest.NewRequest(http.MethodPost, "/device-hmac-credential/batch-delete", bytes.NewReader(batchDelBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("batch delete failed, status=%d, body=%s", w.Code, w.Body.String())
	}

	listAfter, totalAfter, _ := db.ListDeviceHmacCredentials(context.Background(), database.DeviceHmacCredentialFilter{})
	if totalAfter != 0 || len(listAfter) != 0 {
		t.Fatalf("expected 0 items after batch delete, got %d", totalAfter)
	}
}

func TestAdminDeviceActivationEndpoints(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()

	// 1. Setup Initial Activation
	initialAct, err := db.ActivateDeviceBySerialNumber(context.Background(), "sn-test-act-001", "dev-test-act-001", "cli-test-001")
	if err != nil {
		t.Fatalf("activate device failed: %v", err)
	}
	actID := initialAct.ID

	// 2. Test List Activation
	reqList := httptest.NewRequest(http.MethodGet, "/device-activation?page=1&page_size=10", nil)
	wList := httptest.NewRecorder()
	routes.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list activations failed, code=%d, body=%s", wList.Code, wList.Body.String())
	}
	var listResp struct {
		Success bool                     `json:"success"`
		Data    DeviceActivationListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp failed: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("unexpected list data total: %d, len: %d", listResp.Data.Total, len(listResp.Data.Items))
	}

	// 3. Test Update Activation
	updateBody := []byte(fmt.Sprintf(`{
		"id": %d,
		"device_id": "dev-test-act-001-mod",
		"activation_status": "frozen"
	}`, actID))
	reqUpdate := httptest.NewRequest(http.MethodPost, "/device-activation/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	routes.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update activation failed, code=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	act, err := db.FindDeviceActivationBySerialNumber(context.Background(), "sn-test-act-001")
	if err != nil {
		t.Fatalf("find activation failed: %v", err)
	}
	if act.DeviceID != "dev-test-act-001-mod" || act.ActivationStatus != "frozen" {
		t.Fatalf("unexpected updated activation: %+v", act)
	}

	// 4. Test Single Delete
	deleteBody := []byte(fmt.Sprintf(`{"id": %d}`, actID))
	reqDelete := httptest.NewRequest(http.MethodPost, "/device-activation/delete", bytes.NewReader(deleteBody))
	reqDelete.Header.Set("Content-Type", "application/json")
	wDelete := httptest.NewRecorder()
	routes.ServeHTTP(wDelete, reqDelete)

	if wDelete.Code != http.StatusOK {
		t.Fatalf("delete activation failed, code=%d, body=%s", wDelete.Code, wDelete.Body.String())
	}

	// 5. Test Batch Delete
	act1, _ := db.ActivateDeviceBySerialNumber(context.Background(), "sn-batch-del-1", "dev-1", "")
	act2, _ := db.ActivateDeviceBySerialNumber(context.Background(), "sn-batch-del-2", "dev-2", "")

	_, totalAfterCreate, _ := db.ListDeviceActivations(context.Background(), database.DeviceActivationFilter{})
	if totalAfterCreate != 2 {
		t.Fatalf("expected 2 items for batch delete, got %d", totalAfterCreate)
	}

	batchDelBody, _ := json.Marshal(BatchDeleteActivationRequest{
		IDs: []uint64{act1.ID, act2.ID},
	})
	reqBatchDel := httptest.NewRequest(http.MethodPost, "/device-activation/batch-delete", bytes.NewReader(batchDelBody))
	reqBatchDel.Header.Set("Content-Type", "application/json")
	wBatchDel := httptest.NewRecorder()
	routes.ServeHTTP(wBatchDel, reqBatchDel)

	if wBatchDel.Code != http.StatusOK {
		t.Fatalf("batch delete activation failed, code=%d, body=%s", wBatchDel.Code, wBatchDel.Body.String())
	}

	_, finalTotal, _ := db.ListDeviceActivations(context.Background(), database.DeviceActivationFilter{})
	if finalTotal != 0 {
		t.Fatalf("expected 0 items after batch delete, got %d", finalTotal)
	}
}

func TestAdminASRConfigEndpoints(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()

	// 1. Create ASR Config via /asr-config/save
	createBody := []byte(`{
		"name": "百炼语音识别",
		"provider": "bailian",
		"endpoint": "wss://dashscope.aliyuncs.com/api-v1/ws",
		"api_key": "sk-secret-key-123456",
		"model": "qwen-audio-3.0-asr-flash-streaming",
		"hotwords": "[\"小智\", \"ESP32\", \"智能音箱\"]",
		"connect_timeout_ms": 6000,
		"enabled": true
	}`)
	reqCreate := httptest.NewRequest(http.MethodPost, "/asr-config/save", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	wCreate := httptest.NewRecorder()
	routes.ServeHTTP(wCreate, reqCreate)

	if wCreate.Code != http.StatusOK {
		t.Fatalf("create asr config failed, code=%d, body=%s", wCreate.Code, wCreate.Body.String())
	}

	var createResp struct {
		Success bool          `json:"success"`
		Data    ASRConfigItem `json:"data"`
	}
	if err := json.Unmarshal(wCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal createResp failed: %v", err)
	}
	if !createResp.Success || createResp.Data.ID == 0 {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	if !createResp.Data.HasAPIKey {
		t.Fatalf("expected has_api_key true")
	}
	if createResp.Data.Provider != "bailian" {
		t.Fatalf("expected provider bailian, got %q", createResp.Data.Provider)
	}
	asrID := createResp.Data.ID

	// 2. List ASR Configs
	reqList := httptest.NewRequest(http.MethodGet, "/asr-config/list?page=1&page_size=10&name=百炼", nil)
	wList := httptest.NewRecorder()
	routes.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list asr configs failed, code=%d, body=%s", wList.Code, wList.Body.String())
	}
	var listResp struct {
		Success bool              `json:"success"`
		Data    ASRConfigListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp failed: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("unexpected list count: total=%d, items=%d", listResp.Data.Total, len(listResp.Data.Items))
	}
	if listResp.Data.Items[0].Name != "百炼语音识别" || !listResp.Data.Items[0].HasAPIKey || listResp.Data.Items[0].Provider != "bailian" {
		t.Fatalf("unexpected item content: %+v", listResp.Data.Items[0])
	}

	// 3. Update ASR Config without changing API Key (empty api_key should preserve existing key)
	updateBody := []byte(fmt.Sprintf(`{
		"id": %d,
		"name": "百炼语音识别-修改版",
		"provider": "volcengine",
		"endpoint": "wss://dashscope.aliyuncs.com/api-v1/ws",
		"api_key": "",
		"model": "qwen-audio-asr-v2",
		"hotwords": "[\"小智\", \"ESP32\", \"智能音箱\", \"修改热词\"]",
		"connect_timeout_ms": 8000,
		"enabled": false
	}`, asrID))
	reqUpdate := httptest.NewRequest(http.MethodPost, "/asr-config/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	routes.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update asr config failed, code=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	// Verify in DB that original api_key is preserved and fields updated
	found, err := db.FindASRConfigByID(context.Background(), asrID)
	if err != nil {
		t.Fatalf("find asr config in db failed: %v", err)
	}
	if found.APIKey != "sk-secret-key-123456" {
		t.Errorf("expected preserved api_key 'sk-secret-key-123456', got %q", found.APIKey)
	}
	if found.Name != "百炼语音识别-修改版" || found.Model != "qwen-audio-asr-v2" || found.Enabled != false || found.Provider != "volcengine" {
		t.Errorf("unexpected updated fields in DB: %+v", found)
	}

	// 4. Single Delete
	delBody := []byte(fmt.Sprintf(`{"id": %d}`, asrID))
	reqDel := httptest.NewRequest(http.MethodPost, "/asr-config/delete", bytes.NewReader(delBody))
	reqDel.Header.Set("Content-Type", "application/json")
	wDel := httptest.NewRecorder()
	routes.ServeHTTP(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("delete asr config failed, code=%d, body=%s", wDel.Code, wDel.Body.String())
	}

	_, err = db.FindASRConfigByID(context.Background(), asrID)
	if !errors.Is(err, database.ErrASRConfigNotFound) {
		t.Fatalf("expected ErrASRConfigNotFound after delete, got %v", err)
	}

	// 5. Batch Delete
	cfgA := &database.ASRConfig{Name: "A", Endpoint: "ws://localhost/a", Model: "m1", ConnectTimeoutMS: 5000, Enabled: true}
	cfgB := &database.ASRConfig{Name: "B", Endpoint: "ws://localhost/b", Model: "m2", ConnectTimeoutMS: 5000, Enabled: true}
	_ = db.CreateASRConfig(context.Background(), cfgA)
	_ = db.CreateASRConfig(context.Background(), cfgB)

	batchDelBody := []byte(fmt.Sprintf(`{"ids": [%d, %d]}`, cfgA.ID, cfgB.ID))
	reqBatchDel := httptest.NewRequest(http.MethodPost, "/asr-config/batch-delete", bytes.NewReader(batchDelBody))
	reqBatchDel.Header.Set("Content-Type", "application/json")
	wBatchDel := httptest.NewRecorder()
	routes.ServeHTTP(wBatchDel, reqBatchDel)

	if wBatchDel.Code != http.StatusOK {
		t.Fatalf("batch delete asr config failed, code=%d, body=%s", wBatchDel.Code, wBatchDel.Body.String())
	}

	_, finalCount, _ := db.ListASRConfigs(context.Background(), database.ASRConfigFilter{})
	if finalCount != 0 {
		t.Fatalf("expected 0 configs after batch delete, got %d", finalCount)
	}
}

func TestAdminLLMConfigEndpoints(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()

	// 1. Create LLM Config via /llm-config/save
	createBody := []byte(`{
		"name": "百炼大语言模型",
		"provider": "bailian",
		"endpoint": "https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation",
		"api_key": "sk-secret-llm-key-123456",
		"model": "qwen-max",
		"first_token_timeout_ms": 6000,
		"overall_timeout_ms": 35000,
		"enabled": true
	}`)
	reqCreate := httptest.NewRequest(http.MethodPost, "/llm-config/save", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	wCreate := httptest.NewRecorder()
	routes.ServeHTTP(wCreate, reqCreate)

	if wCreate.Code != http.StatusOK {
		t.Fatalf("create llm config failed, code=%d, body=%s", wCreate.Code, wCreate.Body.String())
	}

	var createResp struct {
		Success bool          `json:"success"`
		Data    LLMConfigItem `json:"data"`
	}
	if err := json.Unmarshal(wCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal createResp failed: %v", err)
	}
	if !createResp.Success || createResp.Data.ID == 0 {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	if !createResp.Data.HasAPIKey {
		t.Fatalf("expected has_api_key true")
	}
	if createResp.Data.Provider != "bailian" {
		t.Fatalf("expected provider bailian, got %q", createResp.Data.Provider)
	}
	llmID := createResp.Data.ID

	// 2. List LLM Configs
	reqList := httptest.NewRequest(http.MethodGet, "/llm-config/list?page=1&page_size=10&name=百炼", nil)
	wList := httptest.NewRecorder()
	routes.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list llm configs failed, code=%d, body=%s", wList.Code, wList.Body.String())
	}
	var listResp struct {
		Success bool              `json:"success"`
		Data    LLMConfigListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp failed: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("unexpected list count: total=%d, items=%d", listResp.Data.Total, len(listResp.Data.Items))
	}
	if listResp.Data.Items[0].Name != "百炼大语言模型" || !listResp.Data.Items[0].HasAPIKey || listResp.Data.Items[0].Provider != "bailian" {
		t.Fatalf("unexpected item content: %+v", listResp.Data.Items[0])
	}

	// 3. Update LLM Config without changing API Key (empty api_key should preserve existing key)
	updateBody := []byte(fmt.Sprintf(`{
		"id": %d,
		"name": "百炼大语言模型-修改版",
		"provider": "openai",
		"endpoint": "https://dashscope.aliyuncs.com/api/v1/services/aigc/text-generation/generation",
		"api_key": "",
		"model": "qwen-plus",
		"first_token_timeout_ms": 8000,
		"overall_timeout_ms": 40000,
		"enabled": false
	}`, llmID))
	reqUpdate := httptest.NewRequest(http.MethodPost, "/llm-config/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	routes.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update llm config failed, code=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	// Verify in DB that original api_key is preserved and fields updated
	found, err := db.FindLLMConfigByID(context.Background(), llmID)
	if err != nil {
		t.Fatalf("find llm config in db failed: %v", err)
	}
	if found.APIKey != "sk-secret-llm-key-123456" {
		t.Errorf("expected preserved api_key 'sk-secret-llm-key-123456', got %q", found.APIKey)
	}
	if found.Name != "百炼大语言模型-修改版" || found.Model != "qwen-plus" || found.Enabled != false || found.Provider != "openai" {
		t.Errorf("unexpected updated fields in DB: %+v", found)
	}

	// 4. Single Delete
	delBody := []byte(fmt.Sprintf(`{"id": %d}`, llmID))
	reqDel := httptest.NewRequest(http.MethodPost, "/llm-config/delete", bytes.NewReader(delBody))
	reqDel.Header.Set("Content-Type", "application/json")
	wDel := httptest.NewRecorder()
	routes.ServeHTTP(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("delete llm config failed, code=%d, body=%s", wDel.Code, wDel.Body.String())
	}

	_, err = db.FindLLMConfigByID(context.Background(), llmID)
	if !errors.Is(err, database.ErrLLMConfigNotFound) {
		t.Fatalf("expected ErrLLMConfigNotFound after delete, got %v", err)
	}

	// 5. Batch Delete
	cfgA := &database.LLMConfig{Name: "A", Endpoint: "http://localhost/a", Model: "m1", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	cfgB := &database.LLMConfig{Name: "B", Endpoint: "http://localhost/b", Model: "m2", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	_ = db.CreateLLMConfig(context.Background(), cfgA)
	_ = db.CreateLLMConfig(context.Background(), cfgB)

	batchDelBody := []byte(fmt.Sprintf(`{"ids": [%d, %d]}`, cfgA.ID, cfgB.ID))
	reqBatchDel := httptest.NewRequest(http.MethodPost, "/llm-config/batch-delete", bytes.NewReader(batchDelBody))
	reqBatchDel.Header.Set("Content-Type", "application/json")
	wBatchDel := httptest.NewRecorder()
	routes.ServeHTTP(wBatchDel, reqBatchDel)

	if wBatchDel.Code != http.StatusOK {
		t.Fatalf("batch delete llm config failed, code=%d, body=%s", wBatchDel.Code, wBatchDel.Body.String())
	}

	_, finalCount, _ := db.ListLLMConfigs(context.Background(), database.LLMConfigFilter{})
	if finalCount != 0 {
		t.Fatalf("expected 0 configs after batch delete, got %d", finalCount)
	}
}

func TestAdminTTSConfigEndpoints(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()

	// 1. Create TTS Config via /tts-config/save
	createBody := []byte(`{
		"name": "百炼语音合成",
		"provider": "bailian",
		"endpoint": "wss://dashscope.aliyuncs.com/api-v1/ws",
		"api_key": "sk-secret-tts-key-123456",
		"model": "cosyvoice-v1",
		"voices": "[\"longanlingxi\", \"longxiaochun\"]",
		"connect_timeout_ms": 6000,
		"first_audio_timeout_ms": 6000,
		"sentence_timeout_ms": 12000,
		"enabled": true
	}`)
	reqCreate := httptest.NewRequest(http.MethodPost, "/tts-config/save", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	wCreate := httptest.NewRecorder()
	routes.ServeHTTP(wCreate, reqCreate)

	if wCreate.Code != http.StatusOK {
		t.Fatalf("create tts config failed, code=%d, body=%s", wCreate.Code, wCreate.Body.String())
	}

	var createResp struct {
		Success bool          `json:"success"`
		Data    TTSConfigItem `json:"data"`
	}
	if err := json.Unmarshal(wCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal createResp failed: %v", err)
	}
	if !createResp.Success || createResp.Data.ID == 0 {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	if !createResp.Data.HasAPIKey {
		t.Fatalf("expected has_api_key true")
	}
	if createResp.Data.Provider != "bailian" {
		t.Fatalf("expected provider bailian, got %q", createResp.Data.Provider)
	}
	ttsID := createResp.Data.ID

	// 2. List TTS Configs
	reqList := httptest.NewRequest(http.MethodGet, "/tts-config/list?page=1&page_size=10&name=百炼", nil)
	wList := httptest.NewRecorder()
	routes.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list tts configs failed, code=%d, body=%s", wList.Code, wList.Body.String())
	}
	var listResp struct {
		Success bool              `json:"success"`
		Data    TTSConfigListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp failed: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("unexpected list count: total=%d, items=%d", listResp.Data.Total, len(listResp.Data.Items))
	}
	if listResp.Data.Items[0].Name != "百炼语音合成" || !listResp.Data.Items[0].HasAPIKey || listResp.Data.Items[0].Provider != "bailian" {
		t.Fatalf("unexpected item content: %+v", listResp.Data.Items[0])
	}

	// 3. Update TTS Config without changing API Key (empty api_key should preserve existing key)
	updateBody := []byte(fmt.Sprintf(`{
		"id": %d,
		"name": "百炼语音合成-修改版",
		"provider": "volcengine",
		"endpoint": "wss://dashscope.aliyuncs.com/api-v1/ws",
		"api_key": "",
		"model": "cosyvoice-v2",
		"voices": "[\"longanlingxi\", \"longxiaochun\", \"new_voice\"]",
		"connect_timeout_ms": 8000,
		"first_audio_timeout_ms": 7000,
		"sentence_timeout_ms": 15000,
		"enabled": false
	}`, ttsID))
	reqUpdate := httptest.NewRequest(http.MethodPost, "/tts-config/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	routes.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update tts config failed, code=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	// Verify in DB that original api_key is preserved and fields updated
	found, err := db.FindTTSConfigByID(context.Background(), ttsID)
	if err != nil {
		t.Fatalf("find tts config in db failed: %v", err)
	}
	if found.APIKey != "sk-secret-tts-key-123456" {
		t.Errorf("expected preserved api_key 'sk-secret-tts-key-123456', got %q", found.APIKey)
	}
	if found.Name != "百炼语音合成-修改版" || found.Model != "cosyvoice-v2" || found.Enabled != false || found.Provider != "volcengine" {
		t.Errorf("unexpected updated fields in DB: %+v", found)
	}

	// 4. Single Delete
	delBody := []byte(fmt.Sprintf(`{"id": %d}`, ttsID))
	reqDel := httptest.NewRequest(http.MethodPost, "/tts-config/delete", bytes.NewReader(delBody))
	reqDel.Header.Set("Content-Type", "application/json")
	wDel := httptest.NewRecorder()
	routes.ServeHTTP(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("delete tts config failed, code=%d, body=%s", wDel.Code, wDel.Body.String())
	}

	_, err = db.FindTTSConfigByID(context.Background(), ttsID)
	if !errors.Is(err, database.ErrTTSConfigNotFound) {
		t.Fatalf("expected ErrTTSConfigNotFound after delete, got %v", err)
	}

	// 5. Batch Delete
	cfgA := &database.TTSConfig{Name: "A", Endpoint: "ws://localhost/a", Model: "m1", Voices: "[]", ConnectTimeoutMS: 5000, FirstAudioTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: true}
	cfgB := &database.TTSConfig{Name: "B", Endpoint: "ws://localhost/b", Model: "m2", Voices: "[]", ConnectTimeoutMS: 5000, FirstAudioTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: true}
	_ = db.CreateTTSConfig(context.Background(), cfgA)
	_ = db.CreateTTSConfig(context.Background(), cfgB)

	batchDelBody := []byte(fmt.Sprintf(`{"ids": [%d, %d]}`, cfgA.ID, cfgB.ID))
	reqBatchDel := httptest.NewRequest(http.MethodPost, "/tts-config/batch-delete", bytes.NewReader(batchDelBody))
	reqBatchDel.Header.Set("Content-Type", "application/json")
	wBatchDel := httptest.NewRecorder()
	routes.ServeHTTP(wBatchDel, reqBatchDel)

	if wBatchDel.Code != http.StatusOK {
		t.Fatalf("batch delete tts config failed, code=%d, body=%s", wBatchDel.Code, wBatchDel.Body.String())
	}

	_, finalCount, _ := db.ListTTSConfigs(context.Background(), database.TTSConfigFilter{})
	if finalCount != 0 {
		t.Fatalf("expected 0 configs after batch delete, got %d", finalCount)
	}
}

func TestAdminAgentConfigEndpoints(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()
	ctx := context.Background()

	// 准备基础 ASR, LLM, TTS 组件
	asr := &database.ASRConfig{Name: "默认ASR", Provider: "bailian", Endpoint: "wss://asr.example.com", Model: "asr-v1", ConnectTimeoutMS: 5000, Enabled: true}
	_ = db.CreateASRConfig(ctx, asr)
	llm := &database.LLMConfig{Name: "默认LLM", Provider: "bailian", Endpoint: "https://llm.example.com", Model: "qwen-turbo", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	_ = db.CreateLLMConfig(ctx, llm)
	tts := &database.TTSConfig{Name: "默认TTS", Provider: "bailian", Endpoint: "wss://tts.example.com", Model: "tts-v1", Voices: `["voice1"]`, ConnectTimeoutMS: 5000, FirstAudioTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: true}
	_ = db.CreateTTSConfig(ctx, tts)

	// 1. Create Agent Config via /agent-config/save
	createBody := []byte(fmt.Sprintf(`{
		"name": "智能助手",
		"asr_config_id": %d,
		"llm_config_id": %d,
		"tts_config_id": %d,
		"system_prompt": "你是一个智能音箱助手。",
		"voice": "voice1",
		"enabled": true
	}`, asr.ID, llm.ID, tts.ID))
	reqCreate := httptest.NewRequest(http.MethodPost, "/agent-config/save", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	wCreate := httptest.NewRecorder()
	routes.ServeHTTP(wCreate, reqCreate)

	if wCreate.Code != http.StatusOK {
		t.Fatalf("create agent config failed, code=%d, body=%s", wCreate.Code, wCreate.Body.String())
	}

	var createResp struct {
		Success bool            `json:"success"`
		Data    AgentConfigItem `json:"data"`
	}
	if err := json.Unmarshal(wCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal createResp failed: %v", err)
	}
	if !createResp.Success || createResp.Data.ID == 0 {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	if createResp.Data.Name != "智能助手" || createResp.Data.Voice != "voice1" {
		t.Fatalf("unexpected create data: %+v", createResp.Data)
	}

	agentID := createResp.Data.ID

	// 2. List Agent Configs
	reqList := httptest.NewRequest(http.MethodGet, "/agent-config?page=1&page_size=10", nil)
	wList := httptest.NewRecorder()
	routes.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list agent config failed, code=%d, body=%s", wList.Code, wList.Body.String())
	}

	var listResp struct {
		Success bool                `json:"success"`
		Data    AgentConfigListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp failed: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("unexpected list count: total=%d, items=%d", listResp.Data.Total, len(listResp.Data.Items))
	}
	if listResp.Data.Items[0].Name != "智能助手" || listResp.Data.Items[0].ASRName != "默认ASR" {
		t.Fatalf("unexpected item content: %+v", listResp.Data.Items[0])
	}

	// 3. Update Agent Config
	updateBody := []byte(fmt.Sprintf(`{
		"id": %d,
		"name": "智能助手-修改版",
		"asr_config_id": %d,
		"llm_config_id": %d,
		"tts_config_id": %d,
		"system_prompt": "更新后的提示词",
		"voice": "voice2"
	}`, agentID, asr.ID, llm.ID, tts.ID))
	reqUpdate := httptest.NewRequest(http.MethodPost, "/agent-config/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	routes.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update agent config failed, code=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	found, err := db.FindAgentConfigByID(ctx, agentID)
	if err != nil {
		t.Fatalf("find agent config in db failed: %v", err)
	}
	if found.Name != "智能助手-修改版" || found.SystemPrompt != "更新后的提示词" || found.Voice != "voice2" {
		t.Errorf("unexpected updated fields in DB: %+v", found)
	}

	// 4. Activate Agent Config
	actBody := []byte(fmt.Sprintf(`{"id": %d}`, agentID))
	reqAct := httptest.NewRequest(http.MethodPost, "/agent-config/activate", bytes.NewReader(actBody))
	reqAct.Header.Set("Content-Type", "application/json")
	wAct := httptest.NewRecorder()
	routes.ServeHTTP(wAct, reqAct)
	if wAct.Code != http.StatusOK {
		t.Fatalf("activate agent config failed, code=%d, body=%s", wAct.Code, wAct.Body.String())
	}

	// 5. Delete Agent Config
	delBody := []byte(fmt.Sprintf(`{"id": %d}`, agentID))
	reqDel := httptest.NewRequest(http.MethodPost, "/agent-config/delete", bytes.NewReader(delBody))
	reqDel.Header.Set("Content-Type", "application/json")
	wDel := httptest.NewRecorder()
	routes.ServeHTTP(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("delete agent config failed, code=%d, body=%s", wDel.Code, wDel.Body.String())
	}

	_, err = db.FindAgentConfigByID(ctx, agentID)
	if !errors.Is(err, database.ErrAgentConfigNotFound) {
		t.Fatalf("expected ErrAgentConfigNotFound after delete, got %v", err)
	}

	// 6. Batch Delete
	cfgA := &database.AgentConfig{Name: "A", ASRConfigID: asr.ID, LLMConfigID: llm.ID, TTSConfigID: tts.ID, SystemPrompt: "p1", Voice: "v1"}
	cfgB := &database.AgentConfig{Name: "B", ASRConfigID: asr.ID, LLMConfigID: llm.ID, TTSConfigID: tts.ID, SystemPrompt: "p2", Voice: "v2"}
	_ = db.CreateAgentConfig(ctx, cfgA)
	_ = db.CreateAgentConfig(ctx, cfgB)

	batchDelBody := []byte(fmt.Sprintf(`{"ids": [%d, %d]}`, cfgA.ID, cfgB.ID))
	reqBatchDel := httptest.NewRequest(http.MethodPost, "/agent-config/batch-delete", bytes.NewReader(batchDelBody))
	reqBatchDel.Header.Set("Content-Type", "application/json")
	wBatchDel := httptest.NewRecorder()
	routes.ServeHTTP(wBatchDel, reqBatchDel)

	if wBatchDel.Code != http.StatusOK {
		t.Fatalf("batch delete agent config failed, code=%d, body=%s", wBatchDel.Code, wBatchDel.Body.String())
	}

	_, finalCount, _ := db.ListAgentConfigs(ctx, database.AgentConfigFilter{})
	if finalCount != 0 {
		t.Fatalf("expected 0 configs after batch delete, got %d", finalCount)
	}
}

func TestAdminDeviceTypeEndpoints(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)
	routes := adminHandler.Routes()
	ctx := context.Background()

	// 准备基础 ASR, LLM, TTS 和 Agent
	asr := &database.ASRConfig{Name: "ASR-1", Provider: "bailian", Endpoint: "wss://asr.example.com", Model: "asr-v1", ConnectTimeoutMS: 5000, Enabled: true}
	_ = db.CreateASRConfig(ctx, asr)
	llm := &database.LLMConfig{Name: "LLM-1", Provider: "bailian", Endpoint: "https://llm.example.com", Model: "qwen-turbo", FirstTokenTimeoutMS: 5000, OverallTimeoutMS: 30000, Enabled: true}
	_ = db.CreateLLMConfig(ctx, llm)
	tts := &database.TTSConfig{Name: "TTS-1", Provider: "bailian", Endpoint: "wss://tts.example.com", Model: "tts-v1", Voices: `["v1"]`, ConnectTimeoutMS: 5000, FirstAudioTimeoutMS: 5000, SentenceTimeoutMS: 10000, Enabled: true}
	_ = db.CreateTTSConfig(ctx, tts)

	agent1 := &database.AgentConfig{Name: "Agent-A", ASRConfigID: asr.ID, LLMConfigID: llm.ID, TTSConfigID: tts.ID, SystemPrompt: "prompt A", Voice: "v1", Enabled: true}
	_ = db.CreateAgentConfig(ctx, agent1)
	agent2 := &database.AgentConfig{Name: "Agent-B", ASRConfigID: asr.ID, LLMConfigID: llm.ID, TTSConfigID: tts.ID, SystemPrompt: "prompt B", Voice: "v1", Enabled: true}
	_ = db.CreateAgentConfig(ctx, agent2)

	// 1. Create DeviceType via /device-type/save
	createBody := []byte(fmt.Sprintf(`{
		"device_type": "robot-dog",
		"agent_config_id": %d
	}`, agent1.ID))
	reqCreate := httptest.NewRequest(http.MethodPost, "/device-type/save", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	wCreate := httptest.NewRecorder()
	routes.ServeHTTP(wCreate, reqCreate)

	if wCreate.Code != http.StatusOK {
		t.Fatalf("create device type failed, code=%d, body=%s", wCreate.Code, wCreate.Body.String())
	}

	var createResp struct {
		Success bool           `json:"success"`
		Data    DeviceTypeItem `json:"data"`
	}
	if err := json.Unmarshal(wCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal createResp failed: %v", err)
	}
	if !createResp.Success || createResp.Data.ID == 0 {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	if createResp.Data.DeviceType != "robot-dog" || createResp.Data.AgentConfigID != agent1.ID || createResp.Data.AgentName != "Agent-A" {
		t.Fatalf("unexpected create data: %+v", createResp.Data)
	}

	dtID := createResp.Data.ID

	// 2. List DeviceTypes via GET /device-type
	reqList := httptest.NewRequest(http.MethodGet, "/device-type?page=1&page_size=10", nil)
	wList := httptest.NewRecorder()
	routes.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list device type failed, code=%d, body=%s", wList.Code, wList.Body.String())
	}

	var listResp struct {
		Success bool               `json:"success"`
		Data    DeviceTypeListData `json:"data"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal listResp failed: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("unexpected list count: total=%d, items=%d", listResp.Data.Total, len(listResp.Data.Items))
	}
	if listResp.Data.Items[0].DeviceType != "robot-dog" || listResp.Data.Items[0].AgentName != "Agent-A" {
		t.Fatalf("unexpected item content: %+v", listResp.Data.Items[0])
	}

	// 3. Update DeviceType via POST /device-type/update
	updateBody := []byte(fmt.Sprintf(`{
		"id": %d,
		"device_type": "robot-dog-pro",
		"agent_config_id": %d
	}`, dtID, agent2.ID))
	reqUpdate := httptest.NewRequest(http.MethodPost, "/device-type/update", bytes.NewReader(updateBody))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	routes.ServeHTTP(wUpdate, reqUpdate)

	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update device type failed, code=%d, body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	found, err := db.FindDeviceTypeByID(ctx, dtID)
	if err != nil {
		t.Fatalf("find device type in db failed: %v", err)
	}
	if found.DeviceType != "robot-dog-pro" || found.AgentConfigID != agent2.ID {
		t.Errorf("unexpected updated fields in DB: %+v", found)
	}

	// 4. Delete DeviceType via POST /device-type/delete
	delBody := []byte(fmt.Sprintf(`{"id": %d}`, dtID))
	reqDel := httptest.NewRequest(http.MethodPost, "/device-type/delete", bytes.NewReader(delBody))
	reqDel.Header.Set("Content-Type", "application/json")
	wDel := httptest.NewRecorder()
	routes.ServeHTTP(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("delete device type failed, code=%d, body=%s", wDel.Code, wDel.Body.String())
	}

	_, err = db.FindDeviceTypeByID(ctx, dtID)
	if !errors.Is(err, database.ErrDeviceTypeNotFound) {
		t.Fatalf("expected ErrDeviceTypeNotFound after delete, got %v", err)
	}

	// 5. Batch Delete
	dtA := &database.DeviceType{DeviceType: "type-a", AgentConfigID: agent1.ID}
	dtB := &database.DeviceType{DeviceType: "type-b", AgentConfigID: agent2.ID}
	_ = db.CreateDeviceType(ctx, dtA)
	_ = db.CreateDeviceType(ctx, dtB)

	batchDelBody := []byte(fmt.Sprintf(`{"ids": [%d, %d]}`, dtA.ID, dtB.ID))
	reqBatchDel := httptest.NewRequest(http.MethodPost, "/device-type/batch-delete", bytes.NewReader(batchDelBody))
	reqBatchDel.Header.Set("Content-Type", "application/json")
	wBatchDel := httptest.NewRecorder()
	routes.ServeHTTP(wBatchDel, reqBatchDel)

	if wBatchDel.Code != http.StatusOK {
		t.Fatalf("batch delete device type failed, code=%d, body=%s", wBatchDel.Code, wBatchDel.Body.String())
	}

	_, finalCount, _ := db.ListDeviceTypes(ctx, database.DeviceTypeFilter{})
	if finalCount != 0 {
		t.Fatalf("expected 0 device types after batch delete, got %d", finalCount)
	}
}



