package dashscope

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	genkitai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai/dashscope"
	"github.com/openai/openai-go/option"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/database"
)

func boolPtr(b bool) *bool {
	return &b
}

// LLMClient 实现基于 Genkit DashScope 插件的大语言模型客户端。
type LLMClient struct {
	endpoint          string
	apiKey            string
	model             string
	firstTokenTimeout time.Duration
	overallTimeout    time.Duration
	genkit            *genkit.Genkit
}

// NewLLMClient 基于数据库 LLM 配置实体构造 DashScope LLM 客户端实例。
func NewLLMClient(cfg *database.LLMConfig, opts ...option.RequestOption) (*LLMClient, error) {
	if cfg == nil {
		return nil, errors.New("llm config cannot be nil")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("dashscope api key is required")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("dashscope llm endpoint is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("dashscope llm model is required")
	}

	firstTokenTimeout := time.Duration(cfg.FirstTokenTimeoutMS) * time.Millisecond
	if firstTokenTimeout <= 0 {
		firstTokenTimeout = 15 * time.Second
	}

	overallTimeout := time.Duration(cfg.OverallTimeoutMS) * time.Millisecond
	if overallTimeout <= 0 {
		overallTimeout = 60 * time.Second
	}

	if overallTimeout <= firstTokenTimeout {
		return nil, fmt.Errorf("llm overall timeout (%v) must be greater than first token timeout (%v)", overallTimeout, firstTokenTimeout)
	}

	var httpClient *http.Client
	if strings.TrimSpace(cfg.ProxyURL) != "" {
		proxyURL, err := url.Parse(strings.TrimSpace(cfg.ProxyURL))
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
	}

	pluginOpts := []option.RequestOption{
		option.WithBaseURL(strings.TrimSpace(cfg.Endpoint)),
		option.WithMaxRetries(0),
	}
	if httpClient != nil {
		pluginOpts = append(pluginOpts, option.WithHTTPClient(httpClient))
	}
	pluginOpts = append(pluginOpts, opts...)

	plugin := &dashscope.DashScope{
		APIKey: strings.TrimSpace(cfg.APIKey),
		Opts:   pluginOpts,
	}

	g := genkit.Init(context.Background(), genkit.WithPlugins(plugin))

	return &LLMClient{
		endpoint:          strings.TrimSpace(cfg.Endpoint),
		apiKey:            strings.TrimSpace(cfg.APIKey),
		model:             strings.TrimSpace(cfg.Model),
		firstTokenTimeout: firstTokenTimeout,
		overallTimeout:    overallTimeout,
		genkit:            g,
	}, nil
}

// Generate 基于上下文、请求与流式回调执行完整的模型生成与工具调用循环。
func (c *LLMClient) Generate(
	ctx context.Context,
	request ai.LLMRequest,
	callback ai.LLMStreamCallback,
) (string, error) {
	if ctx == nil {
		return "", errors.New("context cannot be nil")
	}
	if len(request.Messages) == 0 {
		return "", errors.New("messages cannot be empty")
	}

	genkitMessages := make([]*genkitai.Message, 0, len(request.Messages))
	for _, msg := range request.Messages {
		switch msg.Role {
		case ai.RoleSystem:
			genkitMessages = append(genkitMessages, genkitai.NewSystemTextMessage(msg.Content))
		case ai.RoleUser:
			genkitMessages = append(genkitMessages, genkitai.NewUserTextMessage(msg.Content))
		case ai.RoleAssistant:
			genkitMessages = append(genkitMessages, genkitai.NewModelTextMessage(msg.Content))
		default:
			genkitMessages = append(genkitMessages, genkitai.NewUserTextMessage(msg.Content))
		}
	}

	genkitTools := make([]genkitai.ToolRef, 0, len(request.Tools))
	for _, t := range request.Tools {
		tool := t
		toolOpts := make([]genkitai.ToolOption, 0, 1)
		if len(tool.Parameters) > 0 {
			toolOpts = append(toolOpts, genkitai.WithInputSchema(tool.Parameters))
		}
		gTool := genkitai.NewTool(tool.Name, tool.Description, func(tc *genkitai.ToolContext, input any) (any, error) {
			if tool.Run != nil {
				return tool.Run(tc.Context, input)
			}
			return nil, fmt.Errorf("tool %s has no run handler", tool.Name)
		}, toolOpts...)
		genkitTools = append(genkitTools, gTool)
	}

	var currentIteration atomic.Int32
	var firstTokenTimedOut atomic.Bool

	timeoutMiddleware := genkitai.MiddlewareFunc(func(mCtx context.Context) (*genkitai.Hooks, error) {
		return &genkitai.Hooks{
			WrapGenerate: func(wCtx context.Context, params *genkitai.GenerateParams, next genkitai.GenerateNext) (*genkitai.ModelResponse, error) {
				currentIteration.Store(int32(params.Iteration))
				return next(wCtx, params)
			},
			WrapModel: func(wCtx context.Context, params *genkitai.ModelParams, next genkitai.ModelNext) (*genkitai.ModelResponse, error) {
				modelCtx, modelCancel := context.WithCancel(wCtx)
				defer modelCancel()

				var timerMu sync.Mutex
				var firstTokenReceived bool
				var modelTimedOut bool

				timer := time.AfterFunc(c.firstTokenTimeout, func() {
					timerMu.Lock()
					if !firstTokenReceived {
						modelTimedOut = true
						firstTokenTimedOut.Store(true)
						modelCancel()
					}
					timerMu.Unlock()
				})
				defer func() {
					timerMu.Lock()
					timer.Stop()
					timerMu.Unlock()
				}()

				origCallback := params.Callback
				params.Callback = func(cbCtx context.Context, chunk *genkitai.ModelResponseChunk) error {
					timerMu.Lock()
					if !firstTokenReceived {
						hasContent := (chunk != nil) && (chunk.Text() != "" || chunk.Reasoning() != "")
						if !hasContent && chunk != nil {
							for _, p := range chunk.Content {
								if p != nil && (p.IsText() || p.IsToolRequest() || p.IsReasoning()) {
									hasContent = true
									break
								}
							}
						}
						if hasContent {
							firstTokenReceived = true
							timer.Stop()
						}
					}
					timerMu.Unlock()

					if origCallback != nil {
						return origCallback(cbCtx, chunk)
					}
					return nil
				}

				resp, err := next(modelCtx, params)
				if err != nil {
					timerMu.Lock()
					timedOut := modelTimedOut
					timerMu.Unlock()
					if timedOut {
						return nil, fmt.Errorf("%w: first token timeout (%v)", ai.ErrFirstTokenTimeout, c.firstTokenTimeout)
					}
					return nil, err
				}
				return resp, nil
			},
		}, nil
	})

	streamCallback := func(cbCtx context.Context, chunk *genkitai.ModelResponseChunk) error {
		if chunk == nil {
			return nil
		}
		if chunk.Role != "" && chunk.Role != genkitai.RoleModel {
			return nil
		}
		text := chunk.Text()
		if text == "" {
			return nil
		}
		if callback != nil {
			return callback(ctx, ai.LLMChunk{
				Text:      text,
				Iteration: int(currentIteration.Load()),
			})
		}
		return nil
	}

	overallCtx, overallCancel := context.WithTimeout(ctx, c.overallTimeout)
	defer overallCancel()

	maxTurns := request.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 8
	}

	chatCfg := &dashscope.ChatConfig{
		EnableThinking: boolPtr(false),
	}

	resp, err := genkit.Generate(
		overallCtx,
		c.genkit,
		genkitai.WithModel(dashscope.ModelRef(c.model, chatCfg)),
		genkitai.WithMessages(genkitMessages...),
		genkitai.WithTools(genkitTools...),
		genkitai.WithMaxTurns(maxTurns),
		genkitai.WithUse(timeoutMiddleware),
		genkitai.WithStreaming(streamCallback),
	)

	if err != nil {
		if errors.Is(err, ai.ErrFirstTokenTimeout) || firstTokenTimedOut.Load() {
			return "", fmt.Errorf("%w (%v): %w", ai.ErrFirstTokenTimeout, c.firstTokenTimeout, err)
		}
		if errors.Is(err, genkitai.ErrMaxTurnsExceeded) {
			return "", fmt.Errorf("%w: %w", ai.ErrMaxTurnsExceeded, err)
		}
		if errors.Is(overallCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w (%v): %w", ai.ErrOverallTimeout, c.overallTimeout, overallCtx.Err())
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("dashscope generate: %w", err)
	}

	if resp == nil {
		return "", errors.New("empty response from dashscope")
	}

	return resp.Text(), nil
}
