package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/voice"
)

// 哨兵错误定义。
var (
	ErrOutboundClosed = errors.New("outbound actor is closed")
	ErrTurnAborted    = errors.New("turn is aborted")
	ErrOutboundFailed = errors.New("outbound connection failed")
)

// WSConn 定义底层 WebSocket 串行写入所需的最小连接接口。
type WSConn interface {
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
}

type outboundItemType int

const (
	itemText outboundItemType = iota
	itemBinary
)

type outboundItem struct {
	typ     outboundItemType
	payload []byte
}

type outboundBatch struct {
	turnId uint64 // 0 表示 Session 作用域，>0 表示 Turn 作用域
	items  []outboundItem
	done   chan error
}

// OutboundActor 独占底层 WebSocket 连接写入，管理作用域、Batch 顺序与精准失效。
type OutboundActor struct {
	conn         WSConn
	queue        chan outboundBatch
	writeTimeout time.Duration
	logger       *slog.Logger

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once

	mu           sync.Mutex
	invalidTurns map[uint64]bool
	failed       bool
	lastErr      error
	onFailed     func(err error)
}

// NewOutboundActor 创建并启动 Outbound Actor 写入协程。
func NewOutboundActor(
	ctx context.Context,
	conn WSConn,
	capacity int,
	writeTimeout time.Duration,
	l *slog.Logger,
	onFailed func(err error),
) *OutboundActor {
	if conn == nil {
		panic("session: outbound actor requires non-nil WSConn")
	}
	if l == nil {
		l = slog.Default()
	}
	if capacity <= 0 {
		capacity = 100
	}
	if writeTimeout <= 0 {
		writeTimeout = 5 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}

	actCtx, cancel := context.WithCancel(ctx)
	actor := &OutboundActor{
		conn:         conn,
		queue:        make(chan outboundBatch, capacity),
		writeTimeout: writeTimeout,
		logger:       l,
		ctx:          actCtx,
		cancel:       cancel,
		done:         make(chan struct{}),
		invalidTurns: make(map[uint64]bool),
		onFailed:     onFailed,
	}

	go actor.writeLoop()
	return actor
}

func (a *OutboundActor) writeLoop() {
	defer close(a.done)

	for {
		select {
		case <-a.ctx.Done():
			a.drainOnClose(a.ctx.Err())
			return
		case batch, ok := <-a.queue:
			if !ok {
				a.drainOnClose(ErrOutboundClosed)
				return
			}

			// 检查是否已被精准失效
			a.mu.Lock()
			if batch.turnId > 0 && a.invalidTurns[batch.turnId] {
				a.mu.Unlock()
				if batch.done != nil {
					batch.done <- ErrTurnAborted
				}
				continue
			}
			a.mu.Unlock()

			// 执行原子 Batch 写入
			var batchErr error
			for _, item := range batch.items {
				writeCtx, writeCancel := context.WithTimeout(a.ctx, a.writeTimeout)
				msgType := websocket.MessageText
				if item.typ == itemBinary {
					msgType = websocket.MessageBinary
				}
				err := a.conn.Write(writeCtx, msgType, item.payload)
				writeCancel()

				if err != nil {
					batchErr = fmt.Errorf("websocket write: %w", err)
					break
				}
			}

			if batchErr != nil {
				a.mu.Lock()
				a.failed = true
				a.lastErr = batchErr
				a.mu.Unlock()

				if batch.done != nil {
					batch.done <- batchErr
				}

				if a.onFailed != nil {
					a.onFailed(batchErr)
				}

				a.drainOnClose(batchErr)
				return
			}

			if batch.done != nil {
				batch.done <- nil
			}
		}
	}
}

func (a *OutboundActor) drainOnClose(err error) {
	if err == nil {
		err = ErrOutboundClosed
	}
	for {
		select {
		case b, ok := <-a.queue:
			if !ok {
				return
			}
			if b.done != nil {
				b.done <- err
			}
		default:
			return
		}
	}
}

// InvalidateTurn 精准失效指定 Turn 的未开始写入项。
func (a *OutboundActor) InvalidateTurn(turnId uint64) {
	if turnId == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.invalidTurns[turnId] = true
}

// SendTextSession 同步发送 Session 作用域的文本消息。
func (a *OutboundActor) SendTextSession(ctx context.Context, payload []byte) error {
	return a.sendBatchSync(ctx, outboundBatch{
		turnId: 0,
		items: []outboundItem{
			{typ: itemText, payload: payload},
		},
	})
}

func (a *OutboundActor) sendBatchSync(ctx context.Context, b outboundBatch) error {
	if ctx == nil {
		ctx = context.Background()
	}

	a.mu.Lock()
	if a.failed {
		err := a.lastErr
		a.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrOutboundFailed, err)
	}
	if b.turnId > 0 && a.invalidTurns[b.turnId] {
		a.mu.Unlock()
		return ErrTurnAborted
	}
	a.mu.Unlock()

	done := make(chan error, 1)
	b.done = done

	select {
	case <-a.ctx.Done():
		return ErrOutboundClosed
	case <-ctx.Done():
		return ctx.Err()
	case a.queue <- b:
	}

	select {
	case <-a.ctx.Done():
		return ErrOutboundClosed
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// Close 关闭 Outbound Actor 并等待后台写入协程退出。
func (a *OutboundActor) Close() {
	a.closeOnce.Do(func() {
		a.cancel()
		<-a.done
	})
}

// NewTurnOutput 为单轮问答创建专属的 TurnOutput 实例。
func (a *OutboundActor) NewTurnOutput(turnId uint64, sessionId string) voice.TurnOutput {
	return &turnOutputImpl{
		outbound:  a,
		turnId:    turnId,
		sessionId: sessionId,
	}
}

type turnOutputImpl struct {
	outbound   *OutboundActor
	turnId     uint64
	sessionId  string
	mu         sync.Mutex
	ttsStarted bool
	ended      bool
}

// SendSTT 下发 STT 识别文本。
func (o *turnOutputImpl) SendSTT(ctx context.Context, text string) error {
	data, err := EncodeSTTMessage(o.sessionId, text)
	if err != nil {
		return err
	}

	return o.outbound.sendBatchSync(ctx, outboundBatch{
		turnId: o.turnId,
		items: []outboundItem{
			{typ: itemText, payload: data},
		},
	})
}

// SendAudio 下发单包 Opus 音频及其关联的字幕标记。
func (o *turnOutputImpl) SendAudio(ctx context.Context, frame voice.AudioFrame) error {
	o.mu.Lock()
	started := o.ttsStarted
	o.mu.Unlock()

	var items []outboundItem

	if !started {
		// 首帧原子 Batch: tts.start + sentence_start (若有) + Opus 音频
		startData, err := EncodeTTSStartMessage(o.sessionId)
		if err != nil {
			return err
		}
		items = append(items, outboundItem{typ: itemText, payload: startData})

		for _, s := range frame.SentenceStarts {
			sData, sErr := EncodeTTSSentenceStartMessage(o.sessionId, s)
			if sErr != nil {
				return sErr
			}
			items = append(items, outboundItem{typ: itemText, payload: sData})
		}

		items = append(items, outboundItem{typ: itemBinary, payload: frame.OpusData})
	} else {
		// 后续帧：sentence_start (若有) + Opus 音频
		for _, s := range frame.SentenceStarts {
			sData, sErr := EncodeTTSSentenceStartMessage(o.sessionId, s)
			if sErr != nil {
				return sErr
			}
			items = append(items, outboundItem{typ: itemText, payload: sData})
		}
		items = append(items, outboundItem{typ: itemBinary, payload: frame.OpusData})
	}

	err := o.outbound.sendBatchSync(ctx, outboundBatch{
		turnId: o.turnId,
		items:  items,
	})
	if err != nil {
		return err
	}

	if !started {
		o.mu.Lock()
		o.ttsStarted = true
		o.mu.Unlock()
	}

	return nil
}

// End 终结单轮输出，依据 tts.start 实际写入状态严格补发 tts.stop。
func (o *turnOutputImpl) End(ctx context.Context, reason voice.TurnEndReason) error {
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return nil
	}
	o.ended = true
	started := o.ttsStarted
	o.mu.Unlock()

	if !started {
		// tts.start 未曾写出，绝对不发 tts.stop
		return nil
	}

	// tts.start 已真实写出，必须且仅补发一次 tts.stop
	stopData, err := EncodeTTSStopMessage(o.sessionId)
	if err != nil {
		return err
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return o.outbound.sendBatchSync(stopCtx, outboundBatch{
		turnId: o.turnId,
		items: []outboundItem{
			{typ: itemText, payload: stopData},
		},
	})
}
