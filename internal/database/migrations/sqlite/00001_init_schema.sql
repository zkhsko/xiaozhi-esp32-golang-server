-- +goose Up
-- +goose StatementBegin
-- device_hmac_credential: 设备出厂与激活 HMAC 凭证表
CREATE TABLE IF NOT EXISTS device_hmac_credential (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 凭证内部自增主键
    serial_number VARCHAR(64) NOT NULL,                            -- 设备序列号（全局业务唯一）
    auth_method VARCHAR(32) NOT NULL DEFAULT 'efuse_hmac',         -- 认证方式：efuse_hmac / activation_code / manual_code_hmac
    device_type VARCHAR(32) NOT NULL DEFAULT 'default',             -- 设备类型（用于关联 agent）
    hmac_key_ciphertext VARCHAR(64) NOT NULL,                      -- HMAC Key（统一字段名 hmac_key_ciphertext，64位十六进制字符，可直接写入 hmac_0）
    credential_status VARCHAR(16) NOT NULL DEFAULT 'enabled',      -- 凭证状态：enabled / activated / blocked / revoked
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 凭证创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 凭证最近更新时间
    CONSTRAINT uk_serial_number UNIQUE (serial_number)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- device_activation: 设备激活关系表
CREATE TABLE IF NOT EXISTS device_activation (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 已激活设备自增主键
    serial_number VARCHAR(64) NOT NULL,                            -- 设备序列号（全局业务唯一）
    device_id VARCHAR(64) NOT NULL,                                -- 后端设备标识 Device-Id
    client_id VARCHAR(64) DEFAULT NULL,                            -- 固件/客户端安装实例标识
    activation_status VARCHAR(16) NOT NULL DEFAULT 'active',       -- 激活状态：active / frozen / revoked
    activated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,     -- 首次激活时间
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 记录创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 记录最近更新时间
    CONSTRAINT uk_serial_number UNIQUE (serial_number)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- idx_device_id: 后端设备标识普通索引
CREATE INDEX IF NOT EXISTS idx_device_id ON device_activation(device_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- device_user_ref: 设备与用户绑定关系表
CREATE TABLE IF NOT EXISTS device_user_ref (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 绑定记录自增主键
    serial_number VARCHAR(64) NOT NULL,                            -- 设备序列号（全局业务唯一）
    user_id INTEGER NOT NULL,                                      -- 当前绑定的用户 Id
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 绑定记录创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 绑定关系最近更新时间
    CONSTRAINT uk_serial_number UNIQUE (serial_number)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- idx_user_id_serial_number: 用户设备绑定联合索引
CREATE INDEX IF NOT EXISTS idx_user_id_serial_number ON device_user_ref(user_id, serial_number);
-- +goose StatementEnd

-- +goose StatementBegin
-- device_access_token: 设备鉴权 Access Token 表
CREATE TABLE IF NOT EXISTS device_access_token (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- Token 凭证内部自增主键
    serial_number VARCHAR(64) NOT NULL,                            -- 设备序列号（全局业务唯一）
    access_token VARCHAR(128) NOT NULL,                                   -- 设备 Access Token 明文
    device_type VARCHAR(32) NOT NULL DEFAULT 'default',             -- 设备类型（冗余自生产表，用于关联 agent）
    has_exposed INTEGER NOT NULL DEFAULT 0,                        -- 是否已在 OTA 接口展示下发过（0: 待展示下发, 1: 已展示下发）
    issued_at DATETIME NOT NULL,                                   -- Token 签发时间
    expires_at DATETIME DEFAULT NULL,                              -- Token 过期时间（为空表示无固定过期时间）
    revoked_at DATETIME DEFAULT NULL,                              -- Token 撤销时间（为空表示未撤销）
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 记录创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 记录最近更新时间
    CONSTRAINT uk_serial_number UNIQUE (serial_number),
    CONSTRAINT uk_access_token UNIQUE (access_token)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- device_type: 设备类型与 Agent 配置关联表
CREATE TABLE IF NOT EXISTS device_type (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 关联记录自增主键
    device_type VARCHAR(32) NOT NULL,                               -- 设备类型（全局业务唯一）
    agent_config_id INTEGER NOT NULL,                               -- 关联的 Agent 配置 Id
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 最近更新时间
    CONSTRAINT uk_device_type UNIQUE (device_type)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- idx_agent_config_id: Agent 配置 Id 普通索引
CREATE INDEX IF NOT EXISTS idx_agent_config_id ON device_type(agent_config_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- asr_config: 语音识别 ASR 配置表
CREATE TABLE IF NOT EXISTS asr_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 配置内部自增主键
    name VARCHAR(128) NOT NULL,                                    -- 配置展示名称（非唯一）
    provider VARCHAR(64) NOT NULL DEFAULT '',                      -- ASR 服务商/平台：bailian / volcengine / openai 等
    endpoint VARCHAR(1024) NOT NULL,                               -- ASR WebSocket Endpoint
    api_key VARCHAR(1024) NOT NULL DEFAULT '',                     -- 明文 API Key（迁移默认记录初始为空）
    model VARCHAR(255) NOT NULL,                                   -- ASR 模型
    hotwords TEXT NOT NULL DEFAULT '',                             -- 热词配置，支持大量文本
    proxy_url VARCHAR(1024) NOT NULL DEFAULT '',                   -- 代理地址（非空即启用）
    connect_timeout_ms INTEGER NOT NULL DEFAULT 5000,              -- 连接超时，毫秒
    enabled INTEGER NOT NULL DEFAULT 1,                            -- 是否允许 Agent 引用（0: 禁用, 1: 启用）
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP         -- 更新时间
);
-- +goose StatementEnd

-- +goose StatementBegin
-- llm_config: 大语言模型 LLM 配置表
CREATE TABLE IF NOT EXISTS llm_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 配置内部自增主键
    name VARCHAR(128) NOT NULL,                                    -- 配置展示名称（非唯一）
    provider VARCHAR(64) NOT NULL DEFAULT '',                      -- LLM 服务商/平台：bailian / openai / deepseek / ollama 等
    endpoint VARCHAR(1024) NOT NULL,                               -- LLM HTTP Endpoint
    api_key VARCHAR(1024) NOT NULL DEFAULT '',                     -- 明文 API Key（迁移默认记录初始为空）
    model VARCHAR(255) NOT NULL,                                   -- LLM 模型
    proxy_url VARCHAR(1024) NOT NULL DEFAULT '',                   -- 代理地址（非空即启用）
    first_token_timeout_ms INTEGER NOT NULL DEFAULT 5000,          -- 首 Token 超时，毫秒
    overall_timeout_ms INTEGER NOT NULL DEFAULT 30000,             -- 总超时，毫秒
    enabled INTEGER NOT NULL DEFAULT 1,                            -- 是否允许 Agent 引用（0: 禁用, 1: 启用）
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP         -- 更新时间
);
-- +goose StatementEnd

-- +goose StatementBegin
-- tts_config: 语音合成 TTS 配置表
CREATE TABLE IF NOT EXISTS tts_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 配置内部自增主键
    name VARCHAR(128) NOT NULL,                                    -- 配置展示名称（非唯一）
    provider VARCHAR(64) NOT NULL DEFAULT '',                      -- TTS 服务商/平台：bailian / volcengine / openai 等
    endpoint VARCHAR(1024) NOT NULL,                               -- TTS WebSocket Endpoint
    api_key VARCHAR(1024) NOT NULL DEFAULT '',                     -- 明文 API Key（迁移默认记录初始为空）
    model VARCHAR(255) NOT NULL,                                   -- TTS 模型
    voices TEXT NOT NULL DEFAULT '[]',                             -- 支持的音色列表（JSON 格式）
    proxy_url VARCHAR(1024) NOT NULL DEFAULT '',                   -- 代理地址（非空即启用）
    connect_timeout_ms INTEGER NOT NULL DEFAULT 5000,              -- 连接超时，毫秒
    first_audio_timeout_ms INTEGER NOT NULL DEFAULT 5000,          -- 首音频超时，毫秒
    sentence_timeout_ms INTEGER NOT NULL DEFAULT 10000,            -- 单句超时，毫秒
    enabled INTEGER NOT NULL DEFAULT 1,                            -- 是否允许 Agent 引用（0: 禁用, 1: 启用）
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP         -- 更新时间
);
-- +goose StatementEnd

-- +goose StatementBegin
-- agent_config: AI Agent 配置表
CREATE TABLE IF NOT EXISTS agent_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- Agent 身份自增主键
    name VARCHAR(128) NOT NULL,                                    -- 展示名称（非唯一）
    asr_config_id INTEGER NOT NULL,                                -- 引用 asr_config.id
    llm_config_id INTEGER NOT NULL,                                -- 引用 llm_config.id
    tts_config_id INTEGER NOT NULL,                                -- 引用 tts_config.id
    system_prompt TEXT NOT NULL,                                   -- Agent 系统提示词
    voice VARCHAR(128) NOT NULL,                                   -- Agent 使用的 TTS 音色
    enabled INTEGER NOT NULL DEFAULT 0,                            -- 是否为当前 Agent（0: 否, 1: 是）
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP         -- 更新时间
);
-- +goose StatementEnd

-- +goose StatementBegin
-- idx_agent_config_asr_config_id: ASR 配置 Id 普通索引
CREATE INDEX IF NOT EXISTS idx_agent_config_asr_config_id ON agent_config(asr_config_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- idx_agent_config_llm_config_id: LLM 配置 Id 普通索引
CREATE INDEX IF NOT EXISTS idx_agent_config_llm_config_id ON agent_config(llm_config_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- idx_agent_config_tts_config_id: TTS 配置 Id 普通索引
CREATE INDEX IF NOT EXISTS idx_agent_config_tts_config_id ON agent_config(tts_config_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- idx_agent_config_enabled: 是否为当前 Agent 普通索引
CREATE INDEX IF NOT EXISTS idx_agent_config_enabled ON agent_config(enabled);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_config;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS tts_config;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS llm_config;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS asr_config;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS device_type;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS device_access_token;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS device_user_ref;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS device_activation;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS device_hmac_credential;
-- +goose StatementEnd
