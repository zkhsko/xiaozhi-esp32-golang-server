package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// TestDownlinkQueue_PushPopFIFO 验证正常入队与严格 FIFO 顺序出队，数据完全一致。
func TestDownlinkQueue_PushPopFIFO(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	if capVal := q.Capacity(); capVal != 10 {
		t.Fatalf("expected capacity 10, got %d", capVal)
	}
	if l := q.Len(); l != 0 {
		t.Fatalf("expected initial len 0, got %d", l)
	}

	packets := [][]byte{
		[]byte("opus-packet-1"),
		[]byte("opus-packet-2"),
		[]byte("opus-packet-3"),
		[]byte("opus-packet-4"),
	}

	for i, pkt := range packets {
		if err := q.Push(pkt); err != nil {
			t.Fatalf("push packet %d failed: %v", i, err)
		}
		if q.Len() != i+1 {
			t.Fatalf("expected len %d, got %d", i+1, q.Len())
		}
	}

	// 验证使用 Pop 与 Next 按顺序出队
	for i, expected := range packets {
		var (
			got []byte
			err error
		)
		if i%2 == 0 {
			got, err = q.Pop(ctx)
		} else {
			got, err = q.Next(ctx)
		}
		if err != nil {
			t.Fatalf("pop packet %d failed: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("packet %d mismatch: expected %q, got %q", i, expected, got)
		}
	}

	if q.Len() != 0 {
		t.Fatalf("expected len 0 after popping all, got %d", q.Len())
	}
}

// TestDownlinkQueue_DefaultCapacity 验证不合法容量参数时自动采用默认容量。
func TestDownlinkQueue_DefaultCapacity(t *testing.T) {
	q1 := NewDownlinkQueue(nil, 0)
	if q1.Capacity() != DefaultDownlinkOpusQueueCapacity {
		t.Fatalf("expected default capacity %d, got %d", DefaultDownlinkOpusQueueCapacity, q1.Capacity())
	}

	q2 := NewDownlinkAudioQueue(nil, -5)
	if q2.Capacity() != DefaultDownlinkQueueCapacity {
		t.Fatalf("expected default capacity %d, got %d", DefaultDownlinkQueueCapacity, q2.Capacity())
	}
}

// TestDownlinkQueue_BackpressureAndCapacityLimit 验证队列满载背压与容量上限拒绝。
func TestDownlinkQueue_BackpressureAndCapacityLimit(t *testing.T) {
	ctx := context.Background()
	const capLimit = 3
	q := NewDownlinkQueue(ctx, capLimit)

	for i := 0; i < capLimit; i++ {
		pkt := []byte(fmt.Sprintf("packet-%d", i))
		if err := q.Push(pkt); err != nil {
			t.Fatalf("push packet %d failed: %v", i, err)
		}
	}

	if q.Len() != capLimit {
		t.Fatalf("expected len %d, got %d", capLimit, q.Len())
	}

	// 队列已满，再次 Push 必须触发背压并返回 ErrQueueFull
	overflowPkt := []byte("overflow-packet")
	if err := q.Push(overflowPkt); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull on full queue, got %v", err)
	}

	// 队列长度不应增加
	if q.Len() != capLimit {
		t.Fatalf("expected len to remain %d, got %d", capLimit, q.Len())
	}

	// 出队一个包后，应能再次 Push 成功
	popped, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("pop failed: %v", err)
	}
	if string(popped) != "packet-0" {
		t.Fatalf("expected first packet 'packet-0', got %q", string(popped))
	}
	if q.Len() != capLimit-1 {
		t.Fatalf("expected len %d, got %d", capLimit-1, q.Len())
	}

	if err := q.Push(overflowPkt); err != nil {
		t.Fatalf("push after pop failed: %v", err)
	}
	if q.Len() != capLimit {
		t.Fatalf("expected len %d, got %d", capLimit, q.Len())
	}
}

// TestDownlinkQueue_DeepCopyIsolation 验证入队时执行独立深拷贝与内存隔离。
func TestDownlinkQueue_DeepCopyIsolation(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	original := []byte{1, 2, 3, 4, 5}
	if err := q.Push(original); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	// 修改外部原始切片
	original[0] = 99
	original[4] = 88

	popped, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("pop failed: %v", err)
	}

	expected := []byte{1, 2, 3, 4, 5}
	if !bytes.Equal(popped, expected) {
		t.Fatalf("memory isolation failed: expected %v, got %v", expected, popped)
	}

	// 修改出队切片不应影响后续行为
	popped[0] = 77
	if original[0] != 99 {
		t.Fatalf("external slice corrupted: %v", original)
	}
}

// TestDownlinkQueue_FinishDrainAndEOF 验证 Finish 正常排空消费、后续入队拒绝与 io.EOF 返回。
func TestDownlinkQueue_FinishDrainAndEOF(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	packets := [][]byte{
		[]byte("audio-chunk-1"),
		[]byte("audio-chunk-2"),
		[]byte("audio-chunk-3"),
	}

	for _, pkt := range packets {
		if err := q.Push(pkt); err != nil {
			t.Fatalf("push failed: %v", err)
		}
	}

	if q.IsFinished() {
		t.Fatal("expected queue to not be finished initially")
	}

	// 标记输入完成
	if err := q.Finish(); err != nil {
		t.Fatalf("finish failed: %v", err)
	}

	if !q.IsFinished() {
		t.Fatal("expected queue to be finished")
	}

	// Finish 后继续 Push 应返回 ErrQueueClosed
	if err := q.Push([]byte("late-chunk")); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed after finish, got %v", err)
	}

	// 已入队的 3 个包仍可正常排空消费
	for i, expected := range packets {
		got, err := q.Pop(ctx)
		if err != nil {
			t.Fatalf("pop item %d after finish failed: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("item %d mismatch: expected %q, got %q", i, expected, got)
		}
	}

	// 排空后再次 Pop 必须返回 io.EOF
	for i := 0; i < 3; i++ {
		got, err := q.Pop(ctx)
		if got != nil || !errors.Is(err, io.EOF) {
			t.Fatalf("expected nil and io.EOF on empty finished queue, got data=%v, err=%v", got, err)
		}
	}

	// Finish 多次调用幂等
	if err := q.Finish(); err != nil {
		t.Fatalf("subsequent finish failed: %v", err)
	}
}

// TestDownlinkQueue_ClearAndDrain 验证 Clear/Drain 清空未发送旧包与快速丢弃能力。
func TestDownlinkQueue_ClearAndDrain(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	for i := 0; i < 5; i++ {
		_ = q.Push([]byte(fmt.Sprintf("pkt-%d", i)))
	}

	if q.Len() != 5 {
		t.Fatalf("expected len 5, got %d", q.Len())
	}

	cleared := q.Clear()
	if cleared != 5 {
		t.Fatalf("expected cleared 5, got %d", cleared)
	}
	if q.Len() != 0 {
		t.Fatalf("expected len 0 after clear, got %d", q.Len())
	}

	// Clear 后队列仍可正常使用接收新一轮音频
	newPkt := []byte("new-generation-pkt")
	if err := q.Push(newPkt); err != nil {
		t.Fatalf("push after clear failed: %v", err)
	}
	if q.Len() != 1 {
		t.Fatalf("expected len 1, got %d", q.Len())
	}

	got, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("pop after clear failed: %v", err)
	}
	if !bytes.Equal(got, newPkt) {
		t.Fatalf("expected %q, got %q", newPkt, got)
	}

	// 验证 Drain 别名方法
	for i := 0; i < 3; i++ {
		_ = q.Push([]byte(fmt.Sprintf("drain-%d", i)))
	}
	drained := q.Drain()
	if drained != 3 {
		t.Fatalf("expected drained 3, got %d", drained)
	}
	if q.Len() != 0 {
		t.Fatalf("expected len 0 after drain, got %d", q.Len())
	}
}

// TestDownlinkQueue_ClearAfterFinish 验证在 Finish 后执行 Clear 清空残留并使后续 Pop 立即返回 EOF。
func TestDownlinkQueue_ClearAfterFinish(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	_ = q.Push([]byte("pkt-1"))
	_ = q.Push([]byte("pkt-2"))
	_ = q.Finish()

	cleared := q.Clear()
	if cleared != 2 {
		t.Fatalf("expected cleared 2, got %d", cleared)
	}

	got, err := q.Pop(ctx)
	if got != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF immediately after clear on finished queue, got data=%v, err=%v", got, err)
	}
}

// TestDownlinkQueue_Close 验证 Close 主动关闭、清空残留包与后续操作拒绝。
func TestDownlinkQueue_Close(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	_ = q.Push([]byte("pkt-1"))
	_ = q.Push([]byte("pkt-2"))

	if q.IsClosed() {
		t.Fatal("expected queue to not be closed initially")
	}

	if err := q.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	if !q.IsClosed() {
		t.Fatal("expected queue to be closed")
	}
	if q.Len() != 0 {
		t.Fatalf("expected len 0 after close, got %d", q.Len())
	}

	// Close 后 Push 与 Pop 均返回 ErrQueueClosed
	if err := q.Push([]byte("late-pkt")); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed on push after close, got %v", err)
	}

	got, err := q.Pop(ctx)
	if got != nil || !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed on pop after close, got data=%v, err=%v", got, err)
	}

	// Close 幂等性
	if err := q.Close(); err != nil {
		t.Fatalf("subsequent close failed: %v", err)
	}
}

// TestDownlinkQueue_BlockingPopUnblockedByClose 验证阻塞在 Pop 上的协程在 Close 时立即被唤醒退出。
func TestDownlinkQueue_BlockingPopUnblockedByClose(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	errCh := make(chan error, 1)
	go func() {
		_, err := q.Pop(ctx)
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)

	if err := q.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrQueueClosed) {
			t.Fatalf("expected ErrQueueClosed on blocked pop after close, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("pop did not unblock after close within timeout")
	}
}

// TestDownlinkQueue_BlockingPopUnblockedByFinish 验证阻塞在 Pop 上的协程在 Finish 时立即被唤醒并返回 io.EOF。
func TestDownlinkQueue_BlockingPopUnblockedByFinish(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	errCh := make(chan error, 1)
	go func() {
		_, err := q.Pop(ctx)
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)

	if err := q.Finish(); err != nil {
		t.Fatalf("finish failed: %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected io.EOF on blocked pop after finish, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("pop did not unblock after finish within timeout")
	}
}

// TestDownlinkQueue_BlockingPopUnblockedByPopContextCancel 验证 Pop 传入的 Context 取消时立即退出且队列保持可用。
func TestDownlinkQueue_BlockingPopUnblockedByPopContextCancel(t *testing.T) {
	parentCtx := context.Background()
	q := NewDownlinkQueue(parentCtx, 10)

	popCtx, popCancel := context.WithCancel(parentCtx)

	errCh := make(chan error, 1)
	go func() {
		_, err := q.Pop(popCtx)
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)
	popCancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled on pop context cancel, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("pop did not unblock after context cancel within timeout")
	}

	// 队列本身未关闭，后续入队与正常 Context 出队仍应正常工作
	testPkt := []byte("surviving-packet")
	if err := q.Push(testPkt); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	got, err := q.Pop(parentCtx)
	if err != nil {
		t.Fatalf("pop with parentCtx failed: %v", err)
	}
	if !bytes.Equal(got, testPkt) {
		t.Fatalf("expected %q, got %q", testPkt, got)
	}
}

// TestDownlinkQueue_ParentContextCancel 验证构造队列时传入的父级 Context 取消时解除所有阻塞并拒绝新操作。
func TestDownlinkQueue_ParentContextCancel(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	q := NewDownlinkQueue(parentCtx, 10)

	errCh := make(chan error, 1)
	go func() {
		_, err := q.Pop(context.Background())
		errCh <- err
	}()

	time.Sleep(20 * time.Millisecond)
	parentCancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrQueueClosed) && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected ErrQueueClosed or Canceled, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("pop did not unblock after parent context cancel")
	}

	if err := q.Push([]byte("pkt")); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed on push after parent context cancel, got %v", err)
	}
}

// TestDownlinkQueue_EmptyData 验证空数据处理。
func TestDownlinkQueue_EmptyData(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 10)

	if err := q.Push(nil); err != nil {
		t.Fatalf("push nil failed: %v", err)
	}
	if err := q.Push([]byte{}); err != nil {
		t.Fatalf("push empty failed: %v", err)
	}
	if q.Len() != 0 {
		t.Fatalf("expected len 0, got %d", q.Len())
	}

	_ = q.Close()
	if err := q.Push(nil); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed on push nil after close, got %v", err)
	}
}

// TestDownlinkQueue_PushWithContext 验证 PushWithContext 检查 Context 取消。
func TestDownlinkQueue_PushWithContext(t *testing.T) {
	q := NewDownlinkQueue(context.Background(), 10)

	cancCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := q.PushWithContext(cancCtx, []byte("pkt")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if err := q.PushWithContext(context.Background(), []byte("valid-pkt")); err != nil {
		t.Fatalf("push with valid context failed: %v", err)
	}
}

// TestDownlinkQueue_ConcurrentPushPopFinish 高并发多协程入队、出队与排空竞态测试。
func TestDownlinkQueue_ConcurrentPushPopFinish(t *testing.T) {
	const (
		numProducers = 6
		numConsumers = 4
		itemsPerProd = 50
		capacity     = 20
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := NewDownlinkQueue(ctx, capacity)

	var (
		prodWg  sync.WaitGroup
		consWg  sync.WaitGroup
		recvMu  sync.Mutex
		recvMap = make(map[string]int)
	)

	// 启动消费者
	for c := 0; c < numConsumers; c++ {
		consWg.Add(1)
		go func() {
			defer consWg.Done()
			for {
				data, err := q.Pop(ctx)
				if err != nil {
					if errors.Is(err, io.EOF) || errors.Is(err, ErrQueueClosed) || errors.Is(err, context.Canceled) {
						return
					}
					t.Errorf("unexpected pop error: %v", err)
					return
				}
				recvMu.Lock()
				recvMap[string(data)]++
				recvMu.Unlock()
			}
		}()
	}

	// 启动生产者
	for p := 0; p < numProducers; p++ {
		prodWg.Add(1)
		prodID := p
		go func() {
			defer prodWg.Done()
			for i := 0; i < itemsPerProd; i++ {
				pkt := []byte(fmt.Sprintf("prod-%d-item-%d", prodID, i))
				for {
					err := q.Push(pkt)
					if err == nil {
						break
					}
					if errors.Is(err, ErrQueueFull) {
						time.Sleep(1 * time.Millisecond)
						continue
					}
					if errors.Is(err, ErrQueueClosed) || ctx.Err() != nil {
						return
					}
					t.Errorf("unexpected push error: %v", err)
					return
				}
			}
		}()
	}

	// 等待所有生产者完成
	prodWg.Wait()

	// 标记结束
	if err := q.Finish(); err != nil {
		t.Fatalf("finish failed: %v", err)
	}

	// 等待消费者排空消费完毕
	consWg.Wait()

	expectedTotal := numProducers * itemsPerProd
	recvMu.Lock()
	actualTotal := len(recvMap)
	recvMu.Unlock()

	if actualTotal != expectedTotal {
		t.Fatalf("expected total received %d, got %d", expectedTotal, actualTotal)
	}
}

// TestDownlinkQueue_ConcurrentPushPopClear 高并发入队、出队与打断 Clear 竞态测试。
func TestDownlinkQueue_ConcurrentPushPopClear(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	q := NewDownlinkQueue(ctx, 30)

	var wg sync.WaitGroup

	// 生产者
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = q.Push([]byte(fmt.Sprintf("p-%d-%d", id, i)))
				time.Sleep(100 * time.Microsecond)
			}
		}(p)
	}

	// 消费者
	for c := 0; c < 3; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := q.Pop(ctx)
				if err != nil {
					return
				}
			}
		}()
	}

	// 打断与清空
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			time.Sleep(5 * time.Millisecond)
			_ = q.Clear()
		}
	}()

	wg.Wait()
}

// TestDownlinkQueue_ConcurrentPushPopClose 高并发入队、出队与主动 Close 竞态测试。
func TestDownlinkQueue_ConcurrentPushPopClose(t *testing.T) {
	ctx := context.Background()
	q := NewDownlinkQueue(ctx, 20)

	var wg sync.WaitGroup

	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				err := q.Push([]byte(fmt.Sprintf("p-%d-%d", id, i)))
				if err != nil && errors.Is(err, ErrQueueClosed) {
					return
				}
				time.Sleep(50 * time.Microsecond)
			}
		}(p)
	}

	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := q.Pop(ctx)
				if err != nil {
					return
				}
			}
		}()
	}

	time.Sleep(10 * time.Millisecond)
	_ = q.Close()

	wg.Wait()

	if !q.IsClosed() {
		t.Fatal("expected queue to be closed")
	}
}
