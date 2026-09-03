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
	// ErrTTSConfigNotFound 表示 TTS 配置记录未找到。
	ErrTTSConfigNotFound = errors.New("tts config not found")
	// ErrInvalidTTSConfig 表示 TTS 配置结构体为 nil 或非法。
	ErrInvalidTTSConfig = errors.New("invalid tts config")
	// ErrInvalidTTSConfigId 表示 TTS 配置 Id 为 0 或非法。
	ErrInvalidTTSConfigId = errors.New("tts config id cannot be empty or zero")
	// ErrEmptyTTSConfigName 表示 TTS 配置名称为空。
	ErrEmptyTTSConfigName = errors.New("tts config name cannot be empty")
	// ErrInvalidTTSConfigNameLength 表示 TTS 配置名称长度超过 128 字节。
	ErrInvalidTTSConfigNameLength = errors.New("tts config name length exceeds 128 bytes")
	// ErrEmptyTTSProvider 表示 TTS 服务商/平台为空。
	ErrEmptyTTSProvider = errors.New("tts provider cannot be empty")
	// ErrInvalidTTSProviderLength 表示 TTS 服务商/平台长度超过 64 字节。
	ErrInvalidTTSProviderLength = errors.New("tts provider length exceeds 64 bytes")
	// ErrEmptyTTSEndpoint 表示 TTS Endpoint 为空。
	ErrEmptyTTSEndpoint = errors.New("tts endpoint cannot be empty")
	// ErrInvalidTTSEndpointScheme 表示 TTS Endpoint 协议非法（只允许 ws 或 wss）。
	ErrInvalidTTSEndpointScheme = errors.New("tts endpoint scheme must be ws or wss")
	// ErrInvalidTTSEndpointLength 表示 TTS Endpoint 长度超过 1024 字节。
	ErrInvalidTTSEndpointLength = errors.New("tts endpoint length exceeds 1024 bytes")
	// ErrInvalidTTSAPIKeyLength 表示 TTS API Key 长度超过 1024 字节。
	ErrInvalidTTSAPIKeyLength = errors.New("tts api_key length exceeds 1024 bytes")
	// ErrEmptyTTSModel 表示 TTS 模型为空。
	ErrEmptyTTSModel = errors.New("tts model cannot be empty")
	// ErrInvalidTTSModelLength 表示 TTS 模型长度超过 255 字节。
	ErrInvalidTTSModelLength = errors.New("tts model length exceeds 255 bytes")
	// ErrInvalidTTSVoicesJSON 表示 TTS 音色列表不是合法的 JSON 格式。
	ErrInvalidTTSVoicesJSON = errors.New("tts voices must be valid json")
	// ErrInvalidTTSVoicesLength 表示 TTS 音色列表长度超过 1048576 字节（1MB）。
	ErrInvalidTTSVoicesLength = errors.New("tts voices length exceeds 1048576 bytes")
	// ErrInvalidTTSProxyURLLength 表示 TTS Proxy URL 长度超过 1024 字节。
	ErrInvalidTTSProxyURLLength = errors.New("tts proxy_url length exceeds 1024 bytes")
	// ErrInvalidTTSProxyURLScheme 表示 TTS Proxy URL 协议非法（只允许 http, https, socks5, socks5h）。
	ErrInvalidTTSProxyURLScheme = errors.New("tts proxy_url scheme must be http, https, socks5, or socks5h")
	// ErrInvalidTTSConnectTimeout 表示 TTS 连接超时不在合法范围（3000 ~ 30000 毫秒）。
	ErrInvalidTTSConnectTimeout = errors.New("tts connect_timeout_ms must be between 3000 and 30000 ms")
)

// TTSConfig 映射 tts_config 语音合成 TTS 配置表。
//
// 业务用途：
// 保存百炼或语音合成服务所需的连接地址、鉴权 Key、模型、支持的音色列表（JSON 格式）以及超时配置。
// 一个 TTS 配置可以被多个 Agent 关联复用，各 Agent 可从 voices 中选择具体音色。
//
// 字段约束与索引规范：
// - id: 主键自增。
// - name: 展示名称，非唯一（允许重复），最大 128 字节。
// - provider: 服务商/平台标识（如 dashscope / volcengine / openai），默认空字符串，最大 64 字节。
// - endpoint: TTS WebSocket Endpoint，必须以 ws:// 或 wss:// 开头，最大 1024 字节。
// - api_key: 明文 API Key（脱敏时不输出，json:"-"），最大 1024 字节。
// - model: TTS 模型名称，最大 255 字节。
// - voices: 支持的音色列表（限制 JSON 格式，最大 1MB，默认为 '[]'），文本类型。
// - proxy_url: 代理服务器地址（支持 http/https/socks5/socks5h，非空即启用），最大 1024 字节。
// - connect_timeout_ms: 连接超时时间（毫秒），合法范围 3000 ~ 30000。
// - enabled: 是否允许 Agent 引用（布尔值，默认 true）。
// - created_at: 创建时间。
// - updated_at: 更新时间。
type TTSConfig struct {
	Id               uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	Name             string    `gorm:"column:name;size:128;not null" json:"name"`
	Provider         string    `gorm:"column:provider;size:64;not null;default:''" json:"provider"`
	Endpoint         string    `gorm:"column:endpoint;size:1024;not null" json:"endpoint"`
	APIKey           string    `gorm:"column:api_key;size:1024;not null;default:''" json:"-"`
	Model            string    `gorm:"column:model;size:255;not null" json:"model"`
	Voices           string    `gorm:"column:voices;type:text;not null" json:"voices"`
	ProxyURL         string    `gorm:"column:proxy_url;size:1024;not null;default:''" json:"proxy_url"`
	ConnectTimeoutMS int64     `gorm:"column:connect_timeout_ms;not null;default:5000" json:"connect_timeout_ms"`
	Enabled          bool      `gorm:"column:enabled;not null" json:"enabled"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 TTSConfig 对应的表名。
func (TTSConfig) TableName() string {
	return "tts_config"
}

// Validate 校验 TTSConfig 结构体字段合法性。
func (c *TTSConfig) Validate() error {
	if c == nil {
		return ErrInvalidTTSConfig
	}

	name := strings.TrimSpace(c.Name)
	if name == "" {
		return ErrEmptyTTSConfigName
	}
	if len(name) > 128 {
		return ErrInvalidTTSConfigNameLength
	}

	provider := strings.TrimSpace(c.Provider)
	if len(provider) > 64 {
		return ErrInvalidTTSProviderLength
	}

	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		return ErrEmptyTTSEndpoint
	}
	if len(endpoint) > 1024 {
		return ErrInvalidTTSEndpointLength
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return ErrInvalidTTSEndpointScheme
	}

	if len(c.APIKey) > 1024 {
		return ErrInvalidTTSAPIKeyLength
	}

	model := strings.TrimSpace(c.Model)
	if model == "" {
		return ErrEmptyTTSModel
	}
	if len(model) > 255 {
		return ErrInvalidTTSModelLength
	}

	voices := strings.TrimSpace(c.Voices)
	if voices == "" {
		c.Voices = "[]"
	} else {
		if len(voices) > 1024*1024 {
			return ErrInvalidTTSVoicesLength
		}
		if !json.Valid([]byte(voices)) {
			return ErrInvalidTTSVoicesJSON
		}
		c.Voices = voices
	}

	proxyURL := strings.TrimSpace(c.ProxyURL)
	if proxyURL != "" {
		if len(proxyURL) > 1024 {
			return ErrInvalidTTSProxyURLLength
		}
		pu, err := url.Parse(proxyURL)
		if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https" && pu.Scheme != "socks5" && pu.Scheme != "socks5h") {
			return ErrInvalidTTSProxyURLScheme
		}
	}

	if c.ConnectTimeoutMS < 3000 || c.ConnectTimeoutMS > 30000 {
		return ErrInvalidTTSConnectTimeout
	}

	return nil
}

// TTSConfigFilter 定义 TTS 配置查询过滤条件。
type TTSConfigFilter struct {
	Name     string
	Provider string
	Enabled  *bool
	Page     int
	PageSize int
}

// CreateTTSConfig 创建新的 TTS 配置。
func (d *Database) CreateTTSConfig(ctx context.Context, cfg *TTSConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil {
		return ErrInvalidTTSConfig
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
		return fmt.Errorf("create tts config: %w", err)
	}

	return nil
}

// FindTTSConfigById 根据 Id 查询 TTS 配置。
func (d *Database) FindTTSConfigById(ctx context.Context, id uint64) (*TTSConfig, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return nil, ErrInvalidTTSConfigId
	}

	var cfg TTSConfig
	err := d.gormDB.WithContext(ctx).
		Model(&TTSConfig{}).
		Where("id = ?", id).
		Take(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find tts config by id %d: %w", id, ErrTTSConfigNotFound)
		}
		return nil, fmt.Errorf("query tts config by id: %w", err)
	}

	return &cfg, nil
}

// UpdateTTSConfigById 按主键 Id 覆盖更新 TTS 配置。
// 更新结果影响行数为 0 时返回 ErrTTSConfigNotFound。
func (d *Database) UpdateTTSConfigById(ctx context.Context, cfg *TTSConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil || cfg.Id == 0 {
		return ErrInvalidTTSConfigId
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
		"voices":             cfg.Voices,
		"proxy_url":          strings.TrimSpace(cfg.ProxyURL),
		"connect_timeout_ms": cfg.ConnectTimeoutMS,
		"enabled":            cfg.Enabled,
		"updated_at":         time.Now(),
	}

	res := d.gormDB.WithContext(ctx).
		Model(&TTSConfig{}).
		Where("id = ?", cfg.Id).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update tts config by id: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update tts config by id %d: %w", cfg.Id, ErrTTSConfigNotFound)
	}

	return nil
}

// ListTTSConfigs 分页查询 TTS 配置列表。
func (d *Database) ListTTSConfigs(ctx context.Context, filter TTSConfigFilter) ([]*TTSConfig, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&TTSConfig{})

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
		return nil, 0, fmt.Errorf("count tts configs: %w", err)
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
	var configs []*TTSConfig
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&configs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query tts configs list: %w", err)
	}

	return configs, total, nil
}

// DeleteTTSConfig 删除指定 Id 的 TTS 配置记录。
func (d *Database) DeleteTTSConfig(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidTTSConfigId
	}

	res := d.gormDB.WithContext(ctx).Where("id = ?", id).Delete(&TTSConfig{})
	if res.Error != nil {
		return fmt.Errorf("delete tts config: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrTTSConfigNotFound
	}
	return nil
}

// BatchDeleteTTSConfigs 批量删除指定 Id 列表的 TTS 配置记录。
func (d *Database) BatchDeleteTTSConfigs(ctx context.Context, ids []uint64) error {
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

	if err := d.gormDB.WithContext(ctx).Where("id IN ?", validIds).Delete(&TTSConfig{}).Error; err != nil {
		return fmt.Errorf("batch delete tts configs: %w", err)
	}
	return nil
}
