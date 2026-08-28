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

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/session"
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

func TestBindDeviceWithSN_Success_AndWebSocketAuth(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-with-sn-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-sn-001"

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  database.CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	// 1. Pending activation with SerialNumber (simulating OTA phase)
	pending, err := otaHandler.createPendingActivation(testSN, testDeviceID, testClientID)
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
	})

	req := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// 2. Validate HTTP response contract
	var resp BindDeviceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected success true")
	}
	if resp.Message != "device bound successfully" {
		t.Errorf("expected message %q, got %q", "device bound successfully", resp.Message)
	}
	if resp.SerialNumber != testSN {
		t.Errorf("expected SerialNumber %q, got %q", testSN, resp.SerialNumber)
	}
	if resp.DeviceID != testDeviceID {
		t.Errorf("expected DeviceID %q, got %q", testDeviceID, resp.DeviceID)
	}
	if resp.ClientID != testClientID {
		t.Errorf("expected ClientID %q, got %q", testClientID, resp.ClientID)
	}
	if resp.UserID != MockCurrentUserID {
		t.Errorf("expected UserID %d, got %d", MockCurrentUserID, resp.UserID)
	}

	// 3. Verify database state across all tables
	act, err := db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceActivationBySerialNumber failed: %v", err)
	}
	if act.ActivationStatus != database.ActivationStatusActive {
		t.Errorf("expected activation_status active, got %q", act.ActivationStatus)
	}

	ref, err := db.FindDeviceUserRefBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceUserRefBySerialNumber failed: %v", err)
	}
	if ref.UserID != MockCurrentUserID {
		t.Errorf("expected bound user_id %d, got %d", MockCurrentUserID, ref.UserID)
	}

	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != database.CredentialStatusActivated {
		t.Errorf("expected credential_status activated, got %q", cred.CredentialStatus)
	}

	tok, err := db.FindDeviceAccessTokenBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenBySerialNumber failed: %v", err)
	}
	if tok.AccessToken == "" || tok.HasExposed {
		t.Errorf("unexpected token record: %+v", tok)
	}

	// 4. Verify cache is cleaned
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); ok {
		t.Errorf("expected code %q to be deleted from cache", pending.Code)
	}
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); ok {
		t.Errorf("expected challenge %q to be deleted from cache", pending.Challenge)
	}

	// 5. Verify the newly generated token can be used for WebSocket authentication
	wsReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	wsReq.Header.Set("Protocol-Version", "1")
	wsReq.Header.Set("Serial-Number", testSN)
	wsReq.Header.Set("Device-Id", testDeviceID)
	sn, err := session.AuthenticateUpgrade(wsReq, db, 0)
	if err != nil {
		t.Fatalf("WebSocket AuthenticateUpgrade failed with newly bound token: %v", err)
	}
	if sn != testSN {
		t.Errorf("unexpected sn: %q, expected: %q", sn, testSN)
	}
}

func TestBindDeviceWithSN_DuplicateRequest_ReturnsBadRequest(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-dup-test-001"
	pending, err := otaHandler.createPendingActivation(testSN, "11:22:33:44:55:66", "client-dup")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
	})

	// First binding request succeeds
	req1 := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	userHandler.Routes().ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected first request status 200, got %d", w1.Code)
	}

	// Duplicate binding request with same code must fail (code consumed/expired)
	req2 := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	userHandler.Routes().ServeHTTP(w2, req2)

	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate request status 400, got %d, body: %s", w2.Code, w2.Body.String())
	}
}

func TestBindDeviceWithSN_DatabaseFailure_RollbackAndCacheRetained_AndRetrySuccess(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-fail-retry-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-fail-retry"

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  database.CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	pending, err := otaHandler.createPendingActivation(testSN, testDeviceID, testClientID)
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
	})

	// 1. Inject failure using canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	failReq := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody)).WithContext(canceledCtx)
	failReq.Header.Set("Content-Type", "application/json")
	failW := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(failW, failReq)

	if failW.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 on DB failure, got %d", failW.Code)
	}

	// 2. Verify complete database rollback
	_, err = db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound after DB failure, got: %v", err)
	}
	_, err = db.FindDeviceUserRefBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound after DB failure, got: %v", err)
	}
	_, err = db.FindDeviceAccessTokenBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound after DB failure, got: %v", err)
	}
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != database.CredentialStatusEnabled {
		t.Errorf("expected credential to remain enabled, got %q", cred.CredentialStatus)
	}

	// 3. Verify cache is RETAINED for retry
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); !ok {
		t.Fatalf("expected code %q to be retained in cache after failure", pending.Code)
	}
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); !ok {
		t.Fatalf("expected challenge %q to be retained in cache after failure", pending.Challenge)
	}

	// 4. Retry with valid context using the SAME code
	retryReq := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	retryReq.Header.Set("Content-Type", "application/json")
	retryW := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(retryW, retryReq)

	if retryW.Code != http.StatusOK {
		t.Fatalf("expected retry status 200, got %d, body: %s", retryW.Code, retryW.Body.String())
	}

	// 5. Verify successful retry committed all DB state and cleaned cache
	act, err := db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if err != nil || act.ActivationStatus != database.ActivationStatusActive {
		t.Fatalf("expected active activation after retry, got act: %+v, err: %v", act, err)
	}
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); ok {
		t.Errorf("expected code to be cleared from cache after successful retry")
	}
}

func TestBindDeviceWithSN_Rebind_InvalidatesOldToken(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-rebind-ws-001"
	ctx := context.Background()

	// 1. Setup initial binding to user 99 with oldToken
	oldToken := "old-token-11112222333344445555666677778888"
	_, err := db.BindDeviceWithSN(ctx, testSN, "old-dev", "old-cli", oldToken, 99)
	if err != nil {
		t.Fatalf("initial BindDeviceWithSN failed: %v", err)
	}

	// Verify oldToken authenticates
	wsReqOld := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReqOld.Header.Set("Authorization", "Bearer "+oldToken)
	wsReqOld.Header.Set("Protocol-Version", "1")
	wsReqOld.Header.Set("Serial-Number", testSN)
	sn, err := session.AuthenticateUpgrade(wsReqOld, db, 0)
	if err != nil || sn != testSN {
		t.Fatalf("expected old token auth to succeed, got sn: %q, err: %v", sn, err)
	}

	// 2. Re-bind via User Handler
	pending, err := otaHandler.createPendingActivation(testSN, "new-dev", "new-cli")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
	})

	req := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 on rebind, got %d, body: %s", w.Code, w.Body.String())
	}

	// 3. Verify oldToken CANNOT authenticate
	_, authErr := session.AuthenticateUpgrade(wsReqOld, db, 0)
	if authErr == nil {
		t.Fatalf("expected old token auth to fail after rebind, but succeeded")
	}
	if session.HTTPStatus(authErr) != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", session.HTTPStatus(authErr))
	}

	// 4. Verify new token can authenticate
	newTok, err := db.FindDeviceAccessTokenBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenBySerialNumber failed: %v", err)
	}
	wsReqNew := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReqNew.Header.Set("Authorization", "Bearer "+newTok.AccessToken)
	wsReqNew.Header.Set("Protocol-Version", "1")
	wsReqNew.Header.Set("Serial-Number", testSN)
	newSN, err := session.AuthenticateUpgrade(wsReqNew, db, 0)
	if err != nil || newSN != testSN {
		t.Fatalf("expected new token auth to succeed, got sn: %q, err: %v", newSN, err)
	}

	// 5. Verify user binding updated to MockCurrentUserID
	ref, err := db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if err != nil || ref.UserID != MockCurrentUserID {
		t.Errorf("expected user_id %d, got ref: %+v, err: %v", MockCurrentUserID, ref, err)
	}
}

func TestBindDeviceWithoutSN_Success_AndWebSocketAuth(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-manual-bind-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-manual-001"
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

	// 1. Pending activation without SerialNumber (simulating Legacy / No-SN device OTA)
	pending, err := otaHandler.createPendingActivation("", testDeviceID, testClientID)
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

	// 2. Validate HTTP response contract
	var resp BindDeviceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected success true")
	}
	if resp.Message != "device bound successfully" {
		t.Errorf("expected message %q, got %q", "device bound successfully", resp.Message)
	}
	if resp.SerialNumber != testSN {
		t.Errorf("expected SerialNumber %q, got %q", testSN, resp.SerialNumber)
	}
	if resp.DeviceID != testDeviceID {
		t.Errorf("expected DeviceID %q, got %q", testDeviceID, resp.DeviceID)
	}
	if resp.ClientID != testClientID {
		t.Errorf("expected ClientID %q, got %q", testClientID, resp.ClientID)
	}
	if resp.UserID != MockCurrentUserID {
		t.Errorf("expected UserID %d, got %d", MockCurrentUserID, resp.UserID)
	}

	// 3. Verify database state across all tables
	act, err := db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceActivationBySerialNumber failed: %v", err)
	}
	if act.ActivationStatus != database.ActivationStatusActive {
		t.Errorf("expected activation_status active, got %q", act.ActivationStatus)
	}

	ref, err := db.FindDeviceUserRefBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceUserRefBySerialNumber failed: %v", err)
	}
	if ref.UserID != MockCurrentUserID {
		t.Errorf("expected bound user_id %d, got %d", MockCurrentUserID, ref.UserID)
	}

	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != database.CredentialStatusActivated {
		t.Errorf("expected credential_status activated, got %q", cred.CredentialStatus)
	}

	tok, err := db.FindDeviceAccessTokenBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenBySerialNumber failed: %v", err)
	}
	if tok.AccessToken == "" || tok.HasExposed {
		t.Errorf("unexpected token record: %+v", tok)
	}

	// 4. Verify cache is cleaned
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); ok {
		t.Errorf("expected code %q to be deleted from cache", pending.Code)
	}
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); ok {
		t.Errorf("expected challenge %q to be deleted from cache", pending.Challenge)
	}

	// 5. Verify the newly generated token can be used for WebSocket authentication
	wsReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	wsReq.Header.Set("Protocol-Version", "1")
	wsReq.Header.Set("Serial-Number", testSN)
	wsReq.Header.Set("Device-Id", testDeviceID)
	sn, err := session.AuthenticateUpgrade(wsReq, db, 0)
	if err != nil {
		t.Fatalf("WebSocket AuthenticateUpgrade failed with newly bound token: %v", err)
	}
	if sn != testSN {
		t.Errorf("unexpected sn: %q, expected: %q", sn, testSN)
	}
}

func TestBindDeviceWithoutSN_HMACVerificationVariants(t *testing.T) {
	keyHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	keyBytes, _ := hex.DecodeString(keyHex)

	tests := []struct {
		name     string
		calcHMAC func(challenge, code string) string
	}{
		{
			name: "DirectHexKey",
			calcHMAC: func(challenge, code string) string {
				return keyHex
			},
		},
		{
			name: "HMACSHA256OverChallenge",
			calcHMAC: func(challenge, code string) string {
				mac := hmac.New(sha256.New, keyBytes)
				mac.Write([]byte(challenge))
				return hex.EncodeToString(mac.Sum(nil))
			},
		},
		{
			name: "HMACSHA256OverCode",
			calcHMAC: func(challenge, code string) string {
				mac := hmac.New(sha256.New, keyBytes)
				mac.Write([]byte(code))
				return hex.EncodeToString(mac.Sum(nil))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestRouterDB(t)
			cfg := &config.Config{}
			otaHandler := NewOTAHandler(cfg, db, nil)
			userHandler := NewUserHandler(cfg, db, otaHandler, nil)

			testSN := "sn-variant-" + tc.name
			err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
				{
					SerialNumber:      testSN,
					AuthMethod:        database.AuthMethodEfuseHMAC,
					HMACKeyCiphertext: keyHex,
					CredentialStatus:  database.CredentialStatusEnabled,
				},
			})
			if err != nil {
				t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
			}

			pending, err := otaHandler.createPendingActivation("", "dev-id", "cli-id")
			if err != nil {
				t.Fatalf("createPendingActivation failed: %v", err)
			}

			calculatedHMAC := tc.calcHMAC(pending.Challenge, pending.Code)

			bindReqBody, _ := json.Marshal(BindDeviceRequest{
				Code: pending.Code,
				SN:   testSN,
				HMAC: calculatedHMAC,
			})

			req := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			userHandler.Routes().ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
			}

			cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
			if err != nil || cred.CredentialStatus != database.CredentialStatusActivated {
				t.Errorf("expected credential activated, got %+v, err: %v", cred, err)
			}
		})
	}
}

func TestBindDeviceWithoutSN_InvalidHMAC_FailsAndCacheRetained(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-invalid-hmac-001"
	testHexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

	pending, err := otaHandler.createPendingActivation("", "11:22:33:44:55:66", "cli-001")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	// 1. Send invalid HMAC
	badReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
		SN:   testSN,
		HMAC: "invalid_hmac_string_1234567890",
	})

	req := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(badReqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 Forbidden on invalid HMAC, got %d, body: %s", w.Code, w.Body.String())
	}

	// 2. Verify no partial database state
	_, err = db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound, got: %v", err)
	}
	_, err = db.FindDeviceUserRefBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound, got: %v", err)
	}
	_, err = db.FindDeviceAccessTokenBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound, got: %v", err)
	}
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil || cred.CredentialStatus != database.CredentialStatusEnabled {
		t.Errorf("expected credential status enabled, got %+v, err: %v", cred, err)
	}

	// 3. Verify cache is retained
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); !ok {
		t.Fatalf("expected code %q to be retained in cache after HMAC failure", pending.Code)
	}

	// 4. Retry with correct HMAC succeeds
	goodReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
		SN:   testSN,
		HMAC: testHexKey,
	})
	retryReq := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(goodReqBody))
	retryReq.Header.Set("Content-Type", "application/json")
	retryW := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(retryW, retryReq)

	if retryW.Code != http.StatusOK {
		t.Fatalf("expected retry status 200, got %d, body: %s", retryW.Code, retryW.Body.String())
	}
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); ok {
		t.Errorf("expected code to be cleared from cache after successful retry")
	}
}

func TestBindDeviceWithoutSN_CredentialNotFound_FailsAndCacheRetained(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	pending, err := otaHandler.createPendingActivation("", "11:22:33:44:55:66", "cli-001")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
		SN:   "non-existent-sn",
		HMAC: "some-hmac",
	})

	req := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 Forbidden for non-existent credential, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify cache is retained
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); !ok {
		t.Errorf("expected code %q to be retained in cache", pending.Code)
	}
}

func TestBindDeviceWithoutSN_CredentialUnavailable_FailsAndCacheRetained(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-blocked-001"
	testHexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*database.DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        database.AuthMethodEfuseHMAC,
			HMACKeyCiphertext: testHexKey,
			CredentialStatus:  "blocked",
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	pending, err := otaHandler.createPendingActivation("", "11:22:33:44:55:66", "cli-001")
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

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 Forbidden for blocked credential, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify cache is retained
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); !ok {
		t.Errorf("expected code %q to be retained in cache", pending.Code)
	}
}

func TestBindDeviceWithoutSN_MissingRequiredFields_BadRequest(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	pending, err := otaHandler.createPendingActivation("", "11:22:33:44:55:66", "cli-001")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	tests := []struct {
		name string
		req  BindDeviceRequest
	}{
		{
			name: "MissingSNAndHMAC",
			req:  BindDeviceRequest{Code: pending.Code},
		},
		{
			name: "MissingHMAC",
			req:  BindDeviceRequest{Code: pending.Code, SN: "some-sn"},
		},
		{
			name: "MissingSN",
			req:  BindDeviceRequest{Code: pending.Code, HMAC: "some-hmac"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bindReqBody, _ := json.Marshal(tc.req)
			req := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			userHandler.Routes().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400 Bad Request, got %d, body: %s", w.Code, w.Body.String())
			}

			// Verify cache is retained
			if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); !ok {
				t.Errorf("expected code %q to be retained in cache", pending.Code)
			}
		})
	}
}

func TestBindDeviceWithoutSN_DuplicateRequest_ReturnsBadRequest(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-dup-no-sn-001"
	testHexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

	pending, err := otaHandler.createPendingActivation("", "11:22:33:44:55:66", "client-dup")
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
		SN:   testSN,
		HMAC: testHexKey,
	})

	// First binding request succeeds
	req1 := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	userHandler.Routes().ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected first request status 200, got %d", w1.Code)
	}

	// Duplicate binding request with same code must fail (code consumed/expired)
	req2 := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	userHandler.Routes().ServeHTTP(w2, req2)

	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate request status 400, got %d, body: %s", w2.Code, w2.Body.String())
	}
}

func TestBindDeviceWithoutSN_DatabaseFailure_RollbackAndCacheRetained_AndRetrySuccess(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-fail-retry-no-sn-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-fail-retry-no-sn"
	testHexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

	pending, err := otaHandler.createPendingActivation("", testDeviceID, testClientID)
	if err != nil {
		t.Fatalf("createPendingActivation failed: %v", err)
	}

	bindReqBody, _ := json.Marshal(BindDeviceRequest{
		Code: pending.Code,
		SN:   testSN,
		HMAC: testHexKey,
	})

	// 1. Inject failure using canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	failReq := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody)).WithContext(canceledCtx)
	failReq.Header.Set("Content-Type", "application/json")
	failW := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(failW, failReq)

	if failW.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 on DB failure, got %d", failW.Code)
	}

	// 2. Verify complete database rollback
	_, err = db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound after DB failure, got: %v", err)
	}
	_, err = db.FindDeviceUserRefBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound after DB failure, got: %v", err)
	}
	_, err = db.FindDeviceAccessTokenBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound after DB failure, got: %v", err)
	}
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != database.CredentialStatusEnabled {
		t.Errorf("expected credential to remain enabled, got %q", cred.CredentialStatus)
	}

	// 3. Verify cache is RETAINED for retry
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); !ok {
		t.Fatalf("expected code %q to be retained in cache after failure", pending.Code)
	}
	if _, ok := otaHandler.FindPendingActivationByChallenge(pending.Challenge); !ok {
		t.Fatalf("expected challenge %q to be retained in cache after failure", pending.Challenge)
	}

	// 4. Retry with valid context using the SAME code
	retryReq := httptest.NewRequest(http.MethodPost, "/device/bind", bytes.NewReader(bindReqBody))
	retryReq.Header.Set("Content-Type", "application/json")
	retryW := httptest.NewRecorder()

	userHandler.Routes().ServeHTTP(retryW, retryReq)

	if retryW.Code != http.StatusOK {
		t.Fatalf("expected retry status 200, got %d, body: %s", retryW.Code, retryW.Body.String())
	}

	// 5. Verify successful retry committed all DB state and cleaned cache
	act, err := db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if err != nil || act.ActivationStatus != database.ActivationStatusActive {
		t.Fatalf("expected active activation after retry, got act: %+v, err: %v", act, err)
	}
	if _, ok := otaHandler.FindPendingActivationByCode(pending.Code); ok {
		t.Errorf("expected code to be cleared from cache after successful retry")
	}
}

func TestBindDeviceWithoutSN_Rebind_InvalidatesOldToken(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	otaHandler := NewOTAHandler(cfg, db, nil)
	userHandler := NewUserHandler(cfg, db, otaHandler, nil)

	testSN := "sn-rebind-no-sn-001"
	testHexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ctx := context.Background()

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

	// 1. Setup initial binding to user 99 with oldToken
	oldToken := "old-token-11112222333344445555666677778888"
	_, err = db.BindDeviceWithSN(ctx, testSN, "old-dev", "old-cli", oldToken, 99)
	if err != nil {
		t.Fatalf("initial BindDeviceWithSN failed: %v", err)
	}

	// Verify oldToken authenticates
	wsReqOld := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReqOld.Header.Set("Authorization", "Bearer "+oldToken)
	wsReqOld.Header.Set("Protocol-Version", "1")
	wsReqOld.Header.Set("Serial-Number", testSN)
	sn, err := session.AuthenticateUpgrade(wsReqOld, db, 0)
	if err != nil || sn != testSN {
		t.Fatalf("expected old token auth to succeed, got sn: %q, err: %v", sn, err)
	}

	// 2. Re-bind via User Handler (device without SN in OTA stage)
	pending, err := otaHandler.createPendingActivation("", "new-dev", "new-cli")
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
		t.Fatalf("expected status 200 on rebind, got %d, body: %s", w.Code, w.Body.String())
	}

	// 3. Verify oldToken CANNOT authenticate
	_, authErr := session.AuthenticateUpgrade(wsReqOld, db, 0)
	if authErr == nil {
		t.Fatalf("expected old token auth to fail after rebind, but succeeded")
	}
	if session.HTTPStatus(authErr) != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", session.HTTPStatus(authErr))
	}

	// 4. Verify new token can authenticate
	newTok, err := db.FindDeviceAccessTokenBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenBySerialNumber failed: %v", err)
	}
	wsReqNew := httptest.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	wsReqNew.Header.Set("Authorization", "Bearer "+newTok.AccessToken)
	wsReqNew.Header.Set("Protocol-Version", "1")
	wsReqNew.Header.Set("Serial-Number", testSN)
	newSN, err := session.AuthenticateUpgrade(wsReqNew, db, 0)
	if err != nil || newSN != testSN {
		t.Fatalf("expected new token auth to succeed, got sn: %q, err: %v", newSN, err)
	}

	// 5. Verify user binding updated to MockCurrentUserID
	ref, err := db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if err != nil || ref.UserID != MockCurrentUserID {
		t.Errorf("expected user_id %d, got ref: %+v, err: %v", MockCurrentUserID, ref, err)
	}
}
