package session

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/agentkit"
	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockLLMClient struct {
	mu          sync.Mutex
	callCount   int
	generate    func(ctx context.Context, request ai.LLMRequest, callback ai.LLMStreamCallback) (string, error)
	reqReceived []ai.LLMRequest
}

func (m *mockLLMClient) Generate(ctx context.Context, request ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
	m.mu.Lock()
	m.callCount++
	m.reqReceived = append(m.reqReceived, request)
	fn := m.generate
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, request, callback)
	}
	if callback != nil {
		_ = callback(ctx, ai.LLMChunk{Text: "默认回复", Iteration: 0})
	}
	return "默认回复", nil
}

func TestTurnPipeline_MultiTurnTools_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockLLM := &mockLLMClient{}

	var toolExecuted bool
	mockLLM.generate = func(ctx context.Context, req ai.LLMRequest, callback ai.LLMStreamCallback) (string, error) {
		for _, tool := range req.Tools {
			if tool.Name == agentkit.ToolGetCurrentTime {
				res, err := tool.Run(ctx, map[string]any{})
				if err != nil {
					return "", err
				}
				if res != nil {
					toolExecuted = true
				}
			}
		}

		if callback != nil {
			_ = callback(ctx, ai.LLMChunk{Text: "正在为您查询当前时间。", Iteration: 0})
			_ = callback(ctx, ai.LLMChunk{Text: "当前时间是上午十点。", Iteration: 1})
		}
		return "当前时间是上午十点。", nil
	}

	events := make(chan turnEvent, 10)
	toolProvider := NewToolProvider(nil, nil, slog.Default())
	pipeline := NewTurnPipeline(PipelineOptions{
		LLMClient:    mockLLM,
		Config:       NormalizeConfig(SessionConfig{}),
		ToolProvider: toolProvider,
		Logger:       slog.Default(),
		PostEvent: func(ev turnEvent) {
			events <- ev
		},
	})

	_ = pipeline.StartListening(ctx, 1, "sess-multi-tool-test", ListenModeAuto)
	_ = pipeline.StartResponse(1, "sess-multi-tool-test", "现在几点了")

	select {
	case ev := <-events:
		if ev.typ != turnEventTurnCompleted {
			t.Fatalf("expected turnEventTurnCompleted, got %v", ev.typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartResponse timed out")
	}

	if !toolExecuted {
		t.Fatal("expected server tool to be executed during generate")
	}
}
