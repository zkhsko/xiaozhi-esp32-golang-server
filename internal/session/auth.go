package session

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// 常量定义：WebSocket 路径、请求头长度限制、协议版本与认证前缀。
const (
	// WebSocketPath 设备 WebSocket 连接接入路径。
	WebSocketPath = "/xiaozhi/v1/"

	// MaxSingleHeaderBytes 单个请求头键或值的最大长度（1024 字符）。
	MaxSingleHeaderBytes = 1024

	// MaxTotalHeaderBytes 所有请求头键值对累计最大长度（8192 字符）。
	MaxTotalHeaderBytes = 8192

	// ProtocolVersion WebSocket 协议版本号。
	ProtocolVersion = "1"

	// BearerPrefix Authorization 头要求的 Bearer 前缀。
	BearerPrefix = "Bearer "
)

// 认证与请求校验相关的哨兵错误。
var (
	ErrHeaderTooLarge         = errors.New("request header fields too large")
	ErrMissingToken           = errors.New("missing authorization header")
	ErrInvalidTokenFormat     = errors.New("invalid authorization header format")
	ErrInvalidToken           = errors.New("invalid authorization token")
	ErrInvalidProtocolVersion = errors.New("invalid or missing protocol version")
)

// TokenFinder 定义根据 Access Token 查询设备 Token 记录的接口。
type TokenFinder interface {
	FindDeviceAccessTokenByAccessToken(ctx context.Context, accessToken string) (*database.DeviceAccessToken, error)
}

// ClientHeaderInfo 包含握手请求中提取并脱敏的客户端设备诊断信息。
type ClientHeaderInfo struct {
	DeviceID        string
	ClientID        string
	SerialNumber    string
	ProtocolVersion string
	UserAgent       string
}

// AuthError 包装认证与请求校验错误及其建议返回的 HTTP 状态码。
type AuthError struct {
	StatusCode int
	Err        error
}

// Error 返回底层错误的描述文本。
func (e *AuthError) Error() string {
	if e.Err == nil {
		return "authentication error"
	}
	return e.Err.Error()
}

// Unwrap 返回被包装的底层错误，支持 errors.Is 和 errors.As。
func (e *AuthError) Unwrap() error {
	return e.Err
}

// HTTPStatus 返回错误对应的 HTTP 状态码。若传入错误已封装状态码则直接返回；否则按错误类型推导。
func HTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return authErr.StatusCode
	}
	if errors.Is(err, ErrMissingToken) || errors.Is(err, ErrInvalidTokenFormat) || errors.Is(err, ErrInvalidToken) {
		return http.StatusUnauthorized
	}
	if errors.Is(err, ErrHeaderTooLarge) || errors.Is(err, ErrInvalidProtocolVersion) {
		return http.StatusBadRequest
	}
	return http.StatusBadRequest
}

// ValidateHeaders 校验所有请求头键值是否超出单项与总计长度上限。
func ValidateHeaders(headers http.Header, maxSingle, maxTotal int) error {
	if maxSingle <= 0 {
		maxSingle = MaxSingleHeaderBytes
	}
	if maxTotal <= 0 {
		maxTotal = MaxTotalHeaderBytes
	}

	totalLen := 0
	for key, values := range headers {
		if len(key) > maxSingle {
			return ErrHeaderTooLarge
		}
		totalLen += len(key)
		for _, val := range values {
			if len(val) > maxSingle {
				return ErrHeaderTooLarge
			}
			totalLen += len(val)
			if totalLen > maxTotal {
				return ErrHeaderTooLarge
			}
		}
	}
	return nil
}

// AuthenticateUpgrade 执行 WebSocket 升级前的请求头校验、协议版本检查与数据库 Token 认证。
// 校验失败时返回附带 HTTP 状态码的 AuthError；成功时返回提取的客户端诊断信息。
func AuthenticateUpgrade(r *http.Request, finder TokenFinder, maxHeaderBytes int) (*ClientHeaderInfo, error) {
	if r == nil {
		return nil, &AuthError{
			StatusCode: http.StatusBadRequest,
			Err:        errors.New("nil request"),
		}
	}

	// 1. 请求头长度限制校验
	maxSingle := MaxSingleHeaderBytes
	if maxHeaderBytes > 0 {
		maxSingle = maxHeaderBytes
	}
	if err := ValidateHeaders(r.Header, maxSingle, MaxTotalHeaderBytes); err != nil {
		return nil, &AuthError{
			StatusCode: http.StatusBadRequest,
			Err:        err,
		}
	}

	// 2. 协议版本校验
	protocolVer := strings.TrimSpace(r.Header.Get("Protocol-Version"))
	if protocolVer != ProtocolVersion {
		return nil, &AuthError{
			StatusCode: http.StatusBadRequest,
			Err:        ErrInvalidProtocolVersion,
		}
	}

	// 3. Authorization Bearer Token 提取
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrMissingToken,
		}
	}

	if !strings.HasPrefix(authHeader, BearerPrefix) {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrInvalidTokenFormat,
		}
	}

	token := strings.TrimPrefix(authHeader, BearerPrefix)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrInvalidTokenFormat,
		}
	}

	if finder == nil {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrInvalidToken,
		}
	}

	// 4. 从数据库查询 Access Token 并校验其有效性
	tok, err := finder.FindDeviceAccessTokenByAccessToken(r.Context(), token)
	if err != nil {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrInvalidToken,
		}
	}

	if !tok.IsValid(time.Now()) {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrInvalidToken,
		}
	}

	tokenHash := sha256.Sum256([]byte(token))
	expectedHash := sha256.Sum256([]byte(tok.AccessToken))
	if subtle.ConstantTimeCompare(tokenHash[:], expectedHash[:]) != 1 {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrInvalidToken,
		}
	}

	// 5. 提取客户端设备标识并比对 Serial-Number
	serialNum := strings.TrimSpace(r.Header.Get("Serial-Number"))
	if serialNum != "" && serialNum != tok.SerialNumber {
		return nil, &AuthError{
			StatusCode: http.StatusUnauthorized,
			Err:        ErrInvalidToken,
		}
	}
	if serialNum == "" {
		serialNum = tok.SerialNumber
	}

	deviceID := strings.TrimSpace(r.Header.Get("Device-Id"))
	clientID := strings.TrimSpace(r.Header.Get("Client-Id"))

	info := &ClientHeaderInfo{
		DeviceID:        deviceID,
		ClientID:        clientID,
		SerialNumber:    serialNum,
		ProtocolVersion: protocolVer,
		UserAgent:       r.UserAgent(),
	}

	return info, nil
}

// LogAuthRejection 记录升级认证被拒绝的诊断日志，严禁打印任何 Token 或 Authorization 明文。
func LogAuthRejection(l *slog.Logger, r *http.Request, err error) {
	if l == nil {
		l = slog.Default()
	}
	var deviceID, clientID, serialNumber, userAgent, path, remoteAddr string
	if r != nil {
		deviceID = logger.TruncateString(r.Header.Get("Device-Id"))
		clientID = logger.TruncateString(r.Header.Get("Client-Id"))
		serialNumber = logger.TruncateString(r.Header.Get("Serial-Number"))
		userAgent = logger.TruncateString(r.UserAgent())
		path = r.URL.Path
		remoteAddr = r.RemoteAddr
	}

	l.Warn("websocket upgrade authentication rejected",
		"path", path,
		"remote_addr", remoteAddr,
		"device_id", deviceID,
		"client_id", clientID,
		"serial_number", serialNumber,
		"user_agent", userAgent,
		"error", err,
	)
}

// LogAuthSuccess 记录升级认证成功的诊断日志。
func LogAuthSuccess(l *slog.Logger, r *http.Request, info *ClientHeaderInfo) {
	if l == nil {
		l = slog.Default()
	}
	var deviceID, clientID, serialNumber, userAgent, path, remoteAddr string
	if info != nil {
		deviceID = logger.TruncateString(info.DeviceID)
		clientID = logger.TruncateString(info.ClientID)
		serialNumber = logger.TruncateString(info.SerialNumber)
		userAgent = logger.TruncateString(info.UserAgent)
	}
	if r != nil {
		path = r.URL.Path
		remoteAddr = r.RemoteAddr
	}

	l.Info("websocket upgrade authentication successful",
		"path", path,
		"remote_addr", remoteAddr,
		"device_id", deviceID,
		"client_id", clientID,
		"serial_number", serialNumber,
		"user_agent", userAgent,
	)
}

// RejectUpgrade 向客户端写入认证失败的 HTTP 响应并记录脱敏诊断日志，确保拒绝发生在协议升级之前。
func RejectUpgrade(w http.ResponseWriter, r *http.Request, l *slog.Logger, err error) {
	statusCode := HTTPStatus(err)
	LogAuthRejection(l, r, err)
	http.Error(w, err.Error(), statusCode)
}
