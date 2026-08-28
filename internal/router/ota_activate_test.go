package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/session"
)

func TestOTAActivateWithHMAC_Success(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)

	testSN := "sn-activate-test-1"
	testRawKey := []byte("01234567890123456789012345678901") // 32 bytes
	testHexKey := hex.EncodeToString(testRawKey)

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			HMACKeyCiphertext: testHexKey,
			CredentialStatus:  database.CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	// Create pending activation in OTA handler cache (simulating prior OTA check)
	pending, err := otaHandler.createPendingActivation(testSN, "11:22:33:44:55:66", "test-client-id")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	// Calculate HMAC over the challenge
	mac := hmac.New(sha256.New, testRawKey)
	mac.Write([]byte(pending.Challenge))
	hmacHex := hex.EncodeToString(mac.Sum(nil))

	reqBody, _ := json.Marshal(ActivateRequest{
		Algorithm:    "hmac-sha256",
		SerialNumber: testSN,
		Challenge:    pending.Challenge,
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

	// 1. Verify database activation record created
	act, err := db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceActivationBySerialNumber failed: %v", err)
	}
	if act.ActivationStatus != database.ActivationStatusActive {
		t.Errorf("expected activation_status %q, got %q", database.ActivationStatusActive, act.ActivationStatus)
	}
	if act.DeviceId != "11:22:33:44:55:66" {
		t.Errorf("expected DeviceId %q, got %q", "11:22:33:44:55:66", act.DeviceId)
	}

	// 2. Verify credential status updated to activated
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != database.CredentialStatusActivated {
		t.Errorf("expected status %q, got %q", database.CredentialStatusActivated, cred.CredentialStatus)
	}

	// 3. Verify Challenge and Code caches were cleared
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); ok {
		t.Errorf("expected challenge %q to be deleted from cache", pending.Challenge)
	}
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); ok {
		t.Errorf("expected code %q to be deleted from cache", pending.Code)
	}
}

func TestOTAActivateWithHMAC_Reactivation_InvalidatesOldTokenAndBinding(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{
		Server: config.ServerConfig{
			WebSocketURL: "ws://localhost:8080/xiaozhi/v1/",
		},
	}
	otaHandler := NewOTAHandler(cfg, db, nil)

	testSN := "sn-reactivate-001"
	testRawKey := []byte("01234567890123456789012345678901")
	testHexKey := hex.EncodeToString(testRawKey)

	ctx := context.Background()

	// 1. Initial setup: Credential + Activation + User Binding + Access Token
	err := db.BatchCreateDeviceHmacCredentials(ctx, []*database.DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			HMACKeyCiphertext: testHexKey,
			CredentialStatus:  database.CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	_, err = db.ActivateDeviceBySerialNumber(ctx, testSN, "old-dev-id", "old-client-id")
	if err != nil {
		t.Fatalf("ActivateDeviceBySerialNumber failed: %v", err)
	}

	_, err = db.UpsertDeviceUserRef(ctx, testSN, 100)
	if err != nil {
		t.Fatalf("UpsertDeviceUserRef failed: %v", err)
	}

	oldToken := "old-valid-token-11112222333344445555666677778888"
	err = db.UpsertDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: testSN,
		AccessToken:  oldToken,
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDeviceAccessToken failed: %v", err)
	}

	// Verify that before reactivation, the token is valid and WebSocket auth succeeds
	wsReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReq.Header.Set("Authorization", "Bearer "+oldToken)
	wsReq.Header.Set("Protocol-Version", "1")
	wsReq.Header.Set("Serial-Number", testSN)
	sn, err := session.AuthenticateUpgrade(wsReq, db, 0)
	if err != nil || sn != testSN {
		t.Fatalf("expected initial auth to succeed, got sn: %q, err: %v", sn, err)
	}

	// 2. Perform HMAC reactivation
	pending, err := otaHandler.createPendingActivation(testSN, "new-dev-id", "new-client-id")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	mac := hmac.New(sha256.New, testRawKey)
	mac.Write([]byte(pending.Challenge))
	hmacHex := hex.EncodeToString(mac.Sum(nil))

	reqBody, _ := json.Marshal(ActivateRequest{
		Algorithm:    "hmac-sha256",
		SerialNumber: testSN,
		Challenge:    pending.Challenge,
		HMAC:         hmacHex,
	})

	req := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Device-Id", "new-dev-id")
	req.Header.Set("Client-Id", "new-client-id")
	w := httptest.NewRecorder()

	otaHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 on reactivation, got %d, body: %s", w.Code, w.Body.String())
	}

	// 3. Verify user binding is deleted
	_, err = db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound after reactivation, got: %v", err)
	}

	// 4. Verify old Access Token is deleted
	_, err = db.FindDeviceAccessTokenByAccessToken(ctx, oldToken)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound for old token, got: %v", err)
	}

	// 5. Verify old Access Token can NO LONGER authenticate WebSocket upgrade
	wsReqAfter := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReqAfter.Header.Set("Authorization", "Bearer "+oldToken)
	wsReqAfter.Header.Set("Protocol-Version", "1")
	wsReqAfter.Header.Set("Serial-Number", testSN)
	_, authErr := session.AuthenticateUpgrade(wsReqAfter, db, 0)
	if authErr == nil {
		t.Fatalf("expected old token to fail WebSocket auth after reactivation, but succeeded")
	}
	if session.HTTPStatus(authErr) != http.StatusUnauthorized {
		t.Errorf("expected status 401 Unauthorized, got %d", session.HTTPStatus(authErr))
	}

	// 6. Verify Challenge cache was cleaned
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); ok {
		t.Errorf("expected challenge %q to be deleted after reactivation", pending.Challenge)
	}
}

func TestOTAActivateWithHMAC_DatabaseFailure_RollbackAndCacheRetained(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)

	testSN := "sn-failure-001"
	testRawKey := []byte("01234567890123456789012345678901")
	testHexKey := hex.EncodeToString(testRawKey)

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			HMACKeyCiphertext: testHexKey,
			CredentialStatus:  database.CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	pending, err := otaHandler.createPendingActivation(testSN, "11:22:33:44:55:66", "client-fail")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	mac := hmac.New(sha256.New, testRawKey)
	mac.Write([]byte(pending.Challenge))
	hmacHex := hex.EncodeToString(mac.Sum(nil))

	reqBody, _ := json.Marshal(ActivateRequest{
		Algorithm:    "hmac-sha256",
		SerialNumber: testSN,
		Challenge:    pending.Challenge,
		HMAC:         hmacHex,
	})

	// Inject failure: cancel context before handling
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(reqBody)).WithContext(canceledCtx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Device-Id", "11:22:33:44:55:66")
	w := httptest.NewRecorder()

	otaHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 on DB failure, got %d", w.Code)
	}

	// 1. Challenge cache MUST be retained for retry
	cachedPending, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge)
	if !ok {
		t.Fatalf("expected challenge %q to be retained in cache after failure", pending.Challenge)
	}
	if cachedPending.Code != pending.Code {
		t.Errorf("expected cached code %q, got %q", pending.Code, cachedPending.Code)
	}

	// 2. Database state must be clean (no activation record)
	_, err = db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound, got: %v", err)
	}

	// 3. Credential status remains enabled
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != database.CredentialStatusEnabled {
		t.Errorf("expected status %q, got %q", database.CredentialStatusEnabled, cred.CredentialStatus)
	}

	// 4. Retry with valid context must succeed using the SAME challenge
	retryReq := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(reqBody))
	retryReq.Header.Set("Content-Type", "application/json")
	retryReq.Header.Set("Device-Id", "11:22:33:44:55:66")
	retryW := httptest.NewRecorder()

	otaHandler.Routes().ServeHTTP(retryW, retryReq)

	if retryW.Code != http.StatusOK {
		t.Fatalf("expected retry status 200, got %d, body: %s", retryW.Code, retryW.Body.String())
	}

	// 5. After successful retry, cache should be cleared
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); ok {
		t.Errorf("expected challenge to be cleared after successful retry")
	}
}

func TestOTAActivateWithHMAC_InvalidHMAC_CacheRetained(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)

	testSN := "sn-invalid-hmac"
	testRawKey := []byte("01234567890123456789012345678901")
	testHexKey := hex.EncodeToString(testRawKey)

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			HMACKeyCiphertext: testHexKey,
			CredentialStatus:  database.CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	pending, err := otaHandler.createPendingActivation(testSN, "11:22:33:44:55:66", "client-1")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	// Send invalid HMAC
	reqBody, _ := json.Marshal(ActivateRequest{
		Algorithm:    "hmac-sha256",
		SerialNumber: testSN,
		Challenge:    pending.Challenge,
		HMAC:         "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	})

	req := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	otaHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", w.Code)
	}

	// Challenge must still be in cache
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); !ok {
		t.Errorf("expected challenge to be retained on HMAC verification failure")
	}

	// Activation record must not exist
	_, err = db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound, got: %v", err)
	}
}

func TestOTAActivateWithHMAC_CredentialNotFoundOrUnavailable(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)

	// Case 1: SN not in DB
	pending, _ := otaHandler.createPendingActivation("sn-not-found", "11:22:33:44:55:66", "client-1")
	reqBody, _ := json.Marshal(ActivateRequest{
		Algorithm:    "hmac-sha256",
		SerialNumber: "sn-not-found",
		Challenge:    pending.Challenge,
		HMAC:         "abcdef",
	})

	req := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	otaHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 for not found SN, got %d", w.Code)
	}

	// Challenge retained
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); !ok {
		t.Errorf("expected challenge to be retained on not found credential")
	}
}
