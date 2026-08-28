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
