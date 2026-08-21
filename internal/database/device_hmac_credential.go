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
	// AuthMethodActivationCode 表示基于 6 位一次性激活码人工绑定的激活方式（针对未烧录出厂序列号的设备）。
	AuthMethodActivationCode = "activation_code"
	// AuthMethodManualCodeHMAC 表示通过人工输入 serial_number + hmac + code 的组合激活方式。
	AuthMethodManualCodeHMAC = "manual_code_hmac"
)

// 凭证状态常量定义。
const (
	// CredentialStatusEnabled 表示凭证处于可用状态，允许发起 HMAC 挑战验证或激活绑定。
	CredentialStatusEnabled = "enabled"
	// CredentialStatusActivated 表示凭证已完成设备激活并与运行态设备建立权威关联。
	CredentialStatusActivated = "activated"
	// CredentialStatusBlocked 表示凭证被暂时冻结，禁止进行激活和鉴权。
	CredentialStatusBlocked = "blocked"
	// CredentialStatusRevoked 表示凭证已永久作废撤销，禁止再次激活或使用。
	CredentialStatusRevoked = "revoked"
)

// 哨兵错误定义。
var (
	// ErrCredentialNotFound 表示设备 HMAC 凭证未找到。
	ErrCredentialNotFound = errors.New("device hmac credential not found")
	// ErrEmptySerialNumber 表示设备序列号为空。
	ErrEmptySerialNumber = errors.New("serial number cannot be empty")
	// ErrEmptyHMACKeyCiphertext 表示设备 HMAC Key 密文为空。
	ErrEmptyHMACKeyCiphertext = errors.New("hmac key ciphertext cannot be empty")
	// ErrEmptyStatus 表示凭证状态为空。
	ErrEmptyStatus = errors.New("credential status cannot be empty")
	// ErrInvalidCredential 表示凭证结构体为 nil 或非法。
	ErrInvalidCredential = errors.New("invalid device hmac credential")
	// ErrCredentialBlocked 表示设备凭证处于冻结或撤销不可用状态。
	ErrCredentialBlocked = errors.New("device credential is blocked or revoked")
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
// - auth_method: 认证激活方式（efuse_hmac / activation_code / manual_code_hmac）。
// - hmac_key_ciphertext: 加密后的 HMAC Key 密文，不可为空，禁止明文存储。
// - credential_status: 凭证状态（enabled / activated / blocked / revoked），无索引。
type DeviceHmacCredential struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber      string    `gorm:"uniqueIndex:uk_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	AuthMethod        string    `gorm:"column:auth_method;size:32;not null;default:'efuse_hmac'" json:"auth_method"`
	HMACKeyCiphertext []byte    `gorm:"column:hmac_key_ciphertext;type:varbinary(512);not null" json:"-"`
	CredentialStatus  string    `gorm:"column:credential_status;size:16;not null;default:'enabled'" json:"credential_status"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 DeviceHmacCredential 对应的表名。
func (DeviceHmacCredential) TableName() string {
	return "device_hmac_credential"
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

// FindDeviceHmacCredentialByID 根据自增主键 ID 查询设备 HMAC 凭证记录。
func (d *Database) FindDeviceHmacCredentialByID(ctx context.Context, id uint64) (*DeviceHmacCredential, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	if id == 0 {
		return nil, fmt.Errorf("find device hmac credential by id %d: %w", id, ErrCredentialNotFound)
	}

	var cred DeviceHmacCredential
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceHmacCredential{}).
		Where("id = ?", id).
		Take(&cred).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device hmac credential by id %d: %w", id, ErrCredentialNotFound)
		}
		return nil, fmt.Errorf("query device hmac credential by id: %w", err)
	}

	return &cred, nil
}

// CreateDeviceHmacCredential 写入单条设备出厂凭证记录（供制造预置/初始化使用）。
func (d *Database) CreateDeviceHmacCredential(ctx context.Context, cred *DeviceHmacCredential) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cred == nil {
		return ErrInvalidCredential
	}

	cred.SerialNumber = strings.TrimSpace(cred.SerialNumber)
	if cred.SerialNumber == "" {
		return ErrEmptySerialNumber
	}

	if len(cred.HMACKeyCiphertext) == 0 {
		return ErrEmptyHMACKeyCiphertext
	}

	if cred.AuthMethod == "" {
		cred.AuthMethod = AuthMethodEfuseHMAC
	}
	if cred.CredentialStatus == "" {
		cred.CredentialStatus = CredentialStatusEnabled
	}

	if err := d.gormDB.WithContext(ctx).Create(cred).Error; err != nil {
		return fmt.Errorf("create device hmac credential: %w", err)
	}

	return nil
}

// UpdateDeviceHmacCredentialStatus 更新设备凭证状态（如激活、冻结或撤销）。
func (d *Database) UpdateDeviceHmacCredentialStatus(ctx context.Context, serialNumber, status string) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}
	trimmedStatus := strings.TrimSpace(status)
	if trimmedStatus == "" {
		return ErrEmptyStatus
	}

	result := d.gormDB.WithContext(ctx).
		Model(&DeviceHmacCredential{}).
		Where("serial_number = ?", trimmedSN).
		Update("credential_status", trimmedStatus)
	if result.Error != nil {
		return fmt.Errorf("update device hmac credential status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("update device hmac credential status for %q: %w", trimmedSN, ErrCredentialNotFound)
	}

	return nil
}

// UpsertDeviceHmacCredential 针对无序列号设备输入 serial_number、hmac、code 激活场景，创建或更新设备凭证。
func (d *Database) UpsertDeviceHmacCredential(ctx context.Context, cred *DeviceHmacCredential) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cred == nil {
		return ErrInvalidCredential
	}

	cred.SerialNumber = strings.TrimSpace(cred.SerialNumber)
	if cred.SerialNumber == "" {
		return ErrEmptySerialNumber
	}
	if len(cred.HMACKeyCiphertext) == 0 {
		return ErrEmptyHMACKeyCiphertext
	}

	var existing DeviceHmacCredential
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceHmacCredential{}).
		Where("serial_number = ?", cred.SerialNumber).
		Take(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query device hmac credential: %w", err)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if cred.AuthMethod == "" {
			cred.AuthMethod = AuthMethodManualCodeHMAC
		}
		if cred.CredentialStatus == "" {
			cred.CredentialStatus = CredentialStatusActivated
		}
		if err := d.gormDB.WithContext(ctx).Create(cred).Error; err != nil {
			return fmt.Errorf("create device hmac credential: %w", err)
		}
		return nil
	}

	if !existing.IsAvailable() {
		return ErrCredentialBlocked
	}

	updates := map[string]any{
		"hmac_key_ciphertext": cred.HMACKeyCiphertext,
		"credential_status":   CredentialStatusActivated,
	}
	if cred.AuthMethod != "" {
		updates["auth_method"] = cred.AuthMethod
	}

	if err := d.gormDB.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
		return fmt.Errorf("update device hmac credential: %w", err)
	}

	return nil
}

// ValidateActivationInput 校验输入 serial_number、hmac、code 激活请求的基础合法性。
func ValidateActivationInput(serialNumber, hmacKey, code string) error {
	if strings.TrimSpace(serialNumber) == "" {
		return fmt.Errorf("serial_number cannot be empty")
	}
	if strings.TrimSpace(hmacKey) == "" {
		return fmt.Errorf("hmac key cannot be empty")
	}
	if strings.TrimSpace(code) == "" {
		return fmt.Errorf("activation code cannot be empty")
	}
	return nil
}
