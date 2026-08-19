package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/server"
)

func main() {
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

	srv := server.New(cfg.Server, nil)

	slog.Info("starting HTTP server", "addr", cfg.Server.ListenAddr)
	if err := srv.Run(ctx); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
}
