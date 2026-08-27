package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
)

func TestDeviceAccessToken_CreateAndFind(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-TOKEN-001"
	rawToken := "test-device-token-secret-123456"
	issuedAt := time.Now().Truncate(time.Millisecond)
	expiresAt := issuedAt.Add(24 * time.Hour)

	record := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  rawToken,
		IssuedAt:     issuedAt,
		ExpiresAt:    &expiresAt,
	}

	if err := db.CreateDeviceAccessToken(ctx, record); err != nil {
		t.Fatalf("failed to create device access token: %v", err)
	}

	if record.ID == 0 {
		t.Fatalf("expected auto-incremented ID > 0, got %d", record.ID)
	}

	// 1. 按 access_token 查询
	foundToken, err := db.FindDeviceAccessTokenByAccessToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("failed to find device access token by access token: %v", err)
	}
	if foundToken.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundToken.ID)
	}
	if foundToken.SerialNumber != sn {
		t.Errorf("expected serial_number %q, got %q", sn, foundToken.SerialNumber)
	}
	if foundToken.AccessToken != rawToken {
		t.Errorf("expected access_token %q, got %q", rawToken, foundToken.AccessToken)
	}
	if foundToken.ExpiresAt == nil || foundToken.ExpiresAt.IsZero() {
		t.Errorf("expected expires_at to be populated, got %v", foundToken.ExpiresAt)
	}
	if foundToken.RevokedAt != nil {
		t.Errorf("expected revoked_at to be nil, got %v", foundToken.RevokedAt)
	}
	if foundToken.CreatedAt.IsZero() || foundToken.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps to be populated, got created_at=%v, updated_at=%v",
			foundToken.CreatedAt, foundToken.UpdatedAt)
	}
	if !foundToken.IsValid(time.Now()) {
		t.Errorf("expected token to be valid")
	}

	// 2. 按 serial_number 查询
	foundSN, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find device access token by serial_number: %v", err)
	}
	if foundSN.ID != record.ID {
		t.Errorf("expected ID %d, got %d", record.ID, foundSN.ID)
	}

	// 3. 按 id 查询
	foundID, err := db.FindDeviceAccessTokenByID(ctx, record.ID)
	if err != nil {
		t.Fatalf("failed to find device access token by id: %v", err)
	}
	if foundID.SerialNumber != sn {
		t.Errorf("expected serial_number %q, got %q", sn, foundID.SerialNumber)
	}
}

func TestDeviceAccessToken_DuplicateSerialNumber_Fails(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-DUP-SN-001"
	record1 := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  "token-1",
		IssuedAt:     time.Now(),
	}
	if err := db.CreateDeviceAccessToken(ctx, record1); err != nil {
		t.Fatalf("failed to create first device access token: %v", err)
	}

	record2 := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  "token-2",
		IssuedAt:     time.Now(),
	}
	err := db.CreateDeviceAccessToken(ctx, record2)
	if err == nil {
		t.Fatal("expected duplicate serial_number to fail, got nil error")
	}
}

func TestDeviceAccessToken_DuplicateAccessToken_Fails(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	rawToken := "duplicate-token-secret"

	record1 := &database.DeviceAccessToken{
		SerialNumber: "SN-DUP-1",
		AccessToken:  rawToken,
		IssuedAt:     time.Now(),
	}
	if err := db.CreateDeviceAccessToken(ctx, record1); err != nil {
		t.Fatalf("failed to create first device access token: %v", err)
	}

	record2 := &database.DeviceAccessToken{
		SerialNumber: "SN-DUP-2",
		AccessToken:  rawToken,
		IssuedAt:     time.Now(),
	}
	err := db.CreateDeviceAccessToken(ctx, record2)
	if err == nil {
		t.Fatal("expected duplicate access_token to fail, got nil error")
	}
}

func TestDeviceAccessToken_Upsert(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-UPSERT-001"
	token1 := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  "token-version-1",
		IssuedAt:     time.Now().Truncate(time.Millisecond),
	}

	// 1. 首次 Upsert -> 插入新记录
	if err := db.UpsertDeviceAccessToken(ctx, token1); err != nil {
		t.Fatalf("failed to initial upsert: %v", err)
	}
	if token1.ID == 0 {
		t.Fatalf("expected ID > 0 after initial upsert")
	}

	found1, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find token after initial upsert: %v", err)
	}
	if found1.AccessToken != "token-version-1" {
		t.Errorf("expected token %q, got %q", "token-version-1", found1.AccessToken)
	}

	// 2. 再次 Upsert -> 覆盖更新 Token 并支持更新 HasExposed
	token2 := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  "token-version-2",
		HasExposed:   true,
		IssuedAt:     time.Now().Truncate(time.Millisecond),
	}
	if err := db.UpsertDeviceAccessToken(ctx, token2); err != nil {
		t.Fatalf("failed to secondary upsert: %v", err)
	}
	if token2.ID != token1.ID {
		t.Errorf("expected same record ID %d, got %d", token1.ID, token2.ID)
	}

	found2, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find token after secondary upsert: %v", err)
	}
	if found2.AccessToken != "token-version-2" {
		t.Errorf("expected updated token %q, got %q", "token-version-2", found2.AccessToken)
	}
	if !found2.HasExposed {
		t.Errorf("expected has_exposed to be true after secondary upsert")
	}

	// 3. 第三次 Upsert -> 重置 HasExposed 为 false（如重新绑定场景）
	token3 := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  "token-version-3",
		HasExposed:   false,
		IssuedAt:     time.Now().Truncate(time.Millisecond),
	}
	if err := db.UpsertDeviceAccessToken(ctx, token3); err != nil {
		t.Fatalf("failed to third upsert: %v", err)
	}
	found3, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find token after third upsert: %v", err)
	}
	if found3.AccessToken != "token-version-3" {
		t.Errorf("expected updated token %q, got %q", "token-version-3", found3.AccessToken)
	}
	if found3.HasExposed {
		t.Errorf("expected has_exposed to be false after third upsert")
	}
}

func TestDeviceAccessToken_RevokeTokenByAccessToken(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-REVOKE-001"
	rawToken := "token-to-revoke"

	record := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  rawToken,
		IssuedAt:     time.Now(),
	}
	if err := db.CreateDeviceAccessToken(ctx, record); err != nil {
		t.Fatalf("failed to create device access token: %v", err)
	}

	revokeTime := time.Now().Truncate(time.Millisecond)
	if err := db.RevokeDeviceAccessTokenByAccessToken(ctx, rawToken, revokeTime); err != nil {
		t.Fatalf("failed to revoke device access token: %v", err)
	}

	found, err := db.FindDeviceAccessTokenByAccessToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("failed to find revoked token: %v", err)
	}
	if found.RevokedAt == nil {
		t.Fatal("expected revoked_at to be non-nil")
	}
	if found.IsValid(time.Now()) {
		t.Errorf("expected revoked token to be invalid")
	}
}

func TestDeviceAccessToken_RevokeTokenBySerialNumber(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-REVOKE-SN-001"
	token := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  "token-by-sn",
		IssuedAt:     time.Now(),
	}
	if err := db.CreateDeviceAccessToken(ctx, token); err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	revokeTime := time.Now().Truncate(time.Millisecond)
	if err := db.RevokeDeviceAccessTokenBySerialNumber(ctx, sn, revokeTime); err != nil {
		t.Fatalf("failed to revoke token by serial number: %v", err)
	}

	found, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find revoked token: %v", err)
	}
	if found.RevokedAt == nil {
		t.Fatal("expected revoked_at to be non-nil")
	}
	if found.IsValid(time.Now()) {
		t.Errorf("expected revoked token to be invalid")
	}

	// 再次对已撤销的 SN 进行撤销 -> 返回 ErrAccessTokenNotFound (因为没有未撤销的 token)
	err = db.RevokeDeviceAccessTokenBySerialNumber(ctx, sn, revokeTime)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Fatalf("expected ErrAccessTokenNotFound when already revoked, got: %v", err)
	}
}

func TestDeviceAccessToken_UpdateHasExposed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-EXPOSE-001"
	rawToken := "token-for-expose-test"

	record := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  rawToken,
		HasExposed:   false,
		IssuedAt:     time.Now(),
	}
	if err := db.CreateDeviceAccessToken(ctx, record); err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	// 1. 验证初始 has_exposed 为 false
	found1, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find token: %v", err)
	}
	if found1.HasExposed {
		t.Errorf("expected initial has_exposed to be false")
	}

	// 2. 更新 has_exposed 为 true
	if err := db.UpdateDeviceAccessTokenHasExposed(ctx, sn, true); err != nil {
		t.Fatalf("failed to update has_exposed to true: %v", err)
	}

	found2, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find token: %v", err)
	}
	if !found2.HasExposed {
		t.Errorf("expected has_exposed to be true after update")
	}

	// 3. 更新 has_exposed 为 false
	if err := db.UpdateDeviceAccessTokenHasExposed(ctx, sn, false); err != nil {
		t.Fatalf("failed to update has_exposed to false: %v", err)
	}

	found3, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if err != nil {
		t.Fatalf("failed to find token: %v", err)
	}
	if found3.HasExposed {
		t.Errorf("expected has_exposed to be false after second update")
	}

	// 4. 对不存在的 SN 更新 -> 返回 ErrAccessTokenNotFound
	if err := db.UpdateDeviceAccessTokenHasExposed(ctx, "NON-EXISTENT-SN", true); !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound, got: %v", err)
	}

	// 5. 空 SN 参数校验
	if err := db.UpdateDeviceAccessTokenHasExposed(ctx, "  ", true); !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}

	// 6. nil DB 安全校验
	var nilDB *database.Database
	if err := nilDB.UpdateDeviceAccessTokenHasExposed(ctx, sn, true); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
}

func TestDeviceAccessToken_DeleteBySerialNumber(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sn := "SN-DELETE-001"
	token := &database.DeviceAccessToken{
		SerialNumber: sn,
		AccessToken:  "token-for-delete",
		IssuedAt:     time.Now(),
	}
	if err := db.CreateDeviceAccessToken(ctx, token); err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	if err := db.DeleteDeviceAccessTokenBySerialNumber(ctx, sn); err != nil {
		t.Fatalf("failed to delete token: %v", err)
	}

	_, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Fatalf("expected ErrAccessTokenNotFound after delete, got: %v", err)
	}

	// 再次删除不存在的 SN -> 返回 ErrAccessTokenNotFound
	err = db.DeleteDeviceAccessTokenBySerialNumber(ctx, sn)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Fatalf("expected ErrAccessTokenNotFound on deleting non-existent token, got: %v", err)
	}
}

func TestDeviceAccessToken_IsValidHelper(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	var nilToken *database.DeviceAccessToken
	if nilToken.IsValid(now) {
		t.Errorf("expected nil token IsValid to return false")
	}

	// 永久有效且未撤销
	validForever := &database.DeviceAccessToken{}
	if !validForever.IsValid(now) {
		t.Errorf("expected token with no expires/revoked to be valid")
	}

	// 未过期
	validExp := &database.DeviceAccessToken{ExpiresAt: &future}
	if !validExp.IsValid(now) {
		t.Errorf("expected future expires_at token to be valid")
	}

	// 已过期
	expired := &database.DeviceAccessToken{ExpiresAt: &past}
	if expired.IsValid(now) {
		t.Errorf("expected past expires_at token to be invalid")
	}

	// 已撤销
	revoked := &database.DeviceAccessToken{RevokedAt: &past}
	if revoked.IsValid(now) {
		t.Errorf("expected revoked token to be invalid")
	}
}

func TestDeviceAccessToken_ValidationErrors(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Create 校验
	if err := db.CreateDeviceAccessToken(ctx, nil); !errors.Is(err, database.ErrInvalidAccessTokenRecord) {
		t.Errorf("expected ErrInvalidAccessTokenRecord, got: %v", err)
	}
	if err := db.CreateDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: "",
		AccessToken:  "token-test",
	}); !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	if err := db.CreateDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: "SN-TEST",
		AccessToken:  "",
	}); !errors.Is(err, database.ErrEmptyAccessToken) {
		t.Errorf("expected ErrEmptyAccessToken, got: %v", err)
	}

	// 2. Upsert 校验
	if err := db.UpsertDeviceAccessToken(ctx, nil); !errors.Is(err, database.ErrInvalidAccessTokenRecord) {
		t.Errorf("expected ErrInvalidAccessTokenRecord, got: %v", err)
	}
	if err := db.UpsertDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: "",
		AccessToken:  "token-test",
	}); !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	if err := db.UpsertDeviceAccessToken(ctx, &database.DeviceAccessToken{
		SerialNumber: "SN-TEST",
		AccessToken:  "",
	}); !errors.Is(err, database.ErrEmptyAccessToken) {
		t.Errorf("expected ErrEmptyAccessToken, got: %v", err)
	}

	// 3. Find 校验
	if _, err := db.FindDeviceAccessTokenByAccessToken(ctx, ""); !errors.Is(err, database.ErrEmptyAccessToken) {
		t.Errorf("expected ErrEmptyAccessToken, got: %v", err)
	}
	if _, err := db.FindDeviceAccessTokenBySerialNumber(ctx, "  "); !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
	if _, err := db.FindDeviceAccessTokenByID(ctx, 0); !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound for id 0, got: %v", err)
	}

	// 4. Revoke 校验
	if err := db.RevokeDeviceAccessTokenByAccessToken(ctx, "", time.Now()); !errors.Is(err, database.ErrEmptyAccessToken) {
		t.Errorf("expected ErrEmptyAccessToken, got: %v", err)
	}
	if err := db.RevokeDeviceAccessTokenBySerialNumber(ctx, "", time.Now()); !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}

	// 5. Delete 校验
	if err := db.DeleteDeviceAccessTokenBySerialNumber(ctx, ""); !errors.Is(err, database.ErrEmptySerialNumber) {
		t.Errorf("expected ErrEmptySerialNumber, got: %v", err)
	}
}

func TestDeviceAccessToken_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	_, err := db.FindDeviceAccessTokenByAccessToken(ctx, "non-existent-token")
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound, got: %v", err)
	}

	_, err = db.FindDeviceAccessTokenBySerialNumber(ctx, "NON-EXISTENT-SN")
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound, got: %v", err)
	}

	_, err = db.FindDeviceAccessTokenByID(ctx, 999999)
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound, got: %v", err)
	}

	err = db.RevokeDeviceAccessTokenByAccessToken(ctx, "non-existent-token", time.Now())
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound, got: %v", err)
	}

	err = db.RevokeDeviceAccessTokenBySerialNumber(ctx, "NON-EXISTENT-SN", time.Now())
	if !errors.Is(err, database.ErrAccessTokenNotFound) {
		t.Errorf("expected ErrAccessTokenNotFound, got: %v", err)
	}
}

func TestDeviceAccessToken_NilDatabaseSafety(t *testing.T) {
	var nilDB *database.Database
	ctx := context.Background()

	if _, err := nilDB.FindDeviceAccessTokenByAccessToken(ctx, "test-token"); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
	if _, err := nilDB.FindDeviceAccessTokenBySerialNumber(ctx, "SN-001"); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
	if _, err := nilDB.FindDeviceAccessTokenByID(ctx, 1); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
	if err := nilDB.CreateDeviceAccessToken(ctx, &database.DeviceAccessToken{}); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
	if err := nilDB.UpsertDeviceAccessToken(ctx, &database.DeviceAccessToken{}); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
	if err := nilDB.RevokeDeviceAccessTokenByAccessToken(ctx, "test-token", time.Now()); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
	if err := nilDB.RevokeDeviceAccessTokenBySerialNumber(ctx, "SN-001", time.Now()); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
	if err := nilDB.DeleteDeviceAccessTokenBySerialNumber(ctx, "SN-001"); !errors.Is(err, database.ErrDatabaseInstanceRequired) {
		t.Errorf("expected ErrDatabaseInstanceRequired, got: %v", err)
	}
}

func TestDeviceAccessToken_ContextCanceled(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := db.FindDeviceAccessTokenByAccessToken(ctx, "test-token")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.FindDeviceAccessTokenBySerialNumber(ctx, "SN-001")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}

	_, err = db.FindDeviceAccessTokenByID(ctx, 1)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
}
