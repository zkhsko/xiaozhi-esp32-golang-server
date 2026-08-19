package audio

import (
	"context"
	"io"
	"sync"
)

const (
	// DefaultDownlinkOpusQueueCapacity 默认下行 Opus 队列容量（100 包，约 6.0 秒音频）。
	DefaultDownlinkOpusQueueCapacity = 100

	// DefaultDownlinkQueueCapacity 默认下行队列容量别名。
	DefaultDownlinkQueueCapacity = DefaultDownlinkOpusQueueCapacity
)

// DownlinkQueue 表示用于下行 Opus 音频包的有界 FIFO 队列。
// 提供固定容量上限背压保护、跨协程独立深拷贝、正常排空（Finish）、快速清空（Clear/Drain）与主动关闭（Close）能力。
type DownlinkQueue struct {
	ch       chan []byte
	capacity int
	ctx      context.Context
	cancel   context.CancelFunc

	mu       sync.Mutex
	closed   bool
	finished bool
}

// NewDownlinkQueue 创建固定容量上限的有界下行 Opus 队列。
// 若 capacity <= 0，则采用默认容量 DefaultDownlinkOpusQueueCapacity (100)。
func NewDownlinkQueue(ctx context.Context, capacity int) *DownlinkQueue {
	if ctx == nil {
		ctx = context.Background()
	}
	if capacity <= 0 {
		capacity = DefaultDownlinkOpusQueueCapacity
	}

	qCtx, qCancel := context.WithCancel(ctx)
	return &DownlinkQueue{
		ch:       make(chan []byte, capacity),
		capacity: capacity,
		ctx:      qCtx,
		cancel:   qCancel,
	}
}

// DownlinkAudioQueue 是 DownlinkQueue 的类型别名。
type DownlinkAudioQueue = DownlinkQueue

// NewDownlinkAudioQueue 创建有界下行音频队列（NewDownlinkQueue 别名）。
func NewDownlinkAudioQueue(ctx context.Context, capacity int) *DownlinkAudioQueue {
	return NewDownlinkQueue(ctx, capacity)
}

// Push 将一个 Opus 音频包独立深拷贝后推入有界队列。
// 若队列已满，立即触发背压并返回 ErrQueueFull；若队列已关闭或已结束，返回 ErrQueueClosed。
func (q *DownlinkQueue) Push(data []byte) error {
	if len(data) == 0 {
		q.mu.Lock()
		closed := q.closed || q.finished
		q.mu.Unlock()
		if closed {
			return ErrQueueClosed
		}
		return nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed || q.finished {
		return ErrQueueClosed
	}

	select {
	case <-q.ctx.Done():
		return ErrQueueClosed
	default:
	}

	buf := make([]byte, len(data))
	copy(buf, data)

	select {
	case q.ch <- buf:
		return nil
	default:
		return ErrQueueFull
	}
}

// PushWithContext 带调用方 Context 检查将 Opus 音频包推入队列。
func (q *DownlinkQueue) PushWithContext(ctx context.Context, data []byte) error {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return q.Push(data)
}

// Pop 按严格 FIFO 顺序阻塞取出下一个 Opus 音频包。
// 若队列正常结束（Finish）且所有积压包已消费完毕，返回 io.EOF；
// 若队列已主动关闭（Close）或内部 context 被取消，返回 ErrQueueClosed；
// 若传入的 ctx 被取消，返回 ctx.Err()。
func (q *DownlinkQueue) Pop(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// 快速通道：检查 context 取消或队列关闭状态
	select {
	case <-q.ctx.Done():
		return nil, ErrQueueClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil, ErrQueueClosed
	}
	q.mu.Unlock()

	select {
	case <-q.ctx.Done():
		return nil, ErrQueueClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case item, ok := <-q.ch:
		if !ok {
			q.mu.Lock()
			closed := q.closed
			q.mu.Unlock()
			if closed {
				return nil, ErrQueueClosed
			}
			return nil, io.EOF
		}
		return item, nil
	}
}

// Next 是 Pop 的方法别名，按严格 FIFO 顺序获取下一个 Opus 音频包。
func (q *DownlinkQueue) Next(ctx context.Context) ([]byte, error) {
	return q.Pop(ctx)
}

// Finish 标记下行音频输入完成。
// 标记后不允许继续 Push 新包，但已入队的包仍可被 Pop 正常消费完毕，排空后 Pop 返回 io.EOF。
func (q *DownlinkQueue) Finish() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed || q.finished {
		return nil
	}
	q.finished = true
	close(q.ch)
	return nil
}

// Clear 清空队列中积压的所有未发送 Opus 包并返回丢弃的包数量。
// 常用于 abort（打断）时快速丢弃残留下行音频。
func (q *DownlinkQueue) Clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	cleared := 0
	for {
		select {
		case _, ok := <-q.ch:
			if !ok {
				return cleared
			}
			cleared++
		default:
			return cleared
		}
	}
}

// Drain 清空队列中积压的所有未发送 Opus 包并返回丢弃的包数量（Clear 的别名）。
func (q *DownlinkQueue) Drain() int {
	return q.Clear()
}

// Close 主动关闭下行音频队列，清空所有残留包并取消内部上下文以解除所有阻塞。
func (q *DownlinkQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return nil
	}
	q.closed = true
	if !q.finished {
		close(q.ch)
	}
	q.cancel()

	// 清空未发送的旧包
	for {
		select {
		case _, ok := <-q.ch:
			if !ok {
				return nil
			}
		default:
			return nil
		}
	}
}

// Capacity 返回队列的最大容量上限。
func (q *DownlinkQueue) Capacity() int {
	return q.capacity
}

// Len 返回当前队列中已缓冲的 Opus 包数量。
func (q *DownlinkQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ch)
}

// IsClosed 返回队列是否已被主动关闭。
func (q *DownlinkQueue) IsClosed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

// IsFinished 返回队列是否已被标记输入结束。
func (q *DownlinkQueue) IsFinished() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.finished
}
