package voice

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestDrainTurnEffects(t *testing.T) {
	effect := TurnEffect{Type: EffectCloseSession}
	for _, tc := range []struct {
		name       string
		nilChannel bool
		closed     bool
		count      int
	}{
		{name: "nil", nilChannel: true},
		{name: "empty"},
		{name: "buffered", count: 2},
		{name: "closed", closed: true},
		{name: "closed-buffered", closed: true, count: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ch chan TurnEffect
			var want []TurnEffect
			if !tc.nilChannel {
				ch = make(chan TurnEffect, tc.count)
				for i := 0; i < tc.count; i++ {
					ch <- effect
					want = append(want, effect)
				}
				if tc.closed {
					close(ch)
				}
			}
			if got := drainTurnEffects(ch); !slices.Equal(got, want) {
				t.Fatalf("effects = %v, want %v", got, want)
			}
			if got := drainTurnEffects(ch); len(got) != 0 {
				t.Fatalf("effects were returned twice: %v", got)
			}
			if ch != nil && !tc.closed {
				close(ch)
			}
		})
	}
}

func TestTurnEngine_ResponseFailurePreservesEffects(t *testing.T) {
	failure := errors.New("llm failed after tool execution")
	effect := TurnEffect{Type: EffectCloseSession}
	effectsCh := make(chan TurnEffect, 1)
	effectsCh <- effect
	input := make(chan []byte)
	close(input)

	result := NewEngine().HandleTurn(context.Background(), TurnRequest{
		TurnId: 1, Mode: "auto", PromptToneEnabled: true,
		ASRClient: &mockASRClient{text: "再见"},
		LLMClient: &mockLLMClient{err: failure},
		TTSClient: &mockTTSClient{},
		EffectsCh: effectsCh,
	}, input, &mockTurnOutput{})
	if result.Status != TurnFailed || !errors.Is(result.Err, failure) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !slices.Equal(result.Effects, []TurnEffect{effect}) {
		t.Fatalf("lost effects after response failure: %v", result.Effects)
	}
}
