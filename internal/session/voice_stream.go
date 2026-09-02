package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

const (
	// DefaultSentenceQueueCapacity 文本句队列固定容量。
	DefaultSentenceQueueCapacity = 100
)

// 语音流相关的哨兵错误定义。
var (
	// ErrVoiceStreamClosed 表示语音流服务已关闭。
	ErrVoiceStreamClosed = errors.New("voice stream is closed")

	// ErrTurnMismatch 表示请求的轮次 Id 与当前活跃轮次不匹配。
	ErrTurnMismatch = errors.New("turn id mismatch")

	// ErrNoActiveTurn 表示当前没有处于活跃状态的语音轮次。
	ErrNoActiveTurn = errors.New("no active turn")

	// ErrTurnFinished 表示当前轮次文本输入已结束，拒绝继续追加文本。
	ErrTurnFinished = errors.New("turn already finished")

	// ErrTTSClientNil 表示未配置有效的 TTS 客户端。
	ErrTTSClientNil = errors.New("tts client is nil")

	// ErrWriterNil 表示未配置有效的下行写接口。
	ErrWriterNil = errors.New("voice writer is nil")
)

// VoiceWriter 定义语音流所依赖的底层语音下发接口契约。
type VoiceWriter interface {
	SendVoiceTextWait(ctx context.Context, turnId uint64, payload []byte) error
	SendVoiceBinaryWait(ctx context.Context, turnId uint64, payload []byte) error
	EnqueueBarrierWait(ctx context.Context, turnId uint64) error
}

// VoiceStreamEventKind 定义语音流向外部投递的事件类型。
type VoiceStreamEventKind int

const (
	// VoiceStreamEventSpeaking 表示首句已准备好下发，会话进入说话状态。
	VoiceStreamEventSpeaking VoiceStreamEventKind = iota

	// VoiceStreamEventSuccess 表示本轮所有语音已完成合成、编码与设备写出确认。
	VoiceStreamEventSuccess

	// VoiceStreamEventFailed 表示语音流在合成、编码或下发过程中发生不可恢复错误。
	VoiceStreamEventFailed
)

// VoiceStreamEvent 封装语音流生命周期事件，严格携带 turnId。
type VoiceStreamEvent struct {
	TurnId uint64
	Kind   VoiceStreamEventKind
	Err    error
}

// sentenceJob 封装文本句队列中的单个句子任务。
type sentenceJob struct {
	turnId   uint64
	sequence uint32
	text     string
	isEnd    bool
}

// pcmJob 封装 TTS PCM 队列中的单次音频块或句末标记。
type pcmJob struct {
	turnId        uint64
	sequence      uint32
	data          []byte
	endOfSentence bool
	flushDone     chan error
}

// downlinkFrameKind 定义下行语音帧的类型。
type downlinkFrameKind int

const (
	frameKindText downlinkFrameKind = iota
	frameKindBinary
	frameKindBarrier
)

// downlinkFrame 封装设备下行语音帧队列中的单个数据帧或控制屏障。
type downlinkFrame struct {
	turnId    uint64
	sequence  uint32
	kind      downlinkFrameKind
	payload   []byte
	barrierCh chan error
}

// voiceStreamTurn 维护单轮问答独占持有的队列、工作协程与运行时状态。
type voiceStreamTurn struct {
	turnId    uint64
	sessionId string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// 三个有界队列
	sentenceQueue chan sentenceJob
	pcmQueue      chan pcmJob
	downlinkQueue chan downlinkFrame

	// 依赖服务
	ttsClient ai.TTSClient
	writer    VoiceWriter
	cfg       SessionConfig
	logger    *slog.Logger
	onEvent   func(VoiceStreamEvent)

	// 切句与状态（在投递线程中受互斥锁保护）
	mu            sync.Mutex
	splitter      *SentenceSplitter
	lastIteration int
	iterationInit bool
	sentenceSeq   uint32
	turnFinished  bool
	activeWorkers atomic.Int32

	// 仅由句子工作协程独占访问的资源（保证无并发竞争）
	ttsStream    ai.TTSStream
	startedSent  bool
	hasSentences bool
}

// newVoiceStreamTurn 创建单轮问答的语音流运行时。
func newVoiceStreamTurn(
	ctx context.Context,
	turnId uint64,
	sessionId string,
	ttsClient ai.TTSClient,
	writer VoiceWriter,
	cfg SessionConfig,
	logger *slog.Logger,
	onEvent func(VoiceStreamEvent),
) *voiceStreamTurn {
	turnCtx, turnCancel := context.WithCancel(ctx)

	sentenceCap := DefaultSentenceQueueCapacity
	pcmCap := cfg.TTSPCMQueueCapacity
	if pcmCap <= 0 {
		pcmCap = DefaultWriteQueueCapacity
	}
	downlinkCap := cfg.DownlinkOpusQueueCapacity
	if downlinkCap <= 0 {
		downlinkCap = DefaultWriteQueueCapacity
	}

	return &voiceStreamTurn{
		turnId:        turnId,
		sessionId:     sessionId,
		ctx:           turnCtx,
		cancel:        turnCancel,
		sentenceQueue: make(chan sentenceJob, sentenceCap),
		pcmQueue:      make(chan pcmJob, pcmCap),
		downlinkQueue: make(chan downlinkFrame, downlinkCap),
		ttsClient:     ttsClient,
		writer:        writer,
		cfg:           cfg,
		logger:        logger,
		onEvent:       onEvent,
		splitter:      NewSentenceSplitter(),
	}
}

// start 启动本轮规定的三个工作协程：句子工作协程、编码工作协程、下行工作协程。
func (t *voiceStreamTurn) start() {
	t.wg.Add(3)
	t.activeWorkers.Store(3)

	go t.sentenceWorker()
	go t.encoderWorker()
	go t.downlinkWorker()
}

// emit 向外部投递语音流生命周期事件。
func (t *voiceStreamTurn) emit(ev VoiceStreamEvent) {
	if t.onEvent != nil {
		t.onEvent(ev)
	}
}

// sentenceWorker 句子工作协程：唯一消费文本句队列，唯一持有 TTSStream，驱动流式语音合成。
func (t *voiceStreamTurn) sentenceWorker() {
	defer func() {
		if t.ttsStream != nil {
			if err := t.ttsStream.Close(); err != nil {
				t.logger.Warn("failed to close tts stream", "turnId", t.turnId, "error", err)
			}
			t.ttsStream = nil
		}
		t.wg.Done()
		t.activeWorkers.Add(-1)
	}()

	for {
		select {
		case <-t.ctx.Done():
			return
		case job, ok := <-t.sentenceQueue:
			if !ok {
				return
			}
			if job.turnId != t.turnId {
				continue
			}

			if job.isEnd {
				t.handleTextEnd()
				return
			}

			if err := t.processSentence(job); err != nil {
				return
			}
		}
	}
}

// processSentence 处理单条句子的建连、状态帧投递、TTS 合成及句末刷新同步。
func (t *voiceStreamTurn) processSentence(job sentenceJob) error {
	t.hasSentences = true

	// 首句出现时按需建立本轮唯一的物理 TTS 连接
	if t.ttsStream == nil {
		if t.ttsClient == nil {
			t.logger.Warn("tts client is nil", "turnId", t.turnId)
			t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: ErrTTSClientNil})
			t.cancel()
			return ErrTTSClientNil
		}
		stream, err := t.ttsClient.CreateStream(t.ctx)
		if err != nil {
			t.logger.Warn("failed to create tts stream", "turnId", t.turnId, "error", err)
			t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
			t.cancel()
			return err
		}
		t.ttsStream = stream
	}

	// 首次合成前下发 tts/start 并通知 speaking 状态
	if !t.startedSent {
		startMsg, err := EncodeTTSStartMessage(t.sessionId)
		if err != nil {
			t.logger.Warn("failed to encode tts start message", "turnId", t.turnId, "error", err)
			t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
			t.cancel()
			return err
		}
		startFrame := downlinkFrame{
			turnId:   t.turnId,
			sequence: 0,
			kind:     frameKindText,
			payload:  startMsg,
		}
		select {
		case <-t.ctx.Done():
			return t.ctx.Err()
		case t.downlinkQueue <- startFrame:
		}

		t.startedSent = true
		t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventSpeaking})
	}

	// 下发本句的 tts/sentence_start
	sentenceStartMsg, err := EncodeTTSSentenceStartMessage(t.sessionId, job.text)
	if err != nil {
		t.logger.Warn("failed to encode tts sentence start message", "turnId", t.turnId, "error", err)
		t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
		t.cancel()
		return err
	}
	sentenceStartFrame := downlinkFrame{
		turnId:   t.turnId,
		sequence: job.sequence,
		kind:     frameKindText,
		payload:  sentenceStartMsg,
	}
	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	case t.downlinkQueue <- sentenceStartFrame:
	}

	// 调用本轮 TTSStream 执行单句合成
	synthErr := t.ttsStream.SynthesizeSentence(t.ctx, job.text, func(pcmCtx context.Context, pcm []byte) error {
		if len(pcm) == 0 {
			return nil
		}
		item := pcmJob{
			turnId:   t.turnId,
			sequence: job.sequence,
			data:     pcm,
		}
		select {
		case <-t.ctx.Done():
			return t.ctx.Err()
		case <-pcmCtx.Done():
			return pcmCtx.Err()
		case t.pcmQueue <- item:
			return nil
		}
	})
	if synthErr != nil {
		if !errors.Is(synthErr, context.Canceled) {
			t.logger.Warn("failed to synthesize sentence", "turnId", t.turnId, "error", synthErr)
			t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: synthErr})
		}
		t.cancel()
		return synthErr
	}

	// 发送句末标记，阻塞等待编码协程完成残余 Flush 且 Opus 帧全部进入下行队列
	flushDone := make(chan error, 1)
	flushItem := pcmJob{
		turnId:        t.turnId,
		sequence:      job.sequence,
		endOfSentence: true,
		flushDone:     flushDone,
	}
	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	case t.pcmQueue <- flushItem:
	}

	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	case err := <-flushDone:
		if err != nil {
			t.logger.Warn("failed to flush sentence pcm", "turnId", t.turnId, "error", err)
			t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
			t.cancel()
			return err
		}
	}

	return nil
}

// handleTextEnd 处理本轮文本全部输入完成后的收尾流程与写出确认。
func (t *voiceStreamTurn) handleTextEnd() {
	if !t.hasSentences {
		// 整轮无文本时无需建连与下发协议帧，直接报告成功并结束
		t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventSuccess})
		t.cancel()
		return
	}

	// 所有句子合成完成，幂等关闭本轮 TTSStream
	if t.ttsStream != nil {
		if err := t.ttsStream.Close(); err != nil {
			t.logger.Warn("failed to close tts stream after all sentences", "turnId", t.turnId, "error", err)
		}
		t.ttsStream = nil
	}

	// 若已发送 start，则排入 stop 与屏障进行真实写出确认
	if t.startedSent {
		stopMsg, err := EncodeTTSStopMessage(t.sessionId)
		if err != nil {
			t.logger.Warn("failed to encode tts stop message", "turnId", t.turnId, "error", err)
			t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
			t.cancel()
			return
		}
		stopFrame := downlinkFrame{
			turnId:   t.turnId,
			sequence: 0,
			kind:     frameKindText,
			payload:  stopMsg,
		}
		select {
		case <-t.ctx.Done():
			return
		case t.downlinkQueue <- stopFrame:
		}

		barrierCh := make(chan error, 1)
		barrierFrame := downlinkFrame{
			turnId:    t.turnId,
			sequence:  0,
			kind:      frameKindBarrier,
			barrierCh: barrierCh,
		}
		select {
		case <-t.ctx.Done():
			return
		case t.downlinkQueue <- barrierFrame:
		}

		// 阻塞等待下行协程与 Writer 屏障确认前序全部帧已写出
		select {
		case <-t.ctx.Done():
			return
		case err := <-barrierCh:
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.logger.Warn("writer barrier failed after tts stop", "turnId", t.turnId, "error", err)
					t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
				}
				t.cancel()
				return
			}
		}
	}

	// 真实写出确认成功，投递轮次成功事件并退出
	t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventSuccess})
	t.cancel()
}

// encoderWorker 编码工作协程：唯一消费 PCM 队列，唯一持有 StreamEncoder，负责 Opus 分帧编码。
func (t *voiceStreamTurn) encoderWorker() {
	defer func() {
		t.wg.Done()
		t.activeWorkers.Add(-1)
	}()

	enc, err := audio.NewEncoder(t.cfg.MaxOpusPacketBytes)
	if err != nil {
		t.logger.Error("failed to create opus encoder", "turnId", t.turnId, "error", err)
		t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
		return
	}
	defer enc.Close()

	streamEnc := audio.NewStreamEncoder(enc)

	for {
		select {
		case <-t.ctx.Done():
			return
		case job, ok := <-t.pcmQueue:
			if !ok {
				return
			}
			if job.turnId != t.turnId {
				continue
			}

			if len(job.data) > 0 {
				packets, err := streamEnc.Feed(job.data)
				if err != nil {
					t.logger.Error("failed to feed pcm to encoder", "turnId", t.turnId, "error", err)
					t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
					t.cancel()
					return
				}
				for _, pkt := range packets {
					frame := downlinkFrame{
						turnId:   t.turnId,
						sequence: job.sequence,
						kind:     frameKindBinary,
						payload:  pkt,
					}
					select {
					case <-t.ctx.Done():
						return
					case t.downlinkQueue <- frame:
					}
				}
			}

			if job.endOfSentence {
				packets, err := streamEnc.Flush()
				if err != nil {
					t.logger.Error("failed to flush encoder at sentence end", "turnId", t.turnId, "error", err)
					if job.flushDone != nil {
						job.flushDone <- err
					}
					t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
					t.cancel()
					return
				}
				for _, pkt := range packets {
					frame := downlinkFrame{
						turnId:   t.turnId,
						sequence: job.sequence,
						kind:     frameKindBinary,
						payload:  pkt,
					}
					select {
					case <-t.ctx.Done():
						return
					case t.downlinkQueue <- frame:
					}
				}
				if job.flushDone != nil {
					job.flushDone <- nil
				}
			}
		}
	}
}

// downlinkWorker 下行工作协程：唯一消费下行语音帧队列，独占调用 Writer 发送文本帧、二进制帧并处理屏障。
func (t *voiceStreamTurn) downlinkWorker() {
	defer func() {
		t.wg.Done()
		t.activeWorkers.Add(-1)
	}()

	for {
		select {
		case <-t.ctx.Done():
			return
		case frame, ok := <-t.downlinkQueue:
			if !ok {
				return
			}
			if frame.turnId != t.turnId {
				continue
			}

			if t.writer == nil {
				t.logger.Warn("voice writer is nil in downlink worker", "turnId", t.turnId)
				t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: ErrWriterNil})
				t.cancel()
				return
			}

			switch frame.kind {
			case frameKindText:
				if err := t.writer.SendVoiceTextWait(t.ctx, frame.turnId, frame.payload); err != nil {
					if !errors.Is(err, context.Canceled) {
						t.logger.Warn("failed to send voice text frame", "turnId", t.turnId, "error", err)
						t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
					}
					t.cancel()
					return
				}
			case frameKindBinary:
				if err := t.writer.SendVoiceBinaryWait(t.ctx, frame.turnId, frame.payload); err != nil {
					if !errors.Is(err, context.Canceled) {
						t.logger.Warn("failed to send voice binary frame", "turnId", t.turnId, "error", err)
						t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
					}
					t.cancel()
					return
				}
			case frameKindBarrier:
				err := t.writer.EnqueueBarrierWait(t.ctx, frame.turnId)
				if frame.barrierCh != nil {
					frame.barrierCh <- err
				}
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						t.logger.Warn("failed to wait writer barrier in downlink worker", "turnId", t.turnId, "error", err)
						t.emit(VoiceStreamEvent{TurnId: t.turnId, Kind: VoiceStreamEventFailed, Err: err})
					}
					t.cancel()
					return
				}
			}
		}
	}
}

// VoiceStreamOptions 聚合构造 VoiceStream 的依赖与配置。
type VoiceStreamOptions struct {
	SessionId string
	TTSClient ai.TTSClient
	Writer    VoiceWriter
	Config    SessionConfig
	Logger    *slog.Logger
	OnEvent   func(VoiceStreamEvent)
}

// VoiceStream 负责单个会话的多轮语音流调度，管理增量切句、轮次队列与工作协程所有权。
type VoiceStream struct {
	sessionId string
	ttsClient ai.TTSClient
	writer    VoiceWriter
	cfg       SessionConfig
	logger    *slog.Logger
	onEvent   func(VoiceStreamEvent)

	mu         sync.Mutex
	activeTurn *voiceStreamTurn
	closed     bool
	done       chan struct{}
}

// NewVoiceStream 创建配置就绪的语音流管理服务。
func NewVoiceStream(opts VoiceStreamOptions) *VoiceStream {
	l := opts.Logger
	if l == nil {
		l = slog.Default()
	}
	cfg := NormalizeConfig(opts.Config)

	return &VoiceStream{
		sessionId: opts.SessionId,
		ttsClient: opts.TTSClient,
		writer:    opts.Writer,
		cfg:       cfg,
		logger:    l,
		onEvent:   opts.OnEvent,
		done:      make(chan struct{}),
	}
}

// SetSessionId 动态更新当前会话的 SessionId。
func (vs *VoiceStream) SetSessionId(sessionId string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.sessionId = sessionId
	if vs.activeTurn != nil {
		vs.activeTurn.sessionId = sessionId
	}
}

// StartTurn 为指定轮次启动语音流水线，初始化三个有界队列并创建规定的三个工作协程。
// 若已有活跃轮次，先取消旧轮次并等待其平稳退出后再初始化新轮次。
func (vs *VoiceStream) StartTurn(ctx context.Context, turnId uint64) error {
	vs.mu.Lock()
	if vs.closed {
		vs.mu.Unlock()
		return ErrVoiceStreamClosed
	}

	if vs.activeTurn != nil {
		oldTurn := vs.activeTurn
		oldTurn.cancel()
		vs.mu.Unlock()
		oldTurn.wg.Wait()
		vs.mu.Lock()
		if vs.closed {
			vs.mu.Unlock()
			return ErrVoiceStreamClosed
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	turn := newVoiceStreamTurn(
		ctx,
		turnId,
		vs.sessionId,
		vs.ttsClient,
		vs.writer,
		vs.cfg,
		vs.logger,
		vs.onEvent,
	)
	vs.activeTurn = turn
	turn.start()
	vs.mu.Unlock()

	return nil
}

// FeedText 接收 LLM 流式文本分块并执行增量切句，将切出的完整句子按序投递至文本句队列。
func (vs *VoiceStream) FeedText(ctx context.Context, turnId uint64, text string, iteration int) error {
	vs.mu.Lock()
	if vs.closed {
		vs.mu.Unlock()
		return ErrVoiceStreamClosed
	}
	t := vs.activeTurn
	if t == nil {
		vs.mu.Unlock()
		return ErrNoActiveTurn
	}
	if t.turnId != turnId {
		vs.mu.Unlock()
		return ErrTurnMismatch
	}
	if t.turnFinished {
		vs.mu.Unlock()
		return ErrTurnFinished
	}

	var sentences []string
	if !t.iterationInit {
		t.lastIteration = iteration
		t.iterationInit = true
	} else if t.lastIteration != iteration {
		flushed := t.splitter.Flush()
		sentences = append(sentences, flushed...)
		t.lastIteration = iteration
	}

	if text != "" {
		cut := t.splitter.Feed(text)
		sentences = append(sentences, cut...)
	}
	vs.mu.Unlock()

	for _, s := range sentences {
		seq := atomic.AddUint32(&t.sentenceSeq, 1)
		job := sentenceJob{
			turnId:   turnId,
			sequence: seq,
			text:     s,
		}
		select {
		case <-vs.done:
			return ErrVoiceStreamClosed
		case <-t.ctx.Done():
			return t.ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		case t.sentenceQueue <- job:
		}
	}

	return nil
}

// FinishText 标记当前轮次文本输入完成，Flush 切句器残余文本并向文本句队列投递结束标记。
func (vs *VoiceStream) FinishText(ctx context.Context, turnId uint64) error {
	vs.mu.Lock()
	if vs.closed {
		vs.mu.Unlock()
		return ErrVoiceStreamClosed
	}
	t := vs.activeTurn
	if t == nil {
		vs.mu.Unlock()
		return ErrNoActiveTurn
	}
	if t.turnId != turnId {
		vs.mu.Unlock()
		return ErrTurnMismatch
	}
	if t.turnFinished {
		vs.mu.Unlock()
		return ErrTurnFinished
	}
	t.turnFinished = true

	sentences := t.splitter.Flush()
	vs.mu.Unlock()

	for _, s := range sentences {
		seq := atomic.AddUint32(&t.sentenceSeq, 1)
		job := sentenceJob{
			turnId:   turnId,
			sequence: seq,
			text:     s,
		}
		select {
		case <-vs.done:
			return ErrVoiceStreamClosed
		case <-t.ctx.Done():
			return t.ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		case t.sentenceQueue <- job:
		}
	}

	endJob := sentenceJob{
		turnId: turnId,
		isEnd:  true,
	}
	select {
	case <-vs.done:
		return ErrVoiceStreamClosed
	case <-t.ctx.Done():
		return t.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case t.sentenceQueue <- endJob:
	}

	return nil
}

// CancelTurn 取消指定轮次的语音流水线。
func (vs *VoiceStream) CancelTurn(turnId uint64) {
	vs.mu.Lock()
	t := vs.activeTurn
	if t != nil && t.turnId == turnId {
		t.cancel()
	}
	vs.mu.Unlock()
}

// Close 幂等关闭语音流管理服务，终止活跃轮次并等待全部协程退出。
func (vs *VoiceStream) Close() error {
	vs.mu.Lock()
	if vs.closed {
		vs.mu.Unlock()
		return nil
	}
	vs.closed = true
	close(vs.done)

	t := vs.activeTurn
	vs.activeTurn = nil
	vs.mu.Unlock()

	if t != nil {
		t.cancel()
		t.wg.Wait()
	}
	return nil
}

// ActiveWorkers 返回当前活跃轮次中正在运行的工作协程数。
func (vs *VoiceStream) ActiveWorkers() int {
	vs.mu.Lock()
	t := vs.activeTurn
	vs.mu.Unlock()
	if t == nil {
		return 0
	}
	return int(t.activeWorkers.Load())
}

// ActiveTurnId 返回当前活跃轮次的 Id，若无活跃轮次则返回 0。
func (vs *VoiceStream) ActiveTurnId() uint64 {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.activeTurn == nil {
		return 0
	}
	return vs.activeTurn.turnId
}

// QueueCapacities 返回当前活跃轮次的文本、PCM 和下行队列容量。
func (vs *VoiceStream) QueueCapacities() (sentenceCap, pcmCap, downlinkCap int) {
	vs.mu.Lock()
	t := vs.activeTurn
	vs.mu.Unlock()
	if t == nil {
		return 0, 0, 0
	}
	return cap(t.sentenceQueue), cap(t.pcmQueue), cap(t.downlinkQueue)
}
