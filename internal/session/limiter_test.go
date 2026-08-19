package session

import (
	"sync"
	"testing"
	"time"
)

// TestSessionLimiter_BasicAcquireAndRelease 验证基础名额获取与释放流程。
func TestSessionLimiter_BasicAcquireAndRelease(t *testing.T) {
	limiter := NewSessionLimiter(2)

	if limiter.MaxSessions() != 2 {
		t.Fatalf("expected MaxSessions 2, got %d", limiter.MaxSessions())
	}
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected initial ActiveCount 0, got %d", limiter.ActiveCount())
	}

	release1, ok := limiter.TryAcquire()
	if !ok || release1 == nil {
		t.Fatal("expected acquire 1 to succeed")
	}
	if limiter.ActiveCount() != 1 {
		t.Fatalf("expected ActiveCount 1, got %d", limiter.ActiveCount())
	}

	release2, ok := limiter.TryAcquire()
	if !ok || release2 == nil {
		t.Fatal("expected acquire 2 to succeed")
	}
	if limiter.ActiveCount() != 2 {
		t.Fatalf("expected ActiveCount 2, got %d", limiter.ActiveCount())
	}

	// 达到满载，再次获取应失败
	release3, ok := limiter.TryAcquire()
	if ok || release3 != nil {
		t.Fatal("expected acquire 3 to fail on full limiter")
	}
	if limiter.ActiveCount() != 2 {
		t.Fatalf("expected ActiveCount still 2, got %d", limiter.ActiveCount())
	}

	// 释放一个名额
	release1()
	if limiter.ActiveCount() != 1 {
		t.Fatalf("expected ActiveCount 1 after release, got %d", limiter.ActiveCount())
	}

	// 释放后应能再次成功获取
	release4, ok := limiter.TryAcquire()
	if !ok || release4 == nil {
		t.Fatal("expected acquire 4 to succeed after release")
	}
	if limiter.ActiveCount() != 2 {
		t.Fatalf("expected ActiveCount 2, got %d", limiter.ActiveCount())
	}

	release2()
	release4()
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected ActiveCount 0 after all releases, got %d", limiter.ActiveCount())
	}
}

// TestSessionLimiter_ReleaseIdempotence 验证 release 函数的幂等性与非负保证。
func TestSessionLimiter_ReleaseIdempotence(t *testing.T) {
	limiter := NewSessionLimiter(2)

	release, ok := limiter.TryAcquire()
	if !ok {
		t.Fatal("expected acquire to succeed")
	}
	if limiter.ActiveCount() != 1 {
		t.Fatalf("expected ActiveCount 1, got %d", limiter.ActiveCount())
	}

	// 连续多次调用同一 release 函数
	for i := 0; i < 10; i++ {
		release()
	}

	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected ActiveCount 0 after repeated release, got %d", limiter.ActiveCount())
	}

	// 验证幂等释放后仍可正常获取全部容量
	r1, ok1 := limiter.TryAcquire()
	r2, ok2 := limiter.TryAcquire()
	if !ok1 || !ok2 {
		t.Fatal("expected both acquires to succeed")
	}
	if limiter.ActiveCount() != 2 {
		t.Fatalf("expected ActiveCount 2, got %d", limiter.ActiveCount())
	}

	r1()
	r2()
	if limiter.ActiveCount() != 0 {
		t.Fatalf("expected ActiveCount 0, got %d", limiter.ActiveCount())
	}
}

// TestSessionLimiter_DefaultCapacity 验证传入非法容量时的默认回退保护。
func TestSessionLimiter_DefaultCapacity(t *testing.T) {
	tests := []struct {
		input       int
		expectedMax int
	}{
		{input: 0, expectedMax: 1},
		{input: -1, expectedMax: 1},
		{input: -100, expectedMax: 1},
		{input: 5, expectedMax: 5},
	}

	for _, tt := range tests {
		l := NewSessionLimiter(tt.input)
		if l.MaxSessions() != tt.expectedMax {
			t.Errorf("NewSessionLimiter(%d).MaxSessions() = %d, expected %d", tt.input, l.MaxSessions(), tt.expectedMax)
		}
	}
}

// TestSessionLimiter_ConcurrentAcquireRelease 验证并发获取与释放下的数据一致性与竞争安全。
func TestSessionLimiter_ConcurrentAcquireRelease(t *testing.T) {
	const capacity = 10
	const goroutines = 100
	const iterations = 50

	limiter := NewSessionLimiter(capacity)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				release, ok := limiter.TryAcquire()
				if ok {
					active := limiter.ActiveCount()
					if active > capacity {
						t.Errorf("active sessions %d exceeded capacity %d", active, capacity)
					}
					if active < 0 {
						t.Errorf("active sessions %d cannot be negative", active)
					}

					time.Sleep(50 * time.Microsecond)

					// 并发多次释放测试幂等安全性
					var rwg sync.WaitGroup
					for k := 0; k < 3; k++ {
						rwg.Add(1)
						go func() {
							defer rwg.Done()
							release()
						}()
					}
					rwg.Wait()
				}
			}
		}()
	}

	wg.Wait()

	if active := limiter.ActiveCount(); active != 0 {
		t.Fatalf("expected final active count 0, got %d", active)
	}
}
