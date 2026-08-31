package database

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 哨兵错误定义。
var (
	// ErrLLMConfigNotFound 表示 LLM 配置记录未找到。
	ErrLLMConfigNotFound = errors.New("llm config not found")
	// ErrInvalidLLMConfig 表示 LLM 配置结构体为 nil 或非法。
	ErrInvalidLLMConfig = errors.New("invalid llm config")
	// ErrInvalidLLMConfigId 表示 LLM 配置 Id 为 0 或非法。
	ErrInvalidLLMConfigId = errors.New("llm config id cannot be empty or zero")
	// ErrEmptyLLMConfigName 表示 LLM 配置名称为空。
	ErrEmptyLLMConfigName = errors.New("llm config name cannot be empty")
	// ErrInvalidLLMConfigNameLength 表示 LLM 配置名称长度超过 128 字节。
	ErrInvalidLLMConfigNameLength = errors.New("llm config name length exceeds 128 bytes")
	// ErrEmptyLLMProvider 表示 LLM 服务商/平台为空。
	ErrEmptyLLMProvider = errors.New("llm provider cannot be empty")
	// ErrInvalidLLMProviderLength 表示 LLM 服务商/平台长度超过 64 字节。
	ErrInvalidLLMProviderLength = errors.New("llm provider length exceeds 64 bytes")
	// ErrEmptyLLMEndpoint 表示 LLM Endpoint 为空。
	ErrEmptyLLMEndpoint = errors.New("llm endpoint cannot be empty")
	// ErrInvalidLLMEndpointScheme 表示 LLM Endpoint 协议非法（只允许 http 或 https）。
	ErrInvalidLLMEndpointScheme = errors.New("llm endpoint scheme must be http or https")
	// ErrInvalidLLMEndpointLength 表示 LLM Endpoint 长度超过 1024 字节。
	ErrInvalidLLMEndpointLength = errors.New("llm endpoint length exceeds 1024 bytes")
	// ErrInvalidLLMAPIKeyLength 表示 LLM API Key 长度超过 1024 字节。
	ErrInvalidLLMAPIKeyLength = errors.New("llm api_key length exceeds 1024 bytes")
	// ErrEmptyLLMModel 表示 LLM 模型为空。
	ErrEmptyLLMModel = errors.New("llm model cannot be empty")
	// ErrInvalidLLMModelLength 表示 LLM 模型长度超过 255 字节。
	ErrInvalidLLMModelLength = errors.New("llm model length exceeds 255 bytes")
	// ErrInvalidLLMProxyURLLength 表示 LLM Proxy URL 长度超过 1024 字节。
	ErrInvalidLLMProxyURLLength = errors.New("llm proxy_url length exceeds 1024 bytes")
	// ErrInvalidLLMProxyURLScheme 表示 LLM Proxy URL 协议非法（只允许 http, https, socks5, socks5h）。
	ErrInvalidLLMProxyURLScheme = errors.New("llm proxy_url scheme must be http, https, socks5, or socks5h")
	// ErrInvalidLLMFirstTokenTimeout 表示 LLM 首 Token 超时不在合法范围（3000 ~ 30000 毫秒）。
	ErrInvalidLLMFirstTokenTimeout = errors.New("llm first_token_timeout_ms must be between 3000 and 30000 ms")
	// ErrInvalidLLMOverallTimeout 表示 LLM 总超时不在合法范围（10000 ~ 180000 毫秒）。
	ErrInvalidLLMOverallTimeout = errors.New("llm overall_timeout_ms must be between 10000 and 180000 ms")
	// ErrLLMOverallTimeoutMustExceedFirstToken 表示 LLM 总超时未大于首 Token 超时。
	ErrLLMOverallTimeoutMustExceedFirstToken = errors.New("llm overall_timeout_ms must be greater than first_token_timeout_ms")
)

// LLMConfig 映射 llm_config 大语言模型 LLM 配置表。
//
// 业务用途：
// 保存百炼或大模型服务所需的 HTTP 连接地址、鉴权 Key、模型以及超时配置。
// 一个 LLM 配置可以被多个 Agent 关联复用。
//
// 字段约束与索引规范：
// - id: 主键自增。
// - name: 展示名称，非唯一（允许重复），最大 128 字节。
// - provider: 服务商/平台标识（如 dashscope / deepseek / kimi / zai 等），默认空字符串，最大 64 字节。
// - endpoint: LLM HTTP Endpoint，必须以 http:// 或 https:// 开头，最大 1024 字节。
// - api_key: 明文 API Key（脱敏时不输出，json:"-"），最大 1024 字节。
// - model: LLM 模型名称，最大 255 字节。
// - proxy_url: 代理服务器地址（支持 http/https/socks5/socks5h，非空即启用），最大 1024 字节。
// - first_token_timeout_ms: 首 Token 超时时间（毫秒），合法范围 3000 ~ 30000。
// - overall_timeout_ms: 总超时时间（毫秒），合法范围 10000 ~ 180000，且必须大于 first_token_timeout_ms。
// - enabled: 是否允许 Agent 引用（布尔值，默认 true）。
// - created_at: 创建时间。
// - updated_at: 更新时间。
type LLMConfig struct {
	Id                  uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	Name                string    `gorm:"column:name;size:128;not null" json:"name"`
	Provider            string    `gorm:"column:provider;size:64;not null;default:''" json:"provider"`
	Endpoint            string    `gorm:"column:endpoint;size:1024;not null" json:"endpoint"`
	APIKey              string    `gorm:"column:api_key;size:1024;not null;default:''" json:"-"`
	Model               string    `gorm:"column:model;size:255;not null" json:"model"`
	ProxyURL            string    `gorm:"column:proxy_url;size:1024;not null;default:''" json:"proxy_url"`
	FirstTokenTimeoutMS int64     `gorm:"column:first_token_timeout_ms;not null;default:5000" json:"first_token_timeout_ms"`
	OverallTimeoutMS    int64     `gorm:"column:overall_timeout_ms;not null;default:30000" json:"overall_timeout_ms"`
	Enabled             bool      `gorm:"column:enabled;not null" json:"enabled"`
	CreatedAt           time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 LLMConfig 对应的表名。
func (LLMConfig) TableName() string {
	return "llm_config"
}

// Validate 校验 LLMConfig 结构体字段合法性。
func (c *LLMConfig) Validate() error {
	if c == nil {
		return ErrInvalidLLMConfig
	}

	name := strings.TrimSpace(c.Name)
	if name == "" {
		return ErrEmptyLLMConfigName
	}
	if len(name) > 128 {
		return ErrInvalidLLMConfigNameLength
	}

	provider := strings.TrimSpace(c.Provider)
	if len(provider) > 64 {
		return ErrInvalidLLMProviderLength
	}

	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		return ErrEmptyLLMEndpoint
	}
	if len(endpoint) > 1024 {
		return ErrInvalidLLMEndpointLength
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrInvalidLLMEndpointScheme
	}

	if len(c.APIKey) > 1024 {
		return ErrInvalidLLMAPIKeyLength
	}

	model := strings.TrimSpace(c.Model)
	if model == "" {
		return ErrEmptyLLMModel
	}
	if len(model) > 255 {
		return ErrInvalidLLMModelLength
	}

	proxyURL := strings.TrimSpace(c.ProxyURL)
	if proxyURL != "" {
		if len(proxyURL) > 1024 {
			return ErrInvalidLLMProxyURLLength
		}
		pu, err := url.Parse(proxyURL)
		if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https" && pu.Scheme != "socks5" && pu.Scheme != "socks5h") {
			return ErrInvalidLLMProxyURLScheme
		}
	}

	if c.FirstTokenTimeoutMS < 3000 || c.FirstTokenTimeoutMS > 30000 {
		return ErrInvalidLLMFirstTokenTimeout
	}

	if c.OverallTimeoutMS < 10000 || c.OverallTimeoutMS > 180000 {
		return ErrInvalidLLMOverallTimeout
	}

	if c.OverallTimeoutMS <= c.FirstTokenTimeoutMS {
		return ErrLLMOverallTimeoutMustExceedFirstToken
	}

	return nil
}

// LLMConfigFilter 定义 LLM 配置查询过滤条件。
type LLMConfigFilter struct {
	Name     string
	Provider string
	Enabled  *bool
	Page     int
	PageSize int
}

// CreateLLMConfig 创建新的 LLM 配置。
func (d *Database) CreateLLMConfig(ctx context.Context, cfg *LLMConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil {
		return ErrInvalidLLMConfig
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.ProxyURL = strings.TrimSpace(cfg.ProxyURL)

	if err := d.gormDB.WithContext(ctx).Create(cfg).Error; err != nil {
		return fmt.Errorf("create llm config: %w", err)
	}

	return nil
}

// FindLLMConfigById 根据 Id 查询 LLM 配置。
func (d *Database) FindLLMConfigById(ctx context.Context, id uint64) (*LLMConfig, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return nil, ErrInvalidLLMConfigId
	}

	var cfg LLMConfig
	err := d.gormDB.WithContext(ctx).
		Model(&LLMConfig{}).
		Where("id = ?", id).
		Take(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find llm config by id %d: %w", id, ErrLLMConfigNotFound)
		}
		return nil, fmt.Errorf("query llm config by id: %w", err)
	}

	return &cfg, nil
}

// UpdateLLMConfigById 按主键 Id 覆盖更新 LLM 配置。
// 更新结果影响行数为 0 时返回 ErrLLMConfigNotFound。
func (d *Database) UpdateLLMConfigById(ctx context.Context, cfg *LLMConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil || cfg.Id == 0 {
		return ErrInvalidLLMConfigId
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	updates := map[string]any{
		"name":                   strings.TrimSpace(cfg.Name),
		"provider":               strings.TrimSpace(cfg.Provider),
		"endpoint":               strings.TrimSpace(cfg.Endpoint),
		"api_key":                cfg.APIKey,
		"model":                  strings.TrimSpace(cfg.Model),
		"proxy_url":               strings.TrimSpace(cfg.ProxyURL),
		"first_token_timeout_ms": cfg.FirstTokenTimeoutMS,
		"overall_timeout_ms":    cfg.OverallTimeoutMS,
		"enabled":                cfg.Enabled,
		"updated_at":             time.Now(),
	}

	res := d.gormDB.WithContext(ctx).
		Model(&LLMConfig{}).
		Where("id = ?", cfg.Id).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update llm config by id: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update llm config by id %d: %w", cfg.Id, ErrLLMConfigNotFound)
	}

	return nil
}

// ListLLMConfigs 分页查询 LLM 配置列表。
func (d *Database) ListLLMConfigs(ctx context.Context, filter LLMConfigFilter) ([]*LLMConfig, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&LLMConfig{})

	if name := strings.TrimSpace(filter.Name); name != "" {
		query = query.Where("name LIKE ?", "%"+name+"%")
	}
	if provider := strings.TrimSpace(filter.Provider); provider != "" {
		query = query.Where("provider = ?", provider)
	}
	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count llm configs: %w", err)
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
	var configs []*LLMConfig
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&configs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query llm configs list: %w", err)
	}

	return configs, total, nil
}

// DeleteLLMConfig 删除指定 Id 的 LLM 配置记录。
func (d *Database) DeleteLLMConfig(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidLLMConfigId
	}

	res := d.gormDB.WithContext(ctx).Where("id = ?", id).Delete(&LLMConfig{})
	if res.Error != nil {
		return fmt.Errorf("delete llm config: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrLLMConfigNotFound
	}
	return nil
}

// BatchDeleteLLMConfigs 批量删除指定 Id 列表的 LLM 配置记录。
func (d *Database) BatchDeleteLLMConfigs(ctx context.Context, ids []uint64) error {
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

	if err := d.gormDB.WithContext(ctx).Where("id IN ?", validIds).Delete(&LLMConfig{}).Error; err != nil {
		return fmt.Errorf("batch delete llm configs: %w", err)
	}
	return nil
}

