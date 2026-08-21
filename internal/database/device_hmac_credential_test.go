package database_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

// setupTestDB 帮助函数：创建临时 SQLite 数据库并完成初始化和迁移。
func setupTestDB(t *testing.T) *database.Database {
	t.Helper()

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "credential_test.db")
	dsn := "file:" + dbPath + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"

	cfg := config.DatabaseConfig{
		Driver:                "sqlite",
		MaxOpenConns:          1,
		MaxIdleConns:          1,
		ConnectionMaxLifetime: 0,
		ConnectionMaxIdleTime: 0,
		PingTimeout:           3 * time.Second,
		DSN:                   dsn,
	}

	db, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close test database: %v", err)
		}
	})

	return db
}

func TestDeviceHmacCredential_WithSerialNumber_ActivationQuery(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-ESP32-HARDWARE-001"
	hmacKeyCiphertext := []byte("efuse-secret-challenge-key-001-ciphertext")

	record := &database.DeviceHmacCredential{
		SerialNumber:      sn,
		AuthMethod:        database.AuthMethodEfuseHMAC,
		HMACKeyCiphertext: hmacKeyCiphertext,
		CredentialStatus:  database.CredentialStatusActivated,
	}

	if err := db.CreateDeviceHmacCredential(ctx, record); err != nil {
		t.Fatalf("failed to create device hmac credential with serial_number: %v", err)
	}

	if record.ID == 0 {
		t.Fatalf("expected auto-incremented ID > 0, got %d", record.ID)
	}

	// 1. 精确查询
	found, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "SN-ESP32-HARDWARE-001")
	if err != nil {
		t.Fatalf("failed to find device hmac credential by serial number: %v", err)
	}

	if found.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, found.ID)
	}
	if found.SerialNumber != sn {
		t.Errorf("expected serial_number %q, got %q", sn, found.SerialNumber)
	}
	if found.AuthMethod != database.AuthMethodEfuseHMAC {
		t.Errorf("expected auth_method %q, got %q", database.AuthMethodEfuseHMAC, found.AuthMethod)
	}
	if !bytes.Equal(found.HMACKeyCiphertext, hmacKeyCiphertext) {
		t.Errorf("expected hmac_key_ciphertext %v, got %v", hmacKeyCiphertext, found.HMACKeyCiphertext)
	}
	if found.CredentialStatus != database.CredentialStatusActivated {
		t.Errorf("expected credential_status %q, got %q", database.CredentialStatusActivated, found.CredentialStatus)
	}
	if found.CreatedAt.IsZero() || found.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps to be populated, got created_at=%v, updated_at=%v", found.CreatedAt, found.UpdatedAt)
	}

	// 2. 带首尾空格查询（自动 Trim）
	foundTrimmed, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "  SN-ESP32-HARDWARE-001  ")
	if err != nil {
		t.Fatalf("failed to find device hmac credential with whitespace: %v", err)
	}
	if foundTrimmed.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundTrimmed.ID)
	}
}

func TestDeviceHmacCredential_ManualActivation_WithoutSerialNumber(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "L-7K3M9Q2X"
	hmacKeyCiphertext := []byte("USER-INPUT-HMAC-KEY-999-CIPHER")

	record := &database.DeviceHmacCredential{
		SerialNumber:      sn,
		AuthMethod:        database.AuthMethodManualCodeHMAC,
		HMACKeyCiphertext: hmacKeyCiphertext,
		CredentialStatus:  database.CredentialStatusActivated,
	}

	// 1. 无序列号设备输入 serial_number + hmac + code 激活录入
	if err := db.UpsertDeviceHmacCredential(ctx, record); err != nil {
		t.Fatalf("failed to upsert device hmac credential: %v", err)
	}

	found, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find device hmac credential: %v", err)
	}
	if found.SerialNumber != sn || found.AuthMethod != database.AuthMethodManualCodeHMAC {
		t.Errorf("credential mismatch: %+v", found)
	}
	if !found.IsAvailable() {
		t.Errorf("expected credential to be available")
	}

	// 2. 状态更新为 blocked 后 upsert 拦截
	if err := db.UpdateDeviceHmacCredentialStatus(ctx, sn, database.CredentialStatusBlocked); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	err = db.UpsertDeviceHmacCredential(ctx, record)
	if !errors.Is(err, database.ErrCredentialBlocked) {
		t.Fatalf("expected ErrCredentialBlocked, got: %v", err)
	}
}

func TestDeviceHmacCredential_DuplicateSerialNumber_Fails(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	record1 := &database.DeviceHmacCredential{
		SerialNumber:      "SN-UNIQUE-TEST",
		HMACKeyCiphertext: []byte("KEY-1"),
		AuthMethod:        database.AuthMethodEfuseHMAC,
		CredentialStatus:  database.CredentialStatusEnabled,
	}
	if err := db.CreateDeviceHmacCredential(ctx, record1); err != nil {
		t.Fatalf("failed to insert record1: %v", err)
	}

	record2 := &database.DeviceHmacCredential{
		SerialNumber:      "SN-UNIQUE-TEST",
		HMACKeyCiphertext: []byte("KEY-2"),
		AuthMethod:        database.AuthMethodEfuseHMAC,
		CredentialStatus:  database.CredentialStatusEnabled,
	}
	err := db.CreateDeviceHmacCredential(ctx, record2)
	if err == nil {
		t.Fatal("expected duplicate serial_number insertion to fail, got nil")
	}
}

func TestDeviceHmacCredential_EmptyFields_Fails(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. 空 serial_number
	err := db.CreateDeviceHmacCredential(ctx, &database.DeviceHmacCredential{
		SerialNumber:      "",
		HMACKeyCiphertext: []byte("KEY-VALID"),
	})
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}

	err = db.CreateDeviceHmacCredential(ctx, &database.DeviceHmacCredential{
		SerialNumber:      "   ",
		HMACKeyCiphertext: []byte("KEY-VALID"),
	})
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber for whitespace, got: %v", err)
	}

	// 2. 空 hmac_key_ciphertext
	err = db.CreateDeviceHmacCredential(ctx, &database.DeviceHmacCredential{
		SerialNumber:      "SN-VALID",
		HMACKeyCiphertext: nil,
	})
	if !errors.Is(err, database.ErrEmptyHMACKeyCiphertext) {
		t.Errorf("expected ErrEmptyHMACKeyCiphertext, got: %v", err)
	}

	err = db.CreateDeviceHmacCredential(ctx, &database.DeviceHmacCredential{
		SerialNumber:      "SN-VALID",
		HMACKeyCiphertext: []byte{},
	})
	if !errors.Is(err, database.ErrEmptyHMACKeyCiphertext) {
		t.Errorf("expected ErrEmptyHMACKeyCiphertext for empty slice, got: %v", err)
	}
}

func TestDeviceHmacCredential_FindByID(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	record := &database.DeviceHmacCredential{
		SerialNumber:      "SN-FIND-BY-ID-01",
		HMACKeyCiphertext: []byte("KEY-FIND-BY-ID-01"),
		AuthMethod:        database.AuthMethodEfuseHMAC,
		CredentialStatus:  database.CredentialStatusEnabled,
	}
	if err := db.CreateDeviceHmacCredential(ctx, record); err != nil {
		t.Fatalf("failed to insert record: %v", err)
	}

	// 1. 成功查找
	found, err := db.FindDeviceHmacCredentialByID(ctx, record.ID)
	if err != nil {
		t.Fatalf("failed to find by id %d: %v", record.ID, err)
	}
	if found.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, found.ID)
	}

	// 2. 查无记录
	_, err = db.FindDeviceHmacCredentialByID(ctx, 999999)
	if err == nil {
		t.Fatal("expected not found error for non-existent id, got nil")
	}
	if !errors.Is(err, database.ErrCredentialNotFound) {
		t.Errorf("expected ErrCredentialNotFound, got: %v", err)
	}

	// 3. ID 为 0
	_, err = db.FindDeviceHmacCredentialByID(ctx, 0)
	if err == nil {
		t.Fatal("expected not found error for id 0, got nil")
	}
	if !errors.Is(err, database.ErrCredentialNotFound) {
		t.Errorf("expected ErrCredentialNotFound, got: %v", err)
	}
}

func TestDeviceHmacCredential_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// serial_number 不存在
	_, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "NON-EXISTENT-SN")
	if err == nil {
		t.Fatal("expected error for non-existent serial_number, got nil")
	}
	if !errors.Is(err, database.ErrCredentialNotFound) {
		t.Errorf("expected ErrCredentialNotFound, got: %v", err)
	}
}

func TestDeviceHmacCredential_EmptyParameters(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	_, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "")
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	_, err = db.FindDeviceHmacCredentialBySerialNumber(ctx, "   ")
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber for whitespace, got: %v", err)
	}
}

func TestDeviceHmacCredential_NilDatabaseSafety(t *testing.T) {
	var nilDB *database.Database
	ctx := context.Background()

	_, err := nilDB.FindDeviceHmacCredentialBySerialNumber(ctx, "SN-TEST")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	_, err = nilDB.FindDeviceHmacCredentialByID(ctx, 1)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.CreateDeviceHmacCredential(ctx, &database.DeviceHmacCredential{})
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.UpdateDeviceHmacCredentialStatus(ctx, "SN-TEST", "active")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.UpsertDeviceHmacCredential(ctx, &database.DeviceHmacCredential{})
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	db := setupTestDB(t)
	err = db.CreateDeviceHmacCredential(ctx, nil)
	if !errors.Is(err, database.ErrInvalidCredential) {
		t.Errorf("expected ErrInvalidCredential, got: %v", err)
	}
}

func TestDeviceHmacCredential_ContextCanceled(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, "SN-ESP32-001")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.FindDeviceHmacCredentialByID(ctx, 1)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
}

func TestValidateActivationInput(t *testing.T) {
	if err := database.ValidateActivationInput("", "hmac", "123456"); err == nil {
		t.Error("expected error for empty serial_number")
	}
	if err := database.ValidateActivationInput("SN001", "", "123456"); err == nil {
		t.Error("expected error for empty hmac")
	}
	if err := database.ValidateActivationInput("SN001", "hmac", ""); err == nil {
		t.Error("expected error for empty code")
	}
	if err := database.ValidateActivationInput("SN001", "hmac", "123456"); err != nil {
		t.Errorf("unexpected error for valid input: %v", err)
	}
}
