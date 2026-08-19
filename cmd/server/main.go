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
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	_ = llmClient

	sessionLimiter := session.NewSessionLimiter(cfg.Server.MaxConcurrentSessions)

	mux := http.NewServeMux()
	mux.Handle(bootstrap.OTAPath, bootstrap.NewHandler(cfg, slog.Default()))
	mux.Handle(session.WebSocketPath, session.NewHandler(cfg, sessionLimiter, asrClient, slog.Default()))

	srv := server.New(cfg.Server, mux)

	slog.Info("starting HTTP server", "addr", cfg.Server.ListenAddr)
	if err := srv.Run(ctx); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
}
