package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestOTAActivateWithHMAC(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)

	testSN := "sn-activate-test-1"
	testRawKey := []byte("01234567890123456789012345678901") // 32 bytes
	testHexKey := hex.EncodeToString(testRawKey)

	err := db.CreateDeviceHmacCredential(context.Background(), &database.DeviceHmacCredential{
		SerialNumber:      testSN,
		AuthMethod:        database.AuthMethodEfuseHMAC,
		HMACKeyCiphertext: testHexKey,
		CredentialStatus:  database.CredentialStatusEnabled,
	})
	if err != nil {
		t.Fatalf("CreateDeviceHmacCredential failed: %v", err)
	}

	challenge := "test-challenge-nonce-12345"
	mac := hmac.New(sha256.New, testRawKey)
	mac.Write([]byte(challenge))
	hmacHex := hex.EncodeToString(mac.Sum(nil))

	reqBody, _ := json.Marshal(ActivateRequest{
		Algorithm:    "hmac-sha256",
		SerialNumber: testSN,
		Challenge:    challenge,
		HMAC:         hmacHex,
	})

	req := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Device-Id", "11:22:33:44:55:66")
	req.Header.Set("Client-Id", "test-client-id")
	w := httptest.NewRecorder()

	otaHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != database.CredentialStatusActivated {
		t.Errorf("expected status %q, got %q", database.CredentialStatusActivated, cred.CredentialStatus)
	}
}
