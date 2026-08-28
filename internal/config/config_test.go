package config

import (
	"os"
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
  tts_pcm_queue_capacity: 100
  downlink_opus_queue_capacity: 100
  max_history_turns: 6
  listen_prompt_enabled: true

database:
  driver: "sqlite"
  max_open_conns: 1
  max_idle_conns: 1
  connection_max_lifetime: 0s
  connection_max_idle_time: 0s
  ping_timeout: 3s
`

	os.Setenv(EnvDatabaseDSN, "file::memory:?cache=shared")
	defer os.Unsetenv(EnvDatabaseDSN)

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
  tts_pcm_queue_capacity: 100
  downlink_opus_queue_capacity: 100
  max_history_turns: 6
  listen_prompt_enabled: true

database:
  driver: "sqlite"
  max_open_conns: 1
  max_idle_conns: 1
  connection_max_lifetime: 0s
  connection_max_idle_time: 0s
  ping_timeout: 3s
`
	os.Unsetenv(EnvDatabaseDSN)

	_, err := LoadFromReader(strings.NewReader(yamlContent))
	if err == nil {
		t.Error("expected error due to missing DATABASE_DSN, got nil")
	}
}
