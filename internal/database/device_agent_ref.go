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
	// ErrDeviceAgentRefNotFound 表示设备类型与 Agent 关联关系未找到。
	ErrDeviceAgentRefNotFound = errors.New("device agent ref not found")
	// ErrEmptyDeviceType 表示设备类型为空。
	ErrEmptyDeviceType = errors.New("device type cannot be empty")
	// ErrInvalidAgentConfigID 表示 Agent 配置 ID 为 0 或非法。
	ErrInvalidAgentConfigID = errors.New("agent config id cannot be empty or zero")
)

// DeviceAgentRef 映射 device_agent_ref 设备类型与 Agent 配置关联关系表。
//
// 业务用途：
// 记录 device_type 与 agent_config_id 的对应关系。
// 一种设备类型全局唯一对应一个 Agent 配置（device_type 为全局唯一索引）。
// 一个 Agent 配置可以被多种设备类型关联。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - device_type: 设备类型，全局业务唯一，唯一索引 uk_device_type。
// - agent_config_id: 关联的 Agent 配置 ID，非空，普通索引 idx_agent_config_id。
// - created_at: 创建时间。
// - updated_at: 记录最近更新时间。
type DeviceAgentRef struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	DeviceType    string    `gorm:"uniqueIndex:uk_device_type;column:device_type;size:32;not null" json:"device_type"`
	AgentConfigID uint64    `gorm:"index:idx_agent_config_id;column:agent_config_id;not null" json:"agent_config_id"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 DeviceAgentRef 对应的表名。
func (DeviceAgentRef) TableName() string {
	return "device_agent_ref"
}

// FindDeviceAgentRefByDeviceType 根据设备类型查询关联的 Agent 配置记录。
func (d *Database) FindDeviceAgentRefByDeviceType(ctx context.Context, deviceType string) (*DeviceAgentRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedType := strings.TrimSpace(deviceType)
	if trimmedType == "" {
		return nil, ErrEmptyDeviceType
	}

	var ref DeviceAgentRef
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAgentRef{}).
		Where("device_type = ?", trimmedType).
		Take(&ref).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device agent ref by device_type %q: %w", trimmedType, ErrDeviceAgentRefNotFound)
		}
		return nil, fmt.Errorf("query device agent ref by device_type: %w", err)
	}

	return &ref, nil
}

// UpsertDeviceAgentRef 插入或更新设备类型与 Agent 配置的关联关系。
// 若该设备类型已有配置关联，则更新其 agent_config_id；若无关联记录，则创建新的关联记录。
func (d *Database) UpsertDeviceAgentRef(ctx context.Context, deviceType string, agentConfigID uint64) (*DeviceAgentRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedType := strings.TrimSpace(deviceType)
	if trimmedType == "" {
		return nil, ErrEmptyDeviceType
	}
	if agentConfigID == 0 {
		return nil, ErrInvalidAgentConfigID
	}

	var ref DeviceAgentRef
	err := d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DeviceAgentRef
		findErr := tx.Where("device_type = ?", trimmedType).Take(&existing).Error
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device agent ref: %w", findErr)
		}

		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			ref = DeviceAgentRef{
				DeviceType:    trimmedType,
				AgentConfigID: agentConfigID,
			}
			if err := tx.Create(&ref).Error; err != nil {
				return fmt.Errorf("create device agent ref: %w", err)
			}
			return nil
		}

		if existing.AgentConfigID != agentConfigID {
			if err := tx.Model(&existing).Update("agent_config_id", agentConfigID).Error; err != nil {
				return fmt.Errorf("update device agent ref: %w", err)
			}
		}
		ref = existing
		ref.AgentConfigID = agentConfigID
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("upsert device agent ref: %w", err)
	}

	return &ref, nil
}

// FindDeviceAgentRefsByAgentConfigID 根据 Agent 配置 ID 查询关联的所有设备类型列表。
func (d *Database) FindDeviceAgentRefsByAgentConfigID(ctx context.Context, agentConfigID uint64) ([]*DeviceAgentRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if agentConfigID == 0 {
		return nil, ErrInvalidAgentConfigID
	}

	var refs []*DeviceAgentRef
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAgentRef{}).
		Where("agent_config_id = ?", agentConfigID).
		Order("id ASC").
		Find(&refs).Error
	if err != nil {
		return nil, fmt.Errorf("find device agent refs by agent_config_id: %w", err)
	}

	return refs, nil
}

// ListDeviceAgentRefs 查询所有设备类型与 Agent 关联关系列表。
func (d *Database) ListDeviceAgentRefs(ctx context.Context) ([]*DeviceAgentRef, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	var refs []*DeviceAgentRef
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceAgentRef{}).
		Order("id ASC").
		Find(&refs).Error
	if err != nil {
		return nil, fmt.Errorf("list device agent refs: %w", err)
	}

	return refs, nil
}

// DeleteDeviceAgentRefByDeviceType 根据设备类型删除关联记录。
func (d *Database) DeleteDeviceAgentRefByDeviceType(ctx context.Context, deviceType string) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedType := strings.TrimSpace(deviceType)
	if trimmedType == "" {
		return ErrEmptyDeviceType
	}

	result := d.gormDB.WithContext(ctx).
		Where("device_type = ?", trimmedType).
		Delete(&DeviceAgentRef{})
	if result.Error != nil {
		return fmt.Errorf("delete device agent ref by device_type: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete device agent ref by device_type %q: %w", trimmedType, ErrDeviceAgentRefNotFound)
	}
	return nil
}
