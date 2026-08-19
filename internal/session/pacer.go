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
	// ErrDownlinkQueueFull 下行音频队列满载背压拒绝错误。
	ErrDownlinkQueueFull = errors.New("downlink opus queue is full")

	// ErrPacerStopped 节奏调度器已停止错误。
	ErrPacerStopped = errors.New("downlink pacer is stopped")
)

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

// DownlinkPacer 负责按 60 ms 实时节奏将编码后的 Opus 音频包逐包下发至 Writer，
// 并协调 tts.start 与 tts.stop 消息的精确顺序及状态转换。
type DownlinkPacer struct {
	session *Session
	writer  *Writer
	logger  *slog.Logger
	gen     uint64
	ctx     context.Context
	cancel  context.CancelFunc

	packetQueue chan []byte
	finishChan  chan struct{}
	finishOnce  sync.Once
	doneChan    chan struct{}

	tickerFactory func(time.Duration) Ticker
	frameDuration time.Duration

	mu            sync.Mutex
	hasSentStart  bool
	hasSentStop   bool
	stopped       bool
	userText      string
	assistantText string
}

// NewDownlinkPacer 创建指定代次的下行 60 ms 节奏调度器。
func NewDownlinkPacer(ctx context.Context, session *Session, gen uint64, queueCap int, tickerFactory func(time.Duration) Ticker) *DownlinkPacer {
	if queueCap <= 0 {
		queueCap = DefaultWriteQueueCapacity
	}
	if tickerFactory == nil {
		tickerFactory = defaultTickerFactory
	}
	if ctx == nil {
		ctx = context.Background()
	}

	pacerCtx, cancel := context.WithCancel(ctx)

	var w *Writer
	var l *slog.Logger
	if session != nil {
		w = session.Writer()
		l = session.logger
	}
	if l == nil {
		l = slog.Default()
	}

	return &DownlinkPacer{
		session:       session,
		writer:        w,
		logger:        l,
		gen:           gen,
		ctx:           pacerCtx,
		cancel:        cancel,
		packetQueue:   make(chan []byte, queueCap),
		finishChan:    make(chan struct{}),
		doneChan:      make(chan struct{}),
		tickerFactory: tickerFactory,
		frameDuration: DefaultDownlinkFrameDuration,
	}
}

// sessionID 返回关联会话的会话标识。
func (p *DownlinkPacer) sessionID() string {
	if p.session != nil {
		id := p.session.SessionID()
		if id != "" {
			return id
		}
	}
	return "pacer-session"
}

// Enqueue 将单个编码完成的 Opus 音频包存入下行发送队列，队列满时触发背压。
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
	case p.packetQueue <- copied:
		return nil
	default:
		p.logger.Warn("downlink opus queue full, triggering backpressure",
			"session_id", p.sessionID(),
			"generation", p.gen,
			"capacity", cap(p.packetQueue),
		)
		if p.session != nil {
			p.session.PostError(p.gen, ErrDownlinkQueueFull, true)
		}
		return ErrDownlinkQueueFull
	}
}

// FinishInput 标记上游 TTS PCM 输入与分帧编码已全部完成。
// 可选传入本轮对话的用户文本与完整助手回复。
func (p *DownlinkPacer) FinishInput(turnTexts ...string) {
	p.mu.Lock()
	if len(turnTexts) > 0 {
		p.userText = turnTexts[0]
	}
	if len(turnTexts) > 1 {
		p.assistantText = turnTexts[1]
	}
	p.mu.Unlock()

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

// drainQueue 清空队列中残留的数据包。
func (p *DownlinkPacer) drainQueue() {
	for {
		select {
		case <-p.packetQueue:
		default:
			return
		}
	}
}

// Run 启动下行节奏调度循环，阻塞直至当前轮次音频全部播放完毕、发生中断或会话取消。
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

		case pkt := <-p.packetQueue:
			if err := p.sendPacket(pkt); err != nil {
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

// drainRemaining 在输入已结束的情况下，按 60 ms 节奏发送队列中剩余的全部数据包。
func (p *DownlinkPacer) drainRemaining(ticker Ticker) {
	for {
		select {
		case <-p.ctx.Done():
			return
		case pkt := <-p.packetQueue:
			if err := p.sendPacket(pkt); err != nil {
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

// sendTTSStart 发送 tts.start 文本消息并将 Session 状态置为 StateSpeaking。
func (p *DownlinkPacer) sendTTSStart() error {
	p.mu.Lock()
	if p.hasSentStart || p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.hasSentStart = true
	p.mu.Unlock()

	sessionID := p.sessionID()
	startBytes, err := EncodeTTSStartMessage(sessionID)
	if err != nil {
		return fmt.Errorf("encode tts start message: %w", err)
	}

	if p.session != nil {
		if err := p.session.sendTextMessage(startBytes); err != nil {
			return fmt.Errorf("send tts start message: %w", err)
		}
		p.session.transitionToSpeaking(p.gen)
	} else if p.writer != nil {
		if err := p.writer.SendText(p.ctx, startBytes); err != nil {
			return fmt.Errorf("send tts start text message: %w", err)
		}
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
			p.logger.Warn("failed to send tts start", "error", err, "generation", p.gen)
			if !errors.Is(err, context.Canceled) && p.session != nil {
				p.session.PostError(p.gen, err, true)
			}
			return err
		}
	}

	if p.writer != nil {
		if err := p.writer.SendBinary(p.ctx, pkt); err != nil {
			if !errors.Is(err, context.Canceled) {
				p.logger.Warn("failed to send downlink opus binary", "error", err, "generation", p.gen)
				if p.session != nil {
					p.session.PostError(p.gen, err, true)
				}
			}
			return err
		}
	}
	return nil
}

// finishTurn 结束当前问答轮次并通知 Session。
func (p *DownlinkPacer) finishTurn() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	uText := p.userText
	aText := p.assistantText
	p.mu.Unlock()

	if p.session != nil {
		p.session.PostTurnFinished(p.gen, uText, aText)
	}
}
