package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/config"
)

// validHelloPayload 构造标准合法握手首包 JSON。
var validHelloPayload = []byte(`{"type":"hello","version":1,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}`)

// TestRegistry_BasicLifecycle 验证注册表基本的准入、注册、注销和名额释放流程。
func TestRegistry_BasicLifecycle(t *testing.T) {
	limiter := NewSessionLimiter(2)
	reg := NewRegistry(limiter, nil)

	if reg.ActiveCount() != 0 {
		t.Fatalf("expected active count 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
	if reg.IsClosed() {
		t.Fatal("expected registry to not be closed initially")
	}

	// 1. 准入名额获取
	release, ok := reg.Acquire()
	if !ok || release == nil {
		t.Fatal("expected acquire to succeed")
	}
	if reg.Limiter().ActiveCount() != 1 {
		t.Fatalf("expected limiter active count 1, got %d", reg.Limiter().ActiveCount())
	}
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0 before registration, got %d", reg.ActiveCount())
	}

	// 2. 会话注册
	mockConn := &faultWSConn{}
	sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-1", SerialNumber: "sn-1"}})
	sess.writer = NewWriter(context.Background(), mockConn, 10, nil)
	defer sess.writer.Close()

	unregister, registered := reg.Register(sess, release)
	if !registered || unregister == nil {
		t.Fatal("expected registration to succeed")
	}
	if reg.ActiveCount() != 1 {
		t.Fatalf("expected registry active count 1, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 1 {
		t.Fatalf("expected limiter active count 1, got %d", reg.Limiter().ActiveCount())
	}

	sessions := reg.Sessions()
	if len(sessions) != 1 || sessions[0] != sess {
		t.Fatalf("expected sessions list to contain registered session")
	}
	if reg.GetBySerial("sn-1") != sess {
		t.Fatalf("expected GetBySerial to return registered session")
	}

	// 3. 注销与释放
	unregister()
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0 after unregister, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0 after cleanup, got %d", reg.Limiter().ActiveCount())
	}
	if reg.GetBySerial("sn-1") != nil {
		t.Fatalf("expected GetBySerial to return nil after unregister")
	}

	// 4. 幂等性验证（重复调用 unregister 与 release 不引发 panic 或负计数）
	unregister()
	release()
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count to remain 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count to remain 0, got %d", reg.Limiter().ActiveCount())
	}
}

// TestRegistry_DuplicateSerialNumber_EvictsOldSession 验证相同序列号新会话注册时优雅踢出旧会话。
func TestRegistry_DuplicateSerialNumber_EvictsOldSession(t *testing.T) {
	limiter := NewSessionLimiter(5)
	reg := NewRegistry(limiter, nil)

	sn := "SN-SAME-DEVICE-001"

	// 1. 建立第 1 个会话并注册
	rel1, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire for session 1")
	}
	mockConn1 := &faultWSConn{}
	sess1 := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-001", SerialNumber: sn}})
	sess1.writer = NewWriter(context.Background(), mockConn1, 10, nil)

	unreg1, reg1Ok := reg.Register(sess1, rel1)
	if !reg1Ok {
		t.Fatal("failed to register session 1")
	}

	sess1Done := make(chan struct{})
	go func() {
		_ = sess1.Run()
		unreg1()
		close(sess1Done)
	}()

	// sess1 完成握手进入 Ready
	sess1.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
	waitState(t, sess1, StateReady, 2*time.Second)

	if reg.ActiveCount() != 1 {
		t.Fatalf("expected active count 1, got %d", reg.ActiveCount())
	}
	if reg.GetBySerial(sn) != sess1 {
		t.Fatalf("expected GetBySerial to return sess1")
	}

	// 2. 相同序列号建立第 2 个会话并注册（模拟重连/新连接互踢）
	rel2, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire for session 2")
	}
	mockConn2 := &faultWSConn{}
	sess2 := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-001-reconnect", SerialNumber: sn}})
	sess2.writer = NewWriter(context.Background(), mockConn2, 10, nil)

	unreg2, reg2Ok := reg.Register(sess2, rel2)
	if !reg2Ok {
		t.Fatal("failed to register session 2")
	}

	sess2Done := make(chan struct{})
	go func() {
		_ = sess2.Run()
		unreg2()
		close(sess2Done)
	}()

	// sess2 完成握手进入 Ready
	sess2.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
	waitState(t, sess2, StateReady, 2*time.Second)

	// 断言 sess1 被踢下线并正常退出
	select {
	case <-sess1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected session 1 to exit after being evicted")
	}
	if sess1.State() != StateClosed {
		t.Fatalf("expected session 1 state Closed, got %v", sess1.State())
	}

	// 断言 Registry 中该序列号当前指向 sess2，且活跃计数为 1
	if reg.GetBySerial(sn) != sess2 {
		t.Fatalf("expected GetBySerial to return sess2 after eviction")
	}
	if reg.ActiveCount() != 1 {
		t.Fatalf("expected active count 1, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 1 {
		t.Fatalf("expected limiter active count 1, got %d", reg.Limiter().ActiveCount())
	}

	// 3. 关闭 sess2，断言注册表完全清理
	sess2.Close()
	select {
	case <-sess2Done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected session 2 to exit after Close")
	}

	if reg.ActiveCount() != 0 {
		t.Fatalf("expected active count 0 after session 2 cleanup, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
	if reg.GetBySerial(sn) != nil {
		t.Fatalf("expected GetBySerial to be nil after full cleanup")
	}
}

// TestRegistry_DuplicateDeviceID_EvictsOldSession_V1 验证 v1 客户端无 Serial-Number 时按 Device-Id 优雅互踢旧会话。
func TestRegistry_DuplicateDeviceID_EvictsOldSession_V1(t *testing.T) {
	limiter := NewSessionLimiter(5)
	reg := NewRegistry(limiter, nil)

	deviceID := "90:70:69:17:c3:b0"

	// 1. 建立第 1 个 v1 会话（无 SerialNumber）并注册
	rel1, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire for session 1")
	}
	mockConn1 := &faultWSConn{}
	sess1 := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: deviceID, SerialNumber: "", ProtocolVersion: "1"}})
	sess1.writer = NewWriter(context.Background(), mockConn1, 10, nil)

	unreg1, reg1Ok := reg.Register(sess1, rel1)
	if !reg1Ok {
		t.Fatal("failed to register session 1")
	}

	sess1Done := make(chan struct{})
	go func() {
		_ = sess1.Run()
		unreg1()
		close(sess1Done)
	}()

	sess1.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
	waitState(t, sess1, StateReady, 2*time.Second)

	if reg.ActiveCount() != 1 {
		t.Fatalf("expected active count 1, got %d", reg.ActiveCount())
	}
	if reg.GetByDevice(deviceID) != sess1 {
		t.Fatalf("expected GetByDevice to return sess1")
	}

	// 2. 相同 DeviceID 建立第 2 个 v1 会话并注册
	rel2, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire for session 2")
	}
	mockConn2 := &faultWSConn{}
	sess2 := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: deviceID, SerialNumber: "", ProtocolVersion: "1"}})
	sess2.writer = NewWriter(context.Background(), mockConn2, 10, nil)

	unreg2, reg2Ok := reg.Register(sess2, rel2)
	if !reg2Ok {
		t.Fatal("failed to register session 2")
	}

	sess2Done := make(chan struct{})
	go func() {
		_ = sess2.Run()
		unreg2()
		close(sess2Done)
	}()

	sess2.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
	waitState(t, sess2, StateReady, 2*time.Second)

	// 断言 sess1 被踢下线并正常退出
	select {
	case <-sess1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected session 1 to exit after being evicted")
	}
	if sess1.State() != StateClosed {
		t.Fatalf("expected session 1 state Closed, got %v", sess1.State())
	}

	// 断言 Registry 中该 deviceID 当前指向 sess2，且活跃计数为 1
	if reg.GetByDevice(deviceID) != sess2 {
		t.Fatalf("expected GetByDevice to return sess2 after eviction")
	}
	if reg.ActiveCount() != 1 {
		t.Fatalf("expected active count 1, got %d", reg.ActiveCount())
	}

	// 3. 关闭 sess2，断言注册表完全清理
	sess2.Close()
	select {
	case <-sess2Done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected session 2 to exit after Close")
	}

	if reg.ActiveCount() != 0 {
		t.Fatalf("expected active count 0 after session 2 cleanup, got %d", reg.ActiveCount())
	}
	if reg.GetByDevice(deviceID) != nil {
		t.Fatalf("expected GetByDevice to be nil after full cleanup")
	}
}

// TestRegistry_ConcurrentDuplicateConnectRace 验证同一序列号高频并发建连时的互踢正确性与无数据竞争。
func TestRegistry_ConcurrentDuplicateConnectRace(t *testing.T) {
	limiter := NewSessionLimiter(20)
	reg := NewRegistry(limiter, nil)

	sn := "SN-CONCURRENT-RACE-001"
	concurrency := 15
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			rel, ok := reg.Acquire()
			if !ok {
				return
			}

			mockConn := &faultWSConn{}
			sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: fmt.Sprintf("dev-race-%d", idx), SerialNumber: sn}})
			sess.writer = NewWriter(context.Background(), mockConn, 10, nil)

			unreg, registered := reg.Register(sess, rel)
			if !registered {
				rel()
				return
			}

			go func() {
				_ = sess.Run()
				unreg()
			}()

			sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
			time.Sleep(5 * time.Millisecond)
			sess.Close()
		}(i)
	}

	wg.Wait()

	// 等待所有退出与清理完成
	deadline := time.Now().Add(3 * time.Second)
	for (reg.ActiveCount() > 0 || reg.Limiter().ActiveCount() > 0) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
	if reg.GetBySerial(sn) != nil {
		t.Fatalf("expected GetBySerial to return nil, got %v", reg.GetBySerial(sn))
	}
}

// TestRegistry_MultiDeviceIsolationAndIndependentUnregister 验证多个不同设备序列号之间互不影响且独立注销。
func TestRegistry_MultiDeviceIsolationAndIndependentUnregister(t *testing.T) {
	limiter := NewSessionLimiter(10)
	reg := NewRegistry(limiter, nil)

	snA := "SN-DEVICE-AAA"
	snB := "SN-DEVICE-BBB"

	relA, _ := reg.Acquire()
	sessA := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-A", SerialNumber: snA}})
	sessA.writer = NewWriter(context.Background(), &faultWSConn{}, 10, nil)
	unregA, _ := reg.Register(sessA, relA)

	relB, _ := reg.Acquire()
	sessB := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-B", SerialNumber: snB}})
	sessB.writer = NewWriter(context.Background(), &faultWSConn{}, 10, nil)
	unregB, _ := reg.Register(sessB, relB)

	if reg.ActiveCount() != 2 {
		t.Fatalf("expected active count 2, got %d", reg.ActiveCount())
	}
	if reg.GetBySerial(snA) != sessA || reg.GetBySerial(snB) != sessB {
		t.Fatalf("expected both sessions in registry")
	}

	// 注销 A，B 保持
	unregA()
	if reg.ActiveCount() != 1 {
		t.Fatalf("expected active count 1 after A unregister, got %d", reg.ActiveCount())
	}
	if reg.GetBySerial(snA) != nil {
		t.Fatalf("expected A to be nil in registry")
	}
	if reg.GetBySerial(snB) != sessB {
		t.Fatalf("expected B to remain in registry")
	}

	// 注销 B
	unregB()
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected active count 0, got %d", reg.ActiveCount())
	}
	if reg.GetBySerial(snB) != nil {
		t.Fatalf("expected B to be nil in registry")
	}
}

// TestRegistry_ConcurrentAcquireAndRegister 验证高并发注册、注销与并发上限控制。
func TestRegistry_ConcurrentAcquireAndRegister(t *testing.T) {
	maxSessions := 5
	limiter := NewSessionLimiter(maxSessions)
	reg := NewRegistry(limiter, nil)

	concurrency := 50
	var wg sync.WaitGroup
	var peakActive atomic.Int64
	var peakLimiter atomic.Int64
	var successCount atomic.Int64

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			release, ok := reg.Acquire()
			if !ok {
				return
			}

			successCount.Add(1)

			currLimiter := int64(reg.Limiter().ActiveCount())
			for {
				p := peakLimiter.Load()
				if currLimiter <= p || peakLimiter.CompareAndSwap(p, currLimiter) {
					break
				}
			}

			mockConn := &faultWSConn{}
			sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: fmt.Sprintf("dev-%d", idx), SerialNumber: fmt.Sprintf("sn-%d", idx)}})
			sess.writer = NewWriter(context.Background(), mockConn, 10, nil)
			defer sess.writer.Close()

			unregister, registered := reg.Register(sess, release)
			if !registered {
				release()
				return
			}

			currActive := int64(reg.ActiveCount())
			for {
				p := peakActive.Load()
				if currActive <= p || peakActive.CompareAndSwap(p, currActive) {
					break
				}
			}

			time.Sleep(5 * time.Millisecond)
			unregister()
		}(i)
	}

	wg.Wait()

	if peakLimiter.Load() > int64(maxSessions) {
		t.Fatalf("peak limiter active (%d) exceeded max (%d)", peakLimiter.Load(), maxSessions)
	}
	if peakActive.Load() > int64(maxSessions) {
		t.Fatalf("peak registry active (%d) exceeded max (%d)", peakActive.Load(), maxSessions)
	}
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0 after concurrency, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0 after concurrency, got %d", reg.Limiter().ActiveCount())
	}
	if successCount.Load() == 0 {
		t.Fatal("expected at least some sessions to successfully acquire")
	}
}

// TestRegistry_FailedUpgrade_InstantCleanup 验证协议升级失败时名额立即释放且无残留注册。
func TestRegistry_FailedUpgrade_InstantCleanup(t *testing.T) {
	limiter := NewSessionLimiter(2)
	reg := NewRegistry(limiter, nil)

	// 获取名额后模拟升级失败（直接释放名额，未执行 Register）
	release, ok := reg.Acquire()
	if !ok {
		t.Fatal("expected acquire to succeed")
	}
	if reg.Limiter().ActiveCount() != 1 {
		t.Fatalf("expected limiter active count 1, got %d", reg.Limiter().ActiveCount())
	}

	// 模拟升级失败时直接调用 release
	release()

	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0 after failed upgrade release, got %d", reg.Limiter().ActiveCount())
	}

	// 验证名额完全恢复，后续请求可正常获取
	release1, ok1 := reg.Acquire()
	release2, ok2 := reg.Acquire()
	if !ok1 || !ok2 {
		t.Fatal("expected subsequent acquires to succeed")
	}
	release1()
	release2()
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
}

// TestRegistry_Shutdown_RejectsNewAdmissions 验证关闭后拒绝新准入与新会话登记。
func TestRegistry_Shutdown_RejectsNewAdmissions(t *testing.T) {
	limiter := NewSessionLimiter(5)
	reg := NewRegistry(limiter, nil)

	err := reg.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on shutdown: %v", err)
	}

	if !reg.IsClosed() {
		t.Fatal("expected registry to be closed")
	}

	// 准入获取应直接失败
	release, ok := reg.Acquire()
	if ok || release != nil {
		t.Fatal("expected acquire to fail after shutdown")
	}

	// 会话登记应直接失败
	mockConn := &faultWSConn{}
	sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-shut", SerialNumber: "sn-shut"}})
	sess.writer = NewWriter(context.Background(), mockConn, 10, nil)
	defer sess.writer.Close()

	unregister, registered := reg.Register(sess)
	if registered || unregister != nil {
		t.Fatal("expected register to fail after shutdown")
	}
}

// TestRegistry_Shutdown_CancelsAllActiveSessions 验证停服时向所有活跃会话广播取消并等待退出。
func TestRegistry_Shutdown_CancelsAllActiveSessions(t *testing.T) {
	limiter := NewSessionLimiter(5)
	reg := NewRegistry(limiter, nil)

	sessionCount := 3
	var sessions []*Session
	var unregisters []func()

	for i := 0; i < sessionCount; i++ {
		release, ok := reg.Acquire()
		if !ok {
			t.Fatalf("failed to acquire for session %d", i)
		}

		mockConn := &faultWSConn{}
		sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: fmt.Sprintf("dev-%d", i), SerialNumber: fmt.Sprintf("sn-%d", i)}})
		sess.writer = NewWriter(context.Background(), mockConn, 10, nil)
		sessions = append(sessions, sess)

		unreg, registered := reg.Register(sess, release)
		if !registered {
			t.Fatalf("failed to register session %d", i)
		}
		unregisters = append(unregisters, unreg)

		go func(s *Session, u func()) {
			_ = s.Run()
			u()
		}(sess, unreg)
	}

	// 确认所有会话均处于活跃状态
	time.Sleep(20 * time.Millisecond)
	if reg.ActiveCount() != sessionCount {
		t.Fatalf("expected %d active sessions, got %d", sessionCount, reg.ActiveCount())
	}

	// 触发优雅关闭
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := reg.Shutdown(shutdownCtx)
	if err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}

	// 验证所有会话状态已流转至 Closed
	for i, s := range sessions {
		if s.State() != StateClosed {
			t.Errorf("session %d state is %v, expected Closed", i, s.State())
		}
	}

	// 验证活跃数与名额全部精确归零
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
}

// TestRegistry_Shutdown_TimeoutBranch 验证宽限期超时分支能安全返回 DeadlineExceeded 杜绝死锁。
func TestRegistry_Shutdown_TimeoutBranch(t *testing.T) {
	limiter := NewSessionLimiter(2)
	reg := NewRegistry(limiter, nil)

	release, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire")
	}

	mockConn := &faultWSConn{}
	sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-blocked", SerialNumber: "sn-blocked"}})
	sess.writer = NewWriter(context.Background(), mockConn, 10, nil)

	blockCh := make(chan struct{})
	unreg, registered := reg.Register(sess, release)
	if !registered {
		t.Fatal("failed to register")
	}

	go func() {
		<-blockCh
		sess.Close()
		unreg()
	}()

	// 使用极短宽限期触发超时
	shortCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := reg.Shutdown(shortCtx)
	if err == nil {
		t.Fatal("expected error on shutdown timeout, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded error, got: %v", err)
	}

	// 释放阻塞会话
	close(blockCh)

	// 等待最终清理完成
	deadline := time.Now().Add(1 * time.Second)
	for (reg.ActiveCount() > 0 || reg.Limiter().ActiveCount() > 0) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0 eventually, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0 eventually, got %d", reg.Limiter().ActiveCount())
	}
}

// TestRegistry_Shutdown_DuringListeningState 验证在 LISTENING 阶段停服时会话安全退出与资源释放。
func TestRegistry_Shutdown_DuringListeningState(t *testing.T) {
	limiter := NewSessionLimiter(1)
	reg := NewRegistry(limiter, nil)

	asrClient := newFaultASRClient()
	mockConn := &faultWSConn{}

	release, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire")
	}

	sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-listen", SerialNumber: "sn-listen"}, ASRClient: asrClient})
	sess.writer = NewWriter(context.Background(), mockConn, 10, nil)

	unreg, registered := reg.Register(sess, release)
	if !registered {
		t.Fatal("failed to register")
	}

	runDone := make(chan struct{})
	go func() {
		_ = sess.Run()
		unreg()
		close(runDone)
	}()

	// 完成 Hello 进入 Ready
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
	waitState(t, sess, StateReady, 2*time.Second)

	// 发送 listen.start 进入 Listening
	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)

	// 触发停服
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := reg.Shutdown(shutdownCtx)
	if err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}

	select {
	case <-runDone:
	case <-time.After(1 * time.Second):
		t.Fatal("session Run did not return after shutdown")
	}

	if sess.State() != StateClosed {
		t.Fatalf("expected state Closed, got %v", sess.State())
	}
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
}

// TestRegistry_Shutdown_DuringProcessingState 验证在 PROCESSING 阶段停服时会话安全退出。
func TestRegistry_Shutdown_DuringProcessingState(t *testing.T) {
	limiter := NewSessionLimiter(1)
	reg := NewRegistry(limiter, nil)

	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.pauseChan = make(chan struct{}) // 暂停 LLM 模拟 Processing 处理中
	defer close(llmClient.pauseChan)

	ttsClient := newFaultTTSClient()
	mockConn := &faultWSConn{}

	release, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire")
	}

	sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-proc", SerialNumber: "sn-proc"}, ASRClient: asrClient, LLMClient: llmClient, TTSClient: ttsClient})
	sess.writer = NewWriter(context.Background(), mockConn, 10, nil)

	unreg, registered := reg.Register(sess, release)
	if !registered {
		t.Fatal("failed to register")
	}

	runDone := make(chan struct{})
	go func() {
		_ = sess.Run()
		unreg()
		close(runDone)
	}()

	// 完成 Hello
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
	waitState(t, sess, StateReady, 2*time.Second)

	// 进入 Listening
	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)

	// 投递 ASR 识别结果 -> 驱动进入 Processing 并启动 LLM
	gen := sess.Generation()
	sess.PostASRFinal(gen, "你好小智")
	waitState(t, sess, StateProcessing, 2*time.Second)

	// 处于 Processing 阶段触发停服
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := reg.Shutdown(shutdownCtx)
	if err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}

	select {
	case <-runDone:
	case <-time.After(1 * time.Second):
		t.Fatal("session Run did not return after shutdown")
	}

	if sess.State() != StateClosed {
		t.Fatalf("expected state Closed, got %v", sess.State())
	}
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
}

// TestRegistry_Shutdown_DuringSpeakingState 验证在 SPEAKING 阶段停服时会话安全退出与下行停止。
func TestRegistry_Shutdown_DuringSpeakingState(t *testing.T) {
	limiter := NewSessionLimiter(1)
	reg := NewRegistry(limiter, nil)

	asrClient := newFaultASRClient()
	llmClient := newFaultLLMClient()
	llmClient.chunks = []string{"正在为您播报一段很长的话。"}

	ttsClient := newFaultTTSClient()
	// 提供多帧 24 kHz PCM 数据（每帧 2880 字节），并设置 pauseChan 保持在 Speaking 阶段
	pcmFrame := make([]byte, 2880)
	ttsClient.pcmChunks = [][]byte{pcmFrame, pcmFrame, pcmFrame, pcmFrame, pcmFrame}
	ttsClient.pauseChan = make(chan struct{})
	defer close(ttsClient.pauseChan)

	mockConn := &faultWSConn{}

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxConcurrentSessions: 1,
		},
		Session: config.SessionConfig{
			DownlinkOpusQueueCapacity: 50,
		},
	}

	release, ok := reg.Acquire()
	if !ok {
		t.Fatal("failed to acquire")
	}

	sess := NewSession(context.Background(), Options{ClientInfo: &ClientHeaderInfo{DeviceID: "dev-speak", SerialNumber: "sn-speak"}, Config: cfg, ASRClient: asrClient, LLMClient: llmClient, TTSClient: ttsClient})
	sess.writer = NewWriter(context.Background(), mockConn, 50, nil)

	unreg, registered := reg.Register(sess, release)
	if !registered {
		t.Fatal("failed to register")
	}

	runDone := make(chan struct{})
	go func() {
		_ = sess.Run()
		unreg()
		close(runDone)
	}()

	// 完成 Hello
	sess.postEvent(event{kind: eventKindClientHello, rawBytes: validHelloPayload})
	waitState(t, sess, StateReady, 2*time.Second)

	// 进入 Listening
	sess.PostClientText(&ClientMessage{
		Kind: KindListenStart,
		Mode: ListenModeAuto,
	})
	waitState(t, sess, StateListening, 2*time.Second)

	// 投递 ASR 识别结果 -> 触发 LLM/TTS -> 进入 Speaking
	gen := sess.Generation()
	sess.PostASRFinal(gen, "讲个故事")
	waitState(t, sess, StateSpeaking, 2*time.Second)

	// 处于 Speaking 状态时触发停服
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := reg.Shutdown(shutdownCtx)
	if err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}

	select {
	case <-runDone:
	case <-time.After(1 * time.Second):
		t.Fatal("session Run did not return after shutdown")
	}

	if sess.State() != StateClosed {
		t.Fatalf("expected state Closed, got %v", sess.State())
	}
	if reg.ActiveCount() != 0 {
		t.Fatalf("expected registry active count 0, got %d", reg.ActiveCount())
	}
	if reg.Limiter().ActiveCount() != 0 {
		t.Fatalf("expected limiter active count 0, got %d", reg.Limiter().ActiveCount())
	}
}

// TestRegistry_Shutdown_Idempotent 验证多次并发调用 Shutdown 幂等且安全。
func TestRegistry_Shutdown_Idempotent(t *testing.T) {
	limiter := NewSessionLimiter(5)
	reg := NewRegistry(limiter, nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			if err := reg.Shutdown(ctx); err != nil {
				t.Errorf("unexpected error on concurrent shutdown: %v", err)
			}
		}()
	}

	wg.Wait()
	if !reg.IsClosed() {
		t.Fatal("expected registry to be closed")
	}
}
