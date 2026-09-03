package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// turnEventType 定义轮次流水线向 Supervisor 投递的事件类型。
type turnEventType int

const (
	// turnEventASRFinal 表示 ASR 最终识别文本产出。
	turnEventASRFinal turnEventType = iota

	// turnEventPlaybackStarted 表示下行播放开始（首包音频/首句字幕下发）。
	turnEventPlaybackStarted

	// turnEventTurnCompleted 表示当前问答或提示音播放已完整结束。
	turnEventTurnCompleted

	// turnEventTurnFailed 表示当前轮次出现致命或不可恢复错误。
	turnEventTurnFailed
)

// turnEvent 封装轮次流水线投递给会话 Supervisor 的类型化事件。
type turnEvent struct {
	turnId       uint64
	typ          turnEventType
	text         string
	err          error
	fatal        bool
	closeSession bool
}

// activeTurn 保存单轮问答（一次 generation）所独占的全部运行时资源与上下文。
type activeTurn struct {
	turnId     uint64
	ctx        context.Context
	cancel     context.CancelFunc
	effects    *TurnEffects
	mode       string
	manualStop bool

	// ASR 阶段资源
	decoder   *audio.Decoder
	asrStream ai.ASRStream
	asrQueue  *audio.ASRAudioQueue

	// 下行播放阶段
	pacer *DownlinkPacer
}

// PipelineOptions 聚合构造 TurnPipeline 的依赖与配置。
type PipelineOptions struct {
	ASRClient     ai.ASRClient
	LLMClient     ai.LLMClient
	TTSClient     ai.TTSClient
	SystemPrompt  string
	Config        SessionConfig
	History       *ConversationHistory
	ToolProvider  *ToolProvider
	Logger        *slog.Logger
	TickerFactory func(time.Duration) Ticker
	PostEvent     func(turnEvent)
}

// TurnPipeline 负责会话级 AI 客户端与语音轮次流水线调度，将每轮运行时隔离在 activeTurn 中。
type TurnPipeline struct {
	asrClient     ai.ASRClient
	llmClient     ai.LLMClient
	ttsClient     ai.TTSClient
	systemPrompt  string
	cfg           SessionConfig
	history       *ConversationHistory
	toolProvider  *ToolProvider
	logger        *slog.Logger
	tickerFactory func(time.Duration) Ticker
	postEvent     func(turnEvent)

	mu         sync.Mutex
	activeTurn *activeTurn
}

// NewTurnPipeline 创建配置就绪的轮次流水线服务。
func NewTurnPipeline(opts PipelineOptions) *TurnPipeline {
	l := opts.Logger
	if l == nil {
		l = slog.Default()
	}

	hist := opts.History
	if hist == nil {
		hist = NewConversationHistory(opts.Config.MaxHistoryTurns)
	}

	return &TurnPipeline{
		asrClient:     opts.ASRClient,
		llmClient:     opts.LLMClient,
		ttsClient:     opts.TTSClient,
		systemPrompt:  opts.SystemPrompt,
		cfg:           opts.Config,
		history:       hist,
		toolProvider:  opts.ToolProvider,
		logger:        l,
		tickerFactory: opts.TickerFactory,
		postEvent:     opts.PostEvent,
	}
}

// emit 向 Supervisor 投递类型化轮次事件。
func (p *TurnPipeline) emit(ev turnEvent) {
	if p.postEvent != nil {
		p.postEvent(ev)
	}
}

// StartListening 为指定轮次启动语音输入与 ASR 流式识别。
func (p *TurnPipeline) StartListening(ctx context.Context, turnId uint64, sessionId string, mode string) error {
	p.mu.Lock()
	if p.activeTurn != nil {
		p.activeTurn.cancel()
		p.cleanupTurnResources(p.activeTurn)
	}

	turnCtx, turnCancel := context.WithCancel(ctx)
	dec, err := audio.NewDecoder(p.cfg.MaxOpusPacketBytes)
	if err != nil {
		turnCancel()
		p.mu.Unlock()
		return fmt.Errorf("init opus decoder: %w", err)
	}

	var stream ai.ASRStream
	var queue *audio.ASRAudioQueue
	if p.asrClient != nil {
		s, err := p.asrClient.CreateStream(turnCtx)
		if err != nil {
			turnCancel()
			p.mu.Unlock()
			return fmt.Errorf("create asr stream: %w", err)
		}
		stream = s
		queue = audio.NewASRAudioQueue(turnCtx, stream, p.cfg.ASRPCMQueueCapacity, p.logger)
	}

	turn := &activeTurn{
		turnId:    turnId,
		ctx:       turnCtx,
		cancel:    turnCancel,
		effects:   &TurnEffects{},
		mode:      mode,
		decoder:   dec,
		asrStream: stream,
		asrQueue:  queue,
	}
	p.activeTurn = turn
	p.mu.Unlock()

	if mode == ListenModeAuto && stream != nil {
		go p.readASRResultAuto(turnCtx, stream, turnId, sessionId)
	}

	return nil
}

// PushOpus 将客户端上行的 Opus 音频包解码并推入当前轮次的 ASR 队列。
func (p *TurnPipeline) PushOpus(turnId uint64, opusData []byte) error {
	p.mu.Lock()
	turn := p.activeTurn
	p.mu.Unlock()

	if turn == nil || turn.turnId != turnId {
		return nil
	}

	if turn.mode == ListenModeManual && turn.manualStop {
		return nil
	}

	if turn.decoder == nil {
		return errors.New("decoder not initialized")
	}

	pcmBytes, err := turn.decoder.Decode(opusData)
	if err != nil {
		return fmt.Errorf("opus decode error: %w", err)
	}

	if turn.asrQueue != nil {
		if err := turn.asrQueue.Push(pcmBytes); err != nil {
			return fmt.Errorf("asr queue push: %w", err)
		}
	}
	return nil
}

// FinishListening 手动模式结束收音，通知 ASR 队列结束输入并启动结果等待。
func (p *TurnPipeline) FinishListening(turnId uint64, sessionId string) error {
	p.mu.Lock()
	turn := p.activeTurn
	if turn == nil || turn.turnId != turnId {
		p.mu.Unlock()
		return nil
	}
	if turn.manualStop {
		p.mu.Unlock()
		return nil
	}
	turn.manualStop = true
	queue := turn.asrQueue
	stream := turn.asrStream
	turnCtx := turn.ctx
	p.mu.Unlock()

	if queue != nil {
		_ = queue.Finish()
	} else if stream != nil {
		_ = stream.Finish(turnCtx)
	}

	if stream != nil {
		go p.readASRResultManual(turnCtx, stream, queue, turnId, sessionId)
	}
	return nil
}

// StartResponse 启动 LLM 增量生成、逐句 TTS 与 60ms 下行节奏器协同流水线。
func (p *TurnPipeline) StartResponse(turnId uint64, sessionId string, userText string, sender DownlinkSender) error {
	p.mu.Lock()
	turn := p.activeTurn
	if turn == nil || turn.turnId != turnId {
		p.mu.Unlock()
		return errors.New("turn not found or mismatched")
	}

	// 释放 ASR 阶段资源
	if turn.asrQueue != nil {
		turn.asrQueue.Close()
		turn.asrQueue = nil
	}
	if turn.asrStream != nil {
		_ = turn.asrStream.Close()
		turn.asrStream = nil
	}

	turnCtx := turn.ctx
	effects := turn.effects

	if p.llmClient == nil || p.ttsClient == nil {
		p.mu.Unlock()
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    errors.New("ai clients not configured"),
			fatal:  true,
		})
		return nil
	}

	stopBytes, _ := EncodeTTSStopMessage(sessionId)
	pacer := NewDownlinkPacer(turnCtx, DownlinkPacerOptions{
		SessionId:     sessionId,
		Sender:        sender,
		QueueCap:      p.cfg.DownlinkOpusQueueCapacity,
		TickerFactory: p.tickerFactory,
		Logger:        p.logger,
		AbortPayload:  stopBytes,
		Callbacks: PacerCallbacks{
			OnStarted: func() {
				p.emit(turnEvent{
					turnId: turnId,
					typ:    turnEventPlaybackStarted,
				})
			},
			OnCompleted: func() {
				p.emit(turnEvent{
					turnId:       turnId,
					typ:          turnEventTurnCompleted,
					closeSession: effects.CloseSession,
				})
			},
			OnError: func(err error) {
				p.emit(turnEvent{
					turnId: turnId,
					typ:    turnEventTurnFailed,
					err:    err,
					fatal:  true,
				})
			},
		},
	})
	turn.pacer = pacer
	p.mu.Unlock()

	go pacer.Run()
	go p.orchestrateLLMAndTTS(turn, sessionId, userText, sender, pacer)

	return nil
}

// PlayListenPrompt 播放独立的唤醒就绪提示音。
func (p *TurnPipeline) PlayListenPrompt(ctx context.Context, turnId uint64, sessionId string, sender DownlinkSender) error {
	pkts, err := audio.GetListenPromptOpusPackets()
	if err != nil {
		p.logger.Error("failed to get listen prompt opus packets", "error", err, "session_id", sessionId)
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    err,
			fatal:  true,
		})
		return err
	}

	p.mu.Lock()
	if p.activeTurn != nil {
		p.activeTurn.cancel()
		p.cleanupTurnResources(p.activeTurn)
	}

	turnCtx, turnCancel := context.WithCancel(ctx)
	pacer := NewDownlinkPacer(turnCtx, DownlinkPacerOptions{
		SessionId:     sessionId,
		Sender:        sender,
		QueueCap:      p.cfg.DownlinkOpusQueueCapacity,
		TickerFactory: p.tickerFactory,
		Logger:        p.logger,
		Callbacks: PacerCallbacks{
			OnStarted: func() {
				p.emit(turnEvent{
					turnId: turnId,
					typ:    turnEventPlaybackStarted,
				})
			},
			OnCompleted: func() {
				p.emit(turnEvent{
					turnId: turnId,
					typ:    turnEventTurnCompleted,
				})
			},
			OnError: func(err error) {
				p.emit(turnEvent{
					turnId: turnId,
					typ:    turnEventTurnFailed,
					err:    err,
					fatal:  true,
				})
			},
		},
	})

	turn := &activeTurn{
		turnId:  turnId,
		ctx:     turnCtx,
		cancel:  turnCancel,
		effects: &TurnEffects{},
		pacer:   pacer,
	}
	p.activeTurn = turn
	p.mu.Unlock()

	go pacer.Run()

	go func() {
		for _, pkt := range pkts {
			if turnCtx.Err() != nil {
				return
			}
			if err := pacer.Enqueue(pkt); err != nil {
				return
			}
		}
		pacer.FinishInput()
	}()

	return nil
}

// Abort 中止指定轮次并清空相关资源。
func (p *TurnPipeline) Abort(turnId uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.activeTurn == nil {
		return
	}
	if turnId != 0 && p.activeTurn.turnId != turnId {
		return
	}

	p.activeTurn.cancel()
	p.cleanupTurnResources(p.activeTurn)
	p.activeTurn = nil
}

// Close 关闭整个流水线，取消当前轮次并释放所有资源。
func (p *TurnPipeline) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.activeTurn != nil {
		p.activeTurn.cancel()
		p.cleanupTurnResources(p.activeTurn)
		p.activeTurn = nil
	}
}

// cleanupTurnResources 释放 activeTurn 持有的底层资源。
func (p *TurnPipeline) cleanupTurnResources(turn *activeTurn) {
	if turn == nil {
		return
	}
	if turn.pacer != nil {
		turn.pacer.Abort()
		turn.pacer = nil
	}
	if turn.asrQueue != nil {
		turn.asrQueue.Close()
		turn.asrQueue = nil
	}
	if turn.asrStream != nil {
		_ = turn.asrStream.Close()
		turn.asrStream = nil
	}
}

// readASRResultAuto 在自动模式下异步等待 VAD ASR 识别结果。
func (p *TurnPipeline) readASRResultAuto(ctx context.Context, stream ai.ASRStream, turnId uint64, sessionId string) {
	text, err := stream.Result(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		p.logger.Warn("asr recognition failed in auto mode",
			"error", err,
			"session_id", sessionId,
			"turn_id", turnId,
		)
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    err,
			fatal:  true,
		})
		return
	}

	p.emit(turnEvent{
		turnId: turnId,
		typ:    turnEventASRFinal,
		text:   text,
	})
}

// readASRResultManual 在手动模式下等待音频排空并读取最终识别结果。
func (p *TurnPipeline) readASRResultManual(ctx context.Context, stream ai.ASRStream, queue *audio.ASRAudioQueue, turnId uint64, sessionId string) {
	if queue != nil {
		select {
		case <-queue.Done():
			if qErr := queue.Err(); qErr != nil && !errors.Is(qErr, context.Canceled) {
				if ctx.Err() != nil {
					return
				}
				p.logger.Warn("asr audio queue finished with error",
					"error", qErr,
					"session_id", sessionId,
					"turn_id", turnId,
				)
				p.emit(turnEvent{
					turnId: turnId,
					typ:    turnEventTurnFailed,
					err:    qErr,
					fatal:  true,
				})
				return
			}
		case <-ctx.Done():
			return
		}
	}

	text, err := stream.Result(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		p.logger.Warn("asr recognition failed in manual mode",
			"error", err,
			"session_id", sessionId,
			"turn_id", turnId,
		)
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    err,
			fatal:  true,
		})
		return
	}

	p.emit(turnEvent{
		turnId: turnId,
		typ:    turnEventASRFinal,
		text:   text,
	})
}

// orchestrateLLMAndTTS 协同编排流式大语言模型生成、分句与逐句 TTS 语音合成。
func (p *TurnPipeline) orchestrateLLMAndTTS(turn *activeTurn, sessionId string, userText string, sender DownlinkSender, pacer *DownlinkPacer) {
	ctx := turn.ctx
	turnId := turn.turnId

	pipelineSucceeded := false
	defer func() {
		if !pipelineSucceeded {
			pacer.Stop()
		}
	}()

	pcmDone := make(chan error, 1)
	sentenceCh := make(chan string, 100)
	var sentenceChOnce sync.Once
	closeSentenceCh := func() {
		sentenceChOnce.Do(func() {
			close(sentenceCh)
		})
	}
	defer closeSentenceCh()

	go p.consumeSentencesTTS(turn, sessionId, sentenceCh, pacer, pcmDone)

	messages := p.history.BuildLLMMessages(p.systemPrompt, userText)
	var tools []ai.Tool
	if p.toolProvider != nil {
		tools = p.toolProvider.BuildSnapshot(ctx, turnId, sessionId, turn.effects)
	}

	splitter := NewSentenceSplitter()

	sendSentence := func(sentence string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sentenceCh <- sentence:
		}
		return nil
	}

	currentIteration := 0
	finalText, err := p.llmClient.Generate(
		ctx,
		ai.LLMRequest{
			Messages: messages,
			Tools:    tools,
			MaxTurns: 8,
		},
		func(ctx context.Context, chunk ai.LLMChunk) error {
			if chunk.Iteration != currentIteration {
				splitter = NewSentenceSplitter()
				currentIteration = chunk.Iteration
			}

			for _, sentence := range splitter.Feed(chunk.Text) {
				if err := sendSentence(sentence); err != nil {
					return err
				}
			}
			return nil
		},
	)

	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		p.logger.Warn("llm generate failed",
			"error", err,
			"session_id", sessionId,
			"turn_id", turnId,
		)
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    err,
			fatal:  true,
		})
		return
	}

	if ctx.Err() != nil {
		return
	}

	// 刷新末尾残句
	remaining := splitter.Flush()
	for _, sentence := range remaining {
		if err := sendSentence(sentence); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			p.logger.Warn("failed to deliver flushed sentence",
				"error", err,
				"session_id", sessionId,
				"turn_id", turnId,
			)
			p.emit(turnEvent{
				turnId: turnId,
				typ:    turnEventTurnFailed,
				err:    err,
				fatal:  true,
			})
			return
		}
	}

	closeSentenceCh()

	if ctx.Err() != nil {
		return
	}

	select {
	case <-ctx.Done():
		return
	case pcmErr := <-pcmDone:
		if pcmErr != nil {
			return
		}
	}

	if ctx.Err() != nil {
		return
	}

	// 文本成功提交至历史
	if userText != "" && finalText != "" {
		p.history.AppendTurn(userText, finalText)
	}

	pipelineSucceeded = true
	pacer.FinishInput()
}

// consumeSentencesTTS 严格保持单并发合成与流式分帧编码，将单句字幕与 Opus 包实时压入下行节奏器。
func (p *TurnPipeline) consumeSentencesTTS(turn *activeTurn, sessionId string, sentenceCh <-chan string, pacer *DownlinkPacer, done chan<- error) {
	ctx := turn.ctx
	turnId := turn.turnId

	var consumeErr error
	defer func() {
		if done != nil {
			select {
			case done <- consumeErr:
			default:
			}
			close(done)
		}
	}()

	if p.ttsClient == nil {
		return
	}

	enc, err := audio.NewEncoder(p.cfg.MaxOpusPacketBytes)
	if err != nil {
		consumeErr = err
		p.logger.Error("failed to create per-turn opus encoder",
			"error", err,
			"session_id", sessionId,
			"turn_id", turnId,
		)
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    err,
			fatal:  true,
		})
		return
	}
	defer enc.Close()

	streamEncoder := audio.NewStreamEncoder(enc)

	hasSentStart := false
	for sentence := range sentenceCh {
		if ctx.Err() != nil {
			return
		}
		if sentence == "" {
			continue
		}

		if !hasSentStart && pacer != nil {
			hasSentStart = true
			if startBytes, sErr := EncodeTTSStartMessage(sessionId); sErr == nil {
				_ = pacer.EnqueueText(startBytes)
			}
		}

		if err := p.synthesizeSentence(ctx, sessionId, sentence, streamEncoder, pacer); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			consumeErr = err
			p.logger.Warn("tts sentence stream error",
				"error", err,
				"session_id", sessionId,
				"turn_id", turnId,
			)
			p.emit(turnEvent{
				turnId: turnId,
				typ:    turnEventTurnFailed,
				err:    err,
				fatal:  true,
			})
			return
		}
	}

	if ctx.Err() != nil {
		return
	}

	// 统一刷新最后一包 Opus 尾帧
	flushPackets, flushErr := streamEncoder.Flush()
	if flushErr != nil {
		consumeErr = flushErr
		p.logger.Warn("failed to flush tts opus encoder",
			"error", flushErr,
			"session_id", sessionId,
			"turn_id", turnId,
		)
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    flushErr,
			fatal:  true,
		})
		return
	}
	for _, pkt := range flushPackets {
		if ctx.Err() != nil {
			return
		}
		if pacer != nil {
			if err := pacer.Enqueue(pkt); err != nil {
				return
			}
		}
	}

	if hasSentStart && pacer != nil {
		if stopBytes, sErr := EncodeTTSStopMessage(sessionId); sErr == nil {
			_ = pacer.EnqueueText(stopBytes)
		}
	}
}

// synthesizeSentence 为单个句子建立流式 TTS 会话，顺序流式拉取 PCM 数据、进行分帧 Opus 编码并写入下行节奏器。
func (p *TurnPipeline) synthesizeSentence(ctx context.Context, sessionId string, sentence string, streamEncoder *audio.StreamEncoder, pacer *DownlinkPacer) error {
	if p.ttsClient == nil {
		return nil
	}

	if pacer != nil {
		if sentBytes, sErr := EncodeTTSSentenceStartMessage(sessionId, sentence); sErr == nil {
			if err := pacer.EnqueueText(sentBytes); err != nil {
				return err
			}
		}
	}

	stream, err := p.ttsClient.CreateStream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()

	if err := stream.SendSentence(ctx, sentence); err != nil {
		return err
	}

	if err := stream.Finish(ctx); err != nil {
		return err
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		pcmChunk, err := stream.NextPCM(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		if len(pcmChunk) == 0 {
			continue
		}

		packets, err := streamEncoder.Feed(pcmChunk)
		if err != nil {
			return err
		}

		for _, pkt := range packets {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if pacer != nil {
				if err := pacer.Enqueue(pkt); err != nil {
					return err
				}
			}
		}
	}
}
