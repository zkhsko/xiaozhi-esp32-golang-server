package server_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/server"
)

func defaultTestServerConfig() config.ServerConfig {
	return config.ServerConfig{
		ListenAddr:            "127.0.0.1:0",
		WebSocketURL:          "ws://127.0.0.1:8080/xiaozhi/v1/",
		MaxConcurrentSessions: 10,
		ShutdownTimeout:       2 * time.Second,
		HTTPReadTimeout:       5 * time.Second,
		HTTPWriteTimeout:      5 * time.Second,
		HTTPIdleTimeout:       10 * time.Second,
		MaxHTTPBodyBytes:      65536,
		MaxHTTPHeaderBytes:    1024,
	}
}

func waitForServerReady(t *testing.T, srv *server.Server, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		addr := srv.Addr()
		if addr != "" && addr != "127.0.0.1:0" && addr != ":0" {
			conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return addr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server failed to start and accept connections within %s", timeout)
	return ""
}

func TestServer_ListenFailure_AddressAlreadyInUse(t *testing.T) {
	// 先占用一个本地端口
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}
	defer func() {
		_ = occupier.Close()
	}()

	occupiedAddr := occupier.Addr().String()

	cfg := defaultTestServerConfig()
	cfg.ListenAddr = occupiedAddr

	srv := server.New(cfg, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = srv.Run(ctx)
	if err == nil {
		t.Fatal("expected error when port is already occupied, got nil")
	}

	if !strings.Contains(err.Error(), "listen on") {
		t.Errorf("unexpected error format: %v", err)
	}
}

func TestServer_ServeAndRespond404(t *testing.T) {
	cfg := defaultTestServerConfig()
	srv := server.New(cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/unregistered-path", addr))
	if err != nil {
		t.Fatalf("failed to send GET request: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 Not Found, got %d", resp.StatusCode)
	}

	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("server returned unexpected error on shutdown: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to exit within timeout after context cancel")
	}
}

func TestServer_GracefulShutdown_ClosesListener(t *testing.T) {
	cfg := defaultTestServerConfig()
	srv := server.New(cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	// 发送一个正常探测请求
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/probe", addr))
	if err != nil {
		t.Fatalf("failed to send probe request: %v", err)
	}
	_ = resp.Body.Close()

	// 触发优雅退出
	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("server exited with error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to exit gracefully within timeout")
	}

	// 验证退出后监听器已关闭，新连接会被拒绝
	conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Errorf("expected connection to be refused after shutdown, but connected to %s", addr)
	}
}

func TestServer_CustomHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello xiaozhi"))
	})

	cfg := defaultTestServerConfig()
	srv := server.New(cfg, mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/hello", addr))
	if err != nil {
		t.Fatalf("failed to send GET request to custom handler: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if string(body) != "hello xiaozhi" {
		t.Errorf("expected body 'hello xiaozhi', got %q", string(body))
	}

	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("server returned error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to exit within timeout")
	}
}

func TestServer_ShutdownTimeout_Exceeded(t *testing.T) {
	// 模拟一个耗时超过 ShutdownTimeout 的长请求
	requestStarted := make(chan struct{})
	blockCh := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/long-task", func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-blockCh // 阻塞直到测试结束
		w.WriteHeader(http.StatusOK)
	})

	cfg := defaultTestServerConfig()
	cfg.ShutdownTimeout = 100 * time.Millisecond // 极短的宽限期用于测试超时
	srv := server.New(cfg, mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	addr := waitForServerReady(t, srv, 2*time.Second)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		client := &http.Client{Timeout: 2 * time.Second}
		resp, reqErr := client.Get(fmt.Sprintf("http://%s/long-task", addr))
		if reqErr == nil {
			_ = resp.Body.Close()
		}
	}()

	// 等待请求进入 handler
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("long task request did not start in time")
	}

	// 触发优雅关闭
	cancel()

	// 验证在宽限期耗尽后返回 context.DeadlineExceeded 错误
	select {
	case runErr := <-errCh:
		if runErr == nil {
			t.Fatal("expected shutdown timeout error, got nil")
		}
		if !errors.Is(runErr, context.DeadlineExceeded) && !strings.Contains(runErr.Error(), "context deadline exceeded") {
			t.Errorf("expected context deadline exceeded error, got: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server failed to return within timeout after shutdown deadline")
	}

	// 释放阻塞的 handler
	close(blockCh)
	wg.Wait()
}
