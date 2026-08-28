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
	firstID := genResp.Items[0].ID

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
		ID:               firstID,
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
		ID: firstID,
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
	createdActID := initialAct.ID

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
		ID:               createdActID,
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
		ID: createdActID,
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
		Endpoint:         "wss://dashscope.aliyuncs.com/api-v1/ws",
		APIKey:           "sk-e2e-asr-key",
		Model:            "qwen-audio-3.0-asr-flash-streaming",
		Hotwords:         "小智,测试热词",
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
	if !asrCreateResp.Success || asrCreateResp.Data.ID == 0 || !asrCreateResp.Data.HasAPIKey {
		t.Fatalf("unexpected asr create resp: %+v", asrCreateResp)
	}
	createdASRID := asrCreateResp.Data.ID

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
	if asrListResp.Data.Total != 1 || len(asrListResp.Data.Items) != 1 {
		t.Fatalf("expected 1 ASR config item, got %d", asrListResp.Data.Total)
	}

	// 15. ASR 配置单条删除
	delASRBody, _ := json.Marshal(DeleteASRConfigRequest{ID: createdASRID})
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
}

