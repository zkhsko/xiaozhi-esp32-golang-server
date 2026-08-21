package database_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestOpen_SQLite_Success(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	dsn := "file:" + dbPath + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"

	cfg := config.DatabaseConfig{
		Driver:                "sqlite",
		MaxOpenConns:          1,
		MaxIdleConns:          1,
		ConnectionMaxLifetime: 0,
		ConnectionMaxIdleTime: 0,
		PingTimeout:           3 * time.Second,
		DSN:                   dsn,
	}

	ctx := context.Background()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error opening sqlite database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("unexpected error closing database: %v", err)
		}
	}()

	if db.DB() == nil {
		t.Fatal("expected gorm DB instance to be non-nil")
	}

	// 验证 foreign_keys 设置生效
	var foreignKeysEnabled int
	if err := db.DB().Raw("PRAGMA foreign_keys").Scan(&foreignKeysEnabled).Error; err != nil {
		t.Fatalf("failed to query pragma foreign_keys: %v", err)
	}
	if foreignKeysEnabled != 1 {
		t.Errorf("expected foreign_keys to be enabled (1), got %d", foreignKeysEnabled)
	}
}

func TestOpen_UnsupportedDriver(t *testing.T) {
	cfg := config.DatabaseConfig{
		Driver:      "oracle",
		DSN:         "user/secret@127.0.0.1:1521/xe",
		PingTimeout: 1 * time.Second,
	}

	_, err := database.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unsupported driver, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported database driver") {
		t.Errorf("expected unsupported database driver error, got: %v", err)
	}
}

func TestOpen_MySQL_Unreachable_FailsSafely(t *testing.T) {
	// 使用保留测试网段不可达地址验证 dialector 构建与失败安全处理
	cfg := config.DatabaseConfig{
		Driver:       "mysql",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		PingTimeout:  100 * time.Millisecond,
		DSN:          "test_user:sensitive_mysql_pass_123@tcp(192.0.2.1:3306)/test_db?timeout=100ms",
	}

	_, err := database.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unreachable mysql, got nil")
	}
	if !strings.Contains(err.Error(), "mysql database") {
		t.Errorf("expected mysql database error prefix, got: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive_mysql_pass_123") {
		t.Fatalf("error message leaked mysql password: %s", err.Error())
	}
}

func TestOpen_PostgreSQL_Unreachable_FailsSafely(t *testing.T) {
	// 使用保留测试网段不可达地址验证 dialector 构建与失败安全处理
	cfg := config.DatabaseConfig{
		Driver:       "postgres",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		PingTimeout:  100 * time.Millisecond,
		DSN:          "host=192.0.2.1 port=5432 user=test_pg password=sensitive_pg_pass_456 dbname=test_db connect_timeout=1 sslmode=disable",
	}

	_, err := database.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unreachable postgres, got nil")
	}
	if !strings.Contains(err.Error(), "postgres database") {
		t.Errorf("expected postgres database error prefix, got: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive_pg_pass_456") {
		t.Fatalf("error message leaked postgres password: %s", err.Error())
	}
}

func TestOpen_ContextCanceled(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_canceled.db")

	cfg := config.DatabaseConfig{
		Driver:       "sqlite",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		PingTimeout:  3 * time.Second,
		DSN:          "file:" + dbPath,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消上下文

	_, err := database.Open(ctx, cfg)
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
	if !strings.Contains(err.Error(), "ping sqlite database") {
		t.Errorf("expected ping failure error, got: %v", err)
	}
}

func TestOpen_InvalidDSN_Path(t *testing.T) {
	// 指定一个不存在且无法创建的路径
	invalidPath := filepath.Join(t.TempDir(), "non_existent_dir", "sub_dir", "test.db")
	cfg := config.DatabaseConfig{
		Driver:       "sqlite",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		PingTimeout:  1 * time.Second,
		DSN:          "file:" + invalidPath + "?mode=ro",
	}

	_, err := database.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error opening invalid dsn path, got nil")
	}
}

func TestOpen_DSNLeakSafety(t *testing.T) {
	const sensitiveDSN = "file:/invalid/path/with/sensitive-password-12345/db.sqlite"
	cfg := config.DatabaseConfig{
		Driver:       "sqlite",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		PingTimeout:  1 * time.Second,
		DSN:          sensitiveDSN,
	}

	_, err := database.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// 错误前缀由我们定义，不应拼接完整 DSN
	if strings.Contains(err.Error(), "sensitive-password-12345") {
		t.Fatalf("error message leaked sensitive DSN content: %s", err.Error())
	}
}

func TestDatabase_Close_NilSafety(t *testing.T) {
	var nilDB *database.Database
	if err := nilDB.Close(); err != nil {
		t.Errorf("expected nil database Close to return nil, got: %v", err)
	}
	if nilDB.DB() != nil {
		t.Errorf("expected nil database DB to return nil")
	}

	emptyDB := &database.Database{}
	if err := emptyDB.Close(); err != nil {
		t.Errorf("expected empty database Close to return nil, got: %v", err)
	}
}
