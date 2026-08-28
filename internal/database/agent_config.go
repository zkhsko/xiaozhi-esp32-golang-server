package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 哨兵错误定义。
var (
	// ErrAgentConfigNotFound 表示 Agent 配置记录未找到。
	ErrAgentConfigNotFound = errors.New("agent config not found")
	// ErrInvalidAgentConfig 表示 Agent 配置结构体为 nil 或非法。
	ErrInvalidAgentConfig = errors.New("invalid agent config")
	// ErrEmptyAgentConfigName 表示 Agent 配置名称为空。
	ErrEmptyAgentConfigName = errors.New("agent config name cannot be empty")
	// ErrInvalidAgentConfigNameLength 表示 Agent 配置名称长度超过 128 字节。
	ErrInvalidAgentConfigNameLength = errors.New("agent config name length exceeds 128 bytes")
	// ErrInvalidASRConfigReference 表示引用的 ASR 配置 ID 为 0 或非法。
	ErrInvalidASRConfigReference = errors.New("asr config id cannot be empty or zero")
	// ErrInvalidLLMConfigReference 表示引用的 LLM 配置 ID 为 0 或非法。
	ErrInvalidLLMConfigReference = errors.New("llm config id cannot be empty or zero")
	// ErrInvalidTTSConfigReference 表示引用的 TTS 配置 ID 为 0 或非法。
	ErrInvalidTTSConfigReference = errors.New("tts config id cannot be empty or zero")
	// ErrEmptySystemPrompt 表示 Agent 系统提示词为空。
	ErrEmptySystemPrompt = errors.New("system prompt cannot be empty")
	// ErrInvalidSystemPromptLength 表示 Agent 系统提示词长度超过 16384 字节。
	ErrInvalidSystemPromptLength = errors.New("system prompt length exceeds 16384 bytes")
	// ErrEmptyVoice 表示 Agent 使用的音色为空。
	ErrEmptyVoice = errors.New("voice cannot be empty")
	// ErrInvalidVoiceLength 表示 Agent 使用的音色长度超过 128 字节。
	ErrInvalidVoiceLength = errors.New("voice length exceeds 128 bytes")
	// ErrReferencedASRNotFound 表示引用的 ASR 配置记录不存在。
	ErrReferencedASRNotFound = errors.New("referenced asr config not found")
	// ErrReferencedASRDisabled 表示引用的 ASR 配置已禁用。
	ErrReferencedASRDisabled = errors.New("referenced asr config is disabled")
	// ErrReferencedLLMNotFound 表示引用的 LLM 配置记录不存在。
	ErrReferencedLLMNotFound = errors.New("referenced llm config not found")
	// ErrReferencedLLMDisabled 表示引用的 LLM 配置已禁用。
	ErrReferencedLLMDisabled = errors.New("referenced llm config is disabled")
	// ErrReferencedTTSNotFound 表示引用的 TTS 配置记录不存在。
	ErrReferencedTTSNotFound = errors.New("referenced tts config not found")
	// ErrReferencedTTSDisabled 表示引用的 TTS 配置已禁用。
	ErrReferencedTTSDisabled = errors.New("referenced tts config is disabled")
	// ErrActiveAgentStateInvalid 表示当前启用的 Agent 状态非法（必须恰好存在一条 enabled=true 的 Agent）。
	ErrActiveAgentStateInvalid = errors.New("invalid active agent state: exactly one enabled agent required")
)

// AgentConfig 映射 agent_config AI Agent 配置表。
//
// 业务用途：
// 通过外键自由组合一条 ASR、一条 LLM 和一条 TTS 配置，独立保存系统提示词和音色。
// 全局通过 enabled = true 标记当前生效的 Agent。
//
// 字段约束与索引规范：
// - id: 主键自增。
// - name: 展示名称，非唯一（允许重复），最大 128 字节。
// - asr_config_id: 引用 asr_config.id，非空，普通索引 idx_agent_config_asr_config_id，外键 RESTRICT。
// - llm_config_id: 引用 llm_config.id，非空，普通索引 idx_agent_config_llm_config_id，外键 RESTRICT。
// - tts_config_id: 引用 tts_config.id，非空，普通索引 idx_agent_config_tts_config_id，外键 RESTRICT。
// - system_prompt: Agent 系统提示词，最大 16384 字节。
// - voice: Agent 使用的 TTS 音色，最大 128 字节。
// - enabled: 是否为当前 Agent（true 表示当前 Agent），普通索引 idx_agent_config_enabled。
// - created_at: 创建时间。
// - updated_at: 更新时间。
type AgentConfig struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement;column:id" json:"id"`
	Name         string    `gorm:"column:name;size:128;not null" json:"name"`
	ASRConfigID  uint64    `gorm:"column:asr_config_id;not null;index:idx_agent_config_asr_config_id" json:"asr_config_id"`
	LLMConfigID  uint64    `gorm:"column:llm_config_id;not null;index:idx_agent_config_llm_config_id" json:"llm_config_id"`
	TTSConfigID  uint64    `gorm:"column:tts_config_id;not null;index:idx_agent_config_tts_config_id" json:"tts_config_id"`
	SystemPrompt string    `gorm:"column:system_prompt;type:text;not null" json:"system_prompt"`
	Voice        string    `gorm:"column:voice;size:128;not null" json:"voice"`
	Enabled      bool      `gorm:"column:enabled;not null;index:idx_agent_config_enabled;default:false" json:"enabled"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 指定 AgentConfig 对应的表名。
func (AgentConfig) TableName() string {
	return "agent_config"
}

// AgentSnapshot 包含运行时 Agent 的基本信息。
type AgentSnapshot struct {
	ID           uint64 `json:"id"`
	Name         string `json:"name"`
	SystemPrompt string `json:"system_prompt"`
	Voice        string `json:"voice"`
	Enabled      bool   `json:"enabled"`
}

// AgentRuntimeSnapshot 包含生效 Agent 及其关联 ASR、LLM、TTS 组件的完整配置快照。
type AgentRuntimeSnapshot struct {
	Agent     AgentSnapshot `json:"agent"`
	ASRConfig ASRConfig     `json:"asr_config"`
	LLMConfig LLMConfig     `json:"llm_config"`
	TTSConfig TTSConfig     `json:"tts_config"`
}

// Validate 校验 AgentConfig 结构体字段合法性。
func (c *AgentConfig) Validate() error {
	if c == nil {
		return ErrInvalidAgentConfig
	}

	name := strings.TrimSpace(c.Name)
	if name == "" {
		return ErrEmptyAgentConfigName
	}
	if len(name) > 128 {
		return ErrInvalidAgentConfigNameLength
	}

	if c.ASRConfigID == 0 {
		return ErrInvalidASRConfigReference
	}
	if c.LLMConfigID == 0 {
		return ErrInvalidLLMConfigReference
	}
	if c.TTSConfigID == 0 {
		return ErrInvalidTTSConfigReference
	}

	prompt := strings.TrimSpace(c.SystemPrompt)
	if prompt == "" {
		return ErrEmptySystemPrompt
	}
	if len(prompt) > 16384 {
		return ErrInvalidSystemPromptLength
	}

	voice := strings.TrimSpace(c.Voice)
	if voice == "" {
		return ErrEmptyVoice
	}
	if len(voice) > 128 {
		return ErrInvalidVoiceLength
	}

	return nil
}

// AgentConfigFilter 定义 Agent 配置查询过滤条件。
type AgentConfigFilter struct {
	Name     string
	Enabled  *bool
	Page     int
	PageSize int
}

// validateComponentReferences 校验引用的三个组件是否存在且处于启用状态。
func validateComponentReferences(ctx context.Context, tx *gorm.DB, asrID, llmID, ttsID uint64) error {
	var asr ASRConfig
	if err := tx.WithContext(ctx).Where("id = ?", asrID).Take(&asr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReferencedASRNotFound
		}
		return fmt.Errorf("query referenced asr config: %w", err)
	}
	if !asr.Enabled {
		return ErrReferencedASRDisabled
	}

	var llm LLMConfig
	if err := tx.WithContext(ctx).Where("id = ?", llmID).Take(&llm).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReferencedLLMNotFound
		}
		return fmt.Errorf("query referenced llm config: %w", err)
	}
	if !llm.Enabled {
		return ErrReferencedLLMDisabled
	}

	var tts TTSConfig
	if err := tx.WithContext(ctx).Where("id = ?", ttsID).Take(&tts).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReferencedTTSNotFound
		}
		return fmt.Errorf("query referenced tts config: %w", err)
	}
	if !tts.Enabled {
		return ErrReferencedTTSDisabled
	}

	return nil
}

// CreateAgentConfig 创建新的 Agent 配置。
func (d *Database) CreateAgentConfig(ctx context.Context, cfg *AgentConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil {
		return ErrInvalidAgentConfig
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.SystemPrompt = strings.TrimSpace(cfg.SystemPrompt)
	cfg.Voice = strings.TrimSpace(cfg.Voice)

	return d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateComponentReferences(ctx, tx, cfg.ASRConfigID, cfg.LLMConfigID, cfg.TTSConfigID); err != nil {
			return err
		}

		if err := tx.Create(cfg).Error; err != nil {
			return fmt.Errorf("create agent config: %w", err)
		}
		return nil
	})
}

// FindAgentConfigByID 根据 ID 查询 Agent 配置。
func (d *Database) FindAgentConfigByID(ctx context.Context, id uint64) (*AgentConfig, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return nil, ErrInvalidAgentConfigID
	}

	var cfg AgentConfig
	err := d.gormDB.WithContext(ctx).
		Model(&AgentConfig{}).
		Where("id = ?", id).
		Take(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find agent config by id %d: %w", id, ErrAgentConfigNotFound)
		}
		return nil, fmt.Errorf("query agent config by id: %w", err)
	}

	return &cfg, nil
}

// UpdateAgentConfigByID 按主键 ID 覆盖更新 Agent 配置（更新组合、提示词和音色）。
// 更新结果影响行数为 0 时返回 ErrAgentConfigNotFound。
func (d *Database) UpdateAgentConfigByID(ctx context.Context, cfg *AgentConfig) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if cfg == nil || cfg.ID == 0 {
		return ErrInvalidAgentConfigID
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	return d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing AgentConfig
		if err := tx.Where("id = ?", cfg.ID).Take(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("update agent config by id %d: %w", cfg.ID, ErrAgentConfigNotFound)
			}
			return fmt.Errorf("query agent config by id: %w", err)
		}

		if err := validateComponentReferences(ctx, tx, cfg.ASRConfigID, cfg.LLMConfigID, cfg.TTSConfigID); err != nil {
			return err
		}

		updates := map[string]any{
			"name":          strings.TrimSpace(cfg.Name),
			"asr_config_id": cfg.ASRConfigID,
			"llm_config_id": cfg.LLMConfigID,
			"tts_config_id": cfg.TTSConfigID,
			"system_prompt": strings.TrimSpace(cfg.SystemPrompt),
			"voice":         strings.TrimSpace(cfg.Voice),
			"updated_at":    time.Now(),
		}

		res := tx.Model(&AgentConfig{}).Where("id = ?", cfg.ID).Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("update agent config by id: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("update agent config by id %d: %w", cfg.ID, ErrAgentConfigNotFound)
		}

		return nil
	})
}

// ActivateAgent 事务切换当前生效的 Agent。
//
// 执行流程：
// 1. 开启写事务并排他锁定目标 Agent；
// 2. 校验目标 Agent 引用的 ASR、LLM、TTS 组件存在且处于 enabled 状态；
// 3. 将全部 Agent 的 enabled 更新为 false；
// 4. 将目标 Agent 的 enabled 更新为 true；
// 5. 校验事务内 enabled=true 的 Agent 数量恰好为 1 条；
// 6. 提交事务。
func (d *Database) ActivateAgent(ctx context.Context, agentID uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if agentID == 0 {
		return ErrInvalidAgentConfigID
	}

	return d.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var target AgentConfig
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", agentID).
			Take(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("activate agent by id %d: %w", agentID, ErrAgentConfigNotFound)
			}
			return fmt.Errorf("lock target agent config: %w", err)
		}

		if err := validateComponentReferences(ctx, tx, target.ASRConfigID, target.LLMConfigID, target.TTSConfigID); err != nil {
			return err
		}

		if err := tx.Model(&AgentConfig{}).Where("1 = 1").Update("enabled", false).Error; err != nil {
			return fmt.Errorf("reset all agent configs enabled: %w", err)
		}

		res := tx.Model(&AgentConfig{}).Where("id = ?", agentID).Update("enabled", true)
		if res.Error != nil {
			return fmt.Errorf("activate target agent: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("activate agent by id %d: %w", agentID, ErrAgentConfigNotFound)
		}

		var count int64
		if err := tx.Model(&AgentConfig{}).Where("enabled = ?", true).Count(&count).Error; err != nil {
			return fmt.Errorf("count enabled agents: %w", err)
		}
		if count != 1 {
			return ErrActiveAgentStateInvalid
		}

		return nil
	})
}

// snapshotRow 用于接收 Agent JOIN 三个组件配置的单行查询结果。
type snapshotRow struct {
	// Agent 字段
	AgentID           uint64 `gorm:"column:agent_id"`
	AgentName         string `gorm:"column:agent_name"`
	AgentSystemPrompt string `gorm:"column:agent_system_prompt"`
	AgentVoice        string `gorm:"column:agent_voice"`
	AgentEnabled      bool   `gorm:"column:agent_enabled"`

	// ASR 字段
	ASRID               uint64    `gorm:"column:asr_id"`
	ASRName             string    `gorm:"column:asr_name"`
	ASREndpoint         string    `gorm:"column:asr_endpoint"`
	ASRAPIKey           string    `gorm:"column:asr_api_key"`
	ASRModel            string    `gorm:"column:asr_model"`
	ASRHotwords         string    `gorm:"column:asr_hotwords"`
	ASRConnectTimeoutMS int64     `gorm:"column:asr_connect_timeout_ms"`
	ASREnabled          bool      `gorm:"column:asr_enabled"`
	ASRCreatedAt        time.Time `gorm:"column:asr_created_at"`
	ASRUpdatedAt        time.Time `gorm:"column:asr_updated_at"`

	// LLM 字段
	LLMID                  uint64    `gorm:"column:llm_id"`
	LLMName                string    `gorm:"column:llm_name"`
	LLMEndpoint            string    `gorm:"column:llm_endpoint"`
	LLMAPIKey              string    `gorm:"column:llm_api_key"`
	LLMModel               string    `gorm:"column:llm_model"`
	LLMFirstTokenTimeoutMS int64     `gorm:"column:llm_first_token_timeout_ms"`
	LLMOverallTimeoutMS    int64     `gorm:"column:llm_overall_timeout_ms"`
	LLMEnabled             bool      `gorm:"column:llm_enabled"`
	LLMCreatedAt           time.Time `gorm:"column:llm_created_at"`
	LLMUpdatedAt           time.Time `gorm:"column:llm_updated_at"`

	// TTS 字段
	TTSID                  uint64    `gorm:"column:tts_id"`
	TTSName                string    `gorm:"column:tts_name"`
	TTSEndpoint            string    `gorm:"column:tts_endpoint"`
	TTSAPIKey              string    `gorm:"column:tts_api_key"`
	TTSModel               string    `gorm:"column:tts_model"`
	TTSVoices              string    `gorm:"column:tts_voices"`
	TTSConnectTimeoutMS    int64     `gorm:"column:tts_connect_timeout_ms"`
	TTSFirstAudioTimeoutMS int64     `gorm:"column:tts_first_audio_timeout_ms"`
	TTSSentenceTimeoutMS   int64     `gorm:"column:tts_sentence_timeout_ms"`
	TTSEnabled             bool      `gorm:"column:tts_enabled"`
	TTSCreatedAt           time.Time `gorm:"column:tts_created_at"`
	TTSUpdatedAt           time.Time `gorm:"column:tts_updated_at"`
}

const snapshotSelectSQL = `
	a.id AS agent_id, a.name AS agent_name, a.system_prompt AS agent_system_prompt, a.voice AS agent_voice, a.enabled AS agent_enabled,
	asr.id AS asr_id, asr.name AS asr_name, asr.endpoint AS asr_endpoint, asr.api_key AS asr_api_key, asr.model AS asr_model, asr.hotwords AS asr_hotwords, asr.connect_timeout_ms AS asr_connect_timeout_ms, asr.enabled AS asr_enabled, asr.created_at AS asr_created_at, asr.updated_at AS asr_updated_at,
	llm.id AS llm_id, llm.name AS llm_name, llm.endpoint AS llm_endpoint, llm.api_key AS llm_api_key, llm.model AS llm_model, llm.first_token_timeout_ms AS llm_first_token_timeout_ms, llm.overall_timeout_ms AS llm_overall_timeout_ms, llm.enabled AS llm_enabled, llm.created_at AS llm_created_at, llm.updated_at AS llm_updated_at,
	tts.id AS tts_id, tts.name AS tts_name, tts.endpoint AS tts_endpoint, tts.api_key AS tts_api_key, tts.model AS tts_model, tts.voices AS tts_voices, tts.connect_timeout_ms AS tts_connect_timeout_ms, tts.first_audio_timeout_ms AS tts_first_audio_timeout_ms, tts.sentence_timeout_ms AS tts_sentence_timeout_ms, tts.enabled AS tts_enabled, tts.created_at AS tts_created_at, tts.updated_at AS tts_updated_at
`

// toAgentRuntimeSnapshot 将单行 JOIN 结果转换为运行时快照对象。
func (row *snapshotRow) toAgentRuntimeSnapshot() (*AgentRuntimeSnapshot, error) {
	if !row.ASREnabled {
		return nil, fmt.Errorf("active agent references disabled asr config (id=%d): %w", row.ASRID, ErrReferencedASRDisabled)
	}
	if !row.LLMEnabled {
		return nil, fmt.Errorf("active agent references disabled llm config (id=%d): %w", row.LLMID, ErrReferencedLLMDisabled)
	}
	if !row.TTSEnabled {
		return nil, fmt.Errorf("active agent references disabled tts config (id=%d): %w", row.TTSID, ErrReferencedTTSDisabled)
	}

	return &AgentRuntimeSnapshot{
		Agent: AgentSnapshot{
			ID:           row.AgentID,
			Name:         row.AgentName,
			SystemPrompt: row.AgentSystemPrompt,
			Voice:        row.AgentVoice,
			Enabled:      row.AgentEnabled,
		},
		ASRConfig: ASRConfig{
			ID:               row.ASRID,
			Name:             row.ASRName,
			Endpoint:         row.ASREndpoint,
			APIKey:           row.ASRAPIKey,
			Model:            row.ASRModel,
			Hotwords:         row.ASRHotwords,
			ConnectTimeoutMS: row.ASRConnectTimeoutMS,
			Enabled:          row.ASREnabled,
			CreatedAt:        row.ASRCreatedAt,
			UpdatedAt:        row.ASRUpdatedAt,
		},
		LLMConfig: LLMConfig{
			ID:                  row.LLMID,
			Name:                row.LLMName,
			Endpoint:            row.LLMEndpoint,
			APIKey:              row.LLMAPIKey,
			Model:               row.LLMModel,
			FirstTokenTimeoutMS: row.LLMFirstTokenTimeoutMS,
			OverallTimeoutMS:    row.LLMOverallTimeoutMS,
			Enabled:             row.LLMEnabled,
			CreatedAt:           row.LLMCreatedAt,
			UpdatedAt:           row.LLMUpdatedAt,
		},
		TTSConfig: TTSConfig{
			ID:                  row.TTSID,
			Name:                row.TTSName,
			Endpoint:            row.TTSEndpoint,
			APIKey:              row.TTSAPIKey,
			Model:               row.TTSModel,
			Voices:              row.TTSVoices,
			ConnectTimeoutMS:    row.TTSConnectTimeoutMS,
			FirstAudioTimeoutMS: row.TTSFirstAudioTimeoutMS,
			SentenceTimeoutMS:   row.TTSSentenceTimeoutMS,
			Enabled:             row.TTSEnabled,
			CreatedAt:           row.TTSCreatedAt,
			UpdatedAt:           row.TTSUpdatedAt,
		},
	}, nil
}

// FindActiveAgentRuntimeSnapshot JOIN 查询唯一启用的 Agent 及其引用的 ASR、LLM、TTS 完整配置快照。
func (d *Database) FindActiveAgentRuntimeSnapshot(ctx context.Context) (*AgentRuntimeSnapshot, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}

	var enabledCount int64
	if err := d.gormDB.WithContext(ctx).Model(&AgentConfig{}).Where("enabled = ?", true).Count(&enabledCount).Error; err != nil {
		return nil, fmt.Errorf("count active agents: %w", err)
	}
	if enabledCount != 1 {
		return nil, fmt.Errorf("%w: expected 1 active agent, got %d", ErrActiveAgentStateInvalid, enabledCount)
	}

	var row snapshotRow
	err := d.gormDB.WithContext(ctx).
		Table("agent_config a").
		Select(snapshotSelectSQL).
		Joins("INNER JOIN asr_config asr ON a.asr_config_id = asr.id").
		Joins("INNER JOIN llm_config llm ON a.llm_config_id = llm.id").
		Joins("INNER JOIN tts_config tts ON a.tts_config_id = tts.id").
		Where("a.enabled = ?", true).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("active agent snapshot not found or referenced component missing: %w", ErrAgentConfigNotFound)
		}
		return nil, fmt.Errorf("query active agent snapshot: %w", err)
	}

	return row.toAgentRuntimeSnapshot()
}

// FindAgentRuntimeSnapshotByID JOIN 查询指定 ID 的 Agent 及其引用的 ASR、LLM、TTS 完整配置快照。
func (d *Database) FindAgentRuntimeSnapshotByID(ctx context.Context, id uint64) (*AgentRuntimeSnapshot, error) {
	if d == nil || d.gormDB == nil {
		return nil, ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return nil, ErrInvalidAgentConfigID
	}

	var row snapshotRow
	err := d.gormDB.WithContext(ctx).
		Table("agent_config a").
		Select(snapshotSelectSQL).
		Joins("INNER JOIN asr_config asr ON a.asr_config_id = asr.id").
		Joins("INNER JOIN llm_config llm ON a.llm_config_id = llm.id").
		Joins("INNER JOIN tts_config tts ON a.tts_config_id = tts.id").
		Where("a.id = ?", id).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("agent runtime snapshot by id %d: %w", id, ErrAgentConfigNotFound)
		}
		return nil, fmt.Errorf("query agent runtime snapshot by id: %w", err)
	}

	return row.toAgentRuntimeSnapshot()
}

// ListAgentConfigs 分页查询 Agent 配置列表。
func (d *Database) ListAgentConfigs(ctx context.Context, filter AgentConfigFilter) ([]*AgentConfig, int64, error) {
	if d == nil || d.gormDB == nil {
		return nil, 0, ErrDatabaseInstanceRequired
	}

	query := d.gormDB.WithContext(ctx).Model(&AgentConfig{})

	if name := strings.TrimSpace(filter.Name); name != "" {
		query = query.Where("name LIKE ?", "%"+name+"%")
	}
	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count agent configs: %w", err)
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
	var configs []*AgentConfig
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&configs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("query agent configs list: %w", err)
	}

	return configs, total, nil
}

// DeleteAgentConfig 删除指定 ID 的 Agent 配置记录。
func (d *Database) DeleteAgentConfig(ctx context.Context, id uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if id == 0 {
		return ErrInvalidAgentConfigID
	}

	res := d.gormDB.WithContext(ctx).Where("id = ?", id).Delete(&AgentConfig{})
	if res.Error != nil {
		return fmt.Errorf("delete agent config: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrAgentConfigNotFound
	}
	return nil
}

// BatchDeleteAgentConfigs 批量删除指定 ID 列表的 Agent 配置记录。
func (d *Database) BatchDeleteAgentConfigs(ctx context.Context, ids []uint64) error {
	if d == nil || d.gormDB == nil {
		return ErrDatabaseInstanceRequired
	}
	if len(ids) == 0 {
		return nil
	}

	validIDs := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			validIDs = append(validIDs, id)
		}
	}
	if len(validIDs) == 0 {
		return nil
	}

	if err := d.gormDB.WithContext(ctx).Where("id IN ?", validIDs).Delete(&AgentConfig{}).Error; err != nil {
		return fmt.Errorf("batch delete agent configs: %w", err)
	}
	return nil
}
