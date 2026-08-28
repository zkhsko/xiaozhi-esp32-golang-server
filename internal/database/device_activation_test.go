package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestActivateDeviceBySerialNumber_InitialActivation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	testSN := "sn-initial-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-001"

	// Preset credential as enabled
	err := db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	act, err := db.ActivateDeviceBySerialNumber(ctx, testSN, testDeviceID, testClientID)
	if err != nil {
		t.Fatalf("ActivateDeviceBySerialNumber failed: %v", err)
	}
	if act.SerialNumber != testSN {
		t.Errorf("expected SN %q, got %q", testSN, act.SerialNumber)
	}
	if act.DeviceID != testDeviceID {
		t.Errorf("expected DeviceID %q, got %q", testDeviceID, act.DeviceID)
	}
	if act.ClientID != testClientID {
		t.Errorf("expected ClientID %q, got %q", testClientID, act.ClientID)
	}
	if act.ActivationStatus != ActivationStatusActive {
		t.Errorf("expected status %q, got %q", ActivationStatusActive, act.ActivationStatus)
	}

	// Credential status should transition from enabled to activated
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != CredentialStatusActivated {
		t.Errorf("expected credential status %q, got %q", CredentialStatusActivated, cred.CredentialStatus)
	}
}

func TestActivateDeviceBySerialNumber_ReactivationClearsOldBindingAndToken(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	testSN := "sn-reactivate-db-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-001"

	// 1. Setup initial activation, user binding, and access token
	err := db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	_, err = db.ActivateDeviceBySerialNumber(ctx, testSN, testDeviceID, testClientID)
	if err != nil {
		t.Fatalf("initial ActivateDeviceBySerialNumber failed: %v", err)
	}

	_, err = db.UpsertDeviceUserRef(ctx, testSN, 42)
	if err != nil {
		t.Fatalf("UpsertDeviceUserRef failed: %v", err)
	}

	oldToken := "old-token-abcdef1234567890abcdef1234567890"
	err = db.UpsertDeviceAccessToken(ctx, &DeviceAccessToken{
		SerialNumber: testSN,
		AccessToken:  oldToken,
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDeviceAccessToken failed: %v", err)
	}

	// Verify before reactivation that binding and token exist
	ref, err := db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if err != nil || ref.UserID != 42 {
		t.Fatalf("expected user binding 42, got ref: %+v, err: %v", ref, err)
	}
	tok, err := db.FindDeviceAccessTokenByAccessToken(ctx, oldToken)
	if err != nil || tok.AccessToken != oldToken {
		t.Fatalf("expected token found, got tok: %+v, err: %v", tok, err)
	}

	// 2. Reactivate with updated device_id / client_id
	newDeviceID := "AA:BB:CC:DD:EE:FF"
	newClientID := "client-002"
	act, err := db.ActivateDeviceBySerialNumber(ctx, testSN, newDeviceID, newClientID)
	if err != nil {
		t.Fatalf("reactivate ActivateDeviceBySerialNumber failed: %v", err)
	}
	if act.DeviceID != newDeviceID || act.ClientID != newClientID {
		t.Errorf("expected updated device/client IDs (%s, %s), got (%s, %s)",
			newDeviceID, newClientID, act.DeviceID, act.ClientID)
	}

	// 3. User binding must be deleted
	_, err = db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if !errors.Is(err, ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound after reactivation, got: %v", err)
	}

	// 4. Old access token must be deleted
	_, err = db.FindDeviceAccessTokenByAccessToken(ctx, oldToken)
	if !errors.Is(err, ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound for old token, got: %v", err)
	}
	_, err = db.FindDeviceAccessTokenBySerialNumber(ctx, testSN)
	if !errors.Is(err, ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound for SN token, got: %v", err)
	}
}

func TestActivateDeviceBySerialNumber_TransactionRollbackOnContextCancel(t *testing.T) {
	db := setupTestDB(t)

	testSN := "sn-rollback-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-001"

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	// Use canceled context to simulate failure during transaction
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = db.ActivateDeviceBySerialNumber(canceledCtx, testSN, testDeviceID, testClientID)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	// Verify rollback: no activation record created
	_, err = db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound after rollback, got: %v", err)
	}

	// Credential status remains enabled (not updated)
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != CredentialStatusEnabled {
		t.Errorf("expected status %q, got %q", CredentialStatusEnabled, cred.CredentialStatus)
	}
}

func TestBindDeviceWithSN_InitialSuccess(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	testSN := "sn-bind-db-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-bind-001"
	testToken := "token-bind-11112222333344445555666677778888"
	testUserID := uint64(101)

	// Preset credential as enabled
	err := db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	act, err := db.BindDeviceWithSN(ctx, testSN, testDeviceID, testClientID, testToken, testUserID)
	if err != nil {
		t.Fatalf("BindDeviceWithSN failed: %v", err)
	}

	// 1. Verify activation record
	if act.SerialNumber != testSN || act.DeviceID != testDeviceID || act.ClientID != testClientID {
		t.Errorf("unexpected activation: %+v", act)
	}
	if act.ActivationStatus != ActivationStatusActive {
		t.Errorf("expected status %q, got %q", ActivationStatusActive, act.ActivationStatus)
	}

	// 2. Verify user binding record
	ref, err := db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceUserRefBySerialNumber failed: %v", err)
	}
	if ref.UserID != testUserID {
		t.Errorf("expected user_id %d, got %d", testUserID, ref.UserID)
	}

	// 3. Verify credential status transitioned to activated
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != CredentialStatusActivated {
		t.Errorf("expected credential status %q, got %q", CredentialStatusActivated, cred.CredentialStatus)
	}

	// 4. Verify access token record
	tok, err := db.FindDeviceAccessTokenByAccessToken(ctx, testToken)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenByAccessToken failed: %v", err)
	}
	if tok.SerialNumber != testSN || tok.HasExposed {
		t.Errorf("unexpected access token record: %+v", tok)
	}
}

func TestBindDeviceWithSN_RebindUpdatesAllTables(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	testSN := "sn-rebind-db-001"
	oldDeviceID := "11:22:33:44:55:66"
	oldClientID := "client-old"
	oldToken := "old-token-11112222333344445555666677778888"
	oldUserID := uint64(10)

	// 1. Initial binding
	_, err := db.BindDeviceWithSN(ctx, testSN, oldDeviceID, oldClientID, oldToken, oldUserID)
	if err != nil {
		t.Fatalf("initial BindDeviceWithSN failed: %v", err)
	}

	// 2. Re-bind to a different user with new device/client/token
	newDeviceID := "AA:BB:CC:DD:EE:FF"
	newClientID := "client-new"
	newToken := "new-token-99998888777766665555444433332222"
	newUserID := uint64(20)

	act, err := db.BindDeviceWithSN(ctx, testSN, newDeviceID, newClientID, newToken, newUserID)
	if err != nil {
		t.Fatalf("rebind BindDeviceWithSN failed: %v", err)
	}

	// 3. Verify activation updated
	if act.DeviceID != newDeviceID || act.ClientID != newClientID {
		t.Errorf("expected updated device/client (%s, %s), got (%s, %s)",
			newDeviceID, newClientID, act.DeviceID, act.ClientID)
	}

	// 4. Verify user binding updated to new user
	ref, err := db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceUserRefBySerialNumber failed: %v", err)
	}
	if ref.UserID != newUserID {
		t.Errorf("expected user_id %d, got %d", newUserID, ref.UserID)
	}

	// 5. Verify old token no longer exists, new token is valid
	_, err = db.FindDeviceAccessTokenByAccessToken(ctx, oldToken)
	if !errors.Is(err, ErrAccessTokenNotFound) {
		t.Errorf("expected old token to not be found, got: %v", err)
	}

	tok, err := db.FindDeviceAccessTokenByAccessToken(ctx, newToken)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenByAccessToken for new token failed: %v", err)
	}
	if tok.SerialNumber != testSN || tok.HasExposed {
		t.Errorf("unexpected token record: %+v", tok)
	}
}

func TestBindDeviceWithSN_TransactionRollbackOnContextCancel(t *testing.T) {
	db := setupTestDB(t)

	testSN := "sn-rollback-bind-001"
	testDeviceID := "11:22:33:44:55:66"
	testClientID := "client-001"
	testToken := "token-rollback-1111222233334444"
	testUserID := uint64(99)

	err := db.BatchCreateDeviceHmacCredentials(context.Background(), []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = db.BindDeviceWithSN(canceledCtx, testSN, testDeviceID, testClientID, testToken, testUserID)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	// Verify complete rollback: no activation record, no user ref, no access token
	_, err = db.FindDeviceActivationBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound after rollback, got: %v", err)
	}

	_, err = db.FindDeviceUserRefBySerialNumber(context.Background(), testSN)
	if !errors.Is(err, ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound after rollback, got: %v", err)
	}

	_, err = db.FindDeviceAccessTokenByAccessToken(context.Background(), testToken)
	if !errors.Is(err, ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound after rollback, got: %v", err)
	}

	// Credential status remains enabled
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(context.Background(), testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != CredentialStatusEnabled {
		t.Errorf("expected credential status %q, got %q", CredentialStatusEnabled, cred.CredentialStatus)
	}
}

func TestBindDeviceWithSN_ValidationErrors(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Empty SN
	_, err := db.BindDeviceWithSN(ctx, "", "dev", "cli", "tok", 1)
	if !errors.Is(err, ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}

	// Empty Token
	_, err = db.BindDeviceWithSN(ctx, "sn", "dev", "cli", "", 1)
	if !errors.Is(err, ErrEmptyAccessToken) {
		t.Errorf("expected ErrEmptyAccessToken, got: %v", err)
	}

	// Zero UserID
	_, err = db.BindDeviceWithSN(ctx, "sn", "dev", "cli", "tok", 0)
	if !errors.Is(err, ErrEmptyUserID) {
		t.Errorf("expected ErrEmptyUserID, got: %v", err)
	}
}
