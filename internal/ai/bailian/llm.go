package bailian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
)

// LLMClient 实现基于百炼 OpenAI 兼容流式协议的大语言模型客户端。
type LLMClient struct {
	endpoint          string
	apiKey            string
	model             string
	firstTokenTimeout time.Duration
	overallTimeout    time.Duration
	client            openai.Client
}

// NewLLMClient 基于服务端配置构造百炼 LLM 客户端实例。
func NewLLMClient(cfg *config.Config, opts ...option.RequestOption) (*LLMClient, error) {
	if cfg == nil {
		return nil, errors.New("config cannot be nil")
	}
	if cfg.DashScopeAPIKey == "" {
		return nil, errors.New("dashscope api key is required")
	}
	if cfg.AI.Bailian.LLMEndpoint == "" {
		return nil, errors.New("bailian llm endpoint is required")
	}
	if cfg.AI.Bailian.LLMModel == "" {
		return nil, errors.New("bailian llm model is required")
	}

	firstTokenTimeout := cfg.AI.Bailian.LLMFirstTokenTimeout
	if firstTokenTimeout <= 0 {
		firstTokenTimeout = 15 * time.Second
	}

	overallTimeout := cfg.AI.Bailian.LLMOverallTimeout
	if overallTimeout <= 0 {
		overallTimeout = 60 * time.Second
	}

	if overallTimeout <= firstTokenTimeout {
		return nil, fmt.Errorf("llm overall timeout (%v) must be greater than first token timeout (%v)", overallTimeout, firstTokenTimeout)
	}

	var httpClient *http.Client
	if cfg.Proxy.Enabled && cfg.Proxy.URL != "" {
		proxyURL, err := url.Parse(cfg.Proxy.URL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
	}

	clientOpts := []option.RequestOption{
		option.WithBaseURL(cfg.AI.Bailian.LLMEndpoint),
		option.WithAPIKey(cfg.DashScopeAPIKey),
		option.WithMaxRetries(0),
	}
	if httpClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(httpClient))
	}
	clientOpts = append(clientOpts, opts...)

	client := openai.NewClient(clientOpts...)

	return &LLMClient{
		endpoint:          cfg.AI.Bailian.LLMEndpoint,
		apiKey:            cfg.DashScopeAPIKey,
		model:             cfg.AI.Bailian.LLMModel,
		firstTokenTimeout: firstTokenTimeout,
		overallTimeout:    overallTimeout,
		client:            client,
	}, nil
}

// CreateStream 创建一条新的流式回答会话。
func (c *LLMClient) CreateStream(ctx context.Context, messages []ai.Message) (ai.LLMStream, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if len(messages) == 0 {
		return nil, errors.New("messages cannot be empty")
	}

	openAIMessages := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case ai.RoleSystem:
			openAIMessages = append(openAIMessages, openai.SystemMessage(msg.Content))
		case ai.RoleUser:
			openAIMessages = append(openAIMessages, openai.UserMessage(msg.Content))
		case ai.RoleAssistant:
			openAIMessages = append(openAIMessages, openai.AssistantMessage(msg.Content))
		default:
			openAIMessages = append(openAIMessages, openai.UserMessage(msg.Content))
		}
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.model),
		Messages: openAIMessages,
	}

	overallCtx, overallCancel := context.WithTimeout(ctx, c.overallTimeout)
	streamCtx, streamCancel := context.WithCancel(overallCtx)

	streamState := &bailianLLMStream{
		firstTokenTimeout: c.firstTokenTimeout,
		overallTimeout:    c.overallTimeout,
		parentCtx:         ctx,
		overallCtx:        overallCtx,
		overallCancel:     overallCancel,
		streamCancel:      streamCancel,
	}

	streamState.firstTimer = time.AfterFunc(c.firstTokenTimeout, func() {
		if streamState.firstTokenReceived.CompareAndSwap(false, true) {
			streamState.firstTokenTimedOut.Store(true)
			streamCancel()
		}
	})

	reqOpts := []option.RequestOption{
		option.WithJSONSet("enable_thinking", false),
		option.WithMaxRetries(0),
	}

	stream := c.client.Chat.Completions.NewStreaming(streamCtx, params, reqOpts...)
	if err := stream.Err(); err != nil {
		streamState.firstTimer.Stop()
		streamCancel()
		overallCancel()
		return nil, fmt.Errorf("create llm stream: %w", err)
	}

	streamState.stream = stream
	return streamState, nil
}

type bailianLLMStream struct {
	firstTokenTimeout  time.Duration
	overallTimeout     time.Duration
	parentCtx          context.Context
	overallCtx         context.Context
	overallCancel      context.CancelFunc
	streamCancel       context.CancelFunc
	firstTimer         *time.Timer
	firstTokenReceived atomic.Bool
	firstTokenTimedOut atomic.Bool

	mu     sync.Mutex
	stream *ssestream.Stream[openai.ChatCompletionChunk]
	closed bool
}

func (s *bailianLLMStream) Recv() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", io.EOF
	}

	for s.stream.Next() {
		chunk := s.stream.Current()
		if len(chunk.Choices) == 0 {
			continue
		}

		deltaContent := chunk.Choices[0].Delta.Content
		if deltaContent == "" {
			continue
		}

		if s.firstTokenReceived.CompareAndSwap(false, true) {
			s.firstTimer.Stop()
		}

		return deltaContent, nil
	}

	s.firstTimer.Stop()

	if err := s.stream.Err(); err != nil {
		if s.firstTokenTimedOut.Load() {
			return "", fmt.Errorf("llm first token timeout (%v): %w", s.firstTokenTimeout, context.DeadlineExceeded)
		}
		if errors.Is(s.overallCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("llm overall timeout (%v): %w", s.overallTimeout, context.DeadlineExceeded)
		}
		if errors.Is(s.parentCtx.Err(), context.Canceled) {
			return "", s.parentCtx.Err()
		}
		return "", fmt.Errorf("llm stream read: %w", err)
	}

	return "", io.EOF
}

func (s *bailianLLMStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	if s.firstTimer != nil {
		s.firstTimer.Stop()
	}
	s.streamCancel()
	s.overallCancel()

	if s.stream != nil {
		return s.stream.Close()
	}
	return nil
}
