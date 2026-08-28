-- +goose Up
-- +goose StatementBegin
-- device_hmac_credential: 设备出厂与激活 HMAC 凭证表
CREATE TABLE IF NOT EXISTS `device_hmac_credential` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '凭证自增主键',
    `serial_number` VARCHAR(64) NOT NULL COMMENT '设备序列号，全局业务唯一',
    `auth_method` VARCHAR(32) NOT NULL DEFAULT 'efuse_hmac' COMMENT '认证方式：efuse_hmac / activation_code / manual_code_hmac',
    `device_type` VARCHAR(32) NOT NULL DEFAULT 'default' COMMENT '设备类型，用于关联 agent',
    `hmac_key_ciphertext` VARCHAR(64) NOT NULL COMMENT 'HMAC Key（统一字段名 hmac_key_ciphertext，64位十六进制字符，可直接写入 hmac_0）',
    `credential_status` VARCHAR(16) NOT NULL DEFAULT 'enabled' COMMENT '凭证状态：enabled / activated / blocked / revoked',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_serial_number` (`serial_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备出厂与激活 HMAC 凭证表';
-- +goose StatementEnd

-- +goose StatementBegin
-- device_activation: 设备激活关系表
CREATE TABLE IF NOT EXISTS `device_activation` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '已激活设备自增主键',
    `serial_number` VARCHAR(64) NOT NULL COMMENT '设备序列号，全局业务唯一',
    `device_id` VARCHAR(64) NOT NULL COMMENT '后端设备标识 Device-Id',
    `client_id` VARCHAR(64) DEFAULT NULL COMMENT '固件/客户端安装实例标识',
    `activation_status` VARCHAR(16) NOT NULL DEFAULT 'active' COMMENT '激活状态：active / frozen / revoked',
    `activated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '首次激活时间',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录最近更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_serial_number` (`serial_number`),
    KEY `idx_device_id` (`device_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备激活关系表';
-- +goose StatementEnd

-- +goose StatementBegin
-- device_user_ref: 设备与用户绑定关系表
CREATE TABLE IF NOT EXISTS `device_user_ref` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '绑定记录自增主键',
    `serial_number` VARCHAR(64) NOT NULL COMMENT '设备序列号，全局业务唯一（一台设备最多绑定一个当前用户）',
    `user_id` BIGINT UNSIGNED NOT NULL COMMENT '当前绑定的用户 Id',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '绑定记录创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '绑定关系最近更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_serial_number` (`serial_number`),
    KEY `idx_user_id_serial_number` (`user_id`, `serial_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备与用户绑定关系表';
-- +goose StatementEnd

-- +goose StatementBegin
-- device_access_token: 设备鉴权 Access Token 表
CREATE TABLE IF NOT EXISTS `device_access_token` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'Token 凭证自增主键',
    `serial_number` VARCHAR(64) NOT NULL COMMENT '设备序列号，全局业务唯一',
    `access_token` VARCHAR(128) NOT NULL COMMENT '设备 Access Token 明文',
    `device_type` VARCHAR(32) NOT NULL DEFAULT 'default' COMMENT '设备类型（冗余自生产表，用于关联 agent）',
    `has_exposed` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否已在 OTA 接口展示下发过（0: 待展示下发, 1: 已展示下发）',
    `issued_at` DATETIME(3) NOT NULL COMMENT 'Token 签发时间',
    `expires_at` DATETIME(3) DEFAULT NULL COMMENT 'Token 过期时间，为空表示无固定过期时间',
    `revoked_at` DATETIME(3) DEFAULT NULL COMMENT 'Token 撤销时间，为空表示未撤销',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录最近更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_serial_number` (`serial_number`),
    UNIQUE KEY `uk_access_token` (`access_token`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备鉴权 Access Token 表';
-- +goose StatementEnd

-- +goose StatementBegin
-- device_type: 设备类型与 Agent 配置关联表
CREATE TABLE IF NOT EXISTS `device_type` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '关联记录自增主键',
    `device_type` VARCHAR(32) NOT NULL COMMENT '设备类型，全局业务唯一',
    `agent_config_id` BIGINT UNSIGNED NOT NULL COMMENT '关联的 Agent 配置 Id',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_device_type` (`device_type`),
    KEY `idx_agent_config_id` (`agent_config_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备类型与 Agent 配置关联表';
-- +goose StatementEnd

-- +goose StatementBegin
-- asr_config: 语音识别 ASR 配置表
CREATE TABLE IF NOT EXISTS `asr_config` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '配置自增主键',
    `name` VARCHAR(128) NOT NULL COMMENT '配置展示名称（非唯一）',
    `provider` VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'ASR 服务商/平台：bailian / volcengine / openai 等',
    `endpoint` VARCHAR(1024) NOT NULL COMMENT 'ASR WebSocket Endpoint',
    `api_key` VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '明文 API Key',
    `model` VARCHAR(255) NOT NULL COMMENT 'ASR 模型',
    `hotwords` TEXT NOT NULL COMMENT '热词配置，支持大量文本',
    `proxy_url` VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '代理地址（非空即启用）',
    `connect_timeout_ms` BIGINT NOT NULL DEFAULT 5000 COMMENT '连接超时毫秒',
    `enabled` TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否允许 Agent 引用',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
    PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='语音识别 ASR 配置表';
-- +goose StatementEnd

-- +goose StatementBegin
-- llm_config: 大语言模型 LLM 配置表
CREATE TABLE IF NOT EXISTS `llm_config` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '配置自增主键',
    `name` VARCHAR(128) NOT NULL COMMENT '配置展示名称（非唯一）',
    `provider` VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'LLM 服务商/平台：bailian / openai / deepseek / ollama 等',
    `endpoint` VARCHAR(1024) NOT NULL COMMENT 'LLM HTTP Endpoint',
    `api_key` VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '明文 API Key',
    `model` VARCHAR(255) NOT NULL COMMENT 'LLM 模型',
    `proxy_url` VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '代理地址（非空即启用）',
    `first_token_timeout_ms` BIGINT NOT NULL DEFAULT 5000 COMMENT '首 Token 超时毫秒',
    `overall_timeout_ms` BIGINT NOT NULL DEFAULT 30000 COMMENT '总超时毫秒',
    `enabled` TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否允许 Agent 引用',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
    PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='大语言模型 LLM 配置表';
-- +goose StatementEnd

-- +goose StatementBegin
-- tts_config: 语音合成 TTS 配置表
CREATE TABLE IF NOT EXISTS `tts_config` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '配置自增主键',
    `name` VARCHAR(128) NOT NULL COMMENT '配置展示名称（非唯一）',
    `provider` VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'TTS 服务商/平台：bailian / volcengine / openai 等',
    `endpoint` VARCHAR(1024) NOT NULL COMMENT 'TTS WebSocket Endpoint',
    `api_key` VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '明文 API Key',
    `model` VARCHAR(255) NOT NULL COMMENT 'TTS 模型',
    `voices` TEXT NOT NULL COMMENT '支持的音色列表（JSON 格式）',
    `proxy_url` VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '代理地址（非空即启用）',
    `connect_timeout_ms` BIGINT NOT NULL DEFAULT 5000 COMMENT '连接超时毫秒',
    `first_audio_timeout_ms` BIGINT NOT NULL DEFAULT 5000 COMMENT '首音频超时毫秒',
    `sentence_timeout_ms` BIGINT NOT NULL DEFAULT 10000 COMMENT '单句超时毫秒',
    `enabled` TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否允许 Agent 引用',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
    PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='语音合成 TTS 配置表';
-- +goose StatementEnd

-- +goose StatementBegin
-- agent_config: AI Agent 配置表
CREATE TABLE IF NOT EXISTS `agent_config` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'Agent 身份自增主键',
    `name` VARCHAR(128) NOT NULL COMMENT '展示名称（非唯一）',
    `asr_config_id` BIGINT UNSIGNED NOT NULL COMMENT '引用 asr_config.id',
    `llm_config_id` BIGINT UNSIGNED NOT NULL COMMENT '引用 llm_config.id',
    `tts_config_id` BIGINT UNSIGNED NOT NULL COMMENT '引用 tts_config.id',
    `system_prompt` TEXT NOT NULL COMMENT 'Agent 系统提示词',
    `voice` VARCHAR(128) NOT NULL COMMENT 'Agent 使用的 TTS 音色',
    `enabled` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否为当前 Agent（1: 是, 0: 否）',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
    PRIMARY KEY (`id`),
    KEY `idx_agent_config_asr_config_id` (`asr_config_id`),
    KEY `idx_agent_config_llm_config_id` (`llm_config_id`),
    KEY `idx_agent_config_tts_config_id` (`tts_config_id`),
    KEY `idx_agent_config_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='AI Agent 配置表';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS `agent_config`;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS `tts_config`;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS `llm_config`;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS `asr_config`;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS `device_type`;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS `device_access_token`;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS `device_user_ref`;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS `device_activation`;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS `device_hmac_credential`;
-- +goose StatementEnd
