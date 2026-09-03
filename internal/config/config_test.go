package config

import (
	"strings"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
	yamlContent := `
server:
  listen_addr: ":8080"
  websocket_url: "wss://example.com/xiaozhi/v1/"
  max_concurrent_sessions: 10
  shutdown_timeout: 10s
  http_read_timeout: 15s
  http_write_timeout: 30s
  http_idle_timeout: 60s
  max_http_body_bytes: 65536
  max_http_header_bytes: 1024

session:
  hello_timeout: 10s
  max_ws_text_message_bytes: 32768
  max_opus_packet_bytes: 1024
  max_listening_duration: 30s
  asr_pcm_queue_capacity: 100
  downlink_opus_queue_capacity: 100
  max_history_turns: 6

database:
  driver: "sqlite"
  dsn: "file::memory:?cache=shared"
  max_open_conns: 1
  max_idle_conns: 1
  connection_max_lifetime: 0s
  connection_max_idle_time: 0s
  ping_timeout: 3s
`

	cfg, err := LoadFromReader(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("LoadFromReader failed: %v", err)
	}

	if cfg.Server.ListenAddr != ":8080" {
		t.Errorf("expected listen_addr :8080, got %s", cfg.Server.ListenAddr)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("expected driver sqlite, got %s", cfg.Database.Driver)
	}
	if cfg.Database.DSN != "file::memory:?cache=shared" {
		t.Errorf("expected dsn file::memory:?cache=shared, got %s", cfg.Database.DSN)
	}
}

func TestLoadInvalidConfig_MissingDSN(t *testing.T) {
	yamlContent := `
server:
  listen_addr: ":8080"
  websocket_url: "wss://example.com/xiaozhi/v1/"
  max_concurrent_sessions: 10
  shutdown_timeout: 10s
  http_read_timeout: 15s
  http_write_timeout: 30s
  http_idle_timeout: 60s
  max_http_body_bytes: 65536
  max_http_header_bytes: 1024

session:
  hello_timeout: 10s
  max_ws_text_message_bytes: 32768
  max_opus_packet_bytes: 1024
  max_listening_duration: 30s
  asr_pcm_queue_capacity: 100
  downlink_opus_queue_capacity: 100
  max_history_turns: 6

database:
  driver: "sqlite"
  max_open_conns: 1
  max_idle_conns: 1
  connection_max_lifetime: 0s
  connection_max_idle_time: 0s
  ping_timeout: 3s
`

	_, err := LoadFromReader(strings.NewReader(yamlContent))
	if err == nil {
		t.Fatal("expected error due to missing dsn, got nil")
	}
	if !strings.Contains(err.Error(), "dsn is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLoadExampleConfigFile(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("Load config.example.yaml failed: %v", err)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("expected driver sqlite, got %s", cfg.Database.Driver)
	}
	if cfg.Database.DSN != "file:xiaozhi-dev.db?_journal_mode=WAL&_busy_timeout=5000" {
		t.Errorf("unexpected dsn: %s", cfg.Database.DSN)
	}
}
