package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
)

func setupTestDB(t *testing.T) *Database {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := config.DatabaseConfig{
		Driver:       "sqlite",
		DSN:          "file::memory:?cache=shared",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func TestDeviceHmacCredentialCRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	testSN := "test-sn-001"
	testHexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	cred := &DeviceHmacCredential{
		SerialNumber:      testSN,
		AuthMethod:        AuthMethodEfuseHMAC,
		HMACKeyCiphertext: testHexKey,
		CredentialStatus:  CredentialStatusEnabled,
	}

	if err := db.CreateDeviceHmacCredential(ctx, cred); err != nil {
		t.Fatalf("CreateDeviceHmacCredential failed: %v", err)
	}

	found, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if found.SerialNumber != testSN {
		t.Errorf("expected SN %q, got %q", testSN, found.SerialNumber)
	}
	if found.HMACKeyCiphertext != testHexKey {
		t.Errorf("expected hex key %q, got %q", testHexKey, found.HMACKeyCiphertext)
	}
	if !found.IsAvailable() {
		t.Errorf("expected credential to be available")
	}

	// Test find by ID
	foundByID, err := db.FindDeviceHmacCredentialByID(ctx, found.ID)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialByID failed: %v", err)
	}
	if foundByID.SerialNumber != testSN {
		t.Errorf("expected SN %q, got %q", testSN, foundByID.SerialNumber)
	}

	// Update status
	if err := db.UpdateDeviceHmacCredentialStatus(ctx, testSN, CredentialStatusActivated); err != nil {
		t.Fatalf("UpdateDeviceHmacCredentialStatus failed: %v", err)
	}
	foundUpdated, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber after update failed: %v", err)
	}
	if foundUpdated.CredentialStatus != CredentialStatusActivated {
		t.Errorf("expected status %q, got %q", CredentialStatusActivated, foundUpdated.CredentialStatus)
	}

	// Test Upsert existing
	upsertHexKey := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	upsertCred := &DeviceHmacCredential{
		SerialNumber:      testSN,
		HMACKeyCiphertext: upsertHexKey,
	}
	if err := db.UpsertDeviceHmacCredential(ctx, upsertCred); err != nil {
		t.Fatalf("UpsertDeviceHmacCredential failed: %v", err)
	}
	foundUpserted, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber after upsert failed: %v", err)
	}
	if foundUpserted.HMACKeyCiphertext != upsertHexKey {
		t.Errorf("expected updated hex key %q, got %q", upsertHexKey, foundUpserted.HMACKeyCiphertext)
	}

	// Test Upsert new
	newSN := "test-sn-002"
	newCred := &DeviceHmacCredential{
		SerialNumber:      newSN,
		HMACKeyCiphertext: testHexKey,
	}
	if err := db.UpsertDeviceHmacCredential(ctx, newCred); err != nil {
		t.Fatalf("UpsertDeviceHmacCredential new failed: %v", err)
	}
	foundNew, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, newSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber after upsert new failed: %v", err)
	}
	if foundNew.SerialNumber != newSN || foundNew.HMACKeyCiphertext != testHexKey {
		t.Errorf("unexpected upsert new result: %+v", foundNew)
	}
}

func TestBatchCreateDeviceHmacCredentials(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	creds := []*DeviceHmacCredential{
		{
			SerialNumber:      "batch-sn-1",
			HMACKeyCiphertext: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			SerialNumber:      "batch-sn-2",
			HMACKeyCiphertext: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}

	if err := db.BatchCreateDeviceHmacCredentials(ctx, creds); err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	c1, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "batch-sn-1")
	if err != nil || c1.HMACKeyCiphertext != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("batch item 1 verification failed: %v, item: %+v", err, c1)
	}

	c2, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "batch-sn-2")
	if err != nil || c2.HMACKeyCiphertext != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("batch item 2 verification failed: %v, item: %+v", err, c2)
	}
}

func TestDeviceHmacCredentialValidation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Empty SN
	err := db.CreateDeviceHmacCredential(ctx, &DeviceHmacCredential{
		SerialNumber:      "",
		HMACKeyCiphertext: "some-key",
	})
	if !errors.Is(err, ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got %v", err)
	}

	// Empty Key
	err = db.CreateDeviceHmacCredential(ctx, &DeviceHmacCredential{
		SerialNumber:      "sn-empty-key",
		HMACKeyCiphertext: "",
	})
	if !errors.Is(err, ErrEmptyHMACKeyCiphertext) {
		t.Errorf("expected ErrEmptyHMACKeyCiphertext, got %v", err)
	}

	// ValidateActivationInput
	if err := ValidateActivationInput("", "key", "123456"); err == nil {
		t.Errorf("expected error for empty SN")
	}
	if err := ValidateActivationInput("sn", "", "123456"); err == nil {
		t.Errorf("expected error for empty key")
	}
	if err := ValidateActivationInput("sn", "key", ""); err == nil {
		t.Errorf("expected error for empty code")
	}
	if err := ValidateActivationInput("sn", "key", "123456"); err != nil {
		t.Errorf("expected valid input, got %v", err)
	}
}
