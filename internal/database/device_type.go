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
	// ErrDeviceTypeNotFound 表示设备类型与 Agent 关联关系未找到。
	ErrDeviceTypeNotFound = errors.New("device type not found")
	// ErrInvalidDeviceType 表示设备类型结构体为 nil 或非法。
	ErrInvalidDeviceType = errors.New("invalid device type")
	// ErrInvalidDeviceTypeId 表示设备类型 Id 为 0 或非法。
	ErrInvalidDeviceTypeId = errors.New("device type id cannot be empty or zero")
	// ErrEmptyDeviceType 表示设备类型为空。
	ErrEmptyDeviceType = errors.New("device type cannot be empty")
	// ErrInvalidDeviceTypeLength 表示设备类型长度超过 32 字节。
	ErrInvalidDeviceTypeLength = errors.New("device type length exceeds 32 bytes")
	// ErrInvalidAgentConfigId 表示 Agent 配置 Id 为 0 或非法。
	ErrInvalidAgentConfigId = errors.New("agent config id cannot be empty or zero")
	// ErrDeviceTypeAlreadyExists 表示设备类型已存在。
	ErrDeviceTypeAlreadyExists = errors.New("device type already exists")
	// ErrReferencedAgentNotFound 表示引用的 Agent 配置不存在。
	ErrReferencedAgentNotFound = errors.New("referenced agent config not found")
)

// DeviceType 映射 device_type 设备类型与 Agent 配置关联关系表。
//
// 业务用途：
// 记录 device_type 与 agent_config_id 的对应关系。
// 一种设备类型全局唯一对应一个 Agent 配置（device_type 为全局唯一索引）。
// 一个 Agent 配置可以被多种设备类型关联。
//
// 字段约束与索引规范：
// - id: 自增主键。
// - device_type: 设备类型，全局业务唯一，唯一索引 uk_device_type。
// - agent_config_id: 关联的 Agent 配置 Id，非空，普通索引 idx_agent_config_id。
// - created_at: 创建时间。
// - updated_at: 记录最近更新时间。
type DeviceType struct {
	Id            uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	DeviceType    string    `gorm:"uniqueIndex:uk_device_type;column:device_type;size:32;not null" json:"device_type"`
	AgentConfigId uint64    `gorm:"index:idx_agent_config_id;column:agent_config_id;not null" json:"agent_config_id"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 DeviceType 对应的表名。
func (DeviceType) TableName() string {
	return "device_type"
}

// Validate 校验 DeviceType 结构体字段合法性。
func (c *DeviceType) Validate() error {
	if c == nil {
		return ErrInvalidDeviceType
	}

	dt := strings.TrimSpace(c.DeviceType)
	if dt == "" {
		return ErrEmptyDeviceType
	}
	if len(dt) > 32 {
		return ErrInvalidDeviceTypeLength
	}

	if c.AgentConfigId == 0 {
		return ErrInvalidAgentConfigId
	}

	return nil
}

// DeviceTypeFilter 定义设备类型查询过滤条件。
type DeviceTypeFilter struct {
	DeviceType    string
	AgentConfigId uint64
	Page          int
	PageSize      int
}

// CreateDeviceType 创建新的设备类型与 Agent 关联记录。
func (d *Database) CreateDeviceType(ctx context.Context, dt *DeviceType) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if dt == nil {
		return ErrInvalidDeviceType
	}

	if err := dt.Validate(); err != nil {
		return err
	}

	dt.DeviceType = strings.TrimSpace(dt.DeviceType)

	// 校验关联的 Agent 是否存在
	var agent AgentConfig
	if err := d.gormDB.WithContext(ctx).Where("id = ?", dt.AgentConfigId).Take(&agent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReferencedAgentNotFound
		}
		return fmt.Errorf("query referenced agent config: %w", err)
	}

	// 检查设备类型是否已存在
	var existing DeviceType
	if err := d.gormDB.WithContext(ctx).Where("device_type = ?", dt.DeviceType).Take(&existing).Error; err == nil {
		return ErrDeviceTypeAlreadyExists
	}

	if err := d.gormDB.WithContext(ctx).Create(dt).Error; err != nil {
		return fmt.Errorf("create device type: %w", err)
	}

	return nil
}

// FindDeviceTypeById 根据 Id 查询设备类型记录。
func (d *Database) FindDeviceTypeById(ctx context.Context, id uint64) (*DeviceType, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return nil, ErrInvalidDeviceTypeId
	}

	var dt DeviceType
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceType{}).
		Where("id = ?", id).
		Take(&dt).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device type by id %d: %w", id, ErrDeviceTypeNotFound)
		}
		return nil, fmt.Errorf("query device type by id: %w", err)
	}

	return &dt, nil
}

// UpdateDeviceTypeById 按主键 Id 覆盖更新设备类型配置。
func (d *Database) UpdateDeviceTypeById(ctx context.Context, dt *DeviceType) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if dt == nil || dt.Id == 0 {
		return ErrInvalidDeviceTypeId
	}

	if err := dt.Validate(); err != nil {
		return err
	}

	dt.DeviceType = strings.TrimSpace(dt.DeviceType)

	// 校验关联的 Agent 是否存在
	var agent AgentConfig
	if err := d.gormDB.WithContext(ctx).Where("id = ?", dt.AgentConfigId).Take(&agent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReferencedAgentNotFound
		}
		return fmt.Errorf("query referenced agent config: %w", err)
	}

	// 检查 device_type 是否与其他记录冲突
	var existing DeviceType
	if err := d.gormDB.WithContext(ctx).Where("device_type = ? AND id != ?", dt.DeviceType, dt.Id).Take(&existing).Error; err == nil {
		return ErrDeviceTypeAlreadyExists
	}

	updates := map[string]any{
		"device_type":     dt.DeviceType,
		"agent_config_id": dt.AgentConfigId,
		"updated_at":      time.Now(),
	}

	res := d.gormDB.WithContext(ctx).
		Model(&DeviceType{}).
		Where("id = ?", dt.Id).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update device type by id: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update device type by id %d: %w", dt.Id, ErrDeviceTypeNotFound)
	}

	return nil
}

// FindDeviceTypeByDeviceType 根据设备类型查询关联的 Agent 配置记录。
func (d *Database) FindDeviceTypeByDeviceType(ctx context.Context, deviceType string) (*DeviceType, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedType := strings.TrimSpace(deviceType)
	if trimmedType == "" {
		return nil, ErrEmptyDeviceType
	}

	var dt DeviceType
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceType{}).
		Where("device_type = ?", trimmedType).
		Take(&dt).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find device type by device_type %q: %w", trimmedType, ErrDeviceTypeNotFound)
		}
		return nil, fmt.Errorf("query device type by device_type: %w", err)
	}

	return &dt, nil
}

// UpsertDeviceType 插入或更新设备类型与 Agent 配置的关联关系。
// 若该设备类型已有配置关联，则更新其 agent_config_id；若无关联记录，则创建新的关联记录。
func (d *Database) UpsertDeviceType(ctx context.Context, deviceType string, agentConfigId uint64) (*DeviceType, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	trimmedType := strings.TrimSpace(deviceType)
	if trimmedType == "" {
		return nil, ErrEmptyDeviceType
	}
	if agentConfigId == 0 {
		return nil, ErrInvalidAgentConfigId
	}

	var dt DeviceType
	err := d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DeviceType
		findErr := tx.Where("device_type = ?", trimmedType).Take(&existing).Error
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("query device type: %w", findErr)
		}

		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			dt = DeviceType{
				DeviceType:    trimmedType,
				AgentConfigId: agentConfigId,
			}
			if err := tx.Create(&dt).Error; err != nil {
				return fmt.Errorf("create device type: %w", err)
			}
			return nil
		}

		if existing.AgentConfigId != agentConfigId {
			if err := tx.Model(&existing).Update("agent_config_id", agentConfigId).Error; err != nil {
				return fmt.Errorf("update device type: %w", err)
			}
		}
		dt = existing
		dt.AgentConfigId = agentConfigId
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("upsert device type: %w", err)
	}

	return &dt, nil
}

// FindDeviceTypesByAgentConfigId 根据 Agent 配置 Id 查询关联的所有设备类型列表。
func (d *Database) FindDeviceTypesByAgentConfigId(ctx context.Context, agentConfigId uint64) ([]*DeviceType, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if agentConfigId == 0 {
		return nil, ErrInvalidAgentConfigId
	}

	var dts []*DeviceType
	err := d.gormDB.WithContext(ctx).
		Model(&DeviceType{}).
		Where("agent_config_id = ?", agentConfigId).
		Order("id ASC").
		Find(&dts).Error
	if err != nil {
		return nil, fmt.Errorf("find device types by agent_config_id: %w", err)
	}

	return dts, nil
}

// ListDeviceTypes 分页查询所有设备类型与 Agent 关联关系列表。
func (d *Database) ListDeviceTypes(ctx context.Context, filter DeviceTypeFilter) ([]*DeviceType, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&DeviceType{})

	if dt := strings.TrimSpace(filter.DeviceType); dt != "" {
		query = query.Where("device_type LIKE ?", "%"+dt+"%")
	}
	if filter.AgentConfigId > 0 {
		query = query.Where("agent_config_id = ?", filter.AgentConfigId)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count device types: %w", err)
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
	var dts []*DeviceType
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&dts).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list device types: %w", err)
	}

	return dts, total, nil
}

// DeleteDeviceType 删除指定 Id 的设备类型记录。
func (d *Database) DeleteDeviceType(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidDeviceTypeId
	}

	res := d.gormDB.WithContext(ctx).Where("id = ?", id).Delete(&DeviceType{})
	if res.Error != nil {
		return fmt.Errorf("delete device type: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrDeviceTypeNotFound
	}
	return nil
}

// BatchDeleteDeviceTypes 批量删除指定 Id 列表的设备类型记录。
func (d *Database) BatchDeleteDeviceTypes(ctx context.Context, ids []uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if len(ids) == 0 {
		return nil
	}

	validIds := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			validIds = append(validIds, id)
		}
	}
	if len(validIds) == 0 {
		return nil
	}

	if err := d.gormDB.WithContext(ctx).Where("id IN ?", validIds).Delete(&DeviceType{}).Error; err != nil {
		return fmt.Errorf("batch delete device types: %w", err)
	}
	return nil
}

// DeleteDeviceTypeByDeviceType 根据设备类型删除关联记录。
func (d *Database) DeleteDeviceTypeByDeviceType(ctx context.Context, deviceType string) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}

	trimmedType := strings.TrimSpace(deviceType)
	if trimmedType == "" {
		return ErrEmptyDeviceType
	}

	result := d.gormDB.WithContext(ctx).
		Where("device_type = ?", trimmedType).
		Delete(&DeviceType{})
	if result.Error != nil {
		return fmt.Errorf("delete device type by device_type: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete device type by device_type %q: %w", trimmedType, ErrDeviceTypeNotFound)
	}
	return nil
}
