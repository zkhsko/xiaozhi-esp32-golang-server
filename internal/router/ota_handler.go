package router

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"time"

	"github.com/jellydator/ttlcache/v3"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

const (
	// DefaultActivationCodeTTL 激活码默认有效时长（5分钟）。
	DefaultActivationCodeTTL = 5 * time.Minute
	// DefaultActivationCodeCapacity 待激活记录缓存最大容量。
	DefaultActivationCodeCapacity = 10000
	// DefaultActivationMessage 默认设备激活提示文案。
	DefaultActivationMessage = "请在管理后台绑定设备"
)

// PendingActivation 保存未激活设备在等待人工绑定期间的上下文信息。
type PendingActivation struct {
	Code         string    `json:"code"`
	Challenge    string    `json:"challenge"`
	SerialNumber string    `json:"serial_number"`
	DeviceID     string    `json:"device_id"`
	ClientID     string    `json:"client_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// OTAHandler 处理设备 OTA 配置发现及版本检查。
type OTAHandler struct {
	cfg            *config.Config
	db             *database.Database
	logger         *slog.Logger
	codeCache      *ttlcache.Cache[string, PendingActivation]
	challengeCache *ttlcache.Cache[string, PendingActivation]
}

// NewOTAHandler 创建 OTA 处理器实例。
func NewOTAHandler(cfg *config.Config, db *database.Database, l *slog.Logger) *OTAHandler {
	if l == nil {
		l = slog.Default()
	}

	codeCache := ttlcache.New[string, PendingActivation](
		ttlcache.WithTTL[string, PendingActivation](DefaultActivationCodeTTL),
		ttlcache.WithCapacity[string, PendingActivation](DefaultActivationCodeCapacity),
		ttlcache.WithDisableTouchOnHit[string, PendingActivation](),
	)

	challengeCache := ttlcache.New[string, PendingActivation](
		ttlcache.WithTTL[string, PendingActivation](DefaultActivationCodeTTL),
		ttlcache.WithCapacity[string, PendingActivation](DefaultActivationCodeCapacity),
		ttlcache.WithDisableTouchOnHit[string, PendingActivation](),
	)

	return &OTAHandler{
		cfg:            cfg,
		db:             db,
		logger:         l,
		codeCache:      codeCache,
		challengeCache: challengeCache,
	}
}

// createPendingActivation 生成新的 6 位随机激活码与 256-bit Challenge 并存入缓存。
func (h *OTAHandler) createPendingActivation(sn, deviceID, clientID string) (PendingActivation, error) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := generateActivationCode()
		if err != nil {
			return PendingActivation{}, err
		}

		if h.codeCache.Has(code) {
			continue
		}

		challenge, err := generateActivationChallenge()
		if err != nil {
			return PendingActivation{}, err
		}

		pending := PendingActivation{
			Code:         code,
			Challenge:    challenge,
			SerialNumber: sn,
			DeviceID:     deviceID,
			ClientID:     clientID,
			CreatedAt:    time.Now(),
		}

		h.codeCache.Set(code, pending, DefaultActivationCodeTTL)
		if h.challengeCache != nil {
			h.challengeCache.Set(challenge, pending, DefaultActivationCodeTTL)
		}
		return pending, nil
	}

	return PendingActivation{}, errors.New("failed to generate unique activation code after multiple attempts")
}

// FindPendingActivationByCode 根据激活码查询未过期的待激活信息。
func (h *OTAHandler) FindPendingActivationByCode(code string) (PendingActivation, bool) {
	if h.codeCache == nil {
		return PendingActivation{}, false
	}
	item := h.codeCache.Get(code)
	if item == nil {
		return PendingActivation{}, false
	}
	return item.Value(), true
}

// FindPendingActivationByChallenge 根据 Challenge 查询未过期的待激活信息。
func (h *OTAHandler) FindPendingActivationByChallenge(challenge string) (PendingActivation, bool) {
	if h.challengeCache == nil {
		return PendingActivation{}, false
	}
	item := h.challengeCache.Get(challenge)
	if item == nil {
		return PendingActivation{}, false
	}
	return item.Value(), true
}

// generateActivationCode 使用 crypto/rand 生成 6 位随机数字字符串（000000 - 999999）。
func generateActivationCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("crypto rand failed: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// generateActivationChallenge 使用 crypto/rand 生成 256 位（32 字节）安全随机 Challenge 十六进制字符串。
func generateActivationChallenge() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto rand failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// handleOTA 处理设备 OTA 配置检查/版本发现入口，根据请求头中的 Serial-Number 分流。
func (h *OTAHandler) handleOTA(w http.ResponseWriter, r *http.Request) {
	headers, body, ok := h.readAndValidateOTARequest(w, r)
	if !ok {
		return
	}

	h.logger.Info("ota check request received",
		"method", r.Method,
		"path", r.URL.Path,
		"device_id", logger.TruncateString(headers.DeviceID),
		"client_id", logger.TruncateString(headers.ClientID),
		"serial_number", logger.TruncateString(headers.SerialNumber),
		"activation_version", logger.TruncateString(headers.ActivationVersion),
		"user_agent", logger.TruncateString(headers.UserAgent),
	)

	// 根据请求头是否包含 SerialNumber 分流到对应处理框架
	if headers.SerialNumber != "" {
		h.handleOTASerialNumber(w, r, headers, body)
		return
	}

	h.handleOTALegacy(w, r, headers, body)
}

// readAndValidateOTARequest 执行请求头长度、正文大小及 JSON 格式的基础校验。
func (h *OTAHandler) readAndValidateOTARequest(w http.ResponseWriter, r *http.Request) (DeviceHeaders, []byte, bool) {
	// 1. 校验请求头长度限制
	maxHeaderBytes := MaxSingleHeaderBytes
	maxTotalHeaderBytes := MaxTotalHeaderBytes
	if h.cfg != nil && h.cfg.Server.MaxHTTPHeaderBytes > 0 {
		maxTotalHeaderBytes = h.cfg.Server.MaxHTTPHeaderBytes
	}
	if err := validateHeaders(r.Header, maxHeaderBytes, maxTotalHeaderBytes); err != nil {
		http.Error(w, "request header fields too large", http.StatusBadRequest)
		return DeviceHeaders{}, nil, false
	}

	// 2. 校验并读取请求正文
	maxBodyBytes := int64(DefaultMaxBodyBytes)
	if h.cfg != nil && h.cfg.Server.MaxHTTPBodyBytes > 0 {
		maxBodyBytes = h.cfg.Server.MaxHTTPBodyBytes
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return DeviceHeaders{}, nil, false
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return DeviceHeaders{}, nil, false
	}

	// 3. 非空正文必须是有效 JSON
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) > 0 {
		if !json.Valid(trimmedBody) {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return DeviceHeaders{}, nil, false
		}
	}

	headers := extractDeviceHeaders(r)
	return headers, trimmedBody, true
}
