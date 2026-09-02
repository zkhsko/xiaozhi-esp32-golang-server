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

// DownlinkSender 定义下行发送接口。
type DownlinkSender interface {
	SendText(ctx context.Context, payload []byte) error
}

// messageSource 定义写入消息的来源类型。
type messageSource int

const (
	// messageSourceControl 普通控制与业务帧（如 hello、stt、mcp 等）。
	messageSourceControl messageSource = iota
	// messageSourceVoice 语音帧（如 tts 协议文本帧与 Opus 二进制音频帧）。
	messageSourceVoice
)

// writeMessage 封装待写入 WebSocket 的单条消息载荷、类型与来源元数据。
type writeMessage struct {
	msgType websocket.MessageType
	payload []byte
	source  messageSource
	turnId  uint64
}

// Writer 负责单个 WebSocket 连接的串行消息下发与背压保护。
// 单个专属写循环 goroutine 独占调用底层 conn.Write，保证所有文本和 Opus 二进制消息严格串行下发。
type Writer struct {
	conn      WSConn
	queue     chan writeMessage
	parentCtx context.Context
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	logger    *slog.Logger

	mu                    sync.RWMutex
	closed                bool
	stopped               bool
	lastErr               error
	invalidatedVoiceTurns map[uint64]struct{}
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
		conn:                  conn,
		queue:                 make(chan writeMessage, queueCapacity),
		parentCtx:             ctx,
		ctx:                   writeCtx,
		cancel:                cancel,
		done:                  make(chan struct{}),
		logger:                l,
		invalidatedVoiceTurns: make(map[uint64]struct{}),
	}

	go w.writeLoop()
	return w
}

// SendText 复制文本负载并排入串行写队列。
func (w *Writer) SendText(ctx context.Context, payload []byte) error {
	return w.enqueue(ctx, messageSourceControl, 0, websocket.MessageText, payload)
}

// SendTextMessage 复制文本字符串并排入串行写队列。
func (w *Writer) SendTextMessage(ctx context.Context, text string) error {
	return w.enqueue(ctx, messageSourceControl, 0, websocket.MessageText, []byte(text))
}

// SendBinary 复制二进制负载并排入串行写队列。
func (w *Writer) SendBinary(ctx context.Context, payload []byte) error {
	return w.enqueue(ctx, messageSourceControl, 0, websocket.MessageBinary, payload)
}

// SendVoiceText 复制语音文本负载并排入串行写队列，携带语音轮次元数据。
func (w *Writer) SendVoiceText(ctx context.Context, turnId uint64, payload []byte) error {
	return w.enqueue(ctx, messageSourceVoice, turnId, websocket.MessageText, payload)
}

// SendVoiceBinary 复制语音二进制负载并排入串行写队列，携带语音轮次元数据。
func (w *Writer) SendVoiceBinary(ctx context.Context, turnId uint64, payload []byte) error {
	return w.enqueue(ctx, messageSourceVoice, turnId, websocket.MessageBinary, payload)
}

// SendVoiceTextWait 复制语音文本负载并排入串行写队列，携带语音轮次元数据；若队列已满则阻塞等待空间可用、上下文取消或 Writer 关闭。
func (w *Writer) SendVoiceTextWait(ctx context.Context, turnId uint64, payload []byte) error {
	return w.enqueueWait(ctx, messageSourceVoice, turnId, websocket.MessageText, payload)
}

// SendVoiceBinaryWait 复制语音二进制负载并排入串行写队列，携带语音轮次元数据；若队列已满则阻塞等待空间可用、上下文取消或 Writer 关闭。
func (w *Writer) SendVoiceBinaryWait(ctx context.Context, turnId uint64, payload []byte) error {
	return w.enqueueWait(ctx, messageSourceVoice, turnId, websocket.MessageBinary, payload)
}

// SendTextWait 复制文本负载并排入串行写队列；若队列已满则阻塞等待空间可用。
func (w *Writer) SendTextWait(ctx context.Context, payload []byte) error {
	return w.enqueueWait(ctx, messageSourceControl, 0, websocket.MessageText, payload)
}

// SendBinaryWait 复制二进制负载并排入串行写队列；若队列已满则阻塞等待空间可用。
func (w *Writer) SendBinaryWait(ctx context.Context, payload []byte) error {
	return w.enqueueWait(ctx, messageSourceControl, 0, websocket.MessageBinary, payload)
}

// copyPayload 跨异步边界独立深拷贝数据，避免外部调用方修改底层切片。
func copyPayload(payload []byte) []byte {
	if len(payload) > 0 {
		copied := make([]byte, len(payload))
		copy(copied, payload)
		return copied
	}
	if payload != nil {
		return []byte{}
	}
	return nil
}

// enqueue 执行跨异步边界的数据独立深拷贝，并尝试将消息放入有界队列；队列满时立即返回 ErrWriteQueueFull。
func (w *Writer) enqueue(ctx context.Context, source messageSource, turnId uint64, msgType websocket.MessageType, payload []byte) error {
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

	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return ErrWriterClosed
	}
	w.mu.RUnlock()

	item := writeMessage{
		msgType: msgType,
		payload: copyPayload(payload),
		source:  source,
		turnId:  turnId,
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

// enqueueWait 执行跨异步边界的数据独立深拷贝，并将消息放入有界队列；若队列已满则阻塞等待队列空间可用、上下文取消或 Writer 关闭。
func (w *Writer) enqueueWait(ctx context.Context, source messageSource, turnId uint64, msgType websocket.MessageType, payload []byte) error {
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

	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return ErrWriterClosed
	}
	w.mu.RUnlock()

	item := writeMessage{
		msgType: msgType,
		payload: copyPayload(payload),
		source:  source,
		turnId:  turnId,
	}

	select {
	case <-w.ctx.Done():
		return ErrWriterClosed
	case <-ctx.Done():
		return ctx.Err()
	case w.queue <- item:
		return nil
	}
}

// writeLoop 单专属 goroutine 循环，负责从有界队列取出消息并串行写入底层 WebSocket 连接。
func (w *Writer) writeLoop() {
	defer func() {
		w.cancel()
		w.closeOnce.Do(func() {
			w.mu.Lock()
			w.closed = true
			w.mu.Unlock()
		})
		close(w.done)
	}()

	for {
		select {
		case <-w.ctx.Done():
			w.mu.RLock()
			stopped := w.stopped
			lastErr := w.lastErr
			w.mu.RUnlock()

			if stopped || lastErr != nil || (w.parentCtx != nil && w.parentCtx.Err() != nil) {
				w.drainQueue()
				return
			}

			// 优雅关闭：排空并写出队列中已入队的所有有效消息
			for {
				select {
				case msg := <-w.queue:
					if msg.source == messageSourceVoice && w.isVoiceTurnInvalidated(msg.turnId) {
						continue
					}
					writeCtx := w.parentCtx
					if writeCtx == nil {
						writeCtx = context.Background()
					}
					if err := w.conn.Write(writeCtx, msg.msgType, msg.payload); err != nil {
						w.setErr(err)
						if !errors.Is(err, context.Canceled) {
							w.logger.Warn("websocket writer write failed during graceful close", "error", err)
						}
						w.drainQueue()
						return
					}
				default:
					return
				}
			}

		case msg := <-w.queue:
			if msg.source == messageSourceVoice && w.isVoiceTurnInvalidated(msg.turnId) {
				continue
			}
			if err := w.conn.Write(w.ctx, msg.msgType, msg.payload); err != nil {
				w.setErr(err)
				if !errors.Is(err, context.Canceled) {
					w.logger.Warn("websocket writer write failed", "error", err)
				}
				w.cancel()
				w.closeOnce.Do(func() {
					w.mu.Lock()
					w.closed = true
					w.mu.Unlock()
				})
				w.drainQueue()
				return
			}
		}
	}
}

// drainQueue 清空写队列中残留的未发送消息。
func (w *Writer) drainQueue() {
	for {
		select {
		case _, ok := <-w.queue:
			if !ok {
				return
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
		w.mu.Unlock()
		w.cancel()
	})
	<-w.done
	return w.Err()
}

// InvalidateVoiceTurn 记录已失效的语音轮次。
// 写循环在向底层连接实际写入前，会跳过属于已失效轮次的语音帧。
// 普通控制帧和 MCP 帧不受影响，绝不跳过。
func (w *Writer) InvalidateVoiceTurn(turnId uint64) {
	if w == nil || turnId == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.invalidatedVoiceTurns == nil {
		w.invalidatedVoiceTurns = make(map[uint64]struct{})
	}
	w.invalidatedVoiceTurns[turnId] = struct{}{}
}

// isVoiceTurnInvalidated 查询指定语音轮次是否已被标记为失效。
func (w *Writer) isVoiceTurnInvalidated(turnId uint64) bool {
	if turnId == 0 {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.invalidatedVoiceTurns == nil {
		return false
	}
	_, ok := w.invalidatedVoiceTurns[turnId]
	return ok
}

// Stop 立即取消串行写流程并排空丢弃未发送消息。
func (w *Writer) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	w.closed = true
	w.mu.Unlock()
	w.cancel()
	w.drainQueue()
}

// QueueLen 返回当前队列中等待发送的消息数量。
func (w *Writer) QueueLen() int {
	return len(w.queue)
}

// Capacity 返回写队列的容量上限。
func (w *Writer) Capacity() int {
	return cap(w.queue)
}
