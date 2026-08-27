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
	// ErrEmptyUserID 表示用户 ID 为 0 或非法。
	ErrEmptyUserID = errors.New("user id cannot be empty or zero")
	// ErrInvalidBinding 表示绑定记录结构体为 nil 或非法。
	ErrInvalidBinding = errors.New("invalid device user ref")
	// ErrDeviceAlreadyBoundToOther 表示设备已被其他用户绑定。
	ErrDeviceAlreadyBoundToOther = errors.New("device is already bound to another user")
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
// - user_id: 绑定的用户 ID，非空，联合索引 idx_user_id_serial_number。
// - created_at: 创建时间（绑定时间）。
// - updated_at: 记录最近更新时间。
type DeviceUserRef struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	SerialNumber string    `gorm:"uniqueIndex:uk_serial_number;column:serial_number;size:64;not null" json:"serial_number"`
	UserID       uint64    `gorm:"index:idx_user_id_serial_number,priority:1;column:user_id;not null" json:"user_id"`
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

// FindDeviceUserRefByID 根据自增主键 ID 查询设备用户绑定记录。
func (d *Database) FindDeviceUserRefByID(ctx context.Context, id uint64) (*DeviceUserRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	if id == 0 {
		return nil, fmt.Errorf("find device user ref by id %d: %w", id, ErrBindingNotFound)
	}

	var ref DeviceUserRef
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceUserRef{}).
		Where("id = ?", id).
		Take(&ref).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device user ref by id %d: %w", id, ErrBindingNotFound)
		}
		return nil, fmt.Errorf("query device user ref by id: %w", err)
	}

	return &ref, nil
}

// ListDeviceUserRefsByUserID 查询指定用户绑定的所有设备记录列表。
func (d *Database) ListDeviceUserRefsByUserID(ctx context.Context, userID uint64) ([]DeviceUserRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	if userID == 0 {
		return nil, ErrEmptyUserID
	}

	var refs []DeviceUserRef
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceUserRef{}).
		Where("user_id = ?", userID).
		Order("id DESC").
		Find(&refs).Error
	if err != nil {
		return nil, fmt.Errorf("query device user refs by user_id %d: %w", userID, err)
	}

	if refs == nil {
		refs = []DeviceUserRef{}
	}

	return refs, nil
}

// CreateDeviceUserRef 插入一条设备用户绑定记录。
func (d *Database) CreateDeviceUserRef(ctx context.Context, ref *DeviceUserRef) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if ref == nil {
		return ErrInvalidBinding
	}

	ref.SerialNumber = strings.TrimSpace(ref.SerialNumber)
	if ref.SerialNumber == "" {
		return ErrEmptySerialNumber
	}
	if ref.UserID == 0 {
		return ErrEmptyUserID
	}

	if err := d.gormDB.WithContext(ctx).Create(ref).Error; err != nil {
		return fmt.Errorf("create device user ref: %w", err)
	}

	return nil
}

// BindDevice 将设备绑定至指定用户。
// 若设备已绑定该用户，则幂等返回当前绑定；
// 若设备已被其他用户绑定，则返回 ErrDeviceAlreadyBoundToOther 拒绝直接覆盖；
// 若设备未绑定，则创建新的绑定记录。
func (d *Database) BindDevice(ctx context.Context, serialNumber string, userID uint64) (*DeviceUserRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}
	if userID == 0 {
		return nil, ErrEmptyUserID
	}

	var existing DeviceUserRef
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceUserRef{}).
		Where("serial_number = ?", trimmedSN).
		Take(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("query device user ref before bind: %w", err)
	}

	if err == nil {
		if existing.UserID == userID {
			return &existing, nil
		}
		return nil, fmt.Errorf("device %q already bound to user %d: %w", trimmedSN, existing.UserID, ErrDeviceAlreadyBoundToOther)
	}

	newRef := DeviceUserRef{
		SerialNumber: trimmedSN,
		UserID:       userID,
	}
	if createErr := d.gormDB.WithContext(ctx).Create(&newRef).Error; createErr != nil {
		// 并发创建冲突时重新读取判断
		var retryExisting DeviceUserRef
		queryErr := d.gormDB.WithContext(ctx).
			Model(&DeviceUserRef{}).
			Where("serial_number = ?", trimmedSN).
			Take(&retryExisting).Error
		if queryErr == nil {
			if retryExisting.UserID == userID {
				return &retryExisting, nil
			}
			return nil, fmt.Errorf("device %q already bound to user %d: %w", trimmedSN, retryExisting.UserID, ErrDeviceAlreadyBoundToOther)
		}
		return nil, fmt.Errorf("create device user ref on bind: %w", createErr)
	}

	return &newRef, nil
}

// UnbindDevice 解绑指定序列号的设备当前绑定关系。
func (d *Database) UnbindDevice(ctx context.Context, serialNumber string) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}

	result := d.gormDB.WithContext(ctx).
		Where("serial_number = ?", trimmedSN).
		Delete(&DeviceUserRef{})
	if result.Error != nil {
		return fmt.Errorf("delete device user ref: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("unbind device for serial_number %q: %w", trimmedSN, ErrBindingNotFound)
	}

	return nil
}

// UnbindDeviceFromUser 仅当设备属于指定用户时解除绑定，防止越权解绑。
func (d *Database) UnbindDeviceFromUser(ctx context.Context, serialNumber string, userID uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}
	if userID == 0 {
		return ErrEmptyUserID
	}

	result := d.gormDB.WithContext(ctx).
		Where("serial_number = ? AND user_id = ?", trimmedSN, userID).
		Delete(&DeviceUserRef{})
	if result.Error != nil {
		return fmt.Errorf("delete device user ref for user %d: %w", userID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("unbind device %q for user %d: %w", trimmedSN, userID, ErrBindingNotFound)
	}

	return nil
}

// TransferDeviceBinding 转移设备绑定至新用户（管理员或强制转移场景）。
func (d *Database) TransferDeviceBinding(ctx context.Context, serialNumber string, newUserID uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return ErrEmptySerialNumber
	}
	if newUserID == 0 {
		return ErrEmptyUserID
	}

	result := d.gormDB.WithContext(ctx).
		Model(&DeviceUserRef{}).
		Where("serial_number = ?", trimmedSN).
		Update("user_id", newUserID)
	if result.Error != nil {
		return fmt.Errorf("transfer device user ref: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("transfer device user ref for %q: %w", trimmedSN, ErrBindingNotFound)
	}

	return nil
}

// UpsertDeviceUserRef 插入或更新设备与用户的绑定关系。
// 若设备已有绑定记录，则更新其绑定的用户 ID；若无绑定记录，则创建新的绑定记录。
func (d *Database) UpsertDeviceUserRef(ctx context.Context, serialNumber string, userID uint64) (*DeviceUserRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedSN := strings.TrimSpace(serialNumber)
	if trimmedSN == "" {
		return nil, ErrEmptySerialNumber
	}
	if userID == 0 {
		return nil, ErrEmptyUserID
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
				UserID:       userID,
			}
			if err := tx.Create(&ref).Error; err != nil {
				return fmt.Errorf("create device user ref: %w", err)
			}
			return nil
		}

		if existing.UserID != userID {
			if err := tx.Model(&existing).Update("user_id", userID).Error; err != nil {
				return fmt.Errorf("update device user ref: %w", err)
			}
		}
		ref = existing
		ref.UserID = userID
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("upsert device user ref: %w", err)
	}

	return &ref, nil
}
