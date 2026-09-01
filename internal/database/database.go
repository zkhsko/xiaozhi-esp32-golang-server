package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"

	"xiaozhi-esp32-golang-server/internal/config"
)

// Database 封装 GORM 实例和底层 SQL 连接池，管理数据库生命周期。
type Database struct {
	gormDB *gorm.DB
	sqlDB  *sql.DB
}

// Open 根据配置初始化数据库连接并验证连接可用性。
func Open(ctx context.Context, cfg config.DatabaseConfig) (*Database, error) {
	var dialector gorm.Dialector

	switch cfg.Driver {
	case "sqlite":
		dialector = sqlite.Open(cfg.DSN)
	case "mysql":
		dialector = mysql.Open(cfg.DSN)
	case "postgres":
		dialector = postgres.Open(cfg.DSN)
	default:
		return nil, fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}

	gormDB, err := gorm.Open(dialector, &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormLogger.Default.LogMode(gormLogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", cfg.Driver, err)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying sql database: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnectionMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnectionMaxIdleTime)

	pingTimeout := cfg.PingTimeout
	if pingTimeout <= 0 {
		pingTimeout = 3 * time.Second
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping %s database: %w", cfg.Driver, err)
	}

	if err := Migrate(ctx, sqlDB, cfg.Driver); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate %s database: %w", cfg.Driver, err)
	}

	return &Database{
		gormDB: gormDB,
		sqlDB:  sqlDB,
	}, nil
}

// Close 关闭底层数据库连接池。
func (d *Database) Close() error {
	if d == nil || d.sqlDB == nil {
		return nil
	}
	return d.sqlDB.Close()
}
