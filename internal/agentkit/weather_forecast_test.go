package agentkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewWeatherForecastClient_Validation(t *testing.T) {
	_, err := NewWeatherForecastClient(WeatherForecastConfig{}, nil)
	if err == nil {
		t.Fatal("expected error for empty api key, got nil")
	}

	client, err := NewWeatherForecastClient(WeatherForecastConfig{
		APIKey:   "valid-key",
		Location: "WX4SUCU47R3T",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.endpoint != DefaultWeatherForecastEndpoint {
		t.Fatalf("expected default endpoint, got %s", client.endpoint)
	}
	if client.defaultLocation != "WX4SUCU47R3T" {
		t.Fatalf("expected location WX4SUCU47R3T, got %s", client.defaultLocation)
	}
	if client.defaultStart != DefaultWeatherForecastStart {
		t.Fatalf("expected default start %d, got %d", DefaultWeatherForecastStart, client.defaultStart)
	}
	if client.defaultDays != DefaultWeatherForecastDays {
		t.Fatalf("expected default days %d, got %d", DefaultWeatherForecastDays, client.defaultDays)
	}

	// 自定义参数
	customClient, err := NewWeatherForecastClient(WeatherForecastConfig{
		APIKey:   "valid-key",
		Location: "beijing",
		Endpoint: "https://custom.weather/api",
		Start:    1,
		Days:     7,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error for custom config: %v", err)
	}
	if customClient.endpoint != "https://custom.weather/api" {
		t.Fatalf("expected custom endpoint, got %s", customClient.endpoint)
	}
	if customClient.defaultStart != 1 || customClient.defaultDays != 7 {
		t.Fatalf("expected custom start=1 days=7, got start=%d days=%d", customClient.defaultStart, customClient.defaultDays)
	}
}

func TestParseWeatherForecastInput(t *testing.T) {
	start0 := 0
	days3 := 3

	t.Run("Struct", func(t *testing.T) {
		in := ParseWeatherForecastInput(GetWeatherForecastInput{
			Location: "beijing",
			Start:    &start0,
			Days:     &days3,
		})
		if in.Location != "beijing" || in.Start == nil || *in.Start != 0 || in.Days == nil || *in.Days != 3 {
			t.Fatalf("unexpected parsed input: %+v", in)
		}
	})

	t.Run("Pointer", func(t *testing.T) {
		in := ParseWeatherForecastInput(&GetWeatherForecastInput{
			Location: "shanghai",
			Start:    &start0,
			Days:     &days3,
		})
		if in.Location != "shanghai" || *in.Start != 0 || *in.Days != 3 {
			t.Fatalf("unexpected parsed pointer input: %+v", in)
		}
	})

	t.Run("NilPointer", func(t *testing.T) {
		var p *GetWeatherForecastInput
		in := ParseWeatherForecastInput(p)
		if in.Location != "" || in.Start != nil || in.Days != nil {
			t.Fatalf("expected empty, got %+v", in)
		}
	})

	t.Run("Map_VariousTypes", func(t *testing.T) {
		in := ParseWeatherForecastInput(map[string]any{
			"location": "guangzhou",
			"start":    float64(1),
			"days":     "5",
		})
		if in.Location != "guangzhou" {
			t.Fatalf("expected 'guangzhou', got %q", in.Location)
		}
		if in.Start == nil || *in.Start != 1 {
			t.Fatalf("expected start=1, got %v", in.Start)
		}
		if in.Days == nil || *in.Days != 5 {
			t.Fatalf("expected days=5, got %v", in.Days)
		}
	})

	t.Run("JSONString", func(t *testing.T) {
		in := ParseWeatherForecastInput(`{"location":"shenzhen","days":7,"start":2}`)
		if in.Location != "shenzhen" || in.Days == nil || *in.Days != 7 || in.Start == nil || *in.Start != 2 {
			t.Fatalf("unexpected json string parse: %+v", in)
		}
	})

	t.Run("Nil", func(t *testing.T) {
		in := ParseWeatherForecastInput(nil)
		if in.Location != "" || in.Start != nil || in.Days != nil {
			t.Fatalf("expected empty, got %+v", in)
		}
	})
}

func TestWeatherForecastClient_FetchForecast_SuccessAndCache(t *testing.T) {
	mockResponse := `{
		"results": [
			{
				"location": {
					"id": "WX4SUCU47R3T",
					"name": "昌平",
					"country": "CN",
					"path": "昌平,北京,中国",
					"timezone": "Asia/Shanghai",
					"timezone_offset": "+08:00"
				},
				"daily": [
					{
						"date": "2026-09-01",
						"text_day": "晴",
						"code_day": "0",
						"text_night": "晴",
						"code_night": "1",
						"high": "30",
						"low": "16",
						"rainfall": "0.00",
						"precip": "0.00",
						"wind_direction": "东南",
						"wind_direction_degree": "135",
						"wind_speed": "8.4",
						"wind_scale": "2",
						"humidity": "67"
					},
					{
						"date": "2026-09-02",
						"text_day": "多云",
						"code_day": "4",
						"text_night": "多云",
						"code_night": "4",
						"high": "28",
						"low": "18",
						"rainfall": "0.00",
						"precip": "0.00",
						"wind_direction": "东南",
						"wind_direction_degree": "135",
						"wind_speed": "3.0",
						"wind_scale": "1",
						"humidity": "80"
					}
				],
				"last_update": "2026-09-01T08:00:00+08:00"
			}
		]
	}`

	var reqCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)

		q := r.URL.Query()
		if q.Get("key") != "secret-test-key" {
			http.Error(w, "invalid key", http.StatusUnauthorized)
			return
		}
		if q.Get("location") != "WX4SUCU47R3T" {
			http.Error(w, "invalid location", http.StatusBadRequest)
			return
		}
		if q.Get("language") != "zh-Hans" {
			http.Error(w, "invalid language", http.StatusBadRequest)
			return
		}
		if q.Get("unit") != "c" {
			http.Error(w, "invalid unit", http.StatusBadRequest)
			return
		}
		if q.Get("start") != "0" {
			http.Error(w, "invalid start", http.StatusBadRequest)
			return
		}
		if q.Get("days") != "3" {
			http.Error(w, "invalid days", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockResponse))
	}))
	defer server.Close()

	client, err := NewWeatherForecastClient(WeatherForecastConfig{
		APIKey:   "secret-test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherForecastClient failed: %v", err)
	}

	ctx := context.Background()

	// 1. 首次查询：触发 HTTP 请求
	out1, err := client.FetchForecast(ctx, "", nil, nil)
	if err != nil {
		t.Fatalf("first FetchForecast failed: %v", err)
	}
	if out1.Location.Name != "昌平" || len(out1.Daily) != 2 {
		t.Fatalf("unexpected output: %+v", out1)
	}
	if out1.Daily[0].TextDay != "晴" || out1.Daily[0].High != "30" || out1.Daily[0].Low != "16" || out1.Daily[0].DayOfWeek != "星期二" {
		t.Fatalf("unexpected daily[0] data: %+v", out1.Daily[0])
	}
	if out1.Daily[1].TextDay != "多云" || out1.Daily[1].Humidity != "80" || out1.Daily[1].DayOfWeek != "星期三" {
		t.Fatalf("unexpected daily[1] data: %+v", out1.Daily[1])
	}
	if reqCount.Load() != 1 {
		t.Fatalf("expected 1 HTTP request, got %d", reqCount.Load())
	}

	// 2. 第二次查询相同地点与参数：应直接从缓存返回，不产生新的 HTTP 请求
	out2, err := client.FetchForecast(ctx, "WX4SUCU47R3T", nil, nil)
	if err != nil {
		t.Fatalf("second FetchForecast failed: %v", err)
	}
	if out2.Location.Name != "昌平" || len(out2.Daily) != 2 {
		t.Fatalf("unexpected cache output: %+v", out2)
	}
	if reqCount.Load() != 1 {
		t.Fatalf("expected cached request count to stay 1, got %d", reqCount.Load())
	}
}

func TestWeatherForecastClient_FetchForecast_CustomLocationAndParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := r.URL.Query().Get("location")
		start := r.URL.Query().Get("start")
		days := r.URL.Query().Get("days")

		if start != "1" || days != "15" {
			http.Error(w, "unexpected start or days", http.StatusBadRequest)
			return
		}

		resp := `{
			"results": [
				{
					"location": {
						"id": "101020100",
						"name": "` + loc + `",
						"country": "CN",
						"path": "` + loc + `,上海,中国",
						"timezone": "Asia/Shanghai",
						"timezone_offset": "+08:00"
					},
					"daily": [
						{
							"date": "2026-09-02",
							"text_day": "小雨",
							"code_day": "13",
							"text_night": "阴",
							"code_night": "9",
							"high": "26",
							"low": "20",
							"rainfall": "5.0",
							"precip": "0.9",
							"wind_direction": "南",
							"wind_direction_degree": "180",
							"wind_speed": "12.0",
							"wind_scale": "3",
							"humidity": "90"
						}
					],
					"last_update": "2026-09-01T08:00:00+08:00"
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client, err := NewWeatherForecastClient(WeatherForecastConfig{
		APIKey:   "test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherForecastClient failed: %v", err)
	}

	start := 1
	days := 20 // 超过 15，应被限制为 15
	out, err := client.FetchForecast(context.Background(), "上海", &start, &days)
	if err != nil {
		t.Fatalf("FetchForecast failed: %v", err)
	}
	if out.Location.Name != "上海" || len(out.Daily) != 1 || out.Daily[0].TextDay != "小雨" || out.Daily[0].DayOfWeek != "星期三" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestWeatherForecastClient_FetchForecast_SanitizeKeyInError(t *testing.T) {
	secretKey := "super-secret-key-12345"

	client, err := NewWeatherForecastClient(WeatherForecastConfig{
		APIKey:   secretKey,
		Location: "WX4SUCU47R3T",
		Endpoint: "http://127.0.0.1:54321/daily.json",
	}, nil)
	if err != nil {
		t.Fatalf("NewWeatherForecastClient failed: %v", err)
	}

	_, err = client.FetchForecast(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}

	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("error message contains sensitive api key: %v", err)
	}
}

func TestWeatherForecastClient_FetchForecast_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"Invalid key.","status_code":"AP010001"}`))
	}))
	defer server.Close()

	client, err := NewWeatherForecastClient(WeatherForecastConfig{
		APIKey:   "bad-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherForecastClient failed: %v", err)
	}

	_, err = client.FetchForecast(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error for API error response, got nil")
	}
	if !strings.Contains(err.Error(), "AP010001") {
		t.Fatalf("expected AP010001 in error, got: %v", err)
	}
}

func TestWeatherForecastClient_FetchForecast_ResponseSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("a", 300*1024)))
	}))
	defer server.Close()

	client, err := NewWeatherForecastClient(WeatherForecastConfig{
		APIKey:   "test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherForecastClient failed: %v", err)
	}

	_, err = client.FetchForecast(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error for oversized response, got nil")
	}
}

func TestNewWeatherForecastToolFromConfig(t *testing.T) {
	configJSON := `{"api_key":"my-api-key","location":"WX4SUCU47R3T","days":5}`
	tool, err := NewWeatherForecastToolFromConfig(configJSON)
	if err != nil {
		t.Fatalf("NewWeatherForecastToolFromConfig failed: %v", err)
	}
	if tool.Name != ToolGetWeatherForecast {
		t.Fatalf("expected tool name %s, got %s", ToolGetWeatherForecast, tool.Name)
	}
	if tool.Description == "" || tool.Parameters == nil || tool.Run == nil {
		t.Fatal("expected tool properties to be properly initialized")
	}

	_, err = NewWeatherForecastToolFromConfig("invalid-json")
	if err == nil {
		t.Fatal("expected error for invalid json, got nil")
	}
}

func TestGetWeatherForecastTool_DirectRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{
			"results": [
				{
					"location": {
						"id": "WX4SUCU47R3T",
						"name": "北京",
						"country": "CN",
						"path": "北京,北京,中国",
						"timezone": "Asia/Shanghai",
						"timezone_offset": "+08:00"
					},
					"daily": [
						{
							"date": "2026-09-01",
							"text_day": "晴",
							"code_day": "0",
							"text_night": "晴",
							"code_night": "1",
							"high": "30",
							"low": "18"
						}
					],
					"last_update": "2026-09-01T08:00:00+08:00"
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	tool, err := NewWeatherForecastTool(WeatherForecastConfig{
		APIKey:   "test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("NewWeatherForecastTool failed: %v", err)
	}

	ctx := context.Background()
	res, err := tool.Run(ctx, map[string]any{"location": "北京", "days": 1})
	if err != nil {
		t.Fatalf("tool.Run failed: %v", err)
	}
	out, ok := res.(*GetWeatherForecastOutput)
	if !ok || len(out.Daily) != 1 || out.Daily[0].TextDay != "晴" || out.Daily[0].High != "30" || out.Daily[0].DayOfWeek != "星期二" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// 测试 Context 取消
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tool.Run(canceledCtx, nil)
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
}

func TestParseDayOfWeek(t *testing.T) {
	tests := []struct {
		date     string
		expected string
	}{
		{"2026-08-30", "星期日"},
		{"2026-08-31", "星期一"},
		{"2026-09-01", "星期二"},
		{"2026-09-02", "星期三"},
		{"2026-09-03", "星期四"},
		{"2026-09-04", "星期五"},
		{"2026-09-05", "星期六"},
		{"2026-09-06", "星期日"},
		{"", ""},
		{"   ", ""},
		{"invalid-date", ""},
		{"2026/09/01", ""},
	}

	for _, tt := range tests {
		t.Run(tt.date, func(t *testing.T) {
			got := parseDayOfWeek(tt.date)
			if got != tt.expected {
				t.Fatalf("parseDayOfWeek(%q) = %q, want %q", tt.date, got, tt.expected)
			}
		})
	}
}
