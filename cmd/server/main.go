package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"xiaozhi-esp32-golang-server/internal/config"
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

	fmt.Println("server configuration initialized")
}
