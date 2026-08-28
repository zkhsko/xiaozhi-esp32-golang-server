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
}
