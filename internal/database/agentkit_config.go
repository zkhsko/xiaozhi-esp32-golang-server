package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 哨兵错误定义。
var (
	// ErrAgentKitConfigNotFound 表示 AgentKit 配置记录未找到。
	ErrAgentKitConfigNotFound = errors.New("agentkit config not found")
	// ErrInvalidAgentKitConfig 表示 AgentKit 配置结构体为 nil 或非法。
	ErrInvalidAgentKitConfig = errors.New("invalid agentkit config")
	// ErrInvalidAgentKitConfigId 表示 AgentKit 配置 Id 为 0 或非法。
	ErrInvalidAgentKitConfigId = errors.New("agentkit config id cannot be empty or zero")
	// ErrEmptyAgentKitToolName 表示 AgentKit 工具名称为空。
	ErrEmptyAgentKitToolName = errors.New("agentkit tool name cannot be empty")
	// ErrInvalidAgentKitToolNameLength 表示 AgentKit 工具名称长度超过 128 字节。
	ErrInvalidAgentKitToolNameLength = errors.New("agentkit tool name length exceeds 128 bytes")
	// ErrEmptyAgentKitToolConfig 表示 AgentKit 工具配置为空。
	ErrEmptyAgentKitToolConfig = errors.New("agentkit tool config cannot be empty")
	// ErrInvalidAgentKitToolConfigJSON 表示 AgentKit 工具配置非合法 JSON 格式。
	ErrInvalidAgentKitToolConfigJSON = errors.New("agentkit tool config must be valid json format")
	// ErrAgentKitToolNameDuplicate 表示 AgentKit 工具名称已存在。
	ErrAgentKitToolNameDuplicate = errors.New("agentkit tool name already exists")
)

// AgentKitConfig 映射 agentkit_config 内建 Agent 工具配置表。
//
// 业务用途：
// 保存内建 Agent 工具（如天气查询、搜索等）的运行参数与开关状态。
//
// 字段约束与索引规范：
// - id: 主键自增。
// - tool_name: 内建工具稳定名称，全局业务唯一，最大 128 字节，唯一索引 uk_agentkit_config_tool_name。
// - tool_config: 工具配置（JSON 格式，可能包含敏感信息），文本类型。
// - enabled: 是否启用该内建工具（布尔值，默认 true）。
// - created_at: 创建时间。
// - updated_at: 更新时间。
type AgentKitConfig struct {
	Id         uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	ToolName   string    `gorm:"column:tool_name;size:128;not null;uniqueIndex:uk_agentkit_config_tool_name" json:"tool_name"`
	ToolConfig string    `gorm:"column:tool_config;type:text;not null" json:"tool_config"`
	Enabled    bool      `gorm:"column:enabled;not null" json:"enabled"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 AgentKitConfig 对应的表名。
func (AgentKitConfig) TableName() string {
	return "agentkit_config"
}

// Validate 校验 AgentKitConfig 结构体字段合法性。
func (c *AgentKitConfig) Validate() error {
	if c == nil {
		return ErrInvalidAgentKitConfig
	}

	name := strings.TrimSpace(c.ToolName)
	if name == "" {
		return ErrEmptyAgentKitToolName
	}
	if len(name) > 128 {
		return ErrInvalidAgentKitToolNameLength
	}

	cfg := strings.TrimSpace(c.ToolConfig)
	if cfg == "" {
		return ErrEmptyAgentKitToolConfig
	}
	if !json.Valid([]byte(cfg)) {
		return ErrInvalidAgentKitToolConfigJSON
	}

	return nil
}

// AgentKitConfigFilter 定义 AgentKit 配置查询过滤条件。
type AgentKitConfigFilter struct {
	ToolName string
	Enabled  *bool
	Page     int
	PageSize int
}

// CreateAgentKitConfig 创建新的 AgentKit 配置。
func (d *Database) CreateAgentKitConfig(ctx context.Context, cfg *AgentKitConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil {
		return ErrInvalidAgentKitConfig
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	cfg.ToolName = strings.TrimSpace(cfg.ToolName)
	cfg.ToolConfig = strings.TrimSpace(cfg.ToolConfig)

	var existing AgentKitConfig
	if err := d.gormDB.WithContext(ctx).Where("tool_name = ?", cfg.ToolName).Take(&existing).Error; err == nil {
		return ErrAgentKitToolNameDuplicate
	}

	if err := d.gormDB.WithContext(ctx).Create(cfg).Error; err != nil {
		return fmt.Errorf("create agentkit config: %w", err)
	}

	return nil
}

// FindAgentKitConfigById 根据 Id 查询 AgentKit 配置。
func (d *Database) FindAgentKitConfigById(ctx context.Context, id uint64) (*AgentKitConfig, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return nil, ErrInvalidAgentKitConfigId
	}

	var cfg AgentKitConfig
	err := d.gormDB.WithContext(ctx).
		Model(&AgentKitConfig{}).
		Where("id = ?", id).
		Take(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find agentkit config by id %d: %w", id, ErrAgentKitConfigNotFound)
		}
		return nil, fmt.Errorf("query agentkit config by id: %w", err)
	}

	return &cfg, nil
}

// FindAgentKitConfigByToolName 根据 ToolName 唯一查询 AgentKit 配置。
func (d *Database) FindAgentKitConfigByToolName(ctx context.Context, toolName string) (*AgentKitConfig, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	trimmed := strings.TrimSpace(toolName)
	if trimmed == "" {
		return nil, ErrEmptyAgentKitToolName
	}

	var cfg AgentKitConfig
	err := d.gormDB.WithContext(ctx).
		Model(&AgentKitConfig{}).
		Where("tool_name = ?", trimmed).
		Take(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find agentkit config by tool_name %q: %w", trimmed, ErrAgentKitConfigNotFound)
		}
		return nil, fmt.Errorf("query agentkit config by tool_name: %w", err)
	}

	return &cfg, nil
}

// UpdateAgentKitConfigById 按主键 Id 覆盖更新 AgentKit 配置。
// 更新结果影响行数为 0 时返回 ErrAgentKitConfigNotFound。
func (d *Database) UpdateAgentKitConfigById(ctx context.Context, cfg *AgentKitConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil || cfg.Id == 0 {
		return ErrInvalidAgentKitConfigId
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	toolName := strings.TrimSpace(cfg.ToolName)
	var duplicate AgentKitConfig
	if err := d.gormDB.WithContext(ctx).Where("tool_name = ? AND id <> ?", toolName, cfg.Id).Take(&duplicate).Error; err == nil {
		return ErrAgentKitToolNameDuplicate
	}

	updates := map[string]any{
		"tool_name":   toolName,
		"tool_config": strings.TrimSpace(cfg.ToolConfig),
		"enabled":     cfg.Enabled,
		"updated_at":  time.Now(),
	}

	res := d.gormDB.WithContext(ctx).Model(&AgentKitConfig{}).Where("id = ?", cfg.Id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update agentkit config by id: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update agentkit config by id %d: %w", cfg.Id, ErrAgentKitConfigNotFound)
	}

	return nil
}

// DeleteAgentKitConfig 删除指定 Id 的 AgentKit 配置记录。
func (d *Database) DeleteAgentKitConfig(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidAgentKitConfigId
	}

	res := d.gormDB.WithContext(ctx).Where("id = ?", id).Delete(&AgentKitConfig{})
	if res.Error != nil {
		return fmt.Errorf("delete agentkit config: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrAgentKitConfigNotFound
	}
	return nil
}

// BatchDeleteAgentKitConfigs 批量删除指定 Id 列表的 AgentKit 配置记录。
func (d *Database) BatchDeleteAgentKitConfigs(ctx context.Context, ids []uint64) error {
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

	if err := d.gormDB.WithContext(ctx).Where("id IN ?", validIds).Delete(&AgentKitConfig{}).Error; err != nil {
		return fmt.Errorf("batch delete agentkit configs: %w", err)
	}
	return nil
}

// ListAgentKitConfigs 分页查询 AgentKit 配置列表。
func (d *Database) ListAgentKitConfigs(ctx context.Context, filter AgentKitConfigFilter) ([]*AgentKitConfig, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&AgentKitConfig{})

	if toolName := strings.TrimSpace(filter.ToolName); toolName != "" {
		query = query.Where("tool_name LIKE ?", "%"+toolName+"%")
	}
	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count agentkit configs: %w", err)
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
	var configs []*AgentKitConfig
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&configs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query agentkit configs list: %w", err)
	}

	return configs, total, nil
}

// ListEnabledAgentKitConfigs 查询所有处于启用状态的 AgentKit 配置。
func (d *Database) ListEnabledAgentKitConfigs(ctx context.Context) ([]*AgentKitConfig, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	var configs []*AgentKitConfig
	err := d.gormDB.WithContext(ctx).
		Model(&AgentKitConfig{}).
		Where("enabled = ?", true).
		Order("id ASC").
		Find(&configs).Error
	if err != nil {
		return nil, fmt.Errorf("query enabled agentkit configs: %w", err)
	}

	return configs, nil
}
