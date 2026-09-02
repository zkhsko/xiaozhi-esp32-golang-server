package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// turnEventType 定义轮次流水线向 Supervisor 投递的事件类型。
type turnEventType int

const (
	// turnEventASRFinal 表示 ASR 最终识别文本产出。
	turnEventASRFinal turnEventType = iota

	// turnEventSpeaking 表示首句已准备好下发，会话进入说话状态。
	turnEventSpeaking

	// turnEventTurnCompleted 表示当前问答处理已完整结束。
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

	// 历史与意图暂存（在语音下行写出确认前不直接提交）
	userText     string
	finalText    string
	closeSession bool
}

// PipelineOptions 聚合构造 TurnPipeline 的依赖与配置。
type PipelineOptions struct {
	ASRClient    ai.ASRClient
	LLMClient    ai.LLMClient
	VoiceStream  *VoiceStream
	SystemPrompt string
	Config       SessionConfig
	History      *ConversationHistory
	ToolProvider *ToolProvider
	Logger       *slog.Logger
	PostEvent    func(turnEvent)
}

// TurnPipeline 负责会话级 AI 客户端与语音轮次流水线调度，将每轮运行时隔离在 activeTurn 中。
type TurnPipeline struct {
	asrClient    ai.ASRClient
	llmClient    ai.LLMClient
	voiceStream  *VoiceStream
	systemPrompt string
	cfg          SessionConfig
	history      *ConversationHistory
	toolProvider *ToolProvider
	logger       *slog.Logger
	postEvent    func(turnEvent)

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

	p := &TurnPipeline{
		asrClient:    opts.ASRClient,
		llmClient:    opts.LLMClient,
		voiceStream:  opts.VoiceStream,
		systemPrompt: opts.SystemPrompt,
		cfg:          opts.Config,
		history:      hist,
		toolProvider: opts.ToolProvider,
		logger:       l,
		postEvent:    opts.PostEvent,
	}

	if opts.VoiceStream != nil {
		opts.VoiceStream.SetOnEvent(p.handleVoiceStreamEvent)
	}

	return p
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
		activeTurnId := p.activeTurn.turnId
		p.activeTurn.cancel()
		p.cleanupTurnResources(p.activeTurn)
		if p.voiceStream != nil {
			p.voiceStream.CancelTurn(activeTurnId)
		}
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

// StartResponse 启动 LLM 增量生成与多轮工具编排响应。
func (p *TurnPipeline) StartResponse(turnId uint64, sessionId string, userText string) error {
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

	if p.llmClient == nil {
		p.mu.Unlock()
		p.emit(turnEvent{
			turnId: turnId,
			typ:    turnEventTurnFailed,
			err:    errors.New("llm client not configured"),
			fatal:  true,
		})
		return nil
	}

	if p.voiceStream != nil {
		p.voiceStream.SetSessionId(sessionId)
		if err := p.voiceStream.StartTurn(turn.ctx, turnId); err != nil {
			p.mu.Unlock()
			p.logger.Error("failed to start voice stream turn", "error", err, "turnId", turnId)
			p.emit(turnEvent{
				turnId: turnId,
				typ:    turnEventTurnFailed,
				err:    err,
				fatal:  true,
			})
			return err
		}
	}
	p.mu.Unlock()

	go p.orchestrateLLM(turn, sessionId, userText)
	return nil
}

// Abort 中止指定轮次并清空相关资源。
func (p *TurnPipeline) Abort(turnId uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.activeTurn == nil {
		if p.voiceStream != nil {
			p.voiceStream.CancelTurn(turnId)
		}
		return
	}
	if turnId != 0 && p.activeTurn.turnId != turnId {
		return
	}

	activeTurnId := p.activeTurn.turnId
	p.activeTurn.cancel()
	p.cleanupTurnResources(p.activeTurn)
	p.activeTurn = nil
	if p.voiceStream != nil {
		p.voiceStream.CancelTurn(activeTurnId)
	}
}

// Close 关闭整个流水线，取消当前轮次并释放所有资源。
func (p *TurnPipeline) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.activeTurn != nil {
		activeTurnId := p.activeTurn.turnId
		p.activeTurn.cancel()
		p.cleanupTurnResources(p.activeTurn)
		p.activeTurn = nil
		if p.voiceStream != nil {
			p.voiceStream.CancelTurn(activeTurnId)
		}
	}
	if p.voiceStream != nil {
		_ = p.voiceStream.Close()
	}
}

// cleanupTurnResources 释放 activeTurn 持有的底层资源。
func (p *TurnPipeline) cleanupTurnResources(turn *activeTurn) {
	if turn == nil {
		return
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

// orchestrateLLM 编排流式大语言模型生成与多轮工具调用。
func (p *TurnPipeline) orchestrateLLM(turn *activeTurn, sessionId string, userText string) {
	ctx := turn.ctx
	turnId := turn.turnId

	messages := p.history.BuildLLMMessages(p.systemPrompt, userText)
	var tools []ai.Tool
	if p.toolProvider != nil {
		tools = p.toolProvider.BuildSnapshot(ctx, turnId, sessionId, turn.effects)
	}

	var (
		lastIteration   = -1
		iterationInit   = false
		hasStreamedText = false
	)

	finalText, err := p.llmClient.Generate(
		ctx,
		ai.LLMRequest{
			Messages: messages,
			Tools:    tools,
			MaxTurns: 8,
		},
		func(chunkCtx context.Context, chunk ai.LLMChunk) error {
			if iterationInit && chunk.Iteration != lastIteration {
				if p.voiceStream != nil {
					if err := p.voiceStream.FlushIteration(chunkCtx, turnId); err != nil {
						return err
					}
				}
			}
			lastIteration = chunk.Iteration
			iterationInit = true

			if chunk.Text != "" {
				if p.voiceStream != nil {
					if err := p.voiceStream.FeedChunk(chunkCtx, turnId, chunk); err != nil {
						return err
					}
				}
				if strings.TrimSpace(chunk.Text) != "" {
					hasStreamedText = true
				}
			}
			return nil
		},
	)

	if err != nil {
		if p.voiceStream != nil {
			p.voiceStream.CancelTurn(turnId)
		}
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
		if p.voiceStream != nil {
			p.voiceStream.CancelTurn(turnId)
		}
		return
	}

	// 朗读兜底策略：只有在整个流式回调中没有产生任何非空文本时，且 finalText 非空，才将 finalText 作为兜底送入 VoiceStream 朗读一次
	if !hasStreamedText && strings.TrimSpace(finalText) != "" {
		if p.voiceStream != nil {
			fallbackIteration := 0
			if lastIteration >= 0 {
				fallbackIteration = lastIteration
			}
			_ = p.voiceStream.FeedChunk(ctx, turnId, ai.LLMChunk{
				Text:      finalText,
				Iteration: fallbackIteration,
			})
		}
	}

	// 先暂存历史与意图，确保在 Finish 触发的下行完成或无文本直接成功前数据已就绪
	p.mu.Lock()
	if p.activeTurn == turn {
		turn.userText = userText
		turn.finalText = finalText
		if turn.effects != nil {
			turn.closeSession = turn.effects.CloseSession
		}
	}
	p.mu.Unlock()

	if p.voiceStream != nil {
		_ = p.voiceStream.Finish(ctx, turnId)
	} else {
		// 未配置 VoiceStream 时的兼容回退逻辑
		if userText != "" && finalText != "" {
			p.history.AppendTurn(userText, finalText)
		}
		p.mu.Lock()
		if p.activeTurn == turn {
			p.cleanupTurnResources(turn)
			p.activeTurn = nil
		}
		p.mu.Unlock()
		p.emit(turnEvent{
			turnId:       turnId,
			typ:          turnEventTurnCompleted,
			closeSession: turn.effects != nil && turn.effects.CloseSession,
		})
	}
}

// History 返回当前流水线的会话历史管理器。
func (p *TurnPipeline) History() *ConversationHistory {
	return p.history
}

// handleVoiceStreamEvent 处理来自 VoiceStream 的生命周期事件。
func (p *TurnPipeline) handleVoiceStreamEvent(ev VoiceStreamEvent) {
	p.mu.Lock()
	turn := p.activeTurn
	if turn == nil || turn.turnId != ev.TurnId {
		p.mu.Unlock()
		return
	}

	switch ev.Kind {
	case VoiceStreamEventSpeaking:
		p.mu.Unlock()
		p.emit(turnEvent{
			turnId: ev.TurnId,
			typ:    turnEventSpeaking,
		})

	case VoiceStreamEventSuccess:
		if turn.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}

		userText := turn.userText
		finalText := turn.finalText
		closeSession := turn.closeSession

		if userText != "" && finalText != "" {
			p.history.AppendTurn(userText, finalText)
		}

		p.cleanupTurnResources(turn)
		p.activeTurn = nil
		p.mu.Unlock()

		p.emit(turnEvent{
			turnId:       ev.TurnId,
			typ:          turnEventTurnCompleted,
			closeSession: closeSession,
		})

	case VoiceStreamEventFailed:
		p.cleanupTurnResources(turn)
		p.activeTurn = nil
		p.mu.Unlock()

		p.emit(turnEvent{
			turnId: ev.TurnId,
			typ:    turnEventTurnFailed,
			err:    ev.Err,
			fatal:  true,
		})

	default:
		p.mu.Unlock()
	}
}
