package config

import (
	"bytes"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWebSocketVersionConfig(t *testing.T) {
	example, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value any
		want  int
	}{
		{"default", nil, 1},
		{"v1", 1, 1},
		{"v2", 2, 2},
		{"v3", 3, 3},
		{"zero", 0, 0},
		{"negative", -1, 0},
		{"unsupported", 4, 0},
		{"large_integer", 65537, 0},
		{"fraction", 1.5, 0},
		{"invalid_text", "invalid", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var document map[string]any
			if err := yaml.Unmarshal(example, &document); err != nil {
				t.Fatal(err)
			}
			server := document["server"].(map[string]any)
			if tc.value == nil {
				delete(server, "websocket_version")
			} else {
				server["websocket_version"] = tc.value
			}
			data, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFromReader(bytes.NewReader(data))
			if tc.want == 0 {
				if err == nil {
					t.Fatalf("invalid version accepted: %v", cfg.Server.WebSocketVersion)
				}
				return
			}
			if err != nil || int(cfg.Server.WebSocketVersion) != tc.want {
				t.Fatalf("load version %v: cfg=%+v, err=%v", tc.value, cfg, err)
			}
		})
	}
}
