-- +goose Up
-- +goose StatementBegin
-- device_hmac_credential: 设备出厂与激活 HMAC 凭证表
CREATE TABLE IF NOT EXISTS device_hmac_credential (
    id INTEGER PRIMARY KEY AUTOINCREMENT,                          -- 凭证内部自增主键
    serial_number VARCHAR(64) NOT NULL,                            -- 设备序列号（全局业务唯一）
    auth_method VARCHAR(32) NOT NULL DEFAULT 'efuse_hmac',         -- 认证方式：efuse_hmac / activation_code / manual_code_hmac
    hmac_key_ciphertext BLOB NOT NULL,                             -- 加密后的 HMAC Key 密文（禁止明文存储）
    credential_status VARCHAR(16) NOT NULL DEFAULT 'enabled',      -- 凭证状态：enabled / activated / blocked / revoked
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,        -- 凭证创建时间
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP         -- 凭证最近更新时间
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS uk_serial_number ON device_hmac_credential(serial_number);
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
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP         -- 记录最近更新时间
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS uk_device_activation_serial_number ON device_activation(serial_number);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_device_activation_device_id ON device_activation(device_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS device_activation;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS device_hmac_credential;
-- +goose StatementEnd
