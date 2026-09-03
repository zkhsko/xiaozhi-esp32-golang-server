package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// 常量定义：天气预报工具名、心知天气 daily 接口默认地址与默认参数。
const (
	// ToolGetWeatherForecast 获取天气预报工具名称常量。
	ToolGetWeatherForecast = "server.get_weather_forecast"

	// DefaultWeatherForecastEndpoint 心知天气 v3 daily 接口默认地址。
	DefaultWeatherForecastEndpoint = "https://api.seniverse.com/v3/weather/daily.json"

	// DefaultWeatherForecastStart 默认预报起始偏移天数（0 表示今天）。
	DefaultWeatherForecastStart = 0

	// DefaultWeatherForecastDays 默认预报天数（默认 3 天）。
	DefaultWeatherForecastDays = 3
)

// WeatherForecastConfig 定义从 agentkit_config 表中读取的天气预报工具配置结构体。
type WeatherForecastConfig struct {
	APIKey   string `json:"api_key"`
	Location string `json:"location,omitempty"` // 默认城市/地点代码
	Endpoint string `json:"endpoint,omitempty"` // 可选自定义接口地址
	Start    int    `json:"start,omitempty"`    // 可选默认起始偏移天数
	Days     int    `json:"days,omitempty"`     // 可选默认预报天数
}

// GetWeatherForecastInput 定义大模型调用获取天气预报工具的入参。
type GetWeatherForecastInput struct {
	Location string `json:"location,omitempty" jsonschema:"description=要查询天气预报的城市名称、拼音或地点代码（可选，未指定时默认使用预设地点）"`
	Start    *int   `json:"start,omitempty" jsonschema:"description=预报起始天偏移（可选，0表示今天，1表示明天，2表示后天，默认0）"`
	Days     *int   `json:"days,omitempty" jsonschema:"description=查询预报天数（可选，1-15天，默认3天，用来获取最近15天的天气）"`
}

// WeatherDailyInfo 描述单日的天气预报详细信息。
type WeatherDailyInfo struct {
	Date                string `json:"date"`
	TextDay             string `json:"text_day"`
	CodeDay             string `json:"code_day"`
	TextNight           string `json:"text_night"`
	CodeNight           string `json:"code_night"`
	High                string `json:"high"`
	Low                 string `json:"low"`
	Rainfall            string `json:"rainfall,omitempty"`
	Precip              string `json:"precip,omitempty"`
	WindDirection       string `json:"wind_direction,omitempty"`
	WindDirectionDegree string `json:"wind_direction_degree,omitempty"`
	WindSpeed           string `json:"wind_speed,omitempty"`
	WindScale           string `json:"wind_scale,omitempty"`
	Humidity            string `json:"humidity,omitempty"`
}

// GetWeatherForecastOutput 定义获取天气预报工具的结构化返回值。
type GetWeatherForecastOutput struct {
	Location   WeatherLocationInfo `json:"location"`
	Daily      []WeatherDailyInfo  `json:"daily"`
	LastUpdate string              `json:"last_update"`
}

// 心知天气 API daily 原始响应结构体。
type seniverseDailyItem struct {
	Date                string `json:"date"`
	TextDay             string `json:"text_day"`
	CodeDay             string `json:"code_day"`
	TextNight           string `json:"text_night"`
	CodeNight           string `json:"code_night"`
	High                string `json:"high"`
	Low                 string `json:"low"`
	Rainfall            string `json:"rainfall"`
	Precip              string `json:"precip"`
	WindDirection       string `json:"wind_direction"`
	WindDirectionDegree string `json:"wind_direction_degree"`
	WindSpeed           string `json:"wind_speed"`
	WindScale           string `json:"wind_scale"`
	Humidity            string `json:"humidity"`
}

type seniverseDailyResult struct {
	Location   seniverseLocation    `json:"location"`
	Daily      []seniverseDailyItem `json:"daily"`
	LastUpdate string               `json:"last_update"`
}

type seniverseDailyAPIResponse struct {
	Results    []seniverseDailyResult `json:"results,omitempty"`
	Status     string                 `json:"status,omitempty"`
	StatusCode string                 `json:"status_code,omitempty"`
}

// weatherForecastCacheEntry 封装单条天气预报缓存项。
type weatherForecastCacheEntry struct {
	output   *GetWeatherForecastOutput
	expireAt time.Time
}

// WeatherForecastClient 封装心知天气预报 API 请求与内存缓存。
type WeatherForecastClient struct {
	httpClient      *http.Client
	apiKey          string
	defaultLocation string
	endpoint        string
	defaultStart    int
	defaultDays     int
	cacheTTL        time.Duration

	mu    sync.RWMutex
	cache map[string]weatherForecastCacheEntry
}

// NewWeatherForecastClient 使用配置与可选 HTTP 客户端创建 WeatherForecastClient 实例。
func NewWeatherForecastClient(cfg WeatherForecastConfig, httpClient *http.Client) (*WeatherForecastClient, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("seniverse api key cannot be empty")
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = DefaultWeatherForecastEndpoint
	}

	start := cfg.Start
	if start < 0 {
		start = DefaultWeatherForecastStart
	}

	days := cfg.Days
	if days <= 0 {
		days = DefaultWeatherForecastDays
	}

	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: DefaultWeatherTimeout,
		}
	}

	return &WeatherForecastClient{
		httpClient:      httpClient,
		apiKey:          apiKey,
		defaultLocation: strings.TrimSpace(cfg.Location),
		endpoint:        endpoint,
		defaultStart:    start,
		defaultDays:     days,
		cacheTTL:        DefaultWeatherCacheTTL,
		cache:           make(map[string]weatherForecastCacheEntry),
	}, nil
}

// FetchForecast 执行天气预报查询，支持内存缓存并保证 Key 脱敏。
func (c *WeatherForecastClient) FetchForecast(ctx context.Context, location string, start *int, days *int) (*GetWeatherForecastOutput, error) {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = c.defaultLocation
	}
	if loc == "" {
		return nil, errors.New("location is required for weather forecast query")
	}

	reqStart := c.defaultStart
	if start != nil && *start >= 0 {
		reqStart = *start
	}

	reqDays := c.defaultDays
	if days != nil && *days > 0 {
		reqDays = *days
	}
	if reqDays > 15 {
		reqDays = 15
	}

	cacheKey := fmt.Sprintf("%s:%d:%d", strings.ToLower(loc), reqStart, reqDays)

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
		return nil, fmt.Errorf("invalid weather forecast endpoint: %w", err)
	}

	q := reqURL.Query()
	q.Set("key", c.apiKey)
	q.Set("location", loc)
	q.Set("language", DefaultWeatherLanguage)
	q.Set("unit", DefaultWeatherUnit)
	q.Set("start", strconv.Itoa(reqStart))
	q.Set("days", strconv.Itoa(reqDays))
	reqURL.RawQuery = q.Encode()

	reqCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, DefaultWeatherTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create weather forecast request failed: %w", err)
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
		return nil, fmt.Errorf("read weather forecast response body: %w", err)
	}
	if len(bodyBytes) > DefaultWeatherMaxResponseBytes {
		return nil, errors.New("weather forecast response exceeded maximum allowed size (256 KiB)")
	}

	var seniverseResp seniverseDailyAPIResponse
	if err := json.Unmarshal(bodyBytes, &seniverseResp); err != nil {
		return nil, fmt.Errorf("decode weather forecast response: %w", err)
	}

	if seniverseResp.Status != "" {
		return nil, fmt.Errorf("seniverse api error: %s (code: %s)", seniverseResp.Status, seniverseResp.StatusCode)
	}

	if len(seniverseResp.Results) == 0 {
		return nil, errors.New("seniverse api returned empty weather forecast results")
	}

	first := seniverseResp.Results[0]
	dailyList := make([]WeatherDailyInfo, 0, len(first.Daily))
	for _, d := range first.Daily {
		dailyList = append(dailyList, WeatherDailyInfo{
			Date:                d.Date,
			TextDay:             d.TextDay,
			CodeDay:             d.CodeDay,
			TextNight:           d.TextNight,
			CodeNight:           d.CodeNight,
			High:                d.High,
			Low:                 d.Low,
			Rainfall:            d.Rainfall,
			Precip:              d.Precip,
			WindDirection:       d.WindDirection,
			WindDirectionDegree: d.WindDirectionDegree,
			WindSpeed:           d.WindSpeed,
			WindScale:           d.WindScale,
			Humidity:            d.Humidity,
		})
	}

	out := &GetWeatherForecastOutput{
		Location: WeatherLocationInfo{
			Id:             first.Location.Id,
			Name:           first.Location.Name,
			Country:        first.Location.Country,
			Path:           first.Location.Path,
			Timezone:       first.Location.Timezone,
			TimezoneOffset: first.Location.TimezoneOffset,
		},
		Daily:      dailyList,
		LastUpdate: first.LastUpdate,
	}

	// 3. 写入缓存
	c.mu.Lock()
	c.cache[cacheKey] = weatherForecastCacheEntry{
		output:   out,
		expireAt: time.Now().Add(c.cacheTTL),
	}
	c.mu.Unlock()

	outCopy := *out
	return &outCopy, nil
}

// parseOptionalInt 解析任意类型的值为可选 *int。
func parseOptionalInt(v any) *int {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case int:
		return &val
	case int32:
		i := int(val)
		return &i
	case int64:
		i := int(val)
		return &i
	case float64:
		i := int(val)
		return &i
	case float32:
		i := int(val)
		return &i
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return &parsed
		}
	}
	return nil
}

// ParseWeatherForecastInput 将入参解析为 GetWeatherForecastInput。
func ParseWeatherForecastInput(input any) GetWeatherForecastInput {
	var in GetWeatherForecastInput
	if input == nil {
		return in
	}
	switch v := input.(type) {
	case GetWeatherForecastInput:
		return v
	case *GetWeatherForecastInput:
		if v != nil {
			return *v
		}
	case map[string]any:
		if loc, ok := v["location"].(string); ok {
			in.Location = loc
		}
		if startVal, exists := v["start"]; exists {
			in.Start = parseOptionalInt(startVal)
		}
		if daysVal, exists := v["days"]; exists {
			in.Days = parseOptionalInt(daysVal)
		}
	case string:
		if v != "" {
			_ = json.Unmarshal([]byte(v), &in)
		}
	}
	return in
}

// GetWeatherForecastTool 基于已初始化的 WeatherForecastClient 构造 ai.Tool 定义。
func GetWeatherForecastTool(client *WeatherForecastClient) ai.Tool {
	return ai.Tool{
		Name:        ToolGetWeatherForecast,
		Description: "获取指定地点或预设地点最近15天的天气预报（包括每日白天与夜间天气现象、最高最低气温、风向风力、降水概率等）。用来获取最近15天的天气预报，包括今天的天气也用此工具查询。当用户询问今天天气如何、今天最高/最低气温、明天/后天天气、未来几天天气预报、近期15天天气趋势等问题时调用此工具",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{
					"type":        "string",
					"description": "要查询天气预报的城市名称、拼音或城市ID（可选，不填则查询默认地点）",
				},
				"days": map[string]any{
					"type":        "integer",
					"description": "预报天数（1-15天，可选，默认3天，用来获取最近15天的天气）",
				},
				"start": map[string]any{
					"type":        "integer",
					"description": "起始天偏移（0代表今天，1代表明天，2代表后天，可选，默认0）",
				},
			},
		},
		Run: func(ctx context.Context, input any) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if client == nil {
				return nil, errors.New("weather forecast client is not configured")
			}
			in := ParseWeatherForecastInput(input)
			return client.FetchForecast(ctx, in.Location, in.Start, in.Days)
		},
	}
}

// NewWeatherForecastTool 使用 WeatherForecastConfig 构造天气预报工具。
func NewWeatherForecastTool(cfg WeatherForecastConfig) (ai.Tool, error) {
	client, err := NewWeatherForecastClient(cfg, nil)
	if err != nil {
		return ai.Tool{}, err
	}
	return GetWeatherForecastTool(client), nil
}

// NewWeatherForecastToolFromConfig 从 JSON 配置字符串构造天气预报工具。
func NewWeatherForecastToolFromConfig(toolConfigJSON string) (ai.Tool, error) {
	var cfg WeatherForecastConfig
	if err := json.Unmarshal([]byte(toolConfigJSON), &cfg); err != nil {
		return ai.Tool{}, fmt.Errorf("unmarshal weather forecast tool config: %w", err)
	}
	return NewWeatherForecastTool(cfg)
}
