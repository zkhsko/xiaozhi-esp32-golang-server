package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

func TestUserHandler_BindDevice_WithoutSNInCode(t *testing.T) {
	const (
		testToken = "secret-token-user-bind-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	db := setupTestDB(t)
	ctx := context.Background()

	calcHMAC := func(key []byte, msg string) string {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(msg))
		return hex.EncodeToString(mac.Sum(nil))
	}

	t.Run("1. Bind device with direct key HMAC match: creates activation, binds to user 1 and cleans cache", func(t *testing.T) {
		sn := "SN-MANUAL-DEVICE-001"
		key := []byte("secret-manual-hmac-key-001")
		deviceID := "AA:BB:CC:DD:EE:01"
		clientID := "client-uuid-001"

		// 预置凭证表
		cred := &database.DeviceHmacCredential{
			SerialNumber:      sn,
			AuthMethod:        database.AuthMethodManualCodeHMAC,
			HMACKeyCiphertext: key,
			CredentialStatus:  database.CredentialStatusEnabled,
		}
		if err := db.CreateDeviceHmacCredential(ctx, cred); err != nil {
			t.Fatalf("failed to create seed credential: %v", err)
		}

		var logBuf bytes.Buffer
		testLogger := logger.New(&logBuf, slog.LevelDebug)
		otaHandler := NewOTAHandler(cfg, db, testLogger)
		userHandler := NewUserHandler(cfg, db, otaHandler, testLogger)
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		// 步骤 1：无 SN 设备请求 OTA 获得 6 位 code
		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", deviceID)
		otaReq.Header.Set("Client-Id", clientID)
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)
		if otaRec.Code != http.StatusOK {
			t.Fatalf("expected OTA status 200, got %d", otaRec.Code)
		}

		var otaResp Response
		if err := json.Unmarshal(otaRec.Body.Bytes(), &otaResp); err != nil {
			t.Fatalf("failed to unmarshal ota response: %v", err)
		}
		code := otaResp.Activation.Code
		if len(code) != 6 {
			t.Fatalf("expected 6-digit code, got %q", code)
		}

		// 步骤 2：用户调用 POST /user-api/device/bind 传入 code, sn, hmac (直接传密钥)
		bindBody := BindDeviceRequest{
			Code: code,
			SN:   sn,
			HMAC: string(key),
		}
		bodyBytes, _ := json.Marshal(bindBody)

		bindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", bytes.NewReader(bodyBytes))
		bindReq.Header.Set("Content-Type", "application/json")
		bindRec := httptest.NewRecorder()
		r.ServeHTTP(bindRec, bindReq)

		if bindRec.Code != http.StatusOK {
			t.Fatalf("expected bind status 200, got %d, body: %s", bindRec.Code, bindRec.Body.String())
		}

		var bindResp BindDeviceResponse
		if err := json.Unmarshal(bindRec.Body.Bytes(), &bindResp); err != nil {
			t.Fatalf("failed to unmarshal bind response: %v", err)
		}
		if !bindResp.Success {
			t.Errorf("expected success to be true")
		}
		if bindResp.SerialNumber != sn {
			t.Errorf("expected serial_number %q, got %q", sn, bindResp.SerialNumber)
		}
		if bindResp.UserID != MockCurrentUserID {
			t.Errorf("expected user_id %d, got %d", MockCurrentUserID, bindResp.UserID)
		}
		if bindResp.DeviceID != deviceID {
			t.Errorf("expected device_id %q, got %q", deviceID, bindResp.DeviceID)
		}

		// 步骤 3：验证数据库 device_activation 表已插入
		act, err := db.FindDeviceActivationBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("expected activation record to exist: %v", err)
		}
		if act.DeviceID != deviceID || act.ClientID != clientID || act.ActivationStatus != database.ActivationStatusActive {
			t.Errorf("unexpected activation record: %+v", act)
		}

		// 步骤 4：验证数据库 device_user_ref 表已绑定到用户 1
		userRef, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("expected device user ref to exist: %v", err)
		}
		if userRef.UserID != MockCurrentUserID {
			t.Errorf("expected bound user_id %d, got %d", MockCurrentUserID, userRef.UserID)
		}

		// 步骤 5：验证凭证状态已更新为 activated
		credUpdated, err := db.FindDeviceHmacCredentialBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("expected credential to exist: %v", err)
		}
		if credUpdated.CredentialStatus != database.CredentialStatusActivated {
			t.Errorf("expected credential status 'activated', got %q", credUpdated.CredentialStatus)
		}

		// 步骤 6：验证 code 缓存已被清理（不能二次重复使用）
		_, ok := otaHandler.FindPendingActivationByCode(code)
		if ok {
			t.Errorf("expected pending activation code to be deleted after successful bind")
		}

		// 步骤 7：验证 device_access_token 表已生成 64 位双 UUID token 且 has_exposed 为 false
		tok, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("expected device access token to exist: %v", err)
		}
		if len(tok.AccessToken) != 64 {
			t.Errorf("expected 64-character access token, got length %d: %q", len(tok.AccessToken), tok.AccessToken)
		}
		if tok.HasExposed {
			t.Errorf("expected has_exposed to be false initially after binding")
		}
	})

	t.Run("2. Bind device with challenge HMAC match: succeeds on /user-api/device/bind", func(t *testing.T) {
		sn := "SN-MANUAL-DEVICE-002"
		key := []byte("secret-manual-hmac-key-002")
		deviceID := "AA:BB:CC:DD:EE:02"
		clientID := "client-uuid-002"

		cred := &database.DeviceHmacCredential{
			SerialNumber:      sn,
			AuthMethod:        database.AuthMethodManualCodeHMAC,
			HMACKeyCiphertext: key,
			CredentialStatus:  database.CredentialStatusEnabled,
		}
		if err := db.CreateDeviceHmacCredential(ctx, cred); err != nil {
			t.Fatalf("failed to create seed credential: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", deviceID)
		otaReq.Header.Set("Client-Id", clientID)
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)
		if otaRec.Code != http.StatusOK {
			t.Fatalf("expected OTA status 200, got %d", otaRec.Code)
		}

		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code

		// 获取内部保存的 challenge 并计算 HMAC
		pending, ok := otaHandler.FindPendingActivationByCode(code)
		if !ok {
			t.Fatalf("pending activation not found")
		}
		hmacHex := calcHMAC(key, pending.Challenge)

		// 调用 POST /user-api/device/bind
		bindBody := BindDeviceRequest{
			Code:         code,
			SerialNumber: sn, // 兼容 serial_number 别名
			HMAC:         hmacHex,
		}
		bodyBytes, _ := json.Marshal(bindBody)

		bindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", bytes.NewReader(bodyBytes))
		bindReq.Header.Set("Content-Type", "application/json")
		bindRec := httptest.NewRecorder()
		r.ServeHTTP(bindRec, bindReq)

		if bindRec.Code != http.StatusOK {
			t.Fatalf("expected bind status 200, got %d, body: %s", bindRec.Code, bindRec.Body.String())
		}

		userRef, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
		if err != nil || userRef.UserID != MockCurrentUserID {
			t.Fatalf("expected binding to user 1, got %+v, err: %v", userRef, err)
		}
	})

	t.Run("3. Bind device with code HMAC match: succeeds on /user-api/device/bind", func(t *testing.T) {
		sn := "SN-MANUAL-DEVICE-003"
		key := []byte("secret-manual-hmac-key-003")
		deviceID := "AA:BB:CC:DD:EE:03"
		clientID := "client-uuid-003"

		cred := &database.DeviceHmacCredential{
			SerialNumber:      sn,
			AuthMethod:        database.AuthMethodManualCodeHMAC,
			HMACKeyCiphertext: key,
			CredentialStatus:  database.CredentialStatusEnabled,
		}
		if err := db.CreateDeviceHmacCredential(ctx, cred); err != nil {
			t.Fatalf("failed to create seed credential: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", deviceID)
		otaReq.Header.Set("Client-Id", clientID)
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)
		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code

		hmacCodeHex := calcHMAC(key, code)

		bindBody := BindDeviceRequest{
			Code: code,
			SN:   sn,
			HMAC: hmacCodeHex,
		}
		bodyBytes, _ := json.Marshal(bindBody)

		bindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", bytes.NewReader(bodyBytes))
		bindRec := httptest.NewRecorder()
		r.ServeHTTP(bindRec, bindReq)

		if bindRec.Code != http.StatusOK {
			t.Fatalf("expected bind status 200, got %d, body: %s", bindRec.Code, bindRec.Body.String())
		}
	})

	t.Run("4. Missing SN or HMAC when code has no SN returns 400 Bad Request", func(t *testing.T) {
		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", "DEV-NO-SN-004")
		otaReq.Header.Set("Client-Id", "CLI-NO-SN-004")
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)
		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code

		// 缺少 SN
		reqNoSN := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{"code":"`+code+`","hmac":"some-hmac"}`))
		recNoSN := httptest.NewRecorder()
		r.ServeHTTP(recNoSN, reqNoSN)
		if recNoSN.Code != http.StatusBadRequest {
			t.Errorf("expected 400 when SN is missing, got %d", recNoSN.Code)
		}

		// 缺少 HMAC
		reqNoHMAC := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{"code":"`+code+`","sn":"SN-TEST"}`))
		recNoHMAC := httptest.NewRecorder()
		r.ServeHTTP(recNoHMAC, reqNoHMAC)
		if recNoHMAC.Code != http.StatusBadRequest {
			t.Errorf("expected 400 when HMAC is missing, got %d", recNoHMAC.Code)
		}
	})

	t.Run("5. SN not in device_hmac_credential returns 403 Forbidden", func(t *testing.T) {
		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", "DEV-NO-SN-005")
		otaReq.Header.Set("Client-Id", "CLI-NO-SN-005")
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)
		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code

		req := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{"code":"`+code+`","sn":"SN-NOT-EXIST-999","hmac":"abc"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 when credential not found, got %d", rec.Code)
		}
	})

	t.Run("6. Blocked credential returns 403 Forbidden", func(t *testing.T) {
		sn := "SN-BLOCKED-USER-BIND"
		key := []byte("secret-key-blocked")

		cred := &database.DeviceHmacCredential{
			SerialNumber:      sn,
			AuthMethod:        database.AuthMethodManualCodeHMAC,
			HMACKeyCiphertext: key,
			CredentialStatus:  database.CredentialStatusBlocked,
		}
		if err := db.CreateDeviceHmacCredential(ctx, cred); err != nil {
			t.Fatalf("failed to create blocked credential: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", "DEV-BLOCKED-006")
		otaReq.Header.Set("Client-Id", "CLI-BLOCKED-006")
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)
		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code

		req := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{"code":"`+code+`","sn":"`+sn+`","hmac":"`+string(key)+`"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for blocked credential, got %d", rec.Code)
		}
	})

	t.Run("7. Wrong HMAC returns 403 Forbidden", func(t *testing.T) {
		sn := "SN-WRONG-HMAC-007"
		key := []byte("secret-key-007")

		cred := &database.DeviceHmacCredential{
			SerialNumber:      sn,
			AuthMethod:        database.AuthMethodManualCodeHMAC,
			HMACKeyCiphertext: key,
			CredentialStatus:  database.CredentialStatusEnabled,
		}
		if err := db.CreateDeviceHmacCredential(ctx, cred); err != nil {
			t.Fatalf("failed to create credential: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", "DEV-WRONG-HMAC-007")
		otaReq.Header.Set("Client-Id", "CLI-WRONG-HMAC-007")
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)
		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code

		req := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{"code":"`+code+`","sn":"`+sn+`","hmac":"wrong-hmac-value"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 on wrong HMAC, got %d", rec.Code)
		}
	})
}

func TestUserHandler_BindDevice_WithSNInCode(t *testing.T) {
	const (
		testToken = "secret-token-user-bind-sn-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	db := setupTestDB(t)
	ctx := context.Background()

	t.Run("1. Code has SN: binds directly without sn and hmac parameters", func(t *testing.T) {
		sn := "SN-PRE-ACTIVATED-DEVICE-001"
		deviceID := "DEV-ESP32-HARDWARE-001"
		clientID := "CLI-HARDWARE-001"

		// 预置已激活记录（但未绑定用户）
		actRecord := &database.DeviceActivation{
			SerialNumber:     sn,
			DeviceID:         deviceID,
			ClientID:         clientID,
			ActivationStatus: database.ActivationStatusActive,
			ActivatedAt:      time.Now(),
		}
		if err := db.CreateDeviceActivation(ctx, actRecord); err != nil {
			t.Fatalf("failed to create activation: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		// 设备请求 OTA（带 Serial-Number 请求头），由于已激活未绑定，返回 6 位激活码
		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Serial-Number", sn)
		otaReq.Header.Set("Device-Id", deviceID)
		otaReq.Header.Set("Client-Id", clientID)
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)

		if otaRec.Code != http.StatusOK {
			t.Fatalf("expected OTA status 200, got %d", otaRec.Code)
		}

		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code
		if len(code) != 6 {
			t.Fatalf("expected 6-digit code, got %q", code)
		}

		// 用户绑定：只传 code，不需要传 sn 和 hmac
		bindBody := BindDeviceRequest{
			Code: code,
		}
		bodyBytes, _ := json.Marshal(bindBody)

		bindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", bytes.NewReader(bodyBytes))
		bindReq.Header.Set("Content-Type", "application/json")
		bindRec := httptest.NewRecorder()
		r.ServeHTTP(bindRec, bindReq)

		if bindRec.Code != http.StatusOK {
			t.Fatalf("expected bind status 200, got %d, body: %s", bindRec.Code, bindRec.Body.String())
		}

		var bindResp BindDeviceResponse
		_ = json.Unmarshal(bindRec.Body.Bytes(), &bindResp)
		if !bindResp.Success {
			t.Errorf("expected success true")
		}
		if bindResp.SerialNumber != sn {
			t.Errorf("expected serial_number %q, got %q", sn, bindResp.SerialNumber)
		}
		if bindResp.UserID != MockCurrentUserID {
			t.Errorf("expected user_id %d, got %d", MockCurrentUserID, bindResp.UserID)
		}

		// 验证数据库绑定记录已存在
		userRef, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("expected user binding record to exist: %v", err)
		}
		if userRef.UserID != MockCurrentUserID {
			t.Errorf("expected user_id %d, got %d", MockCurrentUserID, userRef.UserID)
		}

		// 验证激活码已被清理
		_, ok := otaHandler.FindPendingActivationByCode(code)
		if ok {
			t.Errorf("expected pending activation code to be cleaned up")
		}

		// 验证 device_access_token 表已生成 64 位双 UUID token 且 has_exposed 为 false
		tok, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("expected device access token to exist: %v", err)
		}
		if len(tok.AccessToken) != 64 {
			t.Errorf("expected 64-character access token, got length %d: %q", len(tok.AccessToken), tok.AccessToken)
		}
		if tok.HasExposed {
			t.Errorf("expected has_exposed to be false initially after binding")
		}
	})

	t.Run("2. Code has SN: re-binding updates user binding to user 1", func(t *testing.T) {
		sn := "SN-REBIND-DEVICE-002"
		deviceID := "DEV-REBIND-002"
		clientID := "CLI-REBIND-002"

		actRecord := &database.DeviceActivation{
			SerialNumber:     sn,
			DeviceID:         deviceID,
			ClientID:         clientID,
			ActivationStatus: database.ActivationStatusActive,
			ActivatedAt:      time.Now(),
		}
		if err := db.CreateDeviceActivation(ctx, actRecord); err != nil {
			t.Fatalf("failed to create activation: %v", err)
		}

		// 预先绑定了其他用户 9999
		if _, err := db.BindDevice(ctx, sn, 9999); err != nil {
			t.Fatalf("failed to bind old user: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		// 手动在 otaHandler 生成待绑定 code（模拟重新配对绑定）
		pending, err := otaHandler.createPendingActivation(sn, deviceID, clientID)
		if err != nil {
			t.Fatalf("failed to create pending: %v", err)
		}

		bindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{"code":"`+pending.Code+`"}`))
		bindRec := httptest.NewRecorder()
		r.ServeHTTP(bindRec, bindReq)

		if bindRec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d, body: %s", bindRec.Code, bindRec.Body.String())
		}

		userRef, err := db.FindDeviceUserRefBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("failed to find user ref: %v", err)
		}
		if userRef.UserID != MockCurrentUserID {
			t.Errorf("expected user_id updated to %d, got %d", MockCurrentUserID, userRef.UserID)
		}
	})
}

func TestUserHandler_BindDevice_CommonErrors(t *testing.T) {
	const (
		testToken = "secret-token-common-errors"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	db := setupTestDB(t)

	otaHandler := NewOTAHandler(cfg, db, slog.Default())
	userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
	r := NewRouter(Options{
		OTA:  otaHandler,
		User: userHandler,
	})

	t.Run("Empty code returns 400 Bad Request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for empty code, got %d", rec.Code)
		}
	})

	t.Run("Non-existent or expired code returns 400 Bad Request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`{"code":"999999"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for non-existent code, got %d", rec.Code)
		}
	})

	t.Run("GET method on /user-api/device/bind returns 405 Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/user-api/device/bind", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405 for GET on /user-api/device/bind, got %d", rec.Code)
		}
	})

	t.Run("PUT method on /user-api/device/bind returns 405 Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/user-api/device/bind", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405 for PUT on /user-api/device/bind, got %d", rec.Code)
		}
	})

	t.Run("Invalid JSON body returns 400 Bad Request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", strings.NewReader(`invalid-json-string`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid JSON body, got %d", rec.Code)
		}
	})

	t.Run("URL Query parameter fallback works when JSON body is empty", func(t *testing.T) {
		pending, err := otaHandler.createPendingActivation("SN-QUERY-PARAM-001", "DEV-01", "CLI-01")
		if err != nil {
			t.Fatalf("failed to create pending: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/user-api/device/bind?code="+pending.Code, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for URL query param, got %d, body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestUserHandler_BindAndOTATokenExposure 验证绑定设备后，OTA 接口在第一次全部检验通过时展示 Token，之后不再展示；重新绑定后重置标记可再次展示一次。
func TestUserHandler_BindAndOTATokenExposure(t *testing.T) {
	const (
		testToken = "secret-token-ota-expose-test"
		testWsURL = "wss://test.example.com/xiaozhi/v1/"
	)

	cfg := newTestConfig(testToken, testWsURL)
	db := setupTestDB(t)
	ctx := context.Background()

	t.Run("Standard SN device: Token is exposed once on first OTA check, hidden on subsequent checks, reset on rebind", func(t *testing.T) {
		sn := "SN-OTA-EXPOSE-001"
		deviceID := "DEV-ESP32-EXPOSE-001"
		clientID := "CLI-EXPOSE-001"

		// 1. 预置激活记录（未绑定状态）
		actRecord := &database.DeviceActivation{
			SerialNumber:     sn,
			DeviceID:         deviceID,
			ClientID:         clientID,
			ActivationStatus: database.ActivationStatusActive,
			ActivatedAt:      time.Now(),
		}
		if err := db.CreateDeviceActivation(ctx, actRecord); err != nil {
			t.Fatalf("failed to create activation: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		// 2. 设备发起 OTA 获取激活码
		otaReq1 := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq1.Header.Set("Serial-Number", sn)
		otaReq1.Header.Set("Device-Id", deviceID)
		otaReq1.Header.Set("Client-Id", clientID)
		otaRec1 := httptest.NewRecorder()
		r.ServeHTTP(otaRec1, otaReq1)

		var otaResp1 Response
		_ = json.Unmarshal(otaRec1.Body.Bytes(), &otaResp1)
		code1 := otaResp1.Activation.Code
		if len(code1) != 6 {
			t.Fatalf("expected 6-digit code, got %q", code1)
		}

		// 3. 用户进行绑定
		bindBody := BindDeviceRequest{Code: code1}
		bodyBytes, _ := json.Marshal(bindBody)
		bindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", bytes.NewReader(bodyBytes))
		bindReq.Header.Set("Content-Type", "application/json")
		bindRec := httptest.NewRecorder()
		r.ServeHTTP(bindRec, bindReq)

		if bindRec.Code != http.StatusOK {
			t.Fatalf("expected bind status 200, got %d, body: %s", bindRec.Code, bindRec.Body.String())
		}

		// 验证数据库 Token 记录生成
		tokRecord1, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("failed to find device access token: %v", err)
		}
		if len(tokRecord1.AccessToken) != 64 {
			t.Fatalf("expected 64-char token, got length %d: %q", len(tokRecord1.AccessToken), tokRecord1.AccessToken)
		}
		if tokRecord1.HasExposed {
			t.Fatalf("expected has_exposed to be false initially")
		}

		// 4. 绑定后设备第一次发起 OTA 请求（检验全部通过） -> 应该下发 Token
		firstOTAReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		firstOTAReq.Header.Set("Serial-Number", sn)
		firstOTAReq.Header.Set("Device-Id", deviceID)
		firstOTAReq.Header.Set("Client-Id", clientID)
		firstOTARec := httptest.NewRecorder()
		r.ServeHTTP(firstOTARec, firstOTAReq)

		if firstOTARec.Code != http.StatusOK {
			t.Fatalf("expected first OTA status 200, got %d", firstOTARec.Code)
		}

		var firstOTAResp Response
		if err := json.Unmarshal(firstOTARec.Body.Bytes(), &firstOTAResp); err != nil {
			t.Fatalf("failed to unmarshal first OTA response: %v", err)
		}
		if firstOTAResp.WebSocket == nil {
			t.Fatalf("expected websocket config in first OTA response")
		}
		if firstOTAResp.WebSocket.Token != tokRecord1.AccessToken {
			t.Errorf("expected WebSocket Token %q, got %q", tokRecord1.AccessToken, firstOTAResp.WebSocket.Token)
		}

		// 验证响应 JSON 中显式包含 "token" 键
		var firstRawMap map[string]json.RawMessage
		_ = json.Unmarshal(firstOTARec.Body.Bytes(), &firstRawMap)
		var firstWSMap map[string]any
		_ = json.Unmarshal(firstRawMap["websocket"], &firstWSMap)
		if _, ok := firstWSMap["token"]; !ok {
			t.Errorf("expected 'token' field to be present in first OTA websocket response JSON")
		}

		// 验证数据库中标记已被更新为 has_exposed = true
		tokRecordAfterFirst, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("failed to find token after first OTA: %v", err)
		}
		if !tokRecordAfterFirst.HasExposed {
			t.Errorf("expected has_exposed to be true in database after first OTA")
		}

		// 5. 设备第二次发起 OTA 请求 -> 不再展示 Token
		secondOTAReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		secondOTAReq.Header.Set("Serial-Number", sn)
		secondOTAReq.Header.Set("Device-Id", deviceID)
		secondOTAReq.Header.Set("Client-Id", clientID)
		secondOTARec := httptest.NewRecorder()
		r.ServeHTTP(secondOTARec, secondOTAReq)

		if secondOTARec.Code != http.StatusOK {
			t.Fatalf("expected second OTA status 200, got %d", secondOTARec.Code)
		}

		var secondOTAResp Response
		if err := json.Unmarshal(secondOTARec.Body.Bytes(), &secondOTAResp); err != nil {
			t.Fatalf("failed to unmarshal second OTA response: %v", err)
		}
		if secondOTAResp.WebSocket == nil {
			t.Fatalf("expected websocket config in second OTA response")
		}
		if secondOTAResp.WebSocket.Token != "" {
			t.Errorf("expected empty token in second OTA response, got %q", secondOTAResp.WebSocket.Token)
		}

		// 验证响应 JSON 中由于 omitempty 不再出现 "token" 字段
		var secondRawMap map[string]json.RawMessage
		_ = json.Unmarshal(secondOTARec.Body.Bytes(), &secondRawMap)
		var secondWSMap map[string]any
		_ = json.Unmarshal(secondRawMap["websocket"], &secondWSMap)
		if _, ok := secondWSMap["token"]; ok {
			t.Errorf("expected 'token' field to be absent in second OTA websocket response JSON, got %v", secondWSMap["token"])
		}

		// 6. 设备重新绑定 -> 模拟重新配对生成新激活码并绑定
		pendingRebind, err := otaHandler.createPendingActivation(sn, deviceID, clientID)
		if err != nil {
			t.Fatalf("failed to create pending activation for rebind: %v", err)
		}

		rebindBody := BindDeviceRequest{Code: pendingRebind.Code}
		rebindBytes, _ := json.Marshal(rebindBody)
		rebindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", bytes.NewReader(rebindBytes))
		rebindReq.Header.Set("Content-Type", "application/json")
		rebindRec := httptest.NewRecorder()
		r.ServeHTTP(rebindRec, rebindReq)

		if rebindRec.Code != http.StatusOK {
			t.Fatalf("expected rebind status 200, got %d", rebindRec.Code)
		}

		// 验证重新绑定后生成了新 Token 且 has_exposed 重置为 false
		tokRecordRebind, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("failed to find token after rebind: %v", err)
		}
		if tokRecordRebind.AccessToken == tokRecord1.AccessToken {
			t.Errorf("expected new token generated after rebind")
		}
		if tokRecordRebind.HasExposed {
			t.Errorf("expected has_exposed to be reset to false after rebind")
		}

		// 7. 重新绑定后设备发起 OTA -> 再次仅在第一次展示新 Token
		rebindOTAReq1 := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		rebindOTAReq1.Header.Set("Serial-Number", sn)
		rebindOTAReq1.Header.Set("Device-Id", deviceID)
		rebindOTAReq1.Header.Set("Client-Id", clientID)
		rebindOTARec1 := httptest.NewRecorder()
		r.ServeHTTP(rebindOTARec1, rebindOTAReq1)

		var rebindOTAResp1 Response
		_ = json.Unmarshal(rebindOTARec1.Body.Bytes(), &rebindOTAResp1)
		if rebindOTAResp1.WebSocket.Token != tokRecordRebind.AccessToken {
			t.Errorf("expected new token %q on first OTA after rebind, got %q", tokRecordRebind.AccessToken, rebindOTAResp1.WebSocket.Token)
		}

		// 紧接着下一次 OTA -> 不再展示
		rebindOTAReq2 := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		rebindOTAReq2.Header.Set("Serial-Number", sn)
		rebindOTAReq2.Header.Set("Device-Id", deviceID)
		rebindOTAReq2.Header.Set("Client-Id", clientID)
		rebindOTARec2 := httptest.NewRecorder()
		r.ServeHTTP(rebindOTARec2, rebindOTAReq2)

		var rebindOTAResp2 Response
		_ = json.Unmarshal(rebindOTARec2.Body.Bytes(), &rebindOTAResp2)
		if rebindOTAResp2.WebSocket.Token != "" {
			t.Errorf("expected empty token on second OTA after rebind, got %q", rebindOTAResp2.WebSocket.Token)
		}
	})

	t.Run("Legacy device without SN: Token is exposed once on first OTA check, hidden on subsequent checks", func(t *testing.T) {
		sn := "SN-LEGACY-EXPOSE-002"
		key := []byte("secret-legacy-expose-key-002")
		deviceID := "DEV-LEGACY-002"
		clientID := "CLI-LEGACY-002"

		// 预置凭证表
		cred := &database.DeviceHmacCredential{
			SerialNumber:      sn,
			AuthMethod:        database.AuthMethodManualCodeHMAC,
			HMACKeyCiphertext: key,
			CredentialStatus:  database.CredentialStatusEnabled,
		}
		if err := db.CreateDeviceHmacCredential(ctx, cred); err != nil {
			t.Fatalf("failed to create seed credential: %v", err)
		}

		otaHandler := NewOTAHandler(cfg, db, slog.Default())
		userHandler := NewUserHandler(cfg, db, otaHandler, slog.Default())
		r := NewRouter(Options{
			OTA:  otaHandler,
			User: userHandler,
		})

		// 1. 无 SN 设备请求 OTA 获得 6 位 code
		otaReq := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		otaReq.Header.Set("Device-Id", deviceID)
		otaReq.Header.Set("Client-Id", clientID)
		otaRec := httptest.NewRecorder()
		r.ServeHTTP(otaRec, otaReq)

		var otaResp Response
		_ = json.Unmarshal(otaRec.Body.Bytes(), &otaResp)
		code := otaResp.Activation.Code

		// 2. 用户绑定（传入 sn 与 key 作为 HMAC）
		bindBody := BindDeviceRequest{
			Code: code,
			SN:   sn,
			HMAC: string(key),
		}
		bodyBytes, _ := json.Marshal(bindBody)
		bindReq := httptest.NewRequest(http.MethodPost, "/user-api/device/bind", bytes.NewReader(bodyBytes))
		bindReq.Header.Set("Content-Type", "application/json")
		bindRec := httptest.NewRecorder()
		r.ServeHTTP(bindRec, bindReq)
		if bindRec.Code != http.StatusOK {
			t.Fatalf("expected bind status 200, got %d", bindRec.Code)
		}

		tokRecord, err := db.FindDeviceAccessTokenBySerialNumber(ctx, sn)
		if err != nil {
			t.Fatalf("failed to find device access token: %v", err)
		}

		// 3. Legacy 设备第一次请求 OTA（不带 SN，仅带 device_id 和 client_id）
		legacyOTAReq1 := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		legacyOTAReq1.Header.Set("Device-Id", deviceID)
		legacyOTAReq1.Header.Set("Client-Id", clientID)
		legacyOTARec1 := httptest.NewRecorder()
		r.ServeHTTP(legacyOTARec1, legacyOTAReq1)

		var legacyOTAResp1 Response
		_ = json.Unmarshal(legacyOTARec1.Body.Bytes(), &legacyOTAResp1)
		if legacyOTAResp1.WebSocket == nil {
			t.Fatalf("expected websocket config in legacy OTA response")
		}
		if legacyOTAResp1.WebSocket.Token != tokRecord.AccessToken {
			t.Errorf("expected token %q in first legacy OTA, got %q", tokRecord.AccessToken, legacyOTAResp1.WebSocket.Token)
		}

		// 4. Legacy 设备第二次请求 OTA -> 不再返回 Token
		legacyOTAReq2 := httptest.NewRequest(http.MethodGet, "/xiaozhi/ota/", nil)
		legacyOTAReq2.Header.Set("Device-Id", deviceID)
		legacyOTAReq2.Header.Set("Client-Id", clientID)
		legacyOTARec2 := httptest.NewRecorder()
		r.ServeHTTP(legacyOTARec2, legacyOTAReq2)

		var legacyOTAResp2 Response
		_ = json.Unmarshal(legacyOTARec2.Body.Bytes(), &legacyOTAResp2)
		if legacyOTAResp2.WebSocket == nil {
			t.Fatalf("expected websocket config in second legacy OTA response")
		}
		if legacyOTAResp2.WebSocket.Token != "" {
			t.Errorf("expected empty token in second legacy OTA, got %q", legacyOTAResp2.WebSocket.Token)
		}
	})
}
