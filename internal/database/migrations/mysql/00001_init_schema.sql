-- +goose Up
-- +goose StatementBegin
-- device_hmac_credential: 设备出厂与激活 HMAC 凭证表
CREATE TABLE IF NOT EXISTS `device_hmac_credential` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '凭证自增主键',
    `serial_number` VARCHAR(64) NOT NULL COMMENT '设备序列号，全局业务唯一',
    `auth_method` VARCHAR(32) NOT NULL DEFAULT 'efuse_hmac' COMMENT '认证方式：efuse_hmac / activation_code / manual_code_hmac',
    `hmac_key_ciphertext` VARBINARY(512) NOT NULL COMMENT 'HMAC Key 明文（统一字段名 hmac_key_ciphertext）',
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
    `user_id` BIGINT UNSIGNED NOT NULL COMMENT '当前绑定的用户 ID',
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

-- +goose Down
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
