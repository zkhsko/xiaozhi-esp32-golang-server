package audio

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// mockASRStream 实现用于测试的 ai.ASRStream。
type mockASRStream struct {
	mu           sync.Mutex
	pcmFrames    [][]byte
	writeErr     error
	writeBlocked chan struct{}
	blockStarted chan struct{}
	finished     bool
	closed       bool
}

func newMockASRStream() *mockASRStream {
	return &mockASRStream{
		pcmFrames: make([][]byte, 0),
	}
}

func (m *mockASRStream) WritePCM(ctx context.Context, data []byte) error {
	m.mu.Lock()
	if m.writeBlocked != nil {
		if m.blockStarted != nil {
			select {
			case <-m.blockStarted:
			default:
				close(m.blockStarted)
			}
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.writeBlocked:
		}
		m.mu.Lock()
	}

	if m.writeErr != nil {
		err := m.writeErr
		m.mu.Unlock()
		return err
	}

	frameCopy := make([]byte, len(data))
	copy(frameCopy, data)
	m.pcmFrames = append(m.pcmFrames, frameCopy)
	m.mu.Unlock()
	return nil
}

func (m *mockASRStream) Finish(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finished = true
	return nil
}

func (m *mockASRStream) Result(ctx context.Context) (string, error) {
	return "test result", nil
}

func (m *mockASRStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockASRStream) FrameCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pcmFrames)
}

func (m *mockASRStream) GetFrames() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([][]byte, len(m.pcmFrames))
	for i, f := range m.pcmFrames {
		res[i] = make([]byte, len(f))
		copy(res[i], f)
	}
	return res
}

// TestASRAudioQueue_NormalPushAndConsume 验证正常推入 PCM 帧后单个后台协程按序消费并写入 ASRStream。
func TestASRAudioQueue_NormalPushAndConsume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)
	defer q.Close()

	if q.Capacity() != 10 {
		t.Fatalf("expected capacity 10, got %d", q.Capacity())
	}

	const frameCount = 5
	sentFrames := make([][]byte, frameCount)
	for i := 0; i < frameCount; i++ {
		frame := make([]byte, UplinkBytesPerFrame)
		frame[0] = byte(i + 1)
		frame[UplinkBytesPerFrame-1] = byte((i + 1) * 2)
		sentFrames[i] = frame

		if err := q.Push(frame); err != nil {
			t.Fatalf("failed to push frame %d: %v", i, err)
		}
	}

	// 等待后台 worker 消费完毕
	deadline := time.Now().Add(2 * time.Second)
	for mockStream.FrameCount() < frameCount && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if mockStream.FrameCount() != frameCount {
		t.Fatalf("expected %d consumed frames, got %d", frameCount, mockStream.FrameCount())
	}

	consumed := mockStream.GetFrames()
	for i := 0; i < frameCount; i++ {
		if !bytes.Equal(consumed[i], sentFrames[i]) {
			t.Errorf("frame %d content mismatch", i)
		}
	}
}

// TestASRAudioQueue_QueueFullBackpressure 验证当下游消费阻塞导致队列满载时，Push 返回 ErrQueueFull。
func TestASRAudioQueue_QueueFullBackpressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	mockStream.writeBlocked = make(chan struct{}) // 阻塞写入
	mockStream.blockStarted = make(chan struct{})

	const capacity = 3
	q := NewASRAudioQueue(ctx, mockStream, capacity, nil)
	defer func() {
		close(mockStream.writeBlocked)
		q.Close()
	}()

	frame := make([]byte, UplinkBytesPerFrame)

	// 第 1 帧被推入后，等待后台 worker 取走并进入 WritePCM 阻塞状态
	if err := q.Push(frame); err != nil {
		t.Fatalf("failed to push first frame: %v", err)
	}

	select {
	case <-mockStream.blockStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter WritePCM within timeout")
	}

	// 此时 worker 已卡在 WritePCM 中，连续推入 capacity 帧占满队列
	for i := 0; i < capacity; i++ {
		err := q.Push(frame)
		if err != nil {
			t.Fatalf("unexpected error pushing frame %d: %v", i, err)
		}
	}

	// 此时队列已满，再次 Push 必须返回 ErrQueueFull
	err := q.Push(frame)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull on full queue, got %v", err)
	}
}

// TestASRAudioQueue_MemoryIsolation 验证 Push 进行独立拷贝，外部数据修改不会污染队列或已推入的数据。
func TestASRAudioQueue_MemoryIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)
	defer q.Close()

	original := make([]byte, UplinkBytesPerFrame)
	original[0] = 0xAA

	if err := q.Push(original); err != nil {
		t.Fatalf("failed to push: %v", err)
	}

	// 修改外部 slice
	original[0] = 0xFF

	deadline := time.Now().Add(2 * time.Second)
	for mockStream.FrameCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	frames := mockStream.GetFrames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if frames[0][0] != 0xAA {
		t.Fatalf("expected original byte 0xAA preserved, got 0x%X", frames[0][0])
	}
}

// TestASRAudioQueue_ContextCancellation 验证 Context 取消后 worker 安全退出，Push 返回上下文错误。
func TestASRAudioQueue_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)

	cancel()

	select {
	case <-q.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit within timeout on context cancellation")
	}

	err := q.Push(make([]byte, UplinkBytesPerFrame))
	if err == nil {
		t.Fatal("expected error after context canceled, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestASRAudioQueue_CloseGraceful 验证 Close 关闭队列并等待后台协程退出。
func TestASRAudioQueue_CloseGraceful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)

	q.Close()

	select {
	case <-q.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after Close()")
	}

	err := q.Push(make([]byte, UplinkBytesPerFrame))
	if !errors.Is(err, ErrQueueClosed) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected ErrQueueClosed or Canceled after Close, got %v", err)
	}
}

// TestASRAudioQueue_WorkerWriteError 验证后台消费协程写错误能够被捕获。
func TestASRAudioQueue_WorkerWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("stream write failure")
	mockStream := newMockASRStream()
	mockStream.writeErr = expectedErr

	q := NewASRAudioQueue(ctx, mockStream, 10, nil)
	defer q.Close()

	_ = q.Push(make([]byte, UplinkBytesPerFrame))

	select {
	case <-q.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after write error")
	}

	if q.Err() == nil || !errors.Is(q.Err(), expectedErr) {
		t.Fatalf("expected worker error %v, got %v", expectedErr, q.Err())
	}
}

// TestASRAudioQueue_EmptyPushIgnored 验证推入空数据被安全忽略。
func TestASRAudioQueue_EmptyPushIgnored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)
	defer q.Close()

	if err := q.Push(nil); err != nil {
		t.Errorf("expected nil error for nil push, got %v", err)
	}
	if err := q.Push([]byte{}); err != nil {
		t.Errorf("expected nil error for empty push, got %v", err)
	}
	if q.Len() != 0 {
		t.Errorf("expected queue length 0, got %d", q.Len())
	}
}

// TestASRAudioQueue_Finish_DrainsAndFinishesStream 验证 Finish 调用后积压的所有帧均被消费写入，且调用了 ASRStream 的 Finish。
func TestASRAudioQueue_Finish_DrainsAndFinishesStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)

	const frameCount = 4
	for i := 0; i < frameCount; i++ {
		frame := make([]byte, UplinkBytesPerFrame)
		frame[0] = byte(i + 1)
		if err := q.Push(frame); err != nil {
			t.Fatalf("push frame %d failed: %v", i, err)
		}
	}

	if err := q.Finish(); err != nil {
		t.Fatalf("finish failed: %v", err)
	}

	select {
	case <-q.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after Finish()")
	}

	if mockStream.FrameCount() != frameCount {
		t.Fatalf("expected %d frames consumed, got %d", frameCount, mockStream.FrameCount())
	}

	mockStream.mu.Lock()
	finished := mockStream.finished
	mockStream.mu.Unlock()

	if !finished {
		t.Fatal("expected mockStream.Finish to be called")
	}
}

// TestASRAudioQueue_Finish_PushAfterFinishReturnsError 验证 Finish 之后再次 Push 会被拒绝。
func TestASRAudioQueue_Finish_PushAfterFinishReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)
	defer q.Close()

	if err := q.Finish(); err != nil {
		t.Fatalf("finish failed: %v", err)
	}

	err := q.Push(make([]byte, UplinkBytesPerFrame))
	if !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed after Finish, got %v", err)
	}
}

// TestASRAudioQueue_Finish_Idempotent 验证重复调用 Finish 幂等且安全。
func TestASRAudioQueue_Finish_Idempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := newMockASRStream()
	q := NewASRAudioQueue(ctx, mockStream, 10, nil)
	defer q.Close()

	for i := 0; i < 3; i++ {
		if err := q.Finish(); err != nil {
			t.Fatalf("call %d Finish failed: %v", i, err)
		}
	}
}
