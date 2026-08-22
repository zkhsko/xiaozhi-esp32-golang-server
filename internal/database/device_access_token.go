package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 哨兵错误定义。
var (
	// ErrAccessTokenNotFound 表示设备 Access Token 记录未找到。
	ErrAccessTokenNotFound = errors.New("device access token not found")
	// ErrEmptyAccessTokenHash 表示 Access Token Hash 为空。
	ErrEmptyAccessTokenHash = errors.New("access token hash cannot be empty")
	// ErrInvalidAccessTokenRecord 表示 Access Token 结构体为 nil 或非法。
	ErrInvalidAccessTokenRecord = errors.New("invalid device access token")
	// ErrAccessTokenRevoked 表示 Access Token 已被撤销。
	ErrAccessTokenRevoked = errors.New("device access token is revoked")
	// ErrAccessTokenExpired 表示 Access Token 已过期。
	ErrAccessTokenExpired = errors.New("device access token is expired")
)

// DeviceAccessToken 映射 device_access_token 设备鉴权 Access Token 表。
//
// 业务用途：
// 记录 serial_number 与 access token 的鉴权对应关系。
// 一台设备全局唯一对应一条 Access Token 记录（serial_number 为全局唯一索引）。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - serial_number: 设备序列号，全局业务唯一，唯一索引 uk_serial_number。
// - access_token_hash: 设备 Access Token 的 SHA-256 哈希值，非空且全局唯一，唯一索引 uk_access_token_hash。
// - issued_at: Token 签发时间。
// - expires_at: Token 过期时间，可为空（为空表示无固定过期时间）。
// - revoked_at: Token 撤销时间，可为空（为空表示未撤销）。
// - created_at: 记录创建时间。
// - updated_at: 记录最近更新时间。
type DeviceAccessToken struct {
	ID              uint64     `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber    string     `gorm:"uniqueIndex:uk_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	AccessTokenHash []byte     `gorm:"uniqueIndex:uk_access_token_hash;column:access_token_hash;type:varbinary(64);not null" json:"-"`
	IssuedAt        time.Time  `gorm:"column:issued_at;not null" json:"issued_at"`
	ExpiresAt       *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	RevokedAt       *time.Time `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 DeviceAccessToken 对应的表名。
func (DeviceAccessToken) TableName() string {
	return "device_access_token"
}

// IsValid 检查 Token 在指定时间点是否有效（未被撤销且未过期）。
func (t *DeviceAccessToken) IsValid(now time.Time) bool {
	if t == nil {
		return false
	}
	if t.RevokedAt != nil && !t.RevokedAt.IsZero() && !t.RevokedAt.After(now) {
		return false
	}
	if t.ExpiresAt != nil && !t.ExpiresAt.IsZero() && !t.ExpiresAt.After(now) {
		return false
	}
	return true
}

// HashAccessToken 计算设备明文 Access Token 的 SHA-256 哈希值。
func HashAccessToken(rawToken string) []byte {
	h := sha256.Sum256([]byte(strings.TrimSpace(rawToken)))
	return h[:]
}

// FindDeviceAccessTokenByAccessTokenHash 根据 Access Token Hash 查询设备 Access Token 记录。
func (d *Database) FindDeviceAccessTokenByAccessTokenHash(ctx context.Context, accessTokenHash []byte) (*DeviceAccessToken, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	if len(accessTokenHash) == 0 {
		return nil, ErrEmptyAccessTokenHash
	}

	var tok DeviceAccessToken
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("access_token_hash = ?", accessTokenHash).
		Take(&tok).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device access token by access token hash: %w", ErrAccessTokenNotFound)
		}
		return nil, fmt.Errorf("query device access token by access token hash: %w", err)
	}

	return &tok, nil
}

// FindDeviceAccessTokenBySerialNumber 根据设备序列号查询 Access Token 记录。
func (d *Database) FindDeviceAccessTokenBySerialNumber(ctx context.Context, serialNumber string) (*DeviceAccessToken, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}

	var tok DeviceAccessToken
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("serial_number = ?", trimmedSN).
		Take(&tok).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device access token by serial_number %q: %w", trimmedSN, ErrAccessTokenNotFound)
		}
		return nil, fmt.Errorf("query device access token by serial_number: %w", err)
	}

	return &tok, nil
}

// FindDeviceAccessTokenByID 根据自增主键 ID 查询设备 Access Token 记录。
func (d *Database) FindDeviceAccessTokenByID(ctx context.Context, id uint64) (*DeviceAccessToken, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	if id == 0 {
		return nil, fmt.Errorf("find device access token by id %d: %w", id, ErrAccessTokenNotFound)
	}

	var tok DeviceAccessToken
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("id = ?", id).
		Take(&tok).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device access token by id %d: %w", id, ErrAccessTokenNotFound)
		}
		return nil, fmt.Errorf("query device access token by id: %w", err)
	}

	return &tok, nil
}

// CreateDeviceAccessToken 插入一条设备 Access Token 记录。
func (d *Database) CreateDeviceAccessToken(ctx context.Context, token *DeviceAccessToken) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if token == nil {
		return ErrInvalidAccessTokenRecord
	}

	token.SerialNumber = strings.TrimSpace(token.SerialNumber)
	if token.SerialNumber == "" {
		return ErrEmptySerialNumber
	}
	if len(token.AccessTokenHash) == 0 {
		return ErrEmptyAccessTokenHash
	}

	if token.IssuedAt.IsZero() {
		token.IssuedAt = time.Now()
	}

	if err := d.gormDB.WithContext(ctx).Create(token).Error; err != nil {
		return fmt.Errorf("create device access token: %w", err)
	}

	return nil
}

// UpsertDeviceAccessToken 创建或覆盖更新指定设备的 Access Token。
// 若该设备已存在 Token，则更新 AccessTokenHash、IssuedAt、ExpiresAt 并清空 RevokedAt。
func (d *Database) UpsertDeviceAccessToken(ctx context.Context, token *DeviceAccessToken) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if token == nil {
		return ErrInvalidAccessTokenRecord
	}

	trimmedSN := strings.TrimSpace(token.SerialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}
	if len(token.AccessTokenHash) == 0 {
		return ErrEmptyAccessTokenHash
	}

	if token.IssuedAt.IsZero() {
		token.IssuedAt = time.Now()
	}

	var existing DeviceAccessToken
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("serial_number = ?", trimmedSN).
		Take(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query device access token before upsert: %w", err)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		token.SerialNumber = trimmedSN
		if err := d.gormDB.WithContext(ctx).Create(token).Error; err != nil {
			return fmt.Errorf("create device access token on upsert: %w", err)
		}
		return nil
	}

	updates := map[string]any{
		"access_token_hash": token.AccessTokenHash,
		"issued_at":         token.IssuedAt,
		"expires_at":        token.ExpiresAt,
		"revoked_at":        token.RevokedAt,
	}

	if err := d.gormDB.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
		return fmt.Errorf("update device access token on upsert: %w", err)
	}

	token.ID = existing.ID
	token.SerialNumber = existing.SerialNumber
	token.CreatedAt = existing.CreatedAt

	return nil
}

// RevokeDeviceAccessTokenByAccessTokenHash 根据 Access Token Hash 撤销指定 Token。
func (d *Database) RevokeDeviceAccessTokenByAccessTokenHash(ctx context.Context, accessTokenHash []byte, revokeTime time.Time) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	if len(accessTokenHash) == 0 {
		return ErrEmptyAccessTokenHash
	}

	if revokeTime.IsZero() {
		revokeTime = time.Now()
	}

	result := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("access_token_hash = ?", accessTokenHash).
		Update("revoked_at", revokeTime)
	if result.Error != nil {
		return fmt.Errorf("revoke device access token: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("revoke device access token: %w", ErrAccessTokenNotFound)
	}

	return nil
}

// RevokeDeviceAccessTokenBySerialNumber 撤销指定设备序列号下的当前未撤销 Token。
func (d *Database) RevokeDeviceAccessTokenBySerialNumber(ctx context.Context, serialNumber string, revokeTime time.Time) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}

	if revokeTime.IsZero() {
		revokeTime = time.Now()
	}

	result := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("serial_number = ? AND (revoked_at IS NULL OR revoked_at > ?)", trimmedSN, revokeTime).
		Update("revoked_at", revokeTime)
	if result.Error != nil {
		return fmt.Errorf("revoke device access token by serial_number %q: %w", trimmedSN, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("revoke device access token for %q: %w", trimmedSN, ErrAccessTokenNotFound)
	}

	return nil
}

// DeleteDeviceAccessTokenBySerialNumber 删除指定设备序列号的 Access Token 记录。
func (d *Database) DeleteDeviceAccessTokenBySerialNumber(ctx context.Context, serialNumber string) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}

	result := d.gormDB.WithContext(ctx).
		Where("serial_number = ?", trimmedSN).
		Delete(&DeviceAccessToken{})
	if result.Error != nil {
		return fmt.Errorf("delete device access token by serial_number %q: %w", trimmedSN, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete device access token for serial_number %q: %w", trimmedSN, ErrAccessTokenNotFound)
	}

	return nil
}
