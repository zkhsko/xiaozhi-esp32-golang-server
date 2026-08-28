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
	// ErrBindingNotFound 表示设备用户绑定记录未找到。
	ErrBindingNotFound = errors.New("device user binding not found")
	// ErrEmptyUserId 表示用户 Id 为 0 或非法。
	ErrEmptyUserId = errors.New("user id cannot be empty or zero")
)

// DeviceUserRef 映射 device_user_ref 设备与用户绑定关系表。
//
// 业务用途：
// 记录 serial_number 与 user_id 的绑定对应关系。
// 一台设备最多绑定一个当前用户，一个用户可以绑定多台设备。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - serial_number: 设备序列号，全局业务唯一，唯一索引 uk_serial_number。
// - user_id: 绑定的用户 Id，非空，联合索引 idx_user_id_serial_number。
// - created_at: 创建时间（绑定时间）。
// - updated_at: 记录最近更新时间。
type DeviceUserRef struct {
	Id           uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber string    `gorm:"uniqueIndex:uk_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	UserId       uint64    `gorm:"index:idx_user_id_serial_number,priority:1;column:user_id;not null" json:"user_id"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 DeviceUserRef 对应的表名。
func (DeviceUserRef) TableName() string {
	return "device_user_ref"
}

// FindDeviceUserRefBySerialNumber 根据设备序列号查询绑定记录。
func (d *Database) FindDeviceUserRefBySerialNumber(ctx context.Context, serialNumber string) (*DeviceUserRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}

	var ref DeviceUserRef
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceUserRef{}).
		Where("serial_number = ?", trimmedSN).
		Take(&ref).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device user ref by serial_number %q: %w", trimmedSN, ErrBindingNotFound)
		}
		return nil, fmt.Errorf("query device user ref by serial_number: %w", err)
	}

	return &ref, nil
}

// UpsertDeviceUserRef 插入或更新设备与用户的绑定关系。
// 若设备已有绑定记录，则更新其绑定的用户 Id；若无绑定记录，则创建新的绑定记录。
func (d *Database) UpsertDeviceUserRef(ctx context.Context, serialNumber string, userId uint64) (*DeviceUserRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}
	if userId == 0 {
		return nil, ErrEmptyUserId
	}

	var ref DeviceUserRef
	err := d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DeviceUserRef
		findErr := tx.Where("serial_number = ?", trimmedSN).Take(&existing).Error
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device user ref: %w", findErr)
		}

		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			ref = DeviceUserRef{
				SerialNumber: trimmedSN,
				UserId:       userId,
			}
			if err := tx.Create(&ref).Error; err != nil {
				return fmt.Errorf("create device user ref: %w", err)
			}
			return nil
		}

		if existing.UserId != userId {
			if err := tx.Model(&existing).Update("user_id", userId).Error; err != nil {
				return fmt.Errorf("update device user ref: %w", err)
			}
		}
		ref = existing
		ref.UserId = userId
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("upsert device user ref: %w", err)
	}

	return &ref, nil
}
