package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
)

func TestDeviceActivation_CreateAndFind(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-ACT-ESP32-001"
	deviceID := "DEV-ID-ABC-001"
	clientID := "CLIENT-INSTALL-001"

	record := &database.DeviceActivation{
		SerialNumber:     sn,
		DeviceID:         deviceID,
		ClientID:         clientID,
		ActivationStatus: database.ActivationStatusActive,
		ActivatedAt:      time.Now().Truncate(time.Millisecond),
	}

	if err := db.CreateDeviceActivation(ctx, record); err != nil {
		t.Fatalf("failed to create device activation: %v", err)
	}

	if record.ID == 0 {
		t.Fatalf("expected auto-incremented ID > 0, got %d", record.ID)
	}

	// 1. 按 serial_number 精确查询
	foundSN, err := db.FindDeviceActivationBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find device activation by serial number: %v", err)
	}
	if foundSN.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundSN.ID)
	}
	if foundSN.SerialNumber != sn {
		t.Errorf("expected serial_number %q, got %q", sn, foundSN.SerialNumber)
	}
	if foundSN.DeviceID != deviceID {
		t.Errorf("expected device_id %q, got %q", deviceID, foundSN.DeviceID)
	}
	if foundSN.ClientID != clientID {
		t.Errorf("expected client_id %q, got %q", clientID, foundSN.ClientID)
	}
	if foundSN.ActivationStatus != database.ActivationStatusActive {
		t.Errorf("expected activation_status %q, got %q", database.ActivationStatusActive, foundSN.ActivationStatus)
	}
	if !foundSN.IsActive() {
		t.Errorf("expected device activation to be active")
	}
	if foundSN.CreatedAt.IsZero() || foundSN.UpdatedAt.IsZero() || foundSN.ActivatedAt.IsZero() {
		t.Errorf("expected timestamps to be populated, got created_at=%v, updated_at=%v, activated_at=%v",
			foundSN.CreatedAt, foundSN.UpdatedAt, foundSN.ActivatedAt)
	}

	// 2. 带首尾空格查询（自动 Trim）
	foundTrimmed, err := db.FindDeviceActivationBySerialNumber(ctx, "  "+sn+"  ")
	if err != nil {
		t.Fatalf("failed to find device activation with whitespace: %v", err)
	}
	if foundTrimmed.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundTrimmed.ID)
	}

	// 3. 按 device_id 查询
	foundDevID, err := db.FindDeviceActivationByDeviceID(ctx, deviceID)
	if err != nil {
		t.Fatalf("failed to find device activation by device_id: %v", err)
	}
	if foundDevID.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundDevID.ID)
	}

	// 4. 按 device_id 和 client_id 联合查询
	foundDevCli, err := db.FindDeviceActivationByDeviceIDAndClientID(ctx, deviceID, clientID)
	if err != nil {
		t.Fatalf("failed to find device activation by device_id and client_id: %v", err)
	}
	if foundDevCli.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundDevCli.ID)
	}
	if foundDevCli.DeviceID != deviceID {
		t.Errorf("expected device_id %q, got %q", deviceID, foundDevCli.DeviceID)
	}
	if foundDevCli.ClientID != clientID {
		t.Errorf("expected client_id %q, got %q", clientID, foundDevCli.ClientID)
	}

	// 5. 按 device_id 和 client_id 带首尾空格查询（自动 Trim）
	foundDevCliTrimmed, err := db.FindDeviceActivationByDeviceIDAndClientID(ctx, "  "+deviceID+"  ", "  "+clientID+"  ")
	if err != nil {
		t.Fatalf("failed to find device activation by device_id and client_id with whitespace: %v", err)
	}
	if foundDevCliTrimmed.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundDevCliTrimmed.ID)
	}

	// 6. 按 id 查询
	foundID, err := db.FindDeviceActivationByID(ctx, record.ID)
	if err != nil {
		t.Fatalf("failed to find device activation by id: %v", err)
	}
	if foundID.SerialNumber != sn {
		t.Errorf("expected serial_number %q, got %q", sn, foundID.SerialNumber)
	}
}

func TestDeviceActivation_DuplicateSerialNumber_Fails(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	record1 := &database.DeviceActivation{
		SerialNumber: "SN-DUPLICATE-TEST",
		DeviceID:     "DEV-001",
	}
	if err := db.CreateDeviceActivation(ctx, record1); err != nil {
		t.Fatalf("failed to insert record1: %v", err)
	}

	record2 := &database.DeviceActivation{
		SerialNumber: "SN-DUPLICATE-TEST",
		DeviceID:     "DEV-002",
	}
	err := db.CreateDeviceActivation(ctx, record2)
	if err == nil {
		t.Fatal("expected duplicate serial_number insertion to fail, got nil")
	}
}

func TestDeviceActivation_DuplicateDeviceID_Allowed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sharedDeviceID := "SHARED-DEV-ID-123"

	// 验证 device_id 不存在唯一索引，允许多条记录拥有相同 device_id
	record1 := &database.DeviceActivation{
		SerialNumber: "SN-DIFF-001",
		DeviceID:     sharedDeviceID,
	}
	if err := db.CreateDeviceActivation(ctx, record1); err != nil {
		t.Fatalf("failed to insert record1: %v", err)
	}

	record2 := &database.DeviceActivation{
		SerialNumber: "SN-DIFF-002",
		DeviceID:     sharedDeviceID,
	}
	if err := db.CreateDeviceActivation(ctx, record2); err != nil {
		t.Fatalf("expected insert record2 with same device_id to succeed, got: %v", err)
	}

	// 按 device_id 查询应返回最新的那条（record2）
	found, err := db.FindDeviceActivationByDeviceID(ctx, sharedDeviceID)
	if err != nil {
		t.Fatalf("failed to find by shared device_id: %v", err)
	}
	if found.ID != record2.ID {
		t.Errorf("expected latest ID %d, got %d", record2.ID, found.ID)
	}
}

func TestDeviceActivation_Upsert(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-UPSERT-TEST-001"
	deviceID1 := "DEV-INITIAL-01"
	clientID1 := "CLIENT-V1"

	record := &database.DeviceActivation{
		SerialNumber: sn,
		DeviceID:     deviceID1,
		ClientID:     clientID1,
	}

	// 1. 首次 upsert -> 插入
	if err := db.UpsertDeviceActivation(ctx, record); err != nil {
		t.Fatalf("failed to first upsert: %v", err)
	}
	firstID := record.ID
	if firstID == 0 {
		t.Fatal("expected non-zero ID after first upsert")
	}

	found1, err := db.FindDeviceActivationBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find after first upsert: %v", err)
	}
	if found1.DeviceID != deviceID1 || found1.ClientID != clientID1 {
		t.Errorf("unexpected record data: %+v", found1)
	}
	if found1.ActivationStatus != database.ActivationStatusActive {
		t.Errorf("expected default status active, got %s", found1.ActivationStatus)
	}

	// 2. 再次 upsert -> 更新 device_id 和 client_id
	deviceID2 := "DEV-UPDATED-02"
	clientID2 := "CLIENT-V2"
	recordUpdated := &database.DeviceActivation{
		SerialNumber: sn,
		DeviceID:     deviceID2,
		ClientID:     clientID2,
	}
	if err := db.UpsertDeviceActivation(ctx, recordUpdated); err != nil {
		t.Fatalf("failed to second upsert: %v", err)
	}
	if recordUpdated.ID != firstID {
		t.Errorf("expected same ID %d, got %d", firstID, recordUpdated.ID)
	}

	found2, err := db.FindDeviceActivationBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find after second upsert: %v", err)
	}
	if found2.DeviceID != deviceID2 || found2.ClientID != clientID2 {
		t.Errorf("expected updated fields, got device_id=%q, client_id=%q", found2.DeviceID, found2.ClientID)
	}

	// 3. 状态更新为 frozen 后 upsert 应被拦截
	if err := db.UpdateDeviceActivationStatus(ctx, sn, database.ActivationStatusFrozen); err != nil {
		t.Fatalf("failed to update status to frozen: %v", err)
	}

	err = db.UpsertDeviceActivation(ctx, recordUpdated)
	if !errors.Is(err, database.ErrActivationBlocked) {
		t.Fatalf("expected ErrActivationBlocked, got: %v", err)
	}
}

func TestDeviceActivation_UpdateStatus(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-STATUS-TEST-001"
	record := &database.DeviceActivation{
		SerialNumber: sn,
		DeviceID:     "DEV-STATUS-01",
	}
	if err := db.CreateDeviceActivation(ctx, record); err != nil {
		t.Fatalf("failed to create: %v", err)
	}

	// 1. 更新为 revoked
	if err := db.UpdateDeviceActivationStatus(ctx, sn, database.ActivationStatusRevoked); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	found, err := db.FindDeviceActivationBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find: %v", err)
	}
	if found.ActivationStatus != database.ActivationStatusRevoked {
		t.Errorf("expected revoked status, got %s", found.ActivationStatus)
	}
	if found.IsActive() {
		t.Errorf("expected IsActive() to be false for revoked status")
	}

	// 2. 不存在的序列号更新
	err = db.UpdateDeviceActivationStatus(ctx, "NON-EXISTENT-SN", database.ActivationStatusActive)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Fatalf("expected ErrActivationNotFound, got: %v", err)
	}
}

func TestDeviceActivation_ValidationErrors(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. 空 serial_number
	err := db.CreateDeviceActivation(ctx, &database.DeviceActivation{
		SerialNumber: "",
		DeviceID:     "DEV-VALID",
	})
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}

	err = db.CreateDeviceActivation(ctx, &database.DeviceActivation{
		SerialNumber: "   ",
		DeviceID:     "DEV-VALID",
	})
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber for whitespace, got: %v", err)
	}

	// 2. 空 device_id
	err = db.CreateDeviceActivation(ctx, &database.DeviceActivation{
		SerialNumber: "SN-VALID",
		DeviceID:     "",
	})
	if !errors.Is(err, database.ErrEmptyDeviceID) {
		t.Errorf("expected ErrEmptyDeviceID, got: %v", err)
	}

	err = db.CreateDeviceActivation(ctx, &database.DeviceActivation{
		SerialNumber: "SN-VALID",
		DeviceID:     "   ",
	})
	if !errors.Is(err, database.ErrEmptyDeviceID) {
		t.Errorf("expected ErrEmptyDeviceID for whitespace, got: %v", err)
	}

	// 3. nil 结构体
	err = db.CreateDeviceActivation(ctx, nil)
	if !errors.Is(err, database.ErrInvalidActivation) {
		t.Errorf("expected ErrInvalidActivation, got: %v", err)
	}

	err = db.UpsertDeviceActivation(ctx, nil)
	if !errors.Is(err, database.ErrInvalidActivation) {
		t.Errorf("expected ErrInvalidActivation for upsert, got: %v", err)
	}

	// 4. UpdateDeviceActivationStatus 参数校验
	err = db.UpdateDeviceActivationStatus(ctx, "", "active")
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	err = db.UpdateDeviceActivationStatus(ctx, "SN-001", "")
	if !errors.Is(err, database.ErrEmptyStatus) {
		t.Errorf("expected ErrEmptyStatus, got: %v", err)
	}
}

func TestDeviceActivation_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. serial_number 不存在
	_, err := db.FindDeviceActivationBySerialNumber(ctx, "NON-EXISTENT-SN")
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound, got: %v", err)
	}

	// 2. device_id 不存在
	_, err = db.FindDeviceActivationByDeviceID(ctx, "NON-EXISTENT-DEV-ID")
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound, got: %v", err)
	}

	// 3. device_id 和 client_id 不存在
	_, err = db.FindDeviceActivationByDeviceIDAndClientID(ctx, "NON-EXISTENT-DEV-ID", "NON-EXISTENT-CLI-ID")
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound, got: %v", err)
	}

	// 4. ID 不存在
	_, err = db.FindDeviceActivationByID(ctx, 999999)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound, got: %v", err)
	}

	// 5. ID 为 0
	_, err = db.FindDeviceActivationByID(ctx, 0)
	if !errors.Is(err, database.ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound for id 0, got: %v", err)
	}
}

func TestDeviceActivation_NilDatabaseSafety(t *testing.T) {
	var nilDB *database.Database
	ctx := context.Background()

	_, err := nilDB.FindDeviceActivationBySerialNumber(ctx, "SN-TEST")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	_, err = nilDB.FindDeviceActivationByDeviceID(ctx, "DEV-TEST")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	_, err = nilDB.FindDeviceActivationByDeviceIDAndClientID(ctx, "DEV-TEST", "CLI-TEST")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	_, err = nilDB.FindDeviceActivationByID(ctx, 1)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.CreateDeviceActivation(ctx, &database.DeviceActivation{})
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.UpdateDeviceActivationStatus(ctx, "SN-TEST", "active")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.UpsertDeviceActivation(ctx, &database.DeviceActivation{})
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
}

func TestDeviceActivation_ContextCanceled(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := db.FindDeviceActivationBySerialNumber(ctx, "SN-001")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.FindDeviceActivationByDeviceID(ctx, "DEV-001")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.FindDeviceActivationByDeviceIDAndClientID(ctx, "DEV-001", "CLI-001")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.FindDeviceActivationByID(ctx, 1)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
}

func TestDeviceActivation_FindByDeviceIDAndClientID_Validation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 空 device_id
	_, err := db.FindDeviceActivationByDeviceIDAndClientID(ctx, "", "CLI-001")
	if !errors.Is(err, database.ErrEmptyDeviceID) {
		t.Errorf("expected ErrEmptyDeviceID, got: %v", err)
	}
	_, err = db.FindDeviceActivationByDeviceIDAndClientID(ctx, "   ", "CLI-001")
	if !errors.Is(err, database.ErrEmptyDeviceID) {
		t.Errorf("expected ErrEmptyDeviceID for whitespace, got: %v", err)
	}

	// 空 client_id
	_, err = db.FindDeviceActivationByDeviceIDAndClientID(ctx, "DEV-001", "")
	if !errors.Is(err, database.ErrEmptyClientID) {
		t.Errorf("expected ErrEmptyClientID, got: %v", err)
	}
	_, err = db.FindDeviceActivationByDeviceIDAndClientID(ctx, "DEV-001", "   ")
	if !errors.Is(err, database.ErrEmptyClientID) {
		t.Errorf("expected ErrEmptyClientID for whitespace, got: %v", err)
	}
}

func TestDeviceActivation_IsActiveHelper(t *testing.T) {
	var nilAct *database.DeviceActivation
	if nilAct.IsActive() {
		t.Error("expected nil.IsActive() to be false")
	}

	act := &database.DeviceActivation{ActivationStatus: database.ActivationStatusActive}
	if !act.IsActive() {
		t.Error("expected active.IsActive() to be true")
	}

	act.ActivationStatus = database.ActivationStatusFrozen
	if act.IsActive() {
		t.Error("expected frozen.IsActive() to be false")
	}

	act.ActivationStatus = database.ActivationStatusRevoked
	if act.IsActive() {
		t.Error("expected revoked.IsActive() to be false")
	}
}

func TestDeviceActivation_ActivateDeviceBySerialNumber_NewAndReactivate(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-ACTIVATE-TEST-001"
	deviceID1 := "DEV-ESP32-INITIAL"
	clientID1 := "CLI-UUID-INITIAL"

	// 1. 首次激活（记录不存在）：直接插入新激活记录
	act1, err := db.ActivateDeviceBySerialNumber(ctx, sn, deviceID1, clientID1)
	if err != nil {
		t.Fatalf("failed to activate new device: %v", err)
	}
	if act1.SerialNumber != sn {
		t.Errorf("expected SN %q, got %q", sn, act1.SerialNumber)
	}
	if act1.DeviceID != deviceID1 {
		t.Errorf("expected DeviceID %q, got %q", deviceID1, act1.DeviceID)
	}
	if act1.ClientID != clientID1 {
		t.Errorf("expected ClientID %q, got %q", clientID1, act1.ClientID)
	}
	if act1.ActivationStatus != database.ActivationStatusActive {
		t.Errorf("expected status %q, got %q", database.ActivationStatusActive, act1.ActivationStatus)
	}
	if act1.ActivatedAt.IsZero() {
		t.Error("expected non-zero ActivatedAt")
	}

	// 绑定用户
	userID := uint64(999)
	if _, err := db.BindDevice(ctx, sn, userID); err != nil {
		t.Fatalf("failed to bind device to user: %v", err)
	}

	// 确认绑定存在
	userRef, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
	if err != nil || userRef.UserID != userID {
		t.Fatalf("failed to verify user binding before reactivation: %v", err)
	}

	// 2. 再次激活（记录已存在）：更新数据并删除用户绑定记录
	deviceID2 := "DEV-ESP32-UPDATED"
	clientID2 := "CLI-UUID-UPDATED"
	act2, err := db.ActivateDeviceBySerialNumber(ctx, sn, deviceID2, clientID2)
	if err != nil {
		t.Fatalf("failed to reactivate existing device: %v", err)
	}
	if act2.DeviceID != deviceID2 {
		t.Errorf("expected updated DeviceID %q, got %q", deviceID2, act2.DeviceID)
	}
	if act2.ClientID != clientID2 {
		t.Errorf("expected updated ClientID %q, got %q", clientID2, act2.ClientID)
	}
	if act2.ActivationStatus != database.ActivationStatusActive {
		t.Errorf("expected status %q, got %q", database.ActivationStatusActive, act2.ActivationStatus)
	}

	// 验证用户绑定已被删除
	_, err = db.FindDeviceUserRefBySerialNumber(ctx, sn)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected user binding to be deleted on reactivation, got err: %v", err)
	}

	// 3. 校验入参错误
	_, err = db.ActivateDeviceBySerialNumber(ctx, "", deviceID1, clientID1)
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got %v", err)
	}

	var nilDB *database.Database
	_, err = nilDB.ActivateDeviceBySerialNumber(ctx, sn, deviceID1, clientID1)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got %v", err)
	}
}
