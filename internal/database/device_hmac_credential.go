package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 设备认证与激活方式常量定义。
const (
	// AuthMethodEfuseHMAC 表示基于硬件 eFuse HMAC 挑战应答的激活方式（针对包含出厂硬件序列号与密钥的设备）。
	AuthMethodEfuseHMAC = "efuse_hmac"
)

// 凭证状态常量定义。
const (
	// CredentialStatusEnabled 表示凭证处于可用状态，允许发起 HMAC 挑战验证或激活绑定。
	CredentialStatusEnabled = "enabled"
	// CredentialStatusActivated 表示凭证已完成设备激活并与运行态设备建立权威关联。
	CredentialStatusActivated = "activated"
	// CredentialStatusBlocked 表示凭证已被临时禁用。
	CredentialStatusBlocked = "blocked"
	// CredentialStatusRevoked 表示凭证已被作废。
	CredentialStatusRevoked = "revoked"
)

// 哨兵错误定义。
var (
	// ErrCredentialNotFound 表示设备 HMAC 凭证未找到。
	ErrCredentialNotFound = errors.New("device hmac credential not found")
	// ErrEmptySerialNumber 表示设备序列号为空。
	ErrEmptySerialNumber = errors.New("serial number cannot be empty")
	// ErrEmptyHMACKeyCiphertext 表示设备 HMAC Key 为空。
	ErrEmptyHMACKeyCiphertext = errors.New("hmac key ciphertext cannot be empty")
	// ErrInvalidCredential 表示凭证结构体为 nil 或非法。
	ErrInvalidCredential = errors.New("invalid device hmac credential")
	// ErrDatabaseInstanceRequired 表示 Database 实例不能为 nil。
	ErrDatabaseInstanceRequired = errors.New("database instance cannot be nil")
)

// DeviceHmacCredential 映射 device_hmac_credential 出厂与制造身份授权凭证表。
//
// 业务用途：
// 1. 包含 serial_number 的设备：出厂烧录序列号与 eFuse HMAC Key，设备激活时根据 serial_number 查本表核验。
// 2. 不包含 serial_number 的设备：用户在后台输入 serial_number、hmac、code 激活创建/更新本表凭证。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - serial_number: 设备序列号，不可为空且全局唯一，唯一索引 uk_serial_number。
// - auth_method: 认证激活方式（efuse_hmac）。
// - device_type: 设备类型（默认 default，用于关联 agent）。
// - hmac_key_ciphertext: HMAC Key（统一字段名 hmac_key_ciphertext，64位十六进制字符串，可直接写入 hmac_0），不可为空。
// - credential_status: 凭证状态（enabled / activated），无索引。
type DeviceHmacCredential struct {
	Id                uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber      string    `gorm:"uniqueIndex:uk_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	AuthMethod        string    `gorm:"column:auth_method;size:32;not null;default:'efuse_hmac'" json:"auth_method"`
	DeviceType        string    `gorm:"column:device_type;size:32;not null;default:'default'" json:"device_type"` // 设备类型，用于关联 agent
	HMACKeyCiphertext string    `gorm:"column:hmac_key_ciphertext;size:64;not null" json:"-"`
	CredentialStatus  string    `gorm:"column:credential_status;size:16;not null;default:'enabled'" json:"credential_status"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 DeviceHmacCredential 对应的表名。
func (DeviceHmacCredential) TableName() string {
	return "device_hmac_credential"
}

// DeviceHmacCredentialFilter 定义设备 HMAC 凭证查询过滤条件。
type DeviceHmacCredentialFilter struct {
	SerialNumber     string
	DeviceType       string
	CredentialStatus string
	Page             int
	PageSize         int
}

// IsAvailable 判断凭证当前是否处于可发起激活或验证的可用状态。
func (c *DeviceHmacCredential) IsAvailable() bool {
	if c == nil {
		return false
	}
	return c.CredentialStatus == CredentialStatusEnabled || c.CredentialStatus == CredentialStatusActivated
}

// FindDeviceHmacCredentialBySerialNumber 根据序列号查询设备 HMAC 凭证记录。
// 用于包含 serial_number 的设备在激活时核验身份。
func (d *Database) FindDeviceHmacCredentialBySerialNumber(ctx context.Context, serialNumber string) (*DeviceHmacCredential, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}

	var cred DeviceHmacCredential
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceHmacCredential{}).
		Where("serial_number = ?", trimmedSN).
		Take(&cred).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device hmac credential by serial_number %q: %w", trimmedSN, ErrCredentialNotFound)
		}
		return nil, fmt.Errorf("query device hmac credential by serial_number: %w", err)
	}

	return &cred, nil
}

// BatchCreateDeviceHmacCredentials 批量写入设备出厂凭证记录。
func (d *Database) BatchCreateDeviceHmacCredentials(ctx context.Context, creds []*DeviceHmacCredential) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if len(creds) == 0 {
		return nil
	}

	for _, cred := range creds {
		if cred == nil {
			return ErrInvalidCredential
		}
		cred.SerialNumber = strings.TrimSpace(cred.SerialNumber)
		if cred.SerialNumber == "" {
			return ErrEmptySerialNumber
		}
		cred.HMACKeyCiphertext = strings.TrimSpace(cred.HMACKeyCiphertext)
		if cred.HMACKeyCiphertext == "" {
			return ErrEmptyHMACKeyCiphertext
		}
		if cred.AuthMethod == "" {
			cred.AuthMethod = AuthMethodEfuseHMAC
		}
		cred.DeviceType = strings.TrimSpace(cred.DeviceType)
		if cred.DeviceType == "" {
			cred.DeviceType = "default"
		}
		if cred.CredentialStatus == "" {
			cred.CredentialStatus = CredentialStatusEnabled
		}
	}

	if err := d.gormDB.WithContext(ctx).Create(&creds).Error; err != nil {
		return fmt.Errorf("batch create device hmac credentials: %w", err)
	}

	return nil
}

// ListDeviceHmacCredentials 分页查询设备 HMAC 凭证列表。
func (d *Database) ListDeviceHmacCredentials(ctx context.Context, filter DeviceHmacCredentialFilter) ([]*DeviceHmacCredential, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&DeviceHmacCredential{})

	if sn := strings.TrimSpace(filter.SerialNumber); sn != "" {
		query = query.Where("serial_number LIKE ?", "%"+sn+"%")
	}
	if dt := strings.TrimSpace(filter.DeviceType); dt != "" {
		query = query.Where("device_type = ?", dt)
	}
	if cs := strings.TrimSpace(filter.CredentialStatus); cs != "" {
		query = query.Where("credential_status = ?", cs)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count device hmac credentials: %w", err)
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 10
	} else if pageSize > 100 {
		pageSize = 100
	}

	offset := (page - 1) * pageSize
	var creds []*DeviceHmacCredential
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&creds).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query device hmac credentials list: %w", err)
	}

	return creds, total, nil
}

// UpdateDeviceHmacCredential 更新指定 Id 的凭证字段。
func (d *Database) UpdateDeviceHmacCredential(ctx context.Context, id uint64, updates map[string]any) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidCredential
	}
	if len(updates) == 0 {
		return nil
	}

	allowedUpdates := make(map[string]any)
	if val, ok := updates["device_type"]; ok {
		allowedUpdates["device_type"] = strings.TrimSpace(fmt.Sprintf("%v", val))
	}
	if val, ok := updates["credential_status"]; ok {
		allowedUpdates["credential_status"] = strings.TrimSpace(fmt.Sprintf("%v", val))
	}
	if val, ok := updates["auth_method"]; ok {
		allowedUpdates["auth_method"] = strings.TrimSpace(fmt.Sprintf("%v", val))
	}
	if len(allowedUpdates) == 0 {
		return nil
	}
	allowedUpdates["updated_at"] = time.Now()

	res := d.gormDB.WithContext(ctx).
		Model(&DeviceHmacCredential{}).
		Where("id = ?", id).
		Updates(allowedUpdates)
	if res.Error != nil {
		return fmt.Errorf("update device hmac credential: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// DeleteDeviceHmacCredential 根据 Id 删除凭证记录。
func (d *Database) DeleteDeviceHmacCredential(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidCredential
	}

	res := d.gormDB.WithContext(ctx).
		Where("id = ?", id).
		Delete(&DeviceHmacCredential{})
	if res.Error != nil {
		return fmt.Errorf("delete device hmac credential: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// BatchDeleteDeviceHmacCredentials 批量删除凭证记录。
func (d *Database) BatchDeleteDeviceHmacCredentials(ctx context.Context, ids []uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if len(ids) == 0 {
		return nil
	}

	res := d.gormDB.WithContext(ctx).
		Where("id IN ?", ids).
		Delete(&DeviceHmacCredential{})
	if res.Error != nil {
		return fmt.Errorf("batch delete device hmac credentials: %w", res.Error)
	}
	return nil
}
