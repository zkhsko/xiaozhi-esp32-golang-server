package dashscope

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	genkitai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai/dashscope"
	"github.com/openai/openai-go/option"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func boolPtr(b bool) *bool {
	return &b
}

// LLMClient 实现基于 Genkit DashScope 插件的大语言模型客户端。
type LLMClient struct {
	model             string
	firstTokenTimeout time.Duration
	overallTimeout    time.Duration
	genkit            *genkit.Genkit
	transport         *http.Transport
}

var _ ai.ManagedLLMClient = (*LLMClient)(nil)

// NewLLMClient 基于运行时配置构造 DashScope LLM 客户端实例。
func NewLLMClient(opts ai.LLMOptions) (*LLMClient, error) {
	opts = opts.Normalized()
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if opts.APIKey == "" {
		return nil, errors.New("dashscope api key is required")
	}

	pluginOpts := []option.RequestOption{
		option.WithBaseURL(opts.Endpoint),
		option.WithMaxRetries(0),
	}

	var transport *http.Transport
	if opts.ProxyURL != "" {
		proxyURL, err := url.Parse(opts.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("default http transport has unexpected type")
		}
		transport = defaultTransport.Clone()
		transport.Proxy = http.ProxyURL(proxyURL)
		pluginOpts = append(pluginOpts, option.WithHTTPClient(&http.Client{Transport: transport}))
	}

	plugin := &dashscope.DashScope{
		APIKey: opts.APIKey,
		Opts:   pluginOpts,
	}
	g := genkit.Init(context.Background(), genkit.WithPlugins(plugin))

	return &LLMClient{
		model:             opts.Model,
		firstTokenTimeout: opts.FirstTokenTimeout,
		overallTimeout:    opts.OverallTimeout,
		genkit:            g,
		transport:         transport,
	}, nil
}

// Close 释放当前客户端拥有的 HTTP 空闲连接资源。
func (c *LLMClient) Close() error {
	if c != nil && c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}

// Generate 基于上下文、请求与流式增量通道执行完整的模型生成与工具调用循环。
func (c *LLMClient) Generate(
	ctx context.Context,
	request ai.LLMRequest,
	chunks chan<- ai.LLMChunk,
) (ai.LLMResult, error) {
	if ctx == nil {
		return ai.LLMResult{}, errors.New("context cannot be nil")
	}
	if len(request.Messages) == 0 {
		return ai.LLMResult{}, errors.New("messages cannot be empty")
	}

	genkitMessages, err := toGenkitMessages(request.Messages)
	if err != nil {
		return ai.LLMResult{}, err
	}
	genkitTools, err := toGenkitTools(request.Tools)
	if err != nil {
		return ai.LLMResult{}, err
	}

	overallCtx, overallCancel := context.WithTimeout(ctx, c.overallTimeout)
	defer overallCancel()

	observer := newGenerationObserver()
	maxTurns := request.MaxTurns
	if maxTurns <= 0 {
		maxTurns = ai.DefaultLLMMaxTurns
	}

	resp, err := genkit.Generate(
		overallCtx,
		c.genkit,
		genkitai.WithModel(dashscope.ModelRef(c.model, &dashscope.ChatConfig{EnableThinking: boolPtr(false)})),
		genkitai.WithMessages(genkitMessages...),
		genkitai.WithTools(genkitTools...),
		genkitai.WithMaxTurns(maxTurns),
		genkitai.WithUse(observer.timeoutMiddleware(c.firstTokenTimeout)),
		genkitai.WithStreaming(observer.streamCallback(overallCtx, chunks)),
	)
	if err != nil {
		return ai.LLMResult{}, c.normalizeGenerateError(ctx, overallCtx, observer, err)
	}
	if resp == nil {
		return ai.LLMResult{}, errors.New("empty response from dashscope")
	}

	result, err := toLLMResult(resp, len(genkitMessages))
	if err != nil {
		return ai.LLMResult{}, fmt.Errorf("convert dashscope response: %w", err)
	}
	return result, nil
}

func (c *LLMClient) normalizeGenerateError(
	ctx context.Context,
	overallCtx context.Context,
	observer *generationObserver,
	err error,
) error {
	if errors.Is(err, ai.ErrFirstTokenTimeout) || observer.firstTokenTimeoutOccurred() {
		return fmt.Errorf("%w (%v): %w", ai.ErrFirstTokenTimeout, c.firstTokenTimeout, err)
	}
	if errors.Is(err, genkitai.ErrMaxTurnsExceeded) {
		return fmt.Errorf("%w: %w", ai.ErrMaxTurnsExceeded, err)
	}
	if errors.Is(overallCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w (%v): %w", ai.ErrOverallTimeout, c.overallTimeout, overallCtx.Err())
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("dashscope generate: %w", err)
}
