package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
	"xiaozhi-esp32-golang-server/internal/router"
	"xiaozhi-esp32-golang-server/internal/server"
	"xiaozhi-esp32-golang-server/internal/session"
)

func main() {
	logger.InitDefault(os.Stdout, slog.LevelInfo)

	configPath := flag.String("config", "config.yaml", "Path to YAML configuration file")
	flag.Parse()

	if *configPath == "" {
		slog.Error("config path cannot be empty")
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.Info("configuration loaded successfully",
		"listen_addr", cfg.Server.ListenAddr,
		"websocket_url", cfg.Server.WebSocketURL,
		"max_concurrent_sessions", cfg.Server.MaxConcurrentSessions,
		"database_driver", cfg.Database.Driver,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		slog.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := db.Close(); err != nil {
			slog.Error("failed to close database", "error", err)
		}
	}()
	slog.Info("database initialized successfully", "driver", cfg.Database.Driver)

	sessionLimiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	websocketSessionHandler := session.NewHandler(session.HandlerOptions{
		Config:  cfg,
		DB:      db,
		Limiter: sessionLimiter,
		Logger:  slog.Default(),
	})

	adminHandler := router.NewAdminHandler(cfg, db, slog.Default())
	otaHandler := router.NewOTAHandler(cfg, db, slog.Default())
	userHandler := router.NewUserHandler(cfg, db, otaHandler, slog.Default())
	httpRouter := router.NewRouter(router.Options{
		Admin:            adminHandler,
		OTA:              otaHandler,
		User:             userHandler,
		WebsocketSession: websocketSessionHandler,
	})

	srv := server.New(cfg.Server, httpRouter)
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		return websocketSessionHandler.Shutdown(shutdownCtx)
	})

	slog.Info("starting HTTP server", "addr", srv.Addr())
	if err := srv.Run(ctx); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
}
