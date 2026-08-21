package database_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestMigrate_NilDB(t *testing.T) {
	err := database.Migrate(context.Background(), nil, "sqlite")
	if err == nil {
		t.Fatal("expected error when sqlDB is nil, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be nil") {
		t.Errorf("expected nil error message, got: %v", err)
	}
}

func TestMigrate_UnsupportedDriver(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer sqlDB.Close()

	err = database.Migrate(context.Background(), sqlDB, "unsupported_db")
	if err == nil {
		t.Fatal("expected error for unsupported driver, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported database driver") {
		t.Errorf("expected unsupported database driver error, got: %v", err)
	}
}

func TestMigrate_ContextCanceled(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_canceled.db")
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = database.Migrate(ctx, sqlDB, "sqlite")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "idempotent.db")
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer sqlDB.Close()

	// 第一次迁移
	if err := database.Migrate(context.Background(), sqlDB, "sqlite"); err != nil {
		t.Fatalf("first migration failed: %v", err)
	}

	// 第二次重复迁移验证幂等性
	if err := database.Migrate(context.Background(), sqlDB, "sqlite"); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}
}

func TestOpen_WithMigration_Success(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "migrated.db")
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
		t.Fatalf("unexpected error opening and migrating sqlite database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("unexpected error closing database: %v", err)
		}
	}()

	// 验证 goose_db_version 表已创建且已应用有效迁移版本
	var currentVersion int64
	var isApplied int
	err = db.DB().Raw("SELECT version_id, is_applied FROM goose_db_version ORDER BY id DESC LIMIT 1").Row().Scan(&currentVersion, &isApplied)
	if err != nil {
		t.Fatalf("failed to query goose_db_version table: %v", err)
	}
	if currentVersion <= 0 {
		t.Errorf("expected database version > 0, got %d", currentVersion)
	}
	if isApplied != 1 {
		t.Errorf("expected latest migration to be applied (is_applied=1), got %d", isApplied)
	}
}
