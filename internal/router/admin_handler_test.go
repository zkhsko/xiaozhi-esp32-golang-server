package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
