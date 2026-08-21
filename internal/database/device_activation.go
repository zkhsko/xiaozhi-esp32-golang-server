package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 设备激活状态常量定义。
const (
	// ActivationStatusActive 表示设备处于正常激活状态，可正常连接与交互。
	ActivationStatusActive = "active"
	// ActivationStatusFrozen 表示设备被暂时冻结，禁止设备连接，但保留激活关系。
	ActivationStatusFrozen = "frozen"
	// ActivationStatusRevoked 表示设备激活关系已被永久撤销。
	ActivationStatusRevoked = "revoked"
)

// 哨兵错误定义。
var (
	// ErrActivationNotFound 表示设备激活记录未找到。
	ErrActivationNotFound = errors.New("device activation not found")
	// ErrEmptyDeviceID 表示设备 Device-Id 为空。
	ErrEmptyDeviceID = errors.New("device id cannot be empty")
	// ErrInvalidActivation 表示设备激活结构体为 nil 或非法。
	ErrInvalidActivation = errors.New("invalid device activation")
	// ErrActivationBlocked 表示设备激活处于冻结或撤销状态。
	ErrActivationBlocked = errors.New("device activation is frozen or revoked")
)

// DeviceActivation 映射 device_activation 设备激活关系表。
//
// 业务用途：
// 记录 serial_number 与 Device-Id 的激活对应关系，作为已激活设备的运行态主表。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - serial_number: 设备出厂序列号，全局业务唯一，唯一索引 uk_device_activation_serial_number。
// - device_id: 后端设备标识 Device-Id，普通索引 idx_device_activation_device_id。
// - client_id: 固件/客户端安装实例标识，可为空。
// - activation_status: 激活状态（active / frozen / revoked）。
// - activated_at: 首次激活时间。
// - created_at: 记录创建时间。
// - updated_at: 记录最近更新时间。
type DeviceActivation struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber     string    `gorm:"uniqueIndex:uk_device_activation_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	DeviceID         string    `gorm:"index:idx_device_activation_device_id;column:device_id;size:64;not null" json:"device_id"`
	ClientID         string    `gorm:"column:client_id;size:64" json:"client_id,omitempty"`
	ActivationStatus string    `gorm:"column:activation_status;size:16;not null;default:'active'" json:"activation_status"`
	ActivatedAt      time.Time `gorm:"column:activated_at;not null" json:"activated_at"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 DeviceActivation 对应的表名。
func (DeviceActivation) TableName() string {
	return "device_activation"
}

// IsActive 判断设备激活状态当前是否可用。
func (a *DeviceActivation) IsActive() bool {
	if a == nil {
		return false
	}
	return a.ActivationStatus == ActivationStatusActive
}

// FindDeviceActivationBySerialNumber 根据设备序列号查询激活记录。
func (d *Database) FindDeviceActivationBySerialNumber(ctx context.Context, serialNumber string) (*DeviceActivation, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}

	var act DeviceActivation
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceActivation{}).
		Where("serial_number = ?", trimmedSN).
		Take(&act).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device activation by serial_number %q: %w", trimmedSN, ErrActivationNotFound)
		}
		return nil, fmt.Errorf("query device activation by serial_number: %w", err)
	}

	return &act, nil
}

// FindDeviceActivationByDeviceID 根据后端 Device-Id 查询最新的激活记录。
func (d *Database) FindDeviceActivationByDeviceID(ctx context.Context, deviceID string) (*DeviceActivation, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedDeviceID := strings.TrimSpace(deviceID)
	if trimmedDeviceID == "" {
		return nil, ErrEmptyDeviceID
	}

	var act DeviceActivation
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceActivation{}).
		Where("device_id = ?", trimmedDeviceID).
		Order("id DESC").
		Take(&act).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device activation by device_id %q: %w", trimmedDeviceID, ErrActivationNotFound)
		}
		return nil, fmt.Errorf("query device activation by device_id: %w", err)
	}

	return &act, nil
}

// FindDeviceActivationByID 根据自增主键 ID 查询设备激活记录。
func (d *Database) FindDeviceActivationByID(ctx context.Context, id uint64) (*DeviceActivation, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	if id == 0 {
		return nil, fmt.Errorf("find device activation by id %d: %w", id, ErrActivationNotFound)
	}

	var act DeviceActivation
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceActivation{}).
		Where("id = ?", id).
		Take(&act).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device activation by id %d: %w", id, ErrActivationNotFound)
		}
		return nil, fmt.Errorf("query device activation by id: %w", err)
	}

	return &act, nil
}

// CreateDeviceActivation 插入一条设备激活记录。
func (d *Database) CreateDeviceActivation(ctx context.Context, act *DeviceActivation) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if act == nil {
		return ErrInvalidActivation
	}

	act.SerialNumber = strings.TrimSpace(act.SerialNumber)
	if act.SerialNumber == "" {
		return ErrEmptySerialNumber
	}
	act.DeviceID = strings.TrimSpace(act.DeviceID)
	if act.DeviceID == "" {
		return ErrEmptyDeviceID
	}
	act.ClientID = strings.TrimSpace(act.ClientID)

	if act.ActivationStatus == "" {
		act.ActivationStatus = ActivationStatusActive
	}
	if act.ActivatedAt.IsZero() {
		act.ActivatedAt = time.Now()
	}

	if err := d.gormDB.WithContext(ctx).Create(act).Error; err != nil {
		return fmt.Errorf("create device activation: %w", err)
	}

	return nil
}

// UpdateDeviceActivationStatus 更新设备激活状态（如激活、冻结或撤销）。
func (d *Database) UpdateDeviceActivationStatus(ctx context.Context, serialNumber, status string) error {
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
		Model(&DeviceActivation{}).
		Where("serial_number = ?", trimmedSN).
		Update("activation_status", trimmedStatus)
	if result.Error != nil {
		return fmt.Errorf("update device activation status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("update device activation status for %q: %w", trimmedSN, ErrActivationNotFound)
	}

	return nil
}

// UpsertDeviceActivation 创建或更新设备激活记录。
// 若设备首次激活则创建新记录；若已有激活记录且未被冻结/撤销，则更新 DeviceID、ClientID 等运行态信息。
func (d *Database) UpsertDeviceActivation(ctx context.Context, act *DeviceActivation) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if act == nil {
		return ErrInvalidActivation
	}

	trimmedSN := strings.TrimSpace(act.SerialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}
	trimmedDeviceID := strings.TrimSpace(act.DeviceID)
	if trimmedDeviceID == "" {
		return ErrEmptyDeviceID
	}
	trimmedClientID := strings.TrimSpace(act.ClientID)

	var existing DeviceActivation
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceActivation{}).
		Where("serial_number = ?", trimmedSN).
		Take(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query device activation: %w", err)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		act.SerialNumber = trimmedSN
		act.DeviceID = trimmedDeviceID
		act.ClientID = trimmedClientID
		if act.ActivationStatus == "" {
			act.ActivationStatus = ActivationStatusActive
		}
		if act.ActivatedAt.IsZero() {
			act.ActivatedAt = time.Now()
		}
		if err := d.gormDB.WithContext(ctx).Create(act).Error; err != nil {
			return fmt.Errorf("create device activation: %w", err)
		}
		return nil
	}

	if !existing.IsActive() {
		return ErrActivationBlocked
	}

	updates := map[string]any{
		"device_id": trimmedDeviceID,
		"client_id": trimmedClientID,
	}
	if act.ActivationStatus != "" {
		updates["activation_status"] = act.ActivationStatus
	}

	if err := d.gormDB.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
		return fmt.Errorf("update device activation: %w", err)
	}

	act.ID = existing.ID
	act.SerialNumber = existing.SerialNumber
	act.DeviceID = trimmedDeviceID
	act.ClientID = trimmedClientID
	act.ActivationStatus = existing.ActivationStatus
	if actStatus, ok := updates["activation_status"].(string); ok {
		act.ActivationStatus = actStatus
	}
	act.ActivatedAt = existing.ActivatedAt
	act.CreatedAt = existing.CreatedAt

	return nil
}
