package dashscope

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	genkitai "github.com/firebase/genkit/go/ai"

	"xiaozhi-esp32-golang-server/internal/ai"
)

type generationObserver struct {
	currentStep        atomic.Int32
	firstTokenTimedOut atomic.Bool
}

func newGenerationObserver() *generationObserver {
	return &generationObserver{}
}

func (o *generationObserver) firstTokenTimeoutOccurred() bool {
	return o.firstTokenTimedOut.Load()
}

func (o *generationObserver) timeoutMiddleware(firstTokenTimeout time.Duration) genkitai.MiddlewareFunc {
	return genkitai.MiddlewareFunc(func(_ context.Context) (*genkitai.Hooks, error) {
		return &genkitai.Hooks{
			WrapGenerate: func(ctx context.Context, params *genkitai.GenerateParams, next genkitai.GenerateNext) (*genkitai.ModelResponse, error) {
				o.currentStep.Store(int32(params.Iteration))
				return next(ctx, params)
			},
			WrapModel: func(ctx context.Context, params *genkitai.ModelParams, next genkitai.ModelNext) (*genkitai.ModelResponse, error) {
				return o.generateWithFirstTokenTimeout(ctx, params, next, firstTokenTimeout)
			},
		}, nil
	})
}

func (o *generationObserver) generateWithFirstTokenTimeout(
	ctx context.Context,
	params *genkitai.ModelParams,
	next genkitai.ModelNext,
	firstTokenTimeout time.Duration,
) (*genkitai.ModelResponse, error) {
	modelCtx, modelCancel := context.WithCancel(ctx)
	defer modelCancel()

	var timerMu sync.Mutex
	var firstTokenReceived bool
	var modelTimedOut bool

	timer := time.AfterFunc(firstTokenTimeout, func() {
		timerMu.Lock()
		defer timerMu.Unlock()
		if firstTokenReceived {
			return
		}
		modelTimedOut = true
		o.firstTokenTimedOut.Store(true)
		modelCancel()
	})
	defer func() {
		timerMu.Lock()
		timer.Stop()
		timerMu.Unlock()
	}()

	originalCallback := params.Callback
	params.Callback = func(callbackCtx context.Context, chunk *genkitai.ModelResponseChunk) error {
		timerMu.Lock()
		if !firstTokenReceived && hasModelOutput(chunk) {
			firstTokenReceived = true
			timer.Stop()
		}
		timerMu.Unlock()

		if originalCallback != nil {
			return originalCallback(callbackCtx, chunk)
		}
		return nil
	}

	response, err := next(modelCtx, params)
	if err == nil {
		return response, nil
	}

	timerMu.Lock()
	timedOut := modelTimedOut
	timerMu.Unlock()
	if timedOut {
		return nil, fmt.Errorf("%w: first token timeout (%v)", ai.ErrFirstTokenTimeout, firstTokenTimeout)
	}
	return nil, err
}

func (o *generationObserver) streamCallback(
	ctx context.Context,
	chunks chan<- ai.LLMChunk,
) func(context.Context, *genkitai.ModelResponseChunk) error {
	return func(_ context.Context, chunk *genkitai.ModelResponseChunk) error {
		if chunk == nil || (chunk.Role != "" && chunk.Role != genkitai.RoleModel) {
			return nil
		}
		text := chunk.Text()
		if text == "" || chunks == nil {
			return nil
		}
		select {
		case chunks <- ai.LLMChunk{Text: text, Step: int(o.currentStep.Load())}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func hasModelOutput(chunk *genkitai.ModelResponseChunk) bool {
	if chunk == nil {
		return false
	}
	if chunk.Text() != "" || chunk.Reasoning() != "" {
		return true
	}
	for _, part := range chunk.Content {
		if part != nil && (part.IsText() || part.IsToolRequest() || part.IsReasoning()) {
			return true
		}
	}
	return false
}
