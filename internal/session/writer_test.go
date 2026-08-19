package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// mockWSConn 记录写入消息并检测并发写入冲突的测试桩。
type mockWSConn struct {
	mu           sync.Mutex
	messages     []receivedMessage
	writeErr     error
	activeWrites int32
	maxWrites    int32
	blockCh      chan struct{}
}

type receivedMessage struct {
	msgType websocket.MessageType
	payload []byte
}

func newMockWSConn() *mockWSConn {
	return &mockWSConn{
		messages: make([]receivedMessage, 0),
	}
}

func (m *mockWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	// 检测是否发生并发写入冲突
	current := atomic.AddInt32(&m.activeWrites, 1)
	defer atomic.AddInt32(&m.activeWrites, -1)

	for {
		max := atomic.LoadInt32(&m.maxWrites)
		if current <= max || atomic.CompareAndSwapInt32(&m.maxWrites, max, current) {
			break
		}
	}

	if m.blockCh != nil {
		select {
		case <-m.blockCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.writeErr != nil {
		return m.writeErr
	}

	// 记录消息副本
	copied := make([]byte, len(p))
	copy(copied, p)
	m.messages = append(m.messages, receivedMessage{
		msgType: typ,
		payload: copied,
	})
	return nil
}

func (m *mockWSConn) getMessages() []receivedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]receivedMessage, len(m.messages))
	copy(copied, m.messages)
	return copied
}

func (m *mockWSConn) maxConcurrentWrites() int32 {
	return atomic.LoadInt32(&m.maxWrites)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestWriter_ConcurrentSend 验证多个 goroutine 并发提交文本与二进制消息时底层严格串行且数据完整。
func TestWriter_ConcurrentSend(t *testing.T) {
	conn := newMockWSConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 500, discardLogger())

	const (
		goroutines = 10
		msgsPerG   = 30
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < msgsPerG; i++ {
				if i%2 == 0 {
					text := fmt.Sprintf("g%d-msg%d", gid, i)
					err := w.SendTextMessage(ctx, text)
					if err != nil {
						t.Errorf("SendTextMessage failed: %v", err)
					}
				} else {
					binData := []byte(fmt.Sprintf("bin-g%d-msg%d", gid, i))
					err := w.SendBinary(ctx, binData)
					if err != nil {
						t.Errorf("SendBinary failed: %v", err)
					}
				}
			}
		}(g)
	}

	wg.Wait()

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// 验证最大并发写数量严格为 1
	if maxW := conn.maxConcurrentWrites(); maxW != 1 {
		t.Fatalf("expected strictly serial writes (max=1), got max concurrent writes = %d", maxW)
	}

	received := conn.getMessages()
	expectedCount := goroutines * msgsPerG
	if len(received) != expectedCount {
		t.Fatalf("expected %d messages, got %d", expectedCount, len(received))
	}

	// 验证所有消息类型匹配
	textCount := 0
	binCount := 0
	for _, msg := range received {
		switch msg.msgType {
		case websocket.MessageText:
			textCount++
		case websocket.MessageBinary:
			binCount++
		default:
			t.Errorf("unexpected message type: %v", msg.msgType)
		}
	}

	if textCount != expectedCount/2 || binCount != expectedCount/2 {
		t.Fatalf("expected %d text and %d binary, got %d text and %d binary",
			expectedCount/2, expectedCount/2, textCount, binCount)
	}
}

// TestWriter_DataIsolation 验证消息跨异步边界被深拷贝，外部修改切片不会污染已排队消息。
func TestWriter_DataIsolation(t *testing.T) {
	conn := newMockWSConn()
	conn.blockCh = make(chan struct{}) // 阻塞底层写，以便在调用方修改切片

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 10, discardLogger())

	original := []byte("hello-secure-token")
	err := w.SendBinary(ctx, original)
	if err != nil {
		t.Fatalf("SendBinary failed: %v", err)
	}

	// 立即就地篡改原始切片
	original[0] = 'X'
	original[1] = 'Y'

	// 放行写循环
	close(conn.blockCh)

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	msgs := conn.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	expected := []byte("hello-secure-token")
	if !bytes.Equal(msgs[0].payload, expected) {
		t.Fatalf("data isolation failed: expected %s, got %s", expected, msgs[0].payload)
	}
}

// TestWriter_QueueFullBackpressure 验证有界队列满载时正确返回 ErrWriteQueueFull。
func TestWriter_QueueFullBackpressure(t *testing.T) {
	conn := newMockWSConn()
	conn.blockCh = make(chan struct{}) // 阻塞写入以填满队列

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queueCap := 3
	w := NewWriter(ctx, conn, queueCap, discardLogger())

	// 第 1 条被 writeLoop 取出并阻塞在 conn.Write 中
	if err := w.SendTextMessage(ctx, "msg-0"); err != nil {
		t.Fatalf("Send 0 failed: %v", err)
	}

	// 等待 writeLoop 消费第 0 条并进入阻塞
	time.Sleep(20 * time.Millisecond)

	// 填满容量为 3 的 channel
	for i := 1; i <= queueCap; i++ {
		if err := w.SendTextMessage(ctx, fmt.Sprintf("msg-%d", i)); err != nil {
			t.Fatalf("Send %d failed: %v", i, err)
		}
	}

	// 此时队列已满，再次发送应触发背压拒绝
	err := w.SendTextMessage(ctx, "overflow-msg")
	if !errors.Is(err, ErrWriteQueueFull) {
		t.Fatalf("expected ErrWriteQueueFull, got %v", err)
	}

	// 发送二进制也同样触发背压
	err = w.SendBinary(ctx, []byte{1, 2, 3})
	if !errors.Is(err, ErrWriteQueueFull) {
		t.Fatalf("expected ErrWriteQueueFull, got %v", err)
	}

	// 放行写循环，使剩余消息正常发出
	close(conn.blockCh)

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	msgs := conn.getMessages()
	// 成功接收的总数为 1 (正在写的) + 3 (队列里的) = 4
	if len(msgs) != queueCap+1 {
		t.Fatalf("expected %d messages, got %d", queueCap+1, len(msgs))
	}
}

// TestWriter_ContextCancellation 验证 context 取消后写循环及时退出且队列被清理。
func TestWriter_ContextCancellation(t *testing.T) {
	conn := newMockWSConn()
	conn.blockCh = make(chan struct{}) // 阻塞使消息积压

	ctx, cancel := context.WithCancel(context.Background())
	w := NewWriter(ctx, conn, 10, discardLogger())

	_ = w.SendTextMessage(ctx, "msg-1")
	_ = w.SendTextMessage(ctx, "msg-2")
	_ = w.SendTextMessage(ctx, "msg-3")

	// 取消 context
	cancel()

	// 写循环应在有限时间内退出
	select {
	case <-w.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("writer did not exit within timeout on context cancellation")
	}

	// 取消后继续发送应被拒绝
	err := w.SendTextMessage(context.Background(), "msg-after-cancel")
	if !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed after cancel, got %v", err)
	}

	// 关闭阻塞通道
	close(conn.blockCh)

	// Close 应安全返回
	_ = w.Close()
}

// TestWriter_CallerContextCancellation 验证单次发送时入参 context 提前取消的拒绝行为。
func TestWriter_CallerContextCancellation(t *testing.T) {
	conn := newMockWSConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 5, discardLogger())
	defer w.Close()

	canceledCtx, cancelCaller := context.WithCancel(context.Background())
	cancelCaller() // 立即取消

	err := w.SendTextMessage(canceledCtx, "should-fail")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestWriter_UnderlyingWriteFailure 验证底层连接写失败时错误正确捕获并退出清理。
func TestWriter_UnderlyingWriteFailure(t *testing.T) {
	conn := newMockWSConn()
	mockErr := errors.New("network connection reset by peer")
	conn.writeErr = mockErr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 10, discardLogger())

	err := w.SendTextMessage(ctx, "test-failure")
	if err != nil {
		t.Fatalf("SendTextMessage returned unexpected error: %v", err)
	}

	// 等待写循环因底层写错误而退出
	select {
	case <-w.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("writer did not exit within timeout on underlying error")
	}

	if !errors.Is(w.Err(), mockErr) {
		t.Fatalf("expected w.Err() to be %v, got %v", mockErr, w.Err())
	}

	// 后续发送应返回 ErrWriterClosed
	err = w.SendTextMessage(ctx, "test-subsequent")
	if !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed, got %v", err)
	}

	// Close 返回记录的底层错误
	if err := w.Close(); !errors.Is(err, mockErr) {
		t.Fatalf("expected Close() to return %v, got %v", mockErr, err)
	}
}

// TestWriter_CloseIdempotent 验证 Close 方法幂等且多次调用安全。
func TestWriter_CloseIdempotent(t *testing.T) {
	conn := newMockWSConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 10, discardLogger())

	if err := w.SendTextMessage(ctx, "hello"); err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}

	// 首次 Close
	if err := w.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}

	// 二次 Close 幂等安全
	if err := w.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

// TestWriter_DefaultCapacityAndNilConn 验证默认容量设置及 nil conn 防御。
func TestWriter_DefaultCapacityAndNilConn(t *testing.T) {
	// 测试 nil conn 触发 panic
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when conn is nil")
		}
	}()

	_ = NewWriter(context.Background(), nil, 0, nil)
}

// TestWriter_DefaultCapacityAndEmptyPayload 验证非正数容量使用默认值及空负载安全写入。
func TestWriter_DefaultCapacityAndEmptyPayload(t *testing.T) {
	conn := newMockWSConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 0, nil)
	if capVal := w.Capacity(); capVal != DefaultWriteQueueCapacity {
		t.Fatalf("expected default capacity %d, got %d", DefaultWriteQueueCapacity, capVal)
	}

	// 发送空负载切片
	if err := w.SendBinary(ctx, []byte{}); err != nil {
		t.Fatalf("SendBinary empty slice failed: %v", err)
	}

	// 发送 nil 负载
	if err := w.SendBinary(ctx, nil); err != nil {
		t.Fatalf("SendBinary nil failed: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	msgs := conn.getMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

// TestWriter_ConcurrentCloseAndSend 验证在并发发送过程中调用 Close 绝不引发 panic 或数据竞争。
func TestWriter_ConcurrentCloseAndSend(t *testing.T) {
	conn := newMockWSConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 50, discardLogger())

	var wg sync.WaitGroup
	const senders = 8

	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				err := w.SendTextMessage(ctx, fmt.Sprintf("sender-%d-%d", id, j))
				if err != nil && !errors.Is(err, ErrWriterClosed) && !errors.Is(err, ErrWriteQueueFull) {
					t.Errorf("unexpected send error: %v", err)
				}
			}
		}(i)
	}

	// 稍作等待后触发并发 Close
	time.Sleep(2 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	wg.Wait()
}

// TestWriter_QueueLen 验证 QueueLen 能够正确反映队列长度。
func TestWriter_QueueLen(t *testing.T) {
	conn := newMockWSConn()
	conn.blockCh = make(chan struct{}) // 阻塞以堆积消息

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWriter(ctx, conn, 10, discardLogger())

	// 发送第 0 条（被取出消费并阻塞在 mock 写入）
	_ = w.SendTextMessage(ctx, "msg-0")
	time.Sleep(20 * time.Millisecond)

	// 队列当前应为 0
	if l := w.QueueLen(); l != 0 {
		t.Fatalf("expected queue len 0, got %d", l)
	}

	// 入队 3 条
	_ = w.SendTextMessage(ctx, "msg-1")
	_ = w.SendTextMessage(ctx, "msg-2")
	_ = w.SendTextMessage(ctx, "msg-3")

	if l := w.QueueLen(); l != 3 {
		t.Fatalf("expected queue len 3, got %d", l)
	}

	close(conn.blockCh)
	_ = w.Close()

	if l := w.QueueLen(); l != 0 {
		t.Fatalf("expected queue len 0 after close, got %d", l)
	}
}
