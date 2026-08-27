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

func TestVerifyHMAC(t *testing.T) {
	testRawKey := []byte("01234567890123456789012345678901") // 32 bytes
	testHexKey := hex.EncodeToString(testRawKey)

	// 1. Direct hex key matching
	if !verifyHMAC(testHexKey, testHexKey, "", "") {
		t.Errorf("expected direct hex key match to succeed")
	}

	// 2. Challenge HMAC-SHA256 matching
	challenge := "random-challenge-123"
	macChallenge := hmac.New(sha256.New, testRawKey)
	macChallenge.Write([]byte(challenge))
	challengeHMAC := hex.EncodeToString(macChallenge.Sum(nil))

	if !verifyHMAC(testHexKey, challengeHMAC, challenge, "") {
		t.Errorf("expected challenge hmac match to succeed")
	}

	// 3. Code HMAC-SHA256 matching
	code := "654321"
	macCode := hmac.New(sha256.New, testRawKey)
	macCode.Write([]byte(code))
	codeHMAC := hex.EncodeToString(macCode.Sum(nil))

	if !verifyHMAC(testHexKey, codeHMAC, "", code) {
		t.Errorf("expected code hmac match to succeed")
	}

	// 4. Invalid HMAC mismatch
	if verifyHMAC(testHexKey, "badhmac", challenge, code) {
		t.Errorf("expected bad hmac to fail")
	}
}

func TestBindDeviceWithoutSN(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-manual-bind-001"
	testRawKey := []byte("01234567890123456789012345678901")
	testHexKey := hex.EncodeToString(testRawKey)

	err := db.CreateDeviceHmacCredential(context.Background(), &database.DeviceHmacCredential{
		SerialNumber:      testSN,
		AuthMethod:        database.AuthMethodManualCodeHMAC,
		HMACKeyCiphertext: testHexKey,
		CredentialStatus:  database.CredentialStatusEnabled,
	})
	if err != nil {
		t.Fatalf("CreateDeviceHmacCredential failed: %v", err)
	}

	// Create pending activation via otaHandler helper
	pending, err := otaHandler.createPendingActivation("", "AA:BB:CC:DD:EE:FF", "test-client")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
		SN:   testSN,
		HMAC: testHexKey,
	})

	req := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp BindDeviceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if !resp.Success || resp.SerialNumber != testSN {
		t.Fatalf("unexpected bind response: %+v", resp)
	}
}
