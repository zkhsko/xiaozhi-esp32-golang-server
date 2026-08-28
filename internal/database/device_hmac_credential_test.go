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

	creds := []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			HMACKeyCiphertext: testHexKey,
			CredentialStatus:  CredentialStatusEnabled,
		},
	}

	if err := db.BatchCreateDeviceHmacCredentials(ctx, creds); err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
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
	err := db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      "",
			HMACKeyCiphertext: "some-key",
		},
	})
	if !errors.Is(err, ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got %v", err)
	}

	// Empty Key
	err = db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      "sn-empty-key",
			HMACKeyCiphertext: "",
		},
	})
	if !errors.Is(err, ErrEmptyHMACKeyCiphertext) {
		t.Errorf("expected ErrEmptyHMACKeyCiphertext, got %v", err)
	}

	// Nil credential in slice
	err = db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{nil})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("expected ErrInvalidCredential, got %v", err)
	}
}
