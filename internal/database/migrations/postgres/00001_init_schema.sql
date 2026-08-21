-- +goose Up
-- +goose StatementBegin
-- device_hmac_credential: 设备出厂与激活 HMAC 凭证表
CREATE TABLE IF NOT EXISTS device_hmac_credential (
    id BIGSERIAL PRIMARY KEY,                                       -- 凭证内部自增主键
    serial_number VARCHAR(64) NOT NULL,                            -- 设备序列号（全局业务唯一）
    auth_method VARCHAR(32) NOT NULL DEFAULT 'efuse_hmac',         -- 认证方式：efuse_hmac / activation_code / manual_code_hmac
    hmac_key_ciphertext BYTEA NOT NULL,                            -- 加密后的 HMAC Key 密文（禁止明文存储）
    credential_status VARCHAR(16) NOT NULL DEFAULT 'enabled',      -- 凭证状态：enabled / activated / blocked / revoked
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,     -- 凭证创建时间
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP      -- 凭证最近更新时间
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS uk_serial_number ON device_hmac_credential(serial_number);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS device_hmac_credential;
-- +goose StatementEnd
