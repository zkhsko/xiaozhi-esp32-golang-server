package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"xiaozhi-esp32-golang-server/internal/ai/bailian"
	"xiaozhi-esp32-golang-server/internal/bootstrap"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
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
		"asr_model", cfg.AI.Bailian.ASRModel,
		"llm_model", cfg.AI.Bailian.LLMModel,
		"tts_model", cfg.AI.Bailian.TTSModel,
		"proxy_enabled", cfg.Proxy.Enabled,
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

	asrClient, err := bailian.NewASRClient(cfg)
	if err != nil {
		slog.Error("failed to initialize bailian asr client", "error", err)
		os.Exit(1)
	}

	llmClient, err := bailian.NewLLMClient(cfg)
	if err != nil {
		slog.Error("failed to initialize bailian llm client", "error", err)
		os.Exit(1)
	}

	ttsClient, err := bailian.NewTTSClient(cfg)
	if err != nil {
		slog.Error("failed to initialize bailian tts client", "error", err)
		os.Exit(1)
	}

	sessionLimiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)
	wsHandler := session.NewHandler(cfg, sessionLimiter, asrClient, llmClient, ttsClient, slog.Default())

	mux := http.NewServeMux()
	mux.Handle(bootstrap.OTAPath, bootstrap.NewHandler(cfg, slog.Default()))
	mux.Handle(session.WebSocketPath, wsHandler)

	srv := server.New(cfg.Server, mux)
	srv.RegisterOnShutdown(func(shutdownCtx context.Context) error {
		return wsHandler.Shutdown(shutdownCtx)
	})

	slog.Info("starting HTTP server", "addr", srv.Addr())
	if err := srv.Run(ctx); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
}
