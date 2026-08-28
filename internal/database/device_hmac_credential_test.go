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
	testDeviceType := "robot-dog"

	creds := []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			DeviceType:        testDeviceType,
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
	if found.DeviceType != testDeviceType {
		t.Errorf("expected DeviceType %q, got %q", testDeviceType, found.DeviceType)
	}
	if found.HMACKeyCiphertext != testHexKey {
		t.Errorf("expected hex key %q, got %q", testHexKey, found.HMACKeyCiphertext)
	}
	if !found.IsAvailable() {
		t.Errorf("expected credential to be available")
	}

	// Test Update
	err = db.UpdateDeviceHmacCredential(ctx, found.ID, map[string]any{
		"credential_status": CredentialStatusBlocked,
		"device_type":       "robot-cat",
	})
	if err != nil {
		t.Fatalf("UpdateDeviceHmacCredential failed: %v", err)
	}

	updated, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("Find updated failed: %v", err)
	}
	if updated.CredentialStatus != CredentialStatusBlocked {
		t.Errorf("expected status %s, got %s", CredentialStatusBlocked, updated.CredentialStatus)
	}
	if updated.DeviceType != "robot-cat" {
		t.Errorf("expected device_type robot-cat, got %s", updated.DeviceType)
	}

	// Test List
	list, total, err := db.ListDeviceHmacCredentials(ctx, DeviceHmacCredentialFilter{
		SerialNumber: "test-sn",
	})
	if err != nil {
		t.Fatalf("ListDeviceHmacCredentials failed: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected total 1, got %d, len %d", total, len(list))
	}

	// Test Delete
	err = db.DeleteDeviceHmacCredential(ctx, found.ID)
	if err != nil {
		t.Fatalf("DeleteDeviceHmacCredential failed: %v", err)
	}

	_, err = db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound after delete, got %v", err)
	}
}

func TestDeviceHmacCredentialListAndBatchDelete(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	err := db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      "sn-single-1",
			HMACKeyCiphertext: "1111111111111111111111111111111111111111111111111111111111111111",
			DeviceType:        "type-a",
		},
		{
			SerialNumber:      "sn-single-2",
			HMACKeyCiphertext: "2222222222222222222222222222222222222222222222222222222222222222",
			DeviceType:        "type-b",
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	list, total, err := db.ListDeviceHmacCredentials(ctx, DeviceHmacCredentialFilter{
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListDeviceHmacCredentials failed: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("expected 2 items, got total=%d len=%d", total, len(list))
	}

	// Filter by device type
	listA, totalA, err := db.ListDeviceHmacCredentials(ctx, DeviceHmacCredentialFilter{
		DeviceType: "type-a",
	})
	if err != nil {
		t.Fatalf("ListDeviceHmacCredentials type-a failed: %v", err)
	}
	if totalA != 1 || len(listA) != 1 {
		t.Fatalf("expected 1 item of type-a, got %d", totalA)
	}

	// Batch delete
	ids := []uint64{list[0].ID, list[1].ID}
	err = db.BatchDeleteDeviceHmacCredentials(ctx, ids)
	if err != nil {
		t.Fatalf("BatchDeleteDeviceHmacCredentials failed: %v", err)
	}

	_, totalAfter, err := db.ListDeviceHmacCredentials(ctx, DeviceHmacCredentialFilter{})
	if err != nil {
		t.Fatalf("List after delete failed: %v", err)
	}
	if totalAfter != 0 {
		t.Fatalf("expected 0 items after batch delete, got %d", totalAfter)
	}
}

func TestBatchCreateDeviceHmacCredentials(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	creds := []*DeviceHmacCredential{
		{
			SerialNumber:      "batch-sn-1",
			DeviceType:        "custom-box",
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
	if c1.DeviceType != "custom-box" {
		t.Errorf("expected DeviceType %q, got %q", "custom-box", c1.DeviceType)
	}

	c2, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "batch-sn-2")
	if err != nil || c2.HMACKeyCiphertext != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("batch item 2 verification failed: %v, item: %+v", err, c2)
	}
	if c2.DeviceType != "default" {
		t.Errorf("expected default DeviceType %q, got %q", "default", c2.DeviceType)
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
