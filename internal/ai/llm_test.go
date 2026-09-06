package ai

import (
	"testing"
	"time"
)

func TestLLMOptionsNormalized(t *testing.T) {
	opts := LLMOptions{
		Provider: " DashScope ",
		Endpoint: " https://example.com/v1 ",
		APIKey:   " test-key ",
		Model:    " qwen-plus ",
		ProxyURL: " socks5://127.0.0.1:1080 ",
	}.Normalized()

	if opts.Provider != DefaultLLMProvider {
		t.Fatalf("expected provider %q, got %q", DefaultLLMProvider, opts.Provider)
	}
	if opts.Endpoint != "https://example.com/v1" || opts.APIKey != "test-key" || opts.Model != "qwen-plus" || opts.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("unexpected normalized options: %+v", opts)
	}
	if opts.FirstTokenTimeout != DefaultLLMFirstTokenTimeout {
		t.Fatalf("expected first token timeout %v, got %v", DefaultLLMFirstTokenTimeout, opts.FirstTokenTimeout)
	}
	if opts.OverallTimeout != DefaultLLMOverallTimeout {
		t.Fatalf("expected overall timeout %v, got %v", DefaultLLMOverallTimeout, opts.OverallTimeout)
	}
}

func TestLLMOptionsValidate(t *testing.T) {
	valid := LLMOptions{
		Endpoint:          "https://example.com/v1",
		Model:             "qwen-plus",
		ProxyURL:          "http://127.0.0.1:7890",
		FirstTokenTimeout: 100 * time.Millisecond,
		OverallTimeout:    time.Second,
	}

	tests := []struct {
		name string
		opts LLMOptions
	}{
		{name: "valid", opts: valid},
		{name: "empty endpoint", opts: LLMOptions{Model: valid.Model}},
		{name: "invalid endpoint", opts: LLMOptions{Endpoint: "ws://example.com", Model: valid.Model}},
		{name: "empty model", opts: LLMOptions{Endpoint: valid.Endpoint}},
		{name: "invalid proxy", opts: LLMOptions{Endpoint: valid.Endpoint, Model: valid.Model, ProxyURL: "ftp://example.com"}},
		{name: "negative first token timeout", opts: LLMOptions{Endpoint: valid.Endpoint, Model: valid.Model, FirstTokenTimeout: -time.Second}},
		{name: "negative overall timeout", opts: LLMOptions{Endpoint: valid.Endpoint, Model: valid.Model, OverallTimeout: -time.Second}},
		{name: "overall timeout not greater", opts: LLMOptions{Endpoint: valid.Endpoint, Model: valid.Model, FirstTokenTimeout: time.Second, OverallTimeout: time.Second}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.name == "valid" && err != nil {
				t.Fatalf("expected valid options, got %v", err)
			}
			if tt.name != "valid" && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
