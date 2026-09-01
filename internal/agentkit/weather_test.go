package agentkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewWeatherClient_Validation(t *testing.T) {
	_, err := NewWeatherClient(WeatherConfig{}, nil)
	if err == nil {
		t.Fatal("expected error for empty api key, got nil")
	}

	client, err := NewWeatherClient(WeatherConfig{
		APIKey:   "valid-key",
		Location: "WX4SUCU47R3T",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.endpoint != DefaultWeatherEndpoint {
		t.Fatalf("expected default endpoint, got %s", client.endpoint)
	}
	if client.defaultLocation != "WX4SUCU47R3T" {
		t.Fatalf("expected location WX4SUCU47R3T, got %s", client.defaultLocation)
	}
}

func TestParseCurrentWeatherInput(t *testing.T) {
	t.Run("Struct", func(t *testing.T) {
		in := ParseCurrentWeatherInput(GetCurrentWeatherInput{Location: "beijing"})
		if in.Location != "beijing" {
			t.Fatalf("expected 'beijing', got %q", in.Location)
		}
	})

	t.Run("Pointer", func(t *testing.T) {
		in := ParseCurrentWeatherInput(&GetCurrentWeatherInput{Location: "shanghai"})
		if in.Location != "shanghai" {
			t.Fatalf("expected 'shanghai', got %q", in.Location)
		}
	})

	t.Run("NilPointer", func(t *testing.T) {
		var p *GetCurrentWeatherInput
		in := ParseCurrentWeatherInput(p)
		if in.Location != "" {
			t.Fatalf("expected empty, got %q", in.Location)
		}
	})

	t.Run("Map", func(t *testing.T) {
		in := ParseCurrentWeatherInput(map[string]any{"location": "guangzhou"})
		if in.Location != "guangzhou" {
			t.Fatalf("expected 'guangzhou', got %q", in.Location)
		}
	})

	t.Run("JSONString", func(t *testing.T) {
		in := ParseCurrentWeatherInput(`{"location":"shenzhen"}`)
		if in.Location != "shenzhen" {
			t.Fatalf("expected 'shenzhen', got %q", in.Location)
		}
	})

	t.Run("Nil", func(t *testing.T) {
		in := ParseCurrentWeatherInput(nil)
		if in.Location != "" {
			t.Fatalf("expected empty, got %q", in.Location)
		}
	})
}

func TestWeatherClient_FetchWeather_SuccessAndCache(t *testing.T) {
	mockResponse := `{
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
				"now": {
					"text": "晴",
					"code": "0",
					"temperature": "22"
				},
				"last_update": "2025-01-01T12:00:00+08:00"
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

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockResponse))
	}))
	defer server.Close()

	client, err := NewWeatherClient(WeatherConfig{
		APIKey:   "secret-test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherClient failed: %v", err)
	}

	ctx := context.Background()

	// 1. 首次查询：触发 HTTP 请求
	out1, err := client.FetchWeather(ctx, "")
	if err != nil {
		t.Fatalf("first FetchWeather failed: %v", err)
	}
	if out1.Location.Name != "北京" || out1.Now.Text != "晴" || out1.Now.Temperature != "22" {
		t.Fatalf("unexpected output: %+v", out1)
	}
	if reqCount.Load() != 1 {
		t.Fatalf("expected 1 HTTP request, got %d", reqCount.Load())
	}

	// 2. 第二次查询相同地点：应直接从缓存返回，不产生新的 HTTP 请求
	out2, err := client.FetchWeather(ctx, "WX4SUCU47R3T")
	if err != nil {
		t.Fatalf("second FetchWeather failed: %v", err)
	}
	if out2.Location.Name != "北京" || out2.Now.Temperature != "22" {
		t.Fatalf("unexpected cache output: %+v", out2)
	}
	if reqCount.Load() != 1 {
		t.Fatalf("expected cached request count to stay 1, got %d", reqCount.Load())
	}
}

func TestWeatherClient_FetchWeather_CustomLocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := r.URL.Query().Get("location")
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
					"now": {
						"text": "多云",
						"code": "4",
						"temperature": "18"
					},
					"last_update": "2025-01-01T12:00:00+08:00"
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client, err := NewWeatherClient(WeatherConfig{
		APIKey:   "test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherClient failed: %v", err)
	}

	out, err := client.FetchWeather(context.Background(), "上海")
	if err != nil {
		t.Fatalf("FetchWeather failed: %v", err)
	}
	if out.Location.Name != "上海" || out.Now.Text != "多云" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestWeatherClient_FetchWeather_SanitizeKeyInError(t *testing.T) {
	secretKey := "super-secret-key-12345"

	// 指向一个关闭的连接
	client, err := NewWeatherClient(WeatherConfig{
		APIKey:   secretKey,
		Location: "WX4SUCU47R3T",
		Endpoint: "http://127.0.0.1:54321/now.json",
	}, nil)
	if err != nil {
		t.Fatalf("NewWeatherClient failed: %v", err)
	}

	_, err = client.FetchWeather(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}

	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("error message contains sensitive api key: %v", err)
	}
}

func TestWeatherClient_FetchWeather_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"Invalid key.","status_code":"AP010001"}`))
	}))
	defer server.Close()

	client, err := NewWeatherClient(WeatherConfig{
		APIKey:   "bad-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherClient failed: %v", err)
	}

	_, err = client.FetchWeather(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for API error response, got nil")
	}
	if !strings.Contains(err.Error(), "AP010001") {
		t.Fatalf("expected AP010001 in error, got: %v", err)
	}
}

func TestWeatherClient_FetchWeather_ResponseSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// 发送 300 KiB 响应
		_, _ = w.Write([]byte(strings.Repeat("a", 300*1024)))
	}))
	defer server.Close()

	client, err := NewWeatherClient(WeatherConfig{
		APIKey:   "test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWeatherClient failed: %v", err)
	}

	_, err = client.FetchWeather(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for oversized response, got nil")
	}
}

func TestNewWeatherToolFromConfig(t *testing.T) {
	configJSON := `{"api_key":"my-api-key","location":"WX4SUCU47R3T"}`
	tool, err := NewWeatherToolFromConfig(configJSON)
	if err != nil {
		t.Fatalf("NewWeatherToolFromConfig failed: %v", err)
	}
	if tool.Name != ToolGetCurrentWeather {
		t.Fatalf("expected tool name %s, got %s", ToolGetCurrentWeather, tool.Name)
	}
	if tool.Description == "" || tool.Parameters == nil || tool.Run == nil {
		t.Fatal("expected tool properties to be properly initialized")
	}

	// 测试非法 JSON
	_, err = NewWeatherToolFromConfig("invalid-json")
	if err == nil {
		t.Fatal("expected error for invalid json, got nil")
	}
}

func TestGetWeatherTool_DirectRun(t *testing.T) {
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
					"now": {
						"text": "阴",
						"code": "9",
						"temperature": "15"
					},
					"last_update": "2025-01-01T12:00:00+08:00"
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	tool, err := NewWeatherTool(WeatherConfig{
		APIKey:   "test-key",
		Location: "WX4SUCU47R3T",
		Endpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("NewWeatherTool failed: %v", err)
	}

	ctx := context.Background()
	res, err := tool.Run(ctx, map[string]any{"location": "北京"})
	if err != nil {
		t.Fatalf("tool.Run failed: %v", err)
	}
	out, ok := res.(*GetCurrentWeatherOutput)
	if !ok || out.Now.Text != "阴" || out.Now.Temperature != "15" {
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
