package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 哨兵错误定义。
var (
	// ErrASRConfigNotFound 表示 ASR 配置记录未找到。
	ErrASRConfigNotFound = errors.New("asr config not found")
	// ErrInvalidASRConfig 表示 ASR 配置结构体为 nil 或非法。
	ErrInvalidASRConfig = errors.New("invalid asr config")
	// ErrInvalidASRConfigId 表示 ASR 配置 Id 为 0 或非法。
	ErrInvalidASRConfigId = errors.New("asr config id cannot be empty or zero")
	// ErrEmptyASRConfigName 表示 ASR 配置名称为空。
	ErrEmptyASRConfigName = errors.New("asr config name cannot be empty")
	// ErrInvalidASRConfigNameLength 表示 ASR 配置名称长度超过 128 字节。
	ErrInvalidASRConfigNameLength = errors.New("asr config name length exceeds 128 bytes")
	// ErrEmptyASRProvider 表示 ASR 服务商/平台为空。
	ErrEmptyASRProvider = errors.New("asr provider cannot be empty")
	// ErrInvalidASRProviderLength 表示 ASR 服务商/平台长度超过 64 字节。
	ErrInvalidASRProviderLength = errors.New("asr provider length exceeds 64 bytes")
	// ErrEmptyASREndpoint 表示 ASR Endpoint 为空。
	ErrEmptyASREndpoint = errors.New("asr endpoint cannot be empty")
	// ErrInvalidASREndpointScheme 表示 ASR Endpoint 协议非法（只允许 ws 或 wss）。
	ErrInvalidASREndpointScheme = errors.New("asr endpoint scheme must be ws or wss")
	// ErrInvalidASREndpointLength 表示 ASR Endpoint 长度超过 1024 字节。
	ErrInvalidASREndpointLength = errors.New("asr endpoint length exceeds 1024 bytes")
	// ErrInvalidASRAPIKeyLength 表示 ASR API Key 长度超过 1024 字节。
	ErrInvalidASRAPIKeyLength = errors.New("asr api_key length exceeds 1024 bytes")
	// ErrEmptyASRModel 表示 ASR 模型为空。
	ErrEmptyASRModel = errors.New("asr model cannot be empty")
	// ErrInvalidASRModelLength 表示 ASR 模型长度超过 255 字节。
	ErrInvalidASRModelLength = errors.New("asr model length exceeds 255 bytes")
	// ErrInvalidASRHotwordsLength 表示 ASR 热词长度超过 1048576 字节（1MB）。
	ErrInvalidASRHotwordsLength = errors.New("asr hotwords length exceeds 1048576 bytes")
	// ErrInvalidASRHotwordsJSON 表示 ASR 热词格式非法（非空时必须为合法 JSON 格式）。
	ErrInvalidASRHotwordsJSON = errors.New("asr hotwords must be valid json format")
	// ErrInvalidASRProxyURLLength 表示 ASR Proxy URL 长度超过 1024 字节。
	ErrInvalidASRProxyURLLength = errors.New("asr proxy_url length exceeds 1024 bytes")
	// ErrInvalidASRProxyURLScheme 表示 ASR Proxy URL 协议非法（只允许 http, https, socks5, socks5h）。
	ErrInvalidASRProxyURLScheme = errors.New("asr proxy_url scheme must be http, https, socks5, or socks5h")
	// ErrInvalidASRConnectTimeout 表示 ASR 连接超时不在合法范围（3000 ~ 30000 毫秒）。
	ErrInvalidASRConnectTimeout = errors.New("asr connect_timeout_ms must be between 3000 and 30000 ms")
)

// ASRConfig 映射 asr_config 语音识别 ASR 配置表。
//
// 业务用途：
// 保存语音识别 ASR 服务所需的连接地址、鉴权 Key、模型以及超时配置。
// 一个 ASR 配置可以被多个 Agent 关联复用。
//
// 字段约束与索引规范：
// - id: 主键自增。
// - name: 展示名称，非唯一（允许重复），最大 128 字节。
// - provider: 服务商/平台标识（如 dashscope / volcengine / openai），默认空字符串，最大 64 字节。
// - endpoint: ASR WebSocket Endpoint，必须以 ws:// 或 wss:// 开头，最大 1024 字节。
// - api_key: 明文 API Key（脱敏时不输出，json:"-"），最大 1024 字节。
// - model: ASR 模型名称，最大 255 字节。
// - hotwords: 热词配置（JSON 格式，如 ["小智", "智能音箱"] 或 [{"word": "小智", "weight": 10}]），文本类型。
// - proxy_url: 代理服务器地址（支持 http/https/socks5/socks5h，非空即启用），最大 1024 字节。
// - connect_timeout_ms: 连接超时时间（毫秒），合法范围 3000 ~ 30000。
// - enabled: 是否允许 Agent 引用（布尔值，默认 true）。
// - created_at: 创建时间。
// - updated_at: 更新时间。
type ASRConfig struct {
	Id               uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	Name             string    `gorm:"column:name;size:128;not null" json:"name"`
	Provider         string    `gorm:"column:provider;size:64;not null;default:''" json:"provider"`
	Endpoint         string    `gorm:"column:endpoint;size:1024;not null" json:"endpoint"`
	APIKey           string    `gorm:"column:api_key;size:1024;not null;default:''" json:"-"`
	Model            string    `gorm:"column:model;size:255;not null" json:"model"`
	Hotwords         string    `gorm:"column:hotwords;type:text;not null" json:"hotwords"`
	ProxyURL         string    `gorm:"column:proxy_url;size:1024;not null;default:''" json:"proxy_url"`
	ConnectTimeoutMS int64     `gorm:"column:connect_timeout_ms;not null;default:5000" json:"connect_timeout_ms"`
	Enabled          bool      `gorm:"column:enabled;not null" json:"enabled"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 ASRConfig 对应的表名。
func (ASRConfig) TableName() string {
	return "asr_config"
}

// Validate 校验 ASRConfig 结构体字段合法性。
func (c *ASRConfig) Validate() error {
	if c == nil {
		return ErrInvalidASRConfig
	}

	name := strings.TrimSpace(c.Name)
	if name == "" {
		return ErrEmptyASRConfigName
	}
	if len(name) > 128 {
		return ErrInvalidASRConfigNameLength
	}

	provider := strings.TrimSpace(c.Provider)
	if len(provider) > 64 {
		return ErrInvalidASRProviderLength
	}

	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		return ErrEmptyASREndpoint
	}
	if len(endpoint) > 1024 {
		return ErrInvalidASREndpointLength
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return ErrInvalidASREndpointScheme
	}

	if len(c.APIKey) > 1024 {
		return ErrInvalidASRAPIKeyLength
	}

	model := strings.TrimSpace(c.Model)
	if model == "" {
		return ErrEmptyASRModel
	}
	if len(model) > 255 {
		return ErrInvalidASRModelLength
	}

	if len(c.Hotwords) > 1024*1024 {
		return ErrInvalidASRHotwordsLength
	}

	trimmedHotwords := strings.TrimSpace(c.Hotwords)
	if trimmedHotwords != "" {
		if !json.Valid([]byte(trimmedHotwords)) {
			return ErrInvalidASRHotwordsJSON
		}
	}

	proxyURL := strings.TrimSpace(c.ProxyURL)
	if proxyURL != "" {
		if len(proxyURL) > 1024 {
			return ErrInvalidASRProxyURLLength
		}
		pu, err := url.Parse(proxyURL)
		if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https" && pu.Scheme != "socks5" && pu.Scheme != "socks5h") {
			return ErrInvalidASRProxyURLScheme
		}
	}

	if c.ConnectTimeoutMS < 3000 || c.ConnectTimeoutMS > 30000 {
		return ErrInvalidASRConnectTimeout
	}

	return nil
}

// ASRConfigFilter 定义 ASR 配置查询过滤条件。
type ASRConfigFilter struct {
	Name     string
	Provider string
	Enabled  *bool
	Page     int
	PageSize int
}

// CreateASRConfig 创建新的 ASR 配置。
func (d *Database) CreateASRConfig(ctx context.Context, cfg *ASRConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil {
		return ErrInvalidASRConfig
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
		return fmt.Errorf("create asr config: %w", err)
	}

	return nil
}

// FindASRConfigById 根据 Id 查询 ASR 配置。
func (d *Database) FindASRConfigById(ctx context.Context, id uint64) (*ASRConfig, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return nil, ErrInvalidASRConfigId
	}

	var cfg ASRConfig
	err := d.gormDB.WithContext(ctx).
		Model(&ASRConfig{}).
		Where("id = ?", id).
		Take(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find asr config by id %d: %w", id, ErrASRConfigNotFound)
		}
		return nil, fmt.Errorf("query asr config by id: %w", err)
	}

	return &cfg, nil
}

// UpdateASRConfigById 按主键 Id 覆盖更新 ASR 配置。
// 更新结果影响行数为 0 时返回 ErrASRConfigNotFound。
func (d *Database) UpdateASRConfigById(ctx context.Context, cfg *ASRConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil || cfg.Id == 0 {
		return ErrInvalidASRConfigId
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	updates := map[string]any{
		"name":               strings.TrimSpace(cfg.Name),
		"provider":           strings.TrimSpace(cfg.Provider),
		"endpoint":           strings.TrimSpace(cfg.Endpoint),
		"api_key":            cfg.APIKey,
		"model":              strings.TrimSpace(cfg.Model),
		"hotwords":           cfg.Hotwords,
		"proxy_url":          strings.TrimSpace(cfg.ProxyURL),
		"connect_timeout_ms": cfg.ConnectTimeoutMS,
		"enabled":            cfg.Enabled,
		"updated_at":         time.Now(),
	}

	res := d.gormDB.WithContext(ctx).
		Model(&ASRConfig{}).
		Where("id = ?", cfg.Id).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update asr config by id: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update asr config by id %d: %w", cfg.Id, ErrASRConfigNotFound)
	}

	return nil
}

// ListASRConfigs 分页查询 ASR 配置列表。
func (d *Database) ListASRConfigs(ctx context.Context, filter ASRConfigFilter) ([]*ASRConfig, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&ASRConfig{})

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
		return nil, 0, fmt.Errorf("count asr configs: %w", err)
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
	var configs []*ASRConfig
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&configs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query asr configs list: %w", err)
	}

	return configs, total, nil
}

// DeleteASRConfig 删除指定 Id 的 ASR 配置记录。
func (d *Database) DeleteASRConfig(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidASRConfigId
	}

	res := d.gormDB.WithContext(ctx).Where("id = ?", id).Delete(&ASRConfig{})
	if res.Error != nil {
		return fmt.Errorf("delete asr config: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrASRConfigNotFound
	}
	return nil
}

// BatchDeleteASRConfigs 批量删除指定 Id 列表的 ASR 配置记录。
func (d *Database) BatchDeleteASRConfigs(ctx context.Context, ids []uint64) error {
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

	if err := d.gormDB.WithContext(ctx).Where("id IN ?", validIds).Delete(&ASRConfig{}).Error; err != nil {
		return fmt.Errorf("batch delete asr configs: %w", err)
	}
	return nil
}

