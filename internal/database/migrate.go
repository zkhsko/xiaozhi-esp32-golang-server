package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*/*.sql
var migrationsFS embed.FS

// driverToGooseDialect 将项目驱动名称映射为 Goose 方言名称。
func driverToGooseDialect(driver string) (string, error) {
	switch driver {
	case "sqlite":
		return "sqlite3", nil
	case "mysql":
		return "mysql", nil
	case "postgres":
		return "postgres", nil
	default:
		return "", fmt.Errorf("unsupported database driver %q", driver)
	}
}

// Migrate 根据数据库驱动执行对应方言目录下的 Goose 向上迁移。
func Migrate(ctx context.Context, sqlDB *sql.DB, driver string) error {
	if sqlDB == nil {
		return fmt.Errorf("sql.DB instance cannot be nil")
	}

	dialect, err := driverToGooseDialect(driver)
	if err != nil {
		return err
	}

	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set goose dialect %s: %w", dialect, err)
	}

	migrationDir := path.Join("migrations", driver)
	if err := goose.UpContext(ctx, sqlDB, migrationDir); err != nil {
		return fmt.Errorf("run goose migrations for %s: %w", driver, err)
	}

	return nil
}
