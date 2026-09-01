package session

import (
	"context"
	"errors"
	"fmt"
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

	// pacerItemSentenceStart 表示分句字幕开始播报文本通知。
	pacerItemSentenceStart
)

// pacerItem 定义下行调度器队列中的单个调度单元。
type pacerItem struct {
	kind     pacerItemKind
	data     []byte
	sentence string
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
	Callbacks     PacerCallbacks
}

// DownlinkPacer 负责按 60 ms 实时节奏将编码后的 Opus 音频包逐包下发至 DownlinkSender，
// 并保证 tts.start、tts.sentence_start 与 tts.stop 消息的严格顺序。
type DownlinkPacer struct {
	sessionId     string
	sender        DownlinkSender
	logger        *slog.Logger
	callbacks     PacerCallbacks
	ctx           context.Context
	cancel        context.CancelFunc

	itemQueue     chan pacerItem
	finishChan    chan struct{}
	finishOnce    sync.Once
	doneChan      chan struct{}

	tickerFactory func(time.Duration) Ticker
	frameDuration time.Duration

	mu           sync.Mutex
	hasSentStart bool
	hasSentStop  bool
	stopped      bool
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

// Enqueue 将单个编码完成的 Opus 音频包存入下行发送队列，队列满时阻塞等待（背压机制）。
func (p *DownlinkPacer) Enqueue(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}

	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return ErrPacerStopped
	}
	p.mu.Unlock()

	// 跨异步边界独立深拷贝数据
	copied := make([]byte, len(packet))
	copy(copied, packet)

	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.itemQueue <- pacerItem{kind: pacerItemAudio, data: copied}:
		return nil
	}
}

// EnqueueSentenceStart 将单句字幕通知存入下行发送队列，确保与对应音频帧保持严格顺序。
func (p *DownlinkPacer) EnqueueSentenceStart(sentence string) error {
	if sentence == "" {
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
	case p.itemQueue <- pacerItem{kind: pacerItemSentenceStart, sentence: sentence}:
		return nil
	}
}

// FinishInput 标记上游 TTS PCM 输入与分帧编码已全部完成。
func (p *DownlinkPacer) FinishInput() {
	p.finishOnce.Do(func() {
		close(p.finishChan)
	})
}

// Stop 立即停止节奏调度器并清空未发送的数据包。
func (p *DownlinkPacer) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()

	p.cancel()
	p.drainQueue()
}

// Abort 中止当前播放，清空积压并在已发送 start 且未发送 stop 时下发 tts.stop。
func (p *DownlinkPacer) Abort() {
	p.mu.Lock()
	p.stopped = true
	needStop := p.hasSentStart && !p.hasSentStop
	p.hasSentStop = true
	p.mu.Unlock()

	p.cancel()
	p.drainQueue()

	if drainer, ok := p.sender.(PendingDrainer); ok && drainer != nil {
		drainer.DrainPending()
	}

	if needStop && p.sender != nil {
		stopBytes, err := EncodeTTSStopMessage(p.sessionId)
		if err == nil {
			_ = p.sender.SendText(context.Background(), stopBytes)
		}
	}
}

// Done 返回节奏调度器完全退出后的信号通道。
func (p *DownlinkPacer) Done() <-chan struct{} {
	return p.doneChan
}

// HasSentStart 返回是否已发送过 tts.start 消息。
func (p *DownlinkPacer) HasSentStart() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hasSentStart
}

// HasSentStop 返回是否已发送过 tts.stop 消息。
func (p *DownlinkPacer) HasSentStop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hasSentStop
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

// Run 启动下行节奏调度循环，阻塞直至当前轮次音频全部播放完毕、发生中断或上下文取消。
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
			if item.kind == pacerItemSentenceStart {
				if err := p.sendTTSSentenceStart(item.sentence); err != nil {
					p.handleError(err)
					return
				}
				continue
			}

			if err := p.sendPacket(item.data); err != nil {
				p.handleError(err)
				return
			}
			if ticker == nil {
				ticker = p.tickerFactory(p.frameDuration)
			}
			if !p.waitTick(ticker) {
				return
			}

		case <-p.finishChan:
			p.drainRemaining(ticker)
			return
		}
	}
}

// drainRemaining 在输入已结束的情况下，按 60 ms 节奏发送队列中剩余的全部数据包并下发 stop。
func (p *DownlinkPacer) drainRemaining(ticker Ticker) {
	for {
		select {
		case <-p.ctx.Done():
			return
		case item := <-p.itemQueue:
			if item.kind == pacerItemSentenceStart {
				if err := p.sendTTSSentenceStart(item.sentence); err != nil {
					p.handleError(err)
					return
				}
				continue
			}

			if err := p.sendPacket(item.data); err != nil {
				p.handleError(err)
				return
			}
			if ticker == nil {
				ticker = p.tickerFactory(p.frameDuration)
			}
			if !p.waitTick(ticker) {
				return
			}
		default:
			p.finishTurn()
			return
		}
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

// sendTTSSentenceStart 在当前句子音频即将播放时下发 tts.sentence_start 文本消息。
func (p *DownlinkPacer) sendTTSSentenceStart(sentence string) error {
	if sentence == "" {
		return nil
	}

	// 确保在下发第一句字幕前，先触发 tts.start 状态切换
	if err := p.sendTTSStart(); err != nil {
		return err
	}

	startBytes, err := EncodeTTSSentenceStartMessage(p.sessionId, sentence)
	if err != nil {
		return fmt.Errorf("encode sentence start message: %w", err)
	}

	if p.sender != nil {
		if err := p.sender.SendText(p.ctx, startBytes); err != nil {
			return fmt.Errorf("send sentence start text message: %w", err)
		}
	}
	return nil
}

// sendTTSStart 发送 tts.start 文本消息并触发 OnStarted 回调。
func (p *DownlinkPacer) sendTTSStart() error {
	p.mu.Lock()
	if p.hasSentStart || p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.hasSentStart = true
	p.mu.Unlock()

	startBytes, err := EncodeTTSStartMessage(p.sessionId)
	if err != nil {
		return fmt.Errorf("encode tts start message: %w", err)
	}

	if p.sender != nil {
		if err := p.sender.SendText(p.ctx, startBytes); err != nil {
			return fmt.Errorf("send tts start message: %w", err)
		}
	}

	if p.callbacks.OnStarted != nil {
		p.callbacks.OnStarted()
	}
	return nil
}

// sendPacket 发送单个 Opus 音频包，并在首包就绪时确保先发送 tts.start 文本消息。
func (p *DownlinkPacer) sendPacket(pkt []byte) error {
	p.mu.Lock()
	needStart := !p.hasSentStart
	p.mu.Unlock()

	if needStart {
		if err := p.sendTTSStart(); err != nil {
			p.logger.Warn("failed to send tts start", "error", err)
			return err
		}
	}

	if p.sender != nil {
		if err := p.sender.SendBinary(p.ctx, pkt); err != nil {
			if !errors.Is(err, context.Canceled) {
				p.logger.Warn("failed to send downlink opus binary", "error", err)
			}
			return err
		}
	}
	return nil
}

// finishTurn 正常发送 tts.stop 并触发 OnCompleted 回调。
func (p *DownlinkPacer) finishTurn() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	wasStarted := p.hasSentStart
	needStop := wasStarted && !p.hasSentStop
	if needStop {
		p.hasSentStop = true
	}
	p.mu.Unlock()

	if needStop && p.sender != nil {
		stopBytes, err := EncodeTTSStopMessage(p.sessionId)
		if err != nil {
			p.logger.Error("failed to encode tts stop message", "error", err)
			p.handleError(err)
			return
		}
		if sendErr := p.sender.SendText(p.ctx, stopBytes); sendErr != nil {
			p.logger.Warn("failed to send tts stop message", "error", sendErr)
			p.handleError(sendErr)
			return
		}
	}

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
