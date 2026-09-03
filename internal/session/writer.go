package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

// 串行写流程相关的默认常量。
const (
	// DefaultWriteQueueCapacity 默认 WebSocket 串行写队列容量。
	DefaultWriteQueueCapacity = 100
)

// 串行写流程相关的哨兵错误定义。
var (
	// ErrWriteQueueFull 串行写队列满载背压拒绝错误。
	ErrWriteQueueFull = errors.New("write queue is full")

	// ErrWriterClosed 串行写流程已关闭错误。
	ErrWriterClosed = errors.New("writer is closed")
)

// WSConn 定义底层 WebSocket 串行写入所依赖的最小连接接口。
type WSConn interface {
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
}

// writeMessage 封装待写入 WebSocket 的单条消息载荷与类型。
type writeMessage struct {
	msgType websocket.MessageType
	payload []byte
	done    chan error
}

// Writer 负责单个 WebSocket 连接的串行消息下发与背压保护。
// 单个专属写循环 goroutine 独占调用底层 conn.Write，保证所有文本和 Opus 二进制消息严格串行下发。
type Writer struct {
	conn      WSConn
	queue     chan writeMessage
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	logger    *slog.Logger

	mu      sync.RWMutex
	closed  bool
	lastErr error
}

// NewWriter 创建并启动 WebSocket 串行写流程。
// queueCapacity 指定有界队列容量，若 <= 0 则采用 DefaultWriteQueueCapacity。
func NewWriter(ctx context.Context, conn WSConn, queueCapacity int, l *slog.Logger) *Writer {
	if conn == nil {
		panic("session: writer requires non-nil WSConn")
	}
	if l == nil {
		l = slog.Default()
	}
	if queueCapacity <= 0 {
		queueCapacity = DefaultWriteQueueCapacity
	}
	if ctx == nil {
		ctx = context.Background()
	}

	writeCtx, cancel := context.WithCancel(ctx)
	w := &Writer{
		conn:   conn,
		queue:  make(chan writeMessage, queueCapacity),
		ctx:    writeCtx,
		cancel: cancel,
		done:   make(chan struct{}),
		logger: l,
	}

	go w.writeLoop()
	return w
}

// SendText 复制文本负载并排入串行写队列。
func (w *Writer) SendText(ctx context.Context, payload []byte) error {
	return w.enqueue(ctx, websocket.MessageText, payload, nil)
}

// SendTextSync 复制文本负载并排入串行写队列，阻塞等待消息真实写入底层连接。
func (w *Writer) SendTextSync(ctx context.Context, payload []byte) error {
	return w.sendSync(ctx, websocket.MessageText, payload)
}

// SendBinary 复制二进制负载并排入串行写队列。
func (w *Writer) SendBinary(ctx context.Context, payload []byte) error {
	return w.enqueue(ctx, websocket.MessageBinary, payload, nil)
}

// SendBinarySync 复制二进制负载并排入串行写队列，阻塞等待消息真实写入底层连接。
func (w *Writer) SendBinarySync(ctx context.Context, payload []byte) error {
	return w.sendSync(ctx, websocket.MessageBinary, payload)
}

// sendSync 封装带写入结果通道的同步写调用。
func (w *Writer) sendSync(ctx context.Context, msgType websocket.MessageType, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan error, 1)
	if err := w.enqueue(ctx, msgType, payload, done); err != nil {
		return err
	}
	select {
	case <-w.ctx.Done():
		return ErrWriterClosed
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// enqueue 执行跨异步边界的数据独立深拷贝，并尝试将消息放入有界队列；队列满时返回 ErrWriteQueueFull。
func (w *Writer) enqueue(ctx context.Context, msgType websocket.MessageType, payload []byte, done chan error) error {
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-w.ctx.Done():
		return ErrWriterClosed
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// 跨异步边界独立深拷贝数据，避免外部调用方修改底层切片
	var copied []byte
	if len(payload) > 0 {
		copied = make([]byte, len(payload))
		copy(copied, payload)
	} else if payload != nil {
		copied = []byte{}
	}

	item := writeMessage{
		msgType: msgType,
		payload: copied,
		done:    done,
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.closed {
		return ErrWriterClosed
	}

	select {
	case <-w.ctx.Done():
		return ErrWriterClosed
	case <-ctx.Done():
		return ctx.Err()
	case w.queue <- item:
		return nil
	default:
		return ErrWriteQueueFull
	}
}

// writeLoop 单专属 goroutine 循环，负责从有界队列取出消息并串行写入底层 WebSocket 连接。
func (w *Writer) writeLoop() {
	defer func() {
		w.cancel()
		w.closeOnce.Do(func() {
			w.mu.Lock()
			w.closed = true
			close(w.queue)
			w.mu.Unlock()
		})
		w.drainQueue()
		close(w.done)
	}()

	for {
		select {
		case <-w.ctx.Done():
			return
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			err := w.conn.Write(w.ctx, msg.msgType, msg.payload)
			if msg.done != nil {
				msg.done <- err
			}
			if err != nil {
				w.setErr(err)
				if !errors.Is(err, context.Canceled) {
					w.logger.Warn("websocket writer write failed", "error", err)
				}
				return
			}
		}
	}
}

// drainQueue 清空写队列中残留的未发送消息。
func (w *Writer) drainQueue() {
	for {
		select {
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			if msg.done != nil {
				msg.done <- ErrWriterClosed
			}
		default:
			return
		}
	}
}

// setErr 记录导致写流程退出的首个错误。
func (w *Writer) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastErr == nil {
		w.lastErr = err
	}
}

// Err 返回导致写循环退出的首个错误。
func (w *Writer) Err() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastErr
}

// Done 返回写循环完全退出后的信号通道。
func (w *Writer) Done() <-chan struct{} {
	return w.done
}

// Close 触发写流程优雅关闭并等待队列中已有消息排空后写循环安全退出。
func (w *Writer) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.queue)
		w.mu.Unlock()
	})
	<-w.done
	return w.Err()
}

// Stop 立即取消串行写流程、关闭队列并排空未发送消息。
func (w *Writer) Stop() {
	if w == nil {
		return
	}
	w.cancel()
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.queue)
		w.mu.Unlock()
	})
	w.drainQueue()
}

// DrainPending 快速清空当前写队列中积压的全部未发送消息，用于 abort 或打断时清理旧轮次残留。
func (w *Writer) DrainPending() {
	if w == nil {
		return
	}
	for {
		select {
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			if msg.done != nil {
				msg.done <- ErrWriterClosed
			}
		default:
			return
		}
	}
}

// QueueLen 返回当前队列中等待发送的消息数量。
func (w *Writer) QueueLen() int {
	return len(w.queue)
}

// Capacity 返回写队列的容量上限。
func (w *Writer) Capacity() int {
	return cap(w.queue)
}
