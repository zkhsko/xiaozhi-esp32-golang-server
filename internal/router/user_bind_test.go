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
