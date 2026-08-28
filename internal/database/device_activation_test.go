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
	testDeviceId := "11:22:33:44:55:66"
	testClientId := "client-001"

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

	act, err := db.ActivateDeviceBySerialNumber(ctx, testSN, testDeviceId, testClientId)
	if err != nil {
		t.Fatalf("ActivateDeviceBySerialNumber failed: %v", err)
	}
	if act.SerialNumber != testSN {
		t.Errorf("expected SN %q, got %q", testSN, act.SerialNumber)
	}
	if act.DeviceId != testDeviceId {
		t.Errorf("expected DeviceId %q, got %q", testDeviceId, act.DeviceId)
	}
	if act.ClientId != testClientId {
		t.Errorf("expected ClientId %q, got %q", testClientId, act.ClientId)
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
	testDeviceId := "11:22:33:44:55:66"
	testClientId := "client-001"

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

	_, err = db.ActivateDeviceBySerialNumber(ctx, testSN, testDeviceId, testClientId)
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
	if err != nil || ref.UserId != 42 {
		t.Fatalf("expected user binding 42, got ref: %+v, err: %v", ref, err)
	}
	tok, err := db.FindDeviceAccessTokenByAccessToken(ctx, oldToken)
	if err != nil || tok.AccessToken != oldToken {
		t.Fatalf("expected token found, got tok: %+v, err: %v", tok, err)
	}

	// 2. Reactivate with updated device_id / client_id
	newDeviceId := "AA:BB:CC:DD:EE:FF"
	newClientId := "client-002"
	act, err := db.ActivateDeviceBySerialNumber(ctx, testSN, newDeviceId, newClientId)
	if err != nil {
		t.Fatalf("reactivate ActivateDeviceBySerialNumber failed: %v", err)
	}
	if act.DeviceId != newDeviceId || act.ClientId != newClientId {
		t.Errorf("expected updated device/client Ids (%s, %s), got (%s, %s)",
			newDeviceId, newClientId, act.DeviceId, act.ClientId)
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
	testDeviceId := "11:22:33:44:55:66"
	testClientId := "client-001"

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

	_, err = db.ActivateDeviceBySerialNumber(canceledCtx, testSN, testDeviceId, testClientId)
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
	testDeviceId := "11:22:33:44:55:66"
	testClientId := "client-bind-001"
	testToken := "token-bind-11112222333344445555666677778888"
	testUserId := uint64(101)
	testDeviceType := "esp32-s3-robot"

	// Preset credential as enabled with specific device_type
	err := db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			AuthMethod:        AuthMethodEfuseHMAC,
			DeviceType:        testDeviceType,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			CredentialStatus:  CredentialStatusEnabled,
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	act, err := db.BindDeviceWithSN(ctx, testSN, testDeviceId, testClientId, testToken, testUserId)
	if err != nil {
		t.Fatalf("BindDeviceWithSN failed: %v", err)
	}

	// 1. Verify activation record
	if act.SerialNumber != testSN || act.DeviceId != testDeviceId || act.ClientId != testClientId {
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
	if ref.UserId != testUserId {
		t.Errorf("expected user_id %d, got %d", testUserId, ref.UserId)
	}

	// 3. Verify credential status transitioned to activated
	cred, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceHmacCredentialBySerialNumber failed: %v", err)
	}
	if cred.CredentialStatus != CredentialStatusActivated {
		t.Errorf("expected credential status %q, got %q", CredentialStatusActivated, cred.CredentialStatus)
	}

	// 4. Verify access token record and redundant device_type from production table
	tok, err := db.FindDeviceAccessTokenByAccessToken(ctx, testToken)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenByAccessToken failed: %v", err)
	}
	if tok.SerialNumber != testSN || tok.HasExposed {
		t.Errorf("unexpected access token record: %+v", tok)
	}
	if tok.DeviceType != testDeviceType {
		t.Errorf("expected token DeviceType %q, got %q", testDeviceType, tok.DeviceType)
	}
}

func TestBindDeviceWithSN_RebindUpdatesAllTables(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	testSN := "sn-rebind-db-001"
	oldDeviceId := "11:22:33:44:55:66"
	oldClientId := "client-old"
	oldToken := "old-token-11112222333344445555666677778888"
	oldUserId := uint64(10)

	// 1. Initial binding
	_, err := db.BindDeviceWithSN(ctx, testSN, oldDeviceId, oldClientId, oldToken, oldUserId)
	if err != nil {
		t.Fatalf("initial BindDeviceWithSN failed: %v", err)
	}

	// 2. Re-bind to a different user with new device/client/token
	newDeviceId := "AA:BB:CC:DD:EE:FF"
	newClientId := "client-new"
	newToken := "new-token-99998888777766665555444433332222"
	newUserId := uint64(20)

	act, err := db.BindDeviceWithSN(ctx, testSN, newDeviceId, newClientId, newToken, newUserId)
	if err != nil {
		t.Fatalf("rebind BindDeviceWithSN failed: %v", err)
	}

	// 3. Verify activation updated
	if act.DeviceId != newDeviceId || act.ClientId != newClientId {
		t.Errorf("expected updated device/client (%s, %s), got (%s, %s)",
			newDeviceId, newClientId, act.DeviceId, act.ClientId)
	}

	// 4. Verify user binding updated to new user
	ref, err := db.FindDeviceUserRefBySerialNumber(ctx, testSN)
	if err != nil {
		t.Fatalf("FindDeviceUserRefBySerialNumber failed: %v", err)
	}
	if ref.UserId != newUserId {
		t.Errorf("expected user_id %d, got %d", newUserId, ref.UserId)
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
	testDeviceId := "11:22:33:44:55:66"
	testClientId := "client-001"
	testToken := "token-rollback-1111222233334444"
	testUserId := uint64(99)

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

	_, err = db.BindDeviceWithSN(canceledCtx, testSN, testDeviceId, testClientId, testToken, testUserId)
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

	// Zero UserId
	_, err = db.BindDeviceWithSN(ctx, "sn", "dev", "cli", "tok", 0)
	if !errors.Is(err, ErrEmptyUserId) {
		t.Errorf("expected ErrEmptyUserId, got: %v", err)
	}
}

func TestBindDeviceWithSN_RedundantDeviceTypeFromCredential(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	testSN := "sn-device-type-001"
	expectedType := "mixgo-nova"

	// 1. Preset credential with custom device type
	err := db.BatchCreateDeviceHmacCredentials(ctx, []*DeviceHmacCredential{
		{
			SerialNumber:      testSN,
			DeviceType:        expectedType,
			HMACKeyCiphertext: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	})
	if err != nil {
		t.Fatalf("BatchCreateDeviceHmacCredentials failed: %v", err)
	}

	// 2. Bind and activate device
	testToken := "token-custom-dev-type-123456789012"
	_, err = db.BindDeviceWithSN(ctx, testSN, "dev-01", "cli-01", testToken, 1)
	if err != nil {
		t.Fatalf("BindDeviceWithSN failed: %v", err)
	}

	// 3. Verify token has the redundant device_type from device_hmac_credential
	tok, err := db.FindDeviceAccessTokenByAccessToken(ctx, testToken)
	if err != nil {
		t.Fatalf("FindDeviceAccessTokenByAccessToken failed: %v", err)
	}
	if tok.DeviceType != expectedType {
		t.Errorf("expected token device_type %q, got %q", expectedType, tok.DeviceType)
	}
}

func TestDeviceActivationCRUDAndFiltering(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Activate devices
	act1, err := db.ActivateDeviceBySerialNumber(ctx, "sn-act-crud-001", "dev-id-001", "client-id-001")
	if err != nil {
		t.Fatalf("ActivateDeviceBySerialNumber act1 failed: %v", err)
	}
	if act1.Id == 0 {
		t.Fatalf("expected non-zero Id for act1")
	}

	act2, err := db.ActivateDeviceBySerialNumber(ctx, "sn-act-crud-002", "dev-id-002", "client-id-002")
	if err != nil {
		t.Fatalf("ActivateDeviceBySerialNumber act2 failed: %v", err)
	}
	_ = db.UpdateDeviceActivation(ctx, act2.Id, map[string]any{"activation_status": ActivationStatusFrozen})

	// 2. List with pagination
	list, total, err := db.ListDeviceActivations(ctx, DeviceActivationFilter{
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListDeviceActivations failed: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total 2, got %d", total)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 items, got %d", len(list))
	}

	// 3. Filter by serial_number
	listSN, totalSN, err := db.ListDeviceActivations(ctx, DeviceActivationFilter{
		SerialNumber: "crud-001",
	})
	if err != nil {
		t.Fatalf("ListDeviceActivations by SN failed: %v", err)
	}
	if totalSN != 1 || len(listSN) != 1 || listSN[0].SerialNumber != "sn-act-crud-001" {
		t.Errorf("unexpected SN filter result: total=%d, list=%+v", totalSN, listSN)
	}

	// 4. Filter by status
	listStatus, totalStatus, err := db.ListDeviceActivations(ctx, DeviceActivationFilter{
		ActivationStatus: ActivationStatusFrozen,
	})
	if err != nil {
		t.Fatalf("ListDeviceActivations by Status failed: %v", err)
	}
	if totalStatus != 1 || listStatus[0].SerialNumber != "sn-act-crud-002" {
		t.Errorf("unexpected status filter result: total=%d, list=%+v", totalStatus, listStatus)
	}

	// 5. Update activation
	err = db.UpdateDeviceActivation(ctx, act1.Id, map[string]any{
		"device_id":         "dev-id-001-updated",
		"activation_status": ActivationStatusRevoked,
	})
	if err != nil {
		t.Fatalf("UpdateDeviceActivation failed: %v", err)
	}

	updatedAct, err := db.FindDeviceActivationBySerialNumber(ctx, "sn-act-crud-001")
	if err != nil {
		t.Fatalf("FindDeviceActivationBySerialNumber failed: %v", err)
	}
	if updatedAct.DeviceId != "dev-id-001-updated" {
		t.Errorf("expected updated device_id, got %q", updatedAct.DeviceId)
	}
	if updatedAct.ActivationStatus != ActivationStatusRevoked {
		t.Errorf("expected status revoked, got %q", updatedAct.ActivationStatus)
	}

	// 6. Delete single activation
	if err := db.DeleteDeviceActivation(ctx, act1.Id); err != nil {
		t.Fatalf("DeleteDeviceActivation failed: %v", err)
	}
	_, err = db.FindDeviceActivationBySerialNumber(ctx, "sn-act-crud-001")
	if !errors.Is(err, ErrActivationNotFound) {
		t.Errorf("expected ErrActivationNotFound after delete, got: %v", err)
	}

	// 7. Batch delete activations
	act3, _ := db.ActivateDeviceBySerialNumber(ctx, "sn-act-crud-003", "dev-id-003", "")

	if err := db.BatchDeleteDeviceActivations(ctx, []uint64{act2.Id, act3.Id}); err != nil {
		t.Fatalf("BatchDeleteDeviceActivations failed: %v", err)
	}

	_, totalRemaining, err := db.ListDeviceActivations(ctx, DeviceActivationFilter{})
	if err != nil {
		t.Fatalf("ListDeviceActivations after batch delete failed: %v", err)
	}
	if totalRemaining != 0 {
		t.Errorf("expected 0 remaining activations, got %d", totalRemaining)
	}
}
