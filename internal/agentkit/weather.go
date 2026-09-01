package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// 常量定义：工具名、心知天气默认地址、语言、单位、超时与缓存。
const (
	// ToolGetCurrentWeather 获取实时天气工具名称常量。
	ToolGetCurrentWeather = "server.get_current_weather"

	// DefaultWeatherEndpoint 心知天气 v3 now 接口默认地址。
	DefaultWeatherEndpoint = "https://api.seniverse.com/v3/weather/now.json"

	// DefaultWeatherLanguage 心知天气固定语言参数（简体中文）。
	DefaultWeatherLanguage = "zh-Hans"

	// DefaultWeatherUnit 心知天气固定温度单位（摄氏度）。
	DefaultWeatherUnit = "c"

	// DefaultWeatherTimeout 天气 HTTP 请求超时时间。
	DefaultWeatherTimeout = 5 * time.Second

	// DefaultWeatherCacheTTL 成功天气结果内存缓存时长。
	DefaultWeatherCacheTTL = 5 * time.Minute

	// DefaultWeatherMaxResponseBytes 心知天气接口响应体大小上限（256 KiB）。
	DefaultWeatherMaxResponseBytes = 256 * 1024
)

// WeatherConfig 定义从 agentkit_config 表中读取的天气工具配置结构体。
type WeatherConfig struct {
	APIKey   string `json:"api_key"`
	Location string `json:"location,omitempty"` // 默认城市/地点代码
	Endpoint string `json:"endpoint,omitempty"` // 可选自定义接口地址
}

// GetCurrentWeatherInput 定义大模型调用获取实时天气工具的入参。
type GetCurrentWeatherInput struct {
	Location string `json:"location,omitempty" jsonschema:"description=要查询天气的城市名称、拼音或地点代码（可选，未指定时默认使用预设地点）"`
}

// WeatherLocationInfo 描述天气查询返回的地理位置信息。
type WeatherLocationInfo struct {
	Id             string `json:"id"`
	Name           string `json:"name"`
	Country        string `json:"country"`
	Path           string `json:"path"`
	Timezone       string `json:"timezone"`
	TimezoneOffset string `json:"timezone_offset"`
}

// WeatherNowInfo 描述实时天气现象和温度。
type WeatherNowInfo struct {
	Text        string `json:"text"`
	Code        string `json:"code"`
	Temperature string `json:"temperature"`
}

// GetCurrentWeatherOutput 定义获取实时天气工具的结构化返回值。
type GetCurrentWeatherOutput struct {
	Location   WeatherLocationInfo `json:"location"`
	Now        WeatherNowInfo      `json:"now"`
	LastUpdate string              `json:"last_update"`
}

// 心知天气 API 原始响应结构体。
type seniverseLocation struct {
	Id             string `json:"id"`
	Name           string `json:"name"`
	Country        string `json:"country"`
	Path           string `json:"path"`
	Timezone       string `json:"timezone"`
	TimezoneOffset string `json:"timezone_offset"`
}

type seniverseNow struct {
	Text        string `json:"text"`
	Code        string `json:"code"`
	Temperature string `json:"temperature"`
}

type seniverseResult struct {
	Location   seniverseLocation `json:"location"`
	Now        seniverseNow      `json:"now"`
	LastUpdate string            `json:"last_update"`
}

type seniverseAPIResponse struct {
	Results    []seniverseResult `json:"results,omitempty"`
	Status     string            `json:"status,omitempty"`
	StatusCode string            `json:"status_code,omitempty"`
}

// weatherCacheEntry 封装单条天气缓存项。
type weatherCacheEntry struct {
	output   *GetCurrentWeatherOutput
	expireAt time.Time
}

// WeatherClient 封装心知天气 API 请求与内存缓存。
type WeatherClient struct {
	httpClient      *http.Client
	apiKey          string
	defaultLocation string
	endpoint        string
	cacheTTL        time.Duration

	mu    sync.RWMutex
	cache map[string]weatherCacheEntry
}

// NewWeatherClient 使用配置与可选 HTTP 客户端创建 WeatherClient 实例。
func NewWeatherClient(cfg WeatherConfig, httpClient *http.Client) (*WeatherClient, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("seniverse api key cannot be empty")
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = DefaultWeatherEndpoint
	}

	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: DefaultWeatherTimeout,
		}
	}

	return &WeatherClient{
		httpClient:      httpClient,
		apiKey:          apiKey,
		defaultLocation: strings.TrimSpace(cfg.Location),
		endpoint:        endpoint,
		cacheTTL:        DefaultWeatherCacheTTL,
		cache:           make(map[string]weatherCacheEntry),
	}, nil
}

// FetchWeather 执行天气查询，支持内存缓存并保证 Key 脱敏。
func (c *WeatherClient) FetchWeather(ctx context.Context, location string) (*GetCurrentWeatherOutput, error) {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = c.defaultLocation
	}
	if loc == "" {
		return nil, errors.New("location is required for weather query")
	}

	cacheKey := strings.ToLower(loc)

	// 1. 检查缓存
	c.mu.RLock()
	entry, found := c.cache[cacheKey]
	c.mu.RUnlock()
	if found && time.Now().Before(entry.expireAt) {
		out := *entry.output
		return &out, nil
	}

	// 2. 构造请求 URL
	reqURL, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid weather endpoint: %w", err)
	}

	q := reqURL.Query()
	q.Set("key", c.apiKey)
	q.Set("location", loc)
	q.Set("language", DefaultWeatherLanguage)
	q.Set("unit", DefaultWeatherUnit)
	reqURL.RawQuery = q.Encode()

	reqCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, DefaultWeatherTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create weather request failed: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, sanitizeWeatherError(err, c.apiKey)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seniverse api returned status code %d", resp.StatusCode)
	}

	limitedReader := io.LimitReader(resp.Body, DefaultWeatherMaxResponseBytes+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("read weather response body: %w", err)
	}
	if len(bodyBytes) > DefaultWeatherMaxResponseBytes {
		return nil, errors.New("weather response exceeded maximum allowed size (256 KiB)")
	}

	var seniverseResp seniverseAPIResponse
	if err := json.Unmarshal(bodyBytes, &seniverseResp); err != nil {
		return nil, fmt.Errorf("decode weather response: %w", err)
	}

	if seniverseResp.Status != "" {
		return nil, fmt.Errorf("seniverse api error: %s (code: %s)", seniverseResp.Status, seniverseResp.StatusCode)
	}

	if len(seniverseResp.Results) == 0 {
		return nil, errors.New("seniverse api returned empty weather results")
	}

	first := seniverseResp.Results[0]
	out := &GetCurrentWeatherOutput{
		Location: WeatherLocationInfo{
			Id:             first.Location.Id,
			Name:           first.Location.Name,
			Country:        first.Location.Country,
			Path:           first.Location.Path,
			Timezone:       first.Location.Timezone,
			TimezoneOffset: first.Location.TimezoneOffset,
		},
		Now: WeatherNowInfo{
			Text:        first.Now.Text,
			Code:        first.Now.Code,
			Temperature: first.Now.Temperature,
		},
		LastUpdate: first.LastUpdate,
	}

	// 3. 写入缓存
	c.mu.Lock()
	c.cache[cacheKey] = weatherCacheEntry{
		output:   out,
		expireAt: time.Now().Add(c.cacheTTL),
	}
	c.mu.Unlock()

	outCopy := *out
	return &outCopy, nil
}

// sanitizeWeatherError 抹除错误字符串中包含的敏感 API Key。
func sanitizeWeatherError(err error, apiKey string) error {
	if err == nil {
		return nil
	}
	if apiKey == "" {
		return err
	}
	errMsg := strings.ReplaceAll(err.Error(), apiKey, "******")
	return errors.New(errMsg)
}

// ParseCurrentWeatherInput 将入参解析为 GetCurrentWeatherInput。
func ParseCurrentWeatherInput(input any) GetCurrentWeatherInput {
	var in GetCurrentWeatherInput
	if input == nil {
		return in
	}
	switch v := input.(type) {
	case GetCurrentWeatherInput:
		return v
	case *GetCurrentWeatherInput:
		if v != nil {
			return *v
		}
	case map[string]any:
		if loc, ok := v["location"].(string); ok {
			in.Location = loc
		}
	case string:
		if v != "" {
			_ = json.Unmarshal([]byte(v), &in)
		}
	}
	return in
}

// GetWeatherTool 基于已初始化的 WeatherClient 构造 ai.Tool 定义。
func GetWeatherTool(client *WeatherClient) ai.Tool {
	return ai.Tool{
		Name:        ToolGetCurrentWeather,
		Description: "获取指定地点或预设地点的实时天气情况（包括天气现象、温度和更新时间）。当用户询问天气、气温、下雨没、冷不冷等天气相关问题时调用此工具",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{
					"type":        "string",
					"description": "要查询天气的城市名称、拼音或城市ID（可选，不填则查询默认地点）",
				},
			},
		},
		Run: func(ctx context.Context, input any) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if client == nil {
				return nil, errors.New("weather client is not configured")
			}
			in := ParseCurrentWeatherInput(input)
			return client.FetchWeather(ctx, in.Location)
		},
	}
}

// NewWeatherTool 使用 WeatherConfig 构造天气查询工具。
func NewWeatherTool(cfg WeatherConfig) (ai.Tool, error) {
	client, err := NewWeatherClient(cfg, nil)
	if err != nil {
		return ai.Tool{}, err
	}
	return GetWeatherTool(client), nil
}

// NewWeatherToolFromConfig 从 JSON 配置字符串构造天气查询工具。
func NewWeatherToolFromConfig(toolConfigJSON string) (ai.Tool, error) {
	var cfg WeatherConfig
	if err := json.Unmarshal([]byte(toolConfigJSON), &cfg); err != nil {
		return ai.Tool{}, fmt.Errorf("unmarshal weather tool config: %w", err)
	}
	return NewWeatherTool(cfg)
}
