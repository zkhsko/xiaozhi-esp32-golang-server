package router

import (
	"bytes"
	"context"
	"encoding/json"
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

	reqBody := []byte(`{"count": 2}`)
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
	}
}
