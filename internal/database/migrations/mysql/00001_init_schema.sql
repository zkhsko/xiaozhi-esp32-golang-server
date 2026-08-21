-- +goose Up
-- +goose StatementBegin
-- device_hmac_credential: 设备出厂与激活 HMAC 凭证表
CREATE TABLE IF NOT EXISTS `device_hmac_credential` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '凭证自增主键',
    `serial_number` VARCHAR(64) NOT NULL COMMENT '设备序列号，全局业务唯一',
    `auth_method` VARCHAR(32) NOT NULL DEFAULT 'efuse_hmac' COMMENT '认证方式：efuse_hmac / activation_code / manual_code_hmac',
    `hmac_key_ciphertext` VARBINARY(512) NOT NULL COMMENT '加密后的 HMAC Key 密文',
    `credential_status` VARCHAR(16) NOT NULL DEFAULT 'enabled' COMMENT '凭证状态：enabled / activated / blocked / revoked',
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
    `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_serial_number` (`serial_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备出厂与激活 HMAC 凭证表';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS `device_hmac_credential`;
-- +goose StatementEnd
