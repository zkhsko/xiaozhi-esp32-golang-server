package database_test

import (
	"context"
	"errors"
	"testing"

	"xiaozhi-esp32-golang-server/internal/database"
)

func TestDeviceUserRef_CreateAndFind(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-USER-REF-001"
	var userID uint64 = 10001

	record := &database.DeviceUserRef{
		SerialNumber: sn,
		UserID:       userID,
	}

	if err := db.CreateDeviceUserRef(ctx, record); err != nil {
		t.Fatalf("failed to create device user ref: %v", err)
	}

	if record.ID == 0 {
		t.Fatalf("expected auto-incremented ID > 0, got %d", record.ID)
	}

	// 1. 按 serial_number 查询
	foundSN, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find device user ref by serial number: %v", err)
	}
	if foundSN.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundSN.ID)
	}
	if foundSN.SerialNumber != sn {
		t.Errorf("expected serial_number %q, got %q", sn, foundSN.SerialNumber)
	}
	if foundSN.UserID != userID {
		t.Errorf("expected user_id %d, got %d", userID, foundSN.UserID)
	}
	if foundSN.CreatedAt.IsZero() || foundSN.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps to be populated, got created_at=%v, updated_at=%v",
			foundSN.CreatedAt, foundSN.UpdatedAt)
	}

	// 2. 带首尾空格查询（自动 Trim）
	foundTrimmed, err := db.FindDeviceUserRefBySerialNumber(ctx, "  "+sn+"  ")
	if err != nil {
		t.Fatalf("failed to find device user ref with whitespace: %v", err)
	}
	if foundTrimmed.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundTrimmed.ID)
	}

	// 3. 按 id 查询
	foundID, err := db.FindDeviceUserRefByID(ctx, record.ID)
	if err != nil {
		t.Fatalf("failed to find device user ref by id: %v", err)
	}
	if foundID.SerialNumber != sn {
		t.Errorf("expected serial_number %q, got %q", sn, foundID.SerialNumber)
	}
}

func TestDeviceUserRef_BindDevice_Idempotent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-BIND-IDEMPOTENT-001"
	var userID uint64 = 20001

	// 1. 首次绑定
	ref1, err := db.BindDevice(ctx, sn, userID)
	if err != nil {
		t.Fatalf("failed to bind device: %v", err)
	}
	if ref1.ID == 0 || ref1.SerialNumber != sn || ref1.UserID != userID {
		t.Fatalf("unexpected ref data: %+v", ref1)
	}

	// 2. 重复绑定同一用户 -> 幂等成功
	ref2, err := db.BindDevice(ctx, sn, userID)
	if err != nil {
		t.Fatalf("expected idempotent bind to succeed, got: %v", err)
	}
	if ref2.ID != ref1.ID {
		t.Errorf("expected same ID %d, got %d", ref1.ID, ref2.ID)
	}
}

func TestDeviceUserRef_BindDevice_AlreadyBoundToOther(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-BIND-CONFLICT-001"
	var userA uint64 = 30001
	var userB uint64 = 30002

	// 用户 A 绑定设备
	_, err := db.BindDevice(ctx, sn, userA)
	if err != nil {
		t.Fatalf("failed to bind to userA: %v", err)
	}

	// 用户 B 尝试绑定已被用户 A 绑定的设备 -> 拒绝
	_, err = db.BindDevice(ctx, sn, userB)
	if !errors.Is(err, database.ErrDeviceAlreadyBoundToOther) {
		t.Fatalf("expected ErrDeviceAlreadyBoundToOther, got: %v", err)
	}
}

func TestDeviceUserRef_ListByUserID(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	var user1 uint64 = 40001
	var user2 uint64 = 40002

	// 为 user1 绑定两台设备
	_, err := db.BindDevice(ctx, "SN-USER1-DEV-01", user1)
	if err != nil {
		t.Fatalf("failed to bind dev1: %v", err)
	}
	_, err = db.BindDevice(ctx, "SN-USER1-DEV-02", user1)
	if err != nil {
		t.Fatalf("failed to bind dev2: %v", err)
	}

	// 为 user2 绑定一台设备
	_, err = db.BindDevice(ctx, "SN-USER2-DEV-01", user2)
	if err != nil {
		t.Fatalf("failed to bind dev for user2: %v", err)
	}

	// 查询 user1 的设备列表
	list1, err := db.ListDeviceUserRefsByUserID(ctx, user1)
	if err != nil {
		t.Fatalf("failed to list devices for user1: %v", err)
	}
	if len(list1) != 2 {
		t.Fatalf("expected 2 devices for user1, got %d", len(list1))
	}
	if list1[0].SerialNumber != "SN-USER1-DEV-02" || list1[1].SerialNumber != "SN-USER1-DEV-01" {
		t.Errorf("expected desc order by id, got: %+v", list1)
	}

	// 查询没有任何设备的用户
	listEmpty, err := db.ListDeviceUserRefsByUserID(ctx, 99999)
	if err != nil {
		t.Fatalf("failed to query empty user devices: %v", err)
	}
	if len(listEmpty) != 0 {
		t.Errorf("expected 0 devices, got %d", len(listEmpty))
	}
}

func TestDeviceUserRef_UnbindDevice(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-UNBIND-TEST-001"
	var userID uint64 = 50001

	_, err := db.BindDevice(ctx, sn, userID)
	if err != nil {
		t.Fatalf("failed to bind: %v", err)
	}

	// 1. 成功解绑
	if err := db.UnbindDevice(ctx, sn); err != nil {
		t.Fatalf("failed to unbind device: %v", err)
	}

	// 2. 再次查询确认已不存在
	_, err = db.FindDeviceUserRefBySerialNumber(ctx, sn)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Fatalf("expected ErrBindingNotFound after unbind, got: %v", err)
	}

	// 3. 重复解绑已不存在的设备 -> 返回 ErrBindingNotFound
	err = db.UnbindDevice(ctx, sn)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Fatalf("expected ErrBindingNotFound on second unbind, got: %v", err)
	}
}

func TestDeviceUserRef_UnbindDeviceFromUser(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-UNBIND-USER-001"
	var userOwner uint64 = 60001
	var userOther uint64 = 60002

	_, err := db.BindDevice(ctx, sn, userOwner)
	if err != nil {
		t.Fatalf("failed to bind: %v", err)
	}

	// 1. 非设备持有者尝试解绑 -> 返回 ErrBindingNotFound (不匹配)
	err = db.UnbindDeviceFromUser(ctx, sn, userOther)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Fatalf("expected ErrBindingNotFound when unbinding with wrong user, got: %v", err)
	}

	// 2. 验证原有绑定仍然存在
	found, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
	if err != nil || found.UserID != userOwner {
		t.Fatalf("expected binding to still exist for userOwner, got: %+v, err: %v", found, err)
	}

	// 3. 正确持有者解绑 -> 成功
	if err := db.UnbindDeviceFromUser(ctx, sn, userOwner); err != nil {
		t.Fatalf("failed to unbind with owner user: %v", err)
	}

	// 4. 验证已解绑
	_, err = db.FindDeviceUserRefBySerialNumber(ctx, sn)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Fatalf("expected ErrBindingNotFound, got: %v", err)
	}
}

func TestDeviceUserRef_TransferDeviceBinding(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-TRANSFER-001"
	var userOld uint64 = 70001
	var userNew uint64 = 70002

	_, err := db.BindDevice(ctx, sn, userOld)
	if err != nil {
		t.Fatalf("failed to bind: %v", err)
	}

	// 1. 转移至新用户
	if err := db.TransferDeviceBinding(ctx, sn, userNew); err != nil {
		t.Fatalf("failed to transfer binding: %v", err)
	}

	found, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find after transfer: %v", err)
	}
	if found.UserID != userNew {
		t.Errorf("expected new user_id %d, got %d", userNew, found.UserID)
	}

	// 2. 转移不存在的设备
	err = db.TransferDeviceBinding(ctx, "NON-EXISTENT-SN", userNew)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Fatalf("expected ErrBindingNotFound, got: %v", err)
	}
}

func TestDeviceUserRef_DuplicateSerialNumber_Fails(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-DUPLICATE-USER-REF"
	record1 := &database.DeviceUserRef{
		SerialNumber: sn,
		UserID:       80001,
	}
	if err := db.CreateDeviceUserRef(ctx, record1); err != nil {
		t.Fatalf("failed to insert record1: %v", err)
	}

	record2 := &database.DeviceUserRef{
		SerialNumber: sn,
		UserID:       80002,
	}
	err := db.CreateDeviceUserRef(ctx, record2)
	if err == nil {
		t.Fatal("expected duplicate serial_number insertion to fail, got nil")
	}
}

func TestDeviceUserRef_ValidationErrors(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. 空 serial_number
	err := db.CreateDeviceUserRef(ctx, &database.DeviceUserRef{
		SerialNumber: "",
		UserID:       1,
	})
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}

	err = db.CreateDeviceUserRef(ctx, &database.DeviceUserRef{
		SerialNumber: "   ",
		UserID:       1,
	})
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber for whitespace, got: %v", err)
	}

	// 2. 空 user_id (0)
	err = db.CreateDeviceUserRef(ctx, &database.DeviceUserRef{
		SerialNumber: "SN-VALID",
		UserID:       0,
	})
	if !errors.Is(err, database.ErrEmptyUserID) {
		t.Errorf("expected ErrEmptyUserID, got: %v", err)
	}

	// 3. nil 结构体
	err = db.CreateDeviceUserRef(ctx, nil)
	if !errors.Is(err, database.ErrInvalidBinding) {
		t.Errorf("expected ErrInvalidBinding, got: %v", err)
	}

	// 4. BindDevice 参数校验
	_, err = db.BindDevice(ctx, "", 1)
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	_, err = db.BindDevice(ctx, "SN-001", 0)
	if !errors.Is(err, database.ErrEmptyUserID) {
		t.Errorf("expected ErrEmptyUserID, got: %v", err)
	}

	// 5. ListDeviceUserRefsByUserID 参数校验
	_, err = db.ListDeviceUserRefsByUserID(ctx, 0)
	if !errors.Is(err, database.ErrEmptyUserID) {
		t.Errorf("expected ErrEmptyUserID, got: %v", err)
	}

	// 6. UnbindDevice / UnbindDeviceFromUser 参数校验
	err = db.UnbindDevice(ctx, "")
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	err = db.UnbindDeviceFromUser(ctx, "", 1)
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	err = db.UnbindDeviceFromUser(ctx, "SN-001", 0)
	if !errors.Is(err, database.ErrEmptyUserID) {
		t.Errorf("expected ErrEmptyUserID, got: %v", err)
	}

	// 7. TransferDeviceBinding 参数校验
	err = db.TransferDeviceBinding(ctx, "", 1)
	if !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	err = db.TransferDeviceBinding(ctx, "SN-001", 0)
	if !errors.Is(err, database.ErrEmptyUserID) {
		t.Errorf("expected ErrEmptyUserID, got: %v", err)
	}
}

func TestDeviceUserRef_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. serial_number 不存在
	_, err := db.FindDeviceUserRefBySerialNumber(ctx, "NON-EXISTENT-SN")
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound, got: %v", err)
	}

	// 2. ID 不存在
	_, err = db.FindDeviceUserRefByID(ctx, 999999)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound, got: %v", err)
	}

	// 3. ID 为 0
	_, err = db.FindDeviceUserRefByID(ctx, 0)
	if !errors.Is(err, database.ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound for id 0, got: %v", err)
	}
}

func TestDeviceUserRef_NilDatabaseSafety(t *testing.T) {
	var nilDB *database.Database
	ctx := context.Background()

	_, err := nilDB.FindDeviceUserRefBySerialNumber(ctx, "SN-TEST")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	_, err = nilDB.FindDeviceUserRefByID(ctx, 1)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	_, err = nilDB.ListDeviceUserRefsByUserID(ctx, 1)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.CreateDeviceUserRef(ctx, &database.DeviceUserRef{})
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	_, err = nilDB.BindDevice(ctx, "SN-TEST", 1)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.UnbindDevice(ctx, "SN-TEST")
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.UnbindDeviceFromUser(ctx, "SN-TEST", 1)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}

	err = nilDB.TransferDeviceBinding(ctx, "SN-TEST", 2)
	if !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
}

func TestDeviceUserRef_ContextCanceled(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := db.FindDeviceUserRefBySerialNumber(ctx, "SN-001")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.FindDeviceUserRefByID(ctx, 1)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.ListDeviceUserRefsByUserID(ctx, 1)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
}
