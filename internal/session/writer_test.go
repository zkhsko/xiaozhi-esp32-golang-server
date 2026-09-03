package session

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type recordMessage struct {
	typ     websocket.MessageType
	payload []byte
}

type recordingWSConn struct {
	mu          sync.Mutex
	messages    []recordMessage
	writeHook   func(ctx context.Context, typ websocket.MessageType, p []byte) error
	writeErr    error
	blockWrites chan struct{}
}

func (c *recordingWSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	if c.blockWrites != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.blockWrites:
		}
	}

	if c.writeHook != nil {
		if err := c.writeHook(ctx, typ, p); err != nil {
			return err
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.writeErr != nil {
		return c.writeErr
	}

	copied := make([]byte, len(p))
	copy(copied, p)
	c.messages = append(c.messages, recordMessage{typ: typ, payload: copied})
	return nil
}

func (c *recordingWSConn) getMessages() []recordMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make([]recordMessage, len(c.messages))
	copy(res, c.messages)
	return res
}

func TestWriter_SendSync_SuccessAndPayloadDeepCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &recordingWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	textBuf := []byte("hello text")
	if err := writer.SendTextSync(ctx, textBuf); err != nil {
		t.Fatalf("unexpected SendTextSync error: %v", err)
	}

	binBuf := []byte{0x01, 0x02, 0x03}
	if err := writer.SendBinarySync(ctx, binBuf); err != nil {
		t.Fatalf("unexpected SendBinarySync error: %v", err)
	}

	// 篡改外部源切片，验证异步深拷贝隔离
	textBuf[0] = 'X'
	binBuf[0] = 0xFF

	msgs := conn.getMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].typ != websocket.MessageText || string(msgs[0].payload) != "hello text" {
		t.Fatalf("unexpected text payload: %s", string(msgs[0].payload))
	}
	if msgs[1].typ != websocket.MessageBinary || !bytes.Equal(msgs[1].payload, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("unexpected binary payload: %v", msgs[1].payload)
	}
}

func TestWriter_SendSync_BlocksWhenQueueFull_ResumesWhenSpaceAvailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &recordingWSConn{
		blockWrites: make(chan struct{}),
	}
	// 容量为 1：1 个正在被 Write 阻塞，1 个填满队列缓冲
	writer := NewWriter(ctx, conn, 1, nil)
	defer writer.Close()

	// 启动写第 1 条，会被 conn.Write 挂起
	go func() {
		_ = writer.SendBinarySync(ctx, []byte{0x01})
	}()
	time.Sleep(20 * time.Millisecond)

	// 第 2 条放入队列缓冲，填满队列
	go func() {
		_ = writer.SendBinarySync(ctx, []byte{0x02})
	}()
	time.Sleep(20 * time.Millisecond)

	if qlen := writer.QueueLen(); qlen != 1 {
		t.Fatalf("expected queue len 1, got %d", qlen)
	}

	// 第 3 条入队时队列已满，应该阻塞等待空间，绝不返回 ErrWriteQueueFull
	var send3Err error
	send3Done := make(chan struct{})
	go func() {
		send3Err = writer.SendBinarySync(ctx, []byte{0x03})
		close(send3Done)
	}()

	select {
	case <-send3Done:
		t.Fatalf("SendBinarySync should block when queue is full, but returned early with err: %v", send3Err)
	case <-time.After(50 * time.Millisecond):
		// 正常阻塞
	}

	// 解除底层的阻塞，写协程消费腾出空间，唤醒等待者
	close(conn.blockWrites)

	select {
	case <-send3Done:
		if send3Err != nil {
			t.Fatalf("expected send3 to succeed after space available, got: %v", send3Err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for send3 to complete")
	}

	msgs := conn.getMessages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages written, got %d", len(msgs))
	}
	for i, m := range msgs {
		expectedVal := byte(i + 1)
		if len(m.payload) != 1 || m.payload[0] != expectedVal {
			t.Fatalf("expected packet %d to be %x, got %v", i+1, expectedVal, m.payload)
		}
	}
}

func TestWriter_SendSync_CallerCanceledContext_Unblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockCh := make(chan struct{})
	conn := &recordingWSConn{
		blockWrites: blockCh,
	}

	writer := NewWriter(ctx, conn, 1, nil)
	defer func() {
		select {
		case <-blockCh:
		default:
			close(blockCh)
		}
		_ = writer.Close()
	}()

	// 占满底层 Write 与队列
	go func() { _ = writer.SendBinarySync(ctx, []byte{0x01}) }()
	time.Sleep(20 * time.Millisecond)
	go func() { _ = writer.SendBinarySync(ctx, []byte{0x02}) }()
	time.Sleep(20 * time.Millisecond)

	// 使用独立的 callerCtx 发送第 3 条
	callerCtx, callerCancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- writer.SendBinarySync(callerCtx, []byte{0x03})
	}()

	// 确认正在阻塞
	select {
	case err := <-errCh:
		t.Fatalf("expected to block, but returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// 取消 callerCtx
	callerCancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for caller canceled context to unblock")
	}
}

func TestWriter_Close_GracefulFlushRemaining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &recordingWSConn{}
	writer := NewWriter(ctx, conn, 10, nil)

	for i := 0; i < 5; i++ {
		_ = writer.SendText(ctx, []byte("msg"))
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("unexpected Close error: %v", err)
	}

	msgs := conn.getMessages()
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages flushed on close, got %d", len(msgs))
	}

	// 关闭后再发送应直接拒绝
	if err := writer.SendText(ctx, []byte("late")); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed after close, got: %v", err)
	}
}

func TestWriter_Close_UnblocksBlockedSenders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockCh := make(chan struct{})
	conn := &recordingWSConn{
		blockWrites: blockCh,
	}
	defer func() {
		select {
		case <-blockCh:
		default:
			close(blockCh)
		}
	}()

	writer := NewWriter(ctx, conn, 1, nil)

	go func() { _ = writer.SendBinary(ctx, []byte{0x01}) }()
	time.Sleep(20 * time.Millisecond)
	go func() { _ = writer.SendBinary(ctx, []byte{0x02}) }()
	time.Sleep(20 * time.Millisecond)

	errCh := make(chan error, 1)
	go func() {
		errCh <- writer.SendBinarySync(ctx, []byte{0x03})
	}()

	time.Sleep(50 * time.Millisecond)

	// 关闭 Writer 应立刻唤醒等待者返回 ErrWriterClosed
	go func() {
		_ = writer.Close()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrWriterClosed) {
			t.Fatalf("expected ErrWriterClosed, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for Close to unblock waiting sender")
	}
}

func TestWriter_Stop_ImmediateCancelAndDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &recordingWSConn{
		blockWrites: make(chan struct{}),
	}
	defer close(conn.blockWrites)

	writer := NewWriter(ctx, conn, 10, nil)

	errCh := make(chan error, 1)
	go func() {
		_ = writer.SendText(ctx, []byte("m1"))
		errCh <- writer.SendTextSync(ctx, []byte("m2"))
	}()

	time.Sleep(30 * time.Millisecond)
	writer.Stop()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrWriterClosed) {
			t.Fatalf("expected ErrWriterClosed on stop, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for Stop to unblock sync sender")
	}

	select {
	case <-writer.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("writer.Done() was not closed on Stop")
	}
}

func TestWriter_UnderlyingWriteFailure_SetsErrorAndUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("underlying network broken")
	conn := &recordingWSConn{
		writeErr: expectedErr,
	}

	writer := NewWriter(ctx, conn, 10, nil)
	defer writer.Close()

	err := writer.SendBinarySync(ctx, []byte{0x01})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected %v, got %v", expectedErr, err)
	}

	if !errors.Is(writer.Err(), expectedErr) {
		t.Fatalf("expected writer.Err() to be %v, got %v", expectedErr, writer.Err())
	}

	// 发生底层写错误后，后续发送均应被拒绝
	if err := writer.SendText(ctx, []byte("after err")); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed after write failure, got %v", err)
	}
}

func TestWriter_ConcurrentSendSync_RaceAndIntegrity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &recordingWSConn{}
	// 容量故意设小（5），促使 10 个 goroutine 频繁发生阻塞背压挂起与唤醒
	writer := NewWriter(ctx, conn, 5, nil)
	defer writer.Close()

	const (
		numGoroutines = 10
		numMessages   = 30
	)

	var wg sync.WaitGroup
	var successCount atomic.Int64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numMessages; i++ {
				payload := []byte{byte(gid), byte(i)}
				if err := writer.SendBinarySync(ctx, payload); err != nil {
					t.Errorf("goroutine %d message %d failed: %v", gid, i, err)
					return
				}
				successCount.Add(1)
			}
		}(g)
	}

	wg.Wait()

	if total := successCount.Load(); total != numGoroutines*numMessages {
		t.Fatalf("expected %d successful sends, got %d", numGoroutines*numMessages, total)
	}

	msgs := conn.getMessages()
	if len(msgs) != numGoroutines*numMessages {
		t.Fatalf("expected %d messages in conn, got %d", numGoroutines*numMessages, len(msgs))
	}
}
