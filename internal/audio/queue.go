package audio

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"xiaozhi-esp32-golang-server/internal/ai"
)

const (
	// DefaultASRPCMQueueCapacity 默认 ASR PCM 队列容量（100 帧，约 6.0 秒音频）。
	DefaultASRPCMQueueCapacity = 100
)

var (
	// ErrQueueFull 表示 ASR 音频队列已满（背压保护）。
	ErrQueueFull = errors.New("asr audio queue is full")

	// ErrQueueClosed 表示 ASR 音频队列已关闭。
	ErrQueueClosed = errors.New("asr audio queue is closed")
)

// ASRAudioQueue 表示用于向 ASRStream 流式写入 PCM 帧的有界音频队列。
// 它通过单个专有后台协程按序消费 PCM 帧并写入下游 ASR 流，避免为每帧创建 goroutine。
type ASRAudioQueue struct {
	stream ai.ASRStream
	queue  chan []byte
	ctx    context.Context
	cancel context.CancelFunc
	logger *slog.Logger

	mu        sync.Mutex
	closed    bool
	workerErr error
	done      chan struct{}
}

// NewASRAudioQueue 创建并启动有界 ASR 音频队列。
func NewASRAudioQueue(ctx context.Context, stream ai.ASRStream, capacity int, l *slog.Logger) *ASRAudioQueue {
	if ctx == nil {
		ctx = context.Background()
	}
	if capacity <= 0 {
		capacity = DefaultASRPCMQueueCapacity
	}
	if l == nil {
		l = slog.Default()
	}

	qCtx, qCancel := context.WithCancel(ctx)
	q := &ASRAudioQueue{
		stream: stream,
		queue:  make(chan []byte, capacity),
		ctx:    qCtx,
		cancel: qCancel,
		logger: l,
		done:   make(chan struct{}),
	}

	go q.worker()
	return q
}

// Push 将一帧 PCM 音频数据独立拷贝后推入有界队列。
// 若队列已满，立即返回 ErrQueueFull；若队列已关闭或上下文已取消，返回相应错误。
func (q *ASRAudioQueue) Push(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	if q.workerErr != nil {
		err := q.workerErr
		q.mu.Unlock()
		return err
	}
	q.mu.Unlock()

	// 优先检查 Context 取消状态
	select {
	case <-q.ctx.Done():
		return q.ctx.Err()
	default:
	}

	buf := make([]byte, len(data))
	copy(buf, data)

	select {
	case <-q.ctx.Done():
		return q.ctx.Err()
	case q.queue <- buf:
		return nil
	default:
		return ErrQueueFull
	}
}

// Capacity 返回当前队列的容量。
func (q *ASRAudioQueue) Capacity() int {
	return cap(q.queue)
}

// Len 返回当前队列中积压的帧数。
func (q *ASRAudioQueue) Len() int {
	return len(q.queue)
}

// worker 是单个后台消费协程，严格按顺序将 PCM 数据写入 ASRStream。
func (q *ASRAudioQueue) worker() {
	defer close(q.done)

	for {
		select {
		case <-q.ctx.Done():
			return
		case pcm, ok := <-q.queue:
			if !ok {
				return
			}
			if q.stream == nil {
				continue
			}
			if err := q.stream.WritePCM(q.ctx, pcm); err != nil {
				if !errors.Is(err, context.Canceled) {
					q.mu.Lock()
					if q.workerErr == nil {
						q.workerErr = err
					}
					q.mu.Unlock()
					q.logger.Warn("failed to write pcm frame to asr stream",
						"error", err,
					)
				}
				return
			}
		}
	}
}

// Close 取消队列上下文并等待后台消费协程退出。
func (q *ASRAudioQueue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()

	q.cancel()
	<-q.done
}

// Done 返回消费协程退出完成信号通道。
func (q *ASRAudioQueue) Done() <-chan struct{} {
	return q.done
}

// Err 返回后台消费协程遇到的错误（若有）。
func (q *ASRAudioQueue) Err() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.workerErr
}
