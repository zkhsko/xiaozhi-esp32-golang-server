package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// 下行节奏器相关的常量与错误定义。
const (
	// DefaultDownlinkFrameDuration 默认下行每帧音频时长（60 毫秒）。
	DefaultDownlinkFrameDuration = 60 * time.Millisecond
)

var (
	// ErrPacerStopped 节奏调度器已停止错误。
	ErrPacerStopped = errors.New("downlink pacer is stopped")
)

// DownlinkSender 定义下行帧发送的最小抽象接口。
type DownlinkSender interface {
	SendText(ctx context.Context, payload []byte) error
	SendBinary(ctx context.Context, payload []byte) error
}

// PendingDrainer 定义排空待发送缓冲区的接口。
type PendingDrainer interface {
	DrainPending()
}

// Ticker 定义下行节奏器依赖的定时器抽象接口，便于单元测试注入确定性模拟时钟。
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// realTicker 封装 Go 标准库 time.Ticker。
type realTicker struct {
	ticker *time.Ticker
}

func (r *realTicker) C() <-chan time.Time {
	return r.ticker.C
}

func (r *realTicker) Stop() {
	r.ticker.Stop()
}

// defaultTickerFactory 创建标准库实时 Ticker。
func defaultTickerFactory(d time.Duration) Ticker {
	return &realTicker{ticker: time.NewTicker(d)}
}

// pacerItemKind 表示下行调度项的类型。
type pacerItemKind uint8

const (
	// pacerItemAudio 表示 Opus 编码音频二进制包。
	pacerItemAudio pacerItemKind = iota

	// pacerItemText 表示文本协议控制消息（如字幕、状态切换通知）。
	pacerItemText
)

// pacerItem 定义下行调度器队列中的单个调度单元。
type pacerItem struct {
	kind pacerItemKind
	data []byte
}

// PacerCallbacks 封装 DownlinkPacer 在关键播放节点触发的类型化回调。
type PacerCallbacks struct {
	OnStarted   func()
	OnCompleted func()
	OnError     func(err error)
}

// DownlinkPacerOptions 聚合构造 DownlinkPacer 的选项。
type DownlinkPacerOptions struct {
	SessionId     string
	Sender        DownlinkSender
	QueueCap      int
	TickerFactory func(time.Duration) Ticker
	Logger        *slog.Logger
	AbortPayload  []byte
	Callbacks     PacerCallbacks
}

// DownlinkPacer 负责按 60 ms 实时节奏将音频包平滑下发至 DownlinkSender，
// 并保证文本消息（如字幕、状态切换）与音频二进制帧的严格 FIFO 顺序。
type DownlinkPacer struct {
	sessionId     string
	sender        DownlinkSender
	logger        *slog.Logger
	abortPayload  []byte
	callbacks     PacerCallbacks
	ctx           context.Context
	cancel        context.CancelFunc

	itemQueue     chan pacerItem
	finishChan    chan struct{}
	finishOnce    sync.Once
	doneChan      chan struct{}

	tickerFactory func(time.Duration) Ticker
	frameDuration time.Duration

	mu         sync.Mutex
	hasStarted bool
	stopped    bool
}

// NewDownlinkPacer 创建配置就绪的下行 60 ms 节奏调度器。
func NewDownlinkPacer(ctx context.Context, opts DownlinkPacerOptions) *DownlinkPacer {
	if ctx == nil {
		ctx = context.Background()
	}
	pacerCtx, cancel := context.WithCancel(ctx)

	queueCap := opts.QueueCap
	if queueCap <= 0 {
		queueCap = DefaultWriteQueueCapacity
	}

	factory := opts.TickerFactory
	if factory == nil {
		factory = defaultTickerFactory
	}

	l := opts.Logger
	if l == nil {
		l = slog.Default()
	}

	sId := opts.SessionId
	if sId == "" {
		sId = "pacer-session"
	}

	return &DownlinkPacer{
		sessionId:     sId,
		sender:        opts.Sender,
		logger:        l,
		abortPayload:  opts.AbortPayload,
		callbacks:     opts.Callbacks,
		ctx:           pacerCtx,
		cancel:        cancel,
		itemQueue:     make(chan pacerItem, queueCap),
		finishChan:    make(chan struct{}),
		doneChan:      make(chan struct{}),
		tickerFactory: factory,
		frameDuration: DefaultDownlinkFrameDuration,
	}
}

// EnqueueAudio 将单个编码完成的 Opus 音频包存入下行发送队列，队列满时阻塞等待（背压机制）。
func (p *DownlinkPacer) EnqueueAudio(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}

	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return ErrPacerStopped
	}
	p.mu.Unlock()

	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.itemQueue <- pacerItem{kind: pacerItemAudio, data: packet}:
		return nil
	}
}

// EnqueueText 将单条文本消息（如字幕、控制消息）存入下行发送队列，调度时立即下发以确保时序。
func (p *DownlinkPacer) EnqueueText(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}

	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return ErrPacerStopped
	}
	p.mu.Unlock()

	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.itemQueue <- pacerItem{kind: pacerItemText, data: payload}:
		return nil
	}
}

// FinishInput 标记上游音频与控制消息输入已全部完成。
func (p *DownlinkPacer) FinishInput() {
	p.finishOnce.Do(func() {
		close(p.finishChan)
	})
}

// Stop 立即停止节奏调度器并清空未发送的数据项。
func (p *DownlinkPacer) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()

	p.cancel()
	p.drainQueue()
}

// Abort 中止当前播放，清空积压并在已启动播放时下发中止控制载荷。
func (p *DownlinkPacer) Abort() {
	p.mu.Lock()
	p.stopped = true
	needAbortPayload := p.hasStarted && len(p.abortPayload) > 0
	p.mu.Unlock()

	p.cancel()
	p.drainQueue()

	if drainer, ok := p.sender.(PendingDrainer); ok && drainer != nil {
		drainer.DrainPending()
	}

	if needAbortPayload && p.sender != nil {
		_ = p.sender.SendText(context.Background(), p.abortPayload)
	}
}

// Done 返回节奏调度器完全退出后的信号通道。
func (p *DownlinkPacer) Done() <-chan struct{} {
	return p.doneChan
}

// drainQueue 清空队列中残留的数据项。
func (p *DownlinkPacer) drainQueue() {
	for {
		select {
		case <-p.itemQueue:
		default:
			return
		}
	}
}

// Run 启动下行节奏调度循环，阻塞直至当前轮次数据全部播放完毕、发生中断或上下文取消。
func (p *DownlinkPacer) Run() {
	defer func() {
		p.drainQueue()
		close(p.doneChan)
	}()

	var ticker Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()

	for {
		select {
		case <-p.ctx.Done():
			return

		case item := <-p.itemQueue:
			if !p.processItem(item, &ticker) {
				return
			}

		case <-p.finishChan:
			p.drainRemaining(&ticker)
			return
		}
	}
}

// drainRemaining 在输入已结束的情况下，按 60 ms 节奏发送队列中剩余的全部数据包并触发完成回调。
func (p *DownlinkPacer) drainRemaining(tickerPtr *Ticker) {
	for {
		select {
		case <-p.ctx.Done():
			return
		case item := <-p.itemQueue:
			if !p.processItem(item, tickerPtr) {
				return
			}
		default:
			p.finishTurn()
			return
		}
	}
}

// processItem 处理单个调度项的发送。文本立即发送，音频发送后等待 60ms 节拍。
func (p *DownlinkPacer) processItem(item pacerItem, tickerPtr *Ticker) bool {
	p.triggerStartOnce()

	switch item.kind {
	case pacerItemText:
		if p.sender != nil {
			if err := p.sender.SendText(p.ctx, item.data); err != nil {
				p.handleError(err)
				return false
			}
		}
		return true

	case pacerItemAudio:
		if p.sender != nil {
			if err := p.sender.SendBinary(p.ctx, item.data); err != nil {
				if !errors.Is(err, context.Canceled) {
					p.logger.Warn("failed to send downlink audio binary", "error", err)
				}
				p.handleError(err)
				return false
			}
		}

		if *tickerPtr == nil {
			*tickerPtr = p.tickerFactory(p.frameDuration)
		}
		return p.waitTick(*tickerPtr)
	}

	return true
}

// triggerStartOnce 在首包（无论文本或音频）开始发送时触发 OnStarted 回调。
func (p *DownlinkPacer) triggerStartOnce() {
	p.mu.Lock()
	if p.hasStarted || p.stopped {
		p.mu.Unlock()
		return
	}
	p.hasStarted = true
	p.mu.Unlock()

	if p.callbacks.OnStarted != nil {
		p.callbacks.OnStarted()
	}
}

// waitTick 等待下一个 60 ms 定时器滴答，若上下文取消则立即返回 false。
func (p *DownlinkPacer) waitTick(ticker Ticker) bool {
	if ticker == nil {
		return true
	}
	select {
	case <-p.ctx.Done():
		return false
	case <-ticker.C():
		return true
	}
}

// finishTurn 触发 OnCompleted 回调。
func (p *DownlinkPacer) finishTurn() {
	if p.callbacks.OnCompleted != nil {
		p.callbacks.OnCompleted()
	}
}

// handleError 处理发送错误并触发 OnError 回调。
func (p *DownlinkPacer) handleError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if p.callbacks.OnError != nil {
		p.callbacks.OnError(err)
	}
}
