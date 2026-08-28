package database

import (
	"context"
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
	// ErrEmptyAccessToken 表示 Access Token 为空。
	ErrEmptyAccessToken = errors.New("access token cannot be empty")
	// ErrInvalidAccessTokenRecord 表示 Access Token 结构体为 nil 或非法。
	ErrInvalidAccessTokenRecord = errors.New("invalid device access token")
)

// DeviceAccessToken 映射 device_access_token 设备鉴权 Access Token 表。
//
// 业务用途：
// 记录 serial_number 与明文 access token 的鉴权对应关系。
// 一台设备全局唯一对应一条 Access Token 记录（serial_number 为全局唯一索引）。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - serial_number: 设备序列号，全局业务唯一，唯一索引 uk_serial_number。
// - access_token: 设备 Access Token 明文，非空且全局唯一，唯一索引 uk_access_token。
// - device_type: 设备类型（冗余自生产表，默认 default，用于关联 agent）。
// - has_exposed: 是否已在 OTA 接口展示下发过（false: 待展示下发, true: 已展示下发）。
// - issued_at: Token 签发时间。
// - expires_at: Token 过期时间，可为空（为空表示无固定过期时间）。
// - revoked_at: Token 撤销时间，可为空（为空表示未撤销）。
// - created_at: 记录创建时间。
// - updated_at: 记录最近更新时间。
type DeviceAccessToken struct {
	ID           uint64     `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber string     `gorm:"uniqueIndex:uk_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	AccessToken  string     `gorm:"uniqueIndex:uk_access_token;column:access_token;size:128;not null" json:"access_token"`
	DeviceType   string     `gorm:"column:device_type;size:32;not null;default:'default'" json:"device_type"` // 设备类型（冗余自生产表，用于关联 agent）
	HasExposed   bool       `gorm:"column:has_exposed;not null;default:false" json:"has_exposed"`
	IssuedAt     time.Time  `gorm:"column:issued_at;not null" json:"issued_at"`
	ExpiresAt    *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	RevokedAt    *time.Time `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
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

// FindDeviceAccessTokenByAccessToken 根据明文 Access Token 查询设备 Access Token 记录。
func (d *Database) FindDeviceAccessTokenByAccessToken(ctx context.Context, accessToken string) (*DeviceAccessToken, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedToken := strings.TrimSpace(accessToken)
	if trimmedToken == "" {
		return nil, ErrEmptyAccessToken
	}

	var tok DeviceAccessToken
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("access_token = ?", trimmedToken).
		Take(&tok).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device access token by access token: %w", ErrAccessTokenNotFound)
		}
		return nil, fmt.Errorf("query device access token by access token: %w", err)
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

// UpsertDeviceAccessToken 创建或覆盖更新指定设备的 Access Token。
// 若该设备已存在 Token，则更新 AccessToken、HasExposed、IssuedAt、ExpiresAt 并清空 RevokedAt。
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
	trimmedToken := strings.TrimSpace(token.AccessToken)
	if trimmedToken == "" {
		return ErrEmptyAccessToken
	}

	token.DeviceType = strings.TrimSpace(token.DeviceType)
	if token.DeviceType == "" {
		token.DeviceType = "default"
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
		token.AccessToken = trimmedToken
		if err := d.gormDB.WithContext(ctx).Create(token).Error; err != nil {
			return fmt.Errorf("create device access token on upsert: %w", err)
		}
		return nil
	}

	updates := map[string]any{
		"access_token": trimmedToken,
		"device_type":  token.DeviceType,
		"has_exposed":  token.HasExposed,
		"issued_at":    token.IssuedAt,
		"expires_at":   token.ExpiresAt,
		"revoked_at":   token.RevokedAt,
	}

	if err := d.gormDB.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
		return fmt.Errorf("update device access token on upsert: %w", err)
	}

	token.ID = existing.ID
	token.SerialNumber = existing.SerialNumber
	token.AccessToken = trimmedToken
	token.CreatedAt = existing.CreatedAt

	return nil
}

// UpdateDeviceAccessTokenHasExposed 更新指定设备序列号的 Access Token 是否已展示下发标记。
func (d *Database) UpdateDeviceAccessTokenHasExposed(ctx context.Context, serialNumber string, hasExposed bool) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}

	result := d.gormDB.WithContext(ctx).
		Model(&DeviceAccessToken{}).
		Where("serial_number = ?", trimmedSN).
		Update("has_exposed", hasExposed)
	if result.Error != nil {
		return fmt.Errorf("update device access token has_exposed for serial_number %q: %w", trimmedSN, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("update device access token has_exposed for %q: %w", trimmedSN, ErrAccessTokenNotFound)
	}

	return nil
}
