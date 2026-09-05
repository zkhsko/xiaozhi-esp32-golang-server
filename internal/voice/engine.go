package voice

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// TurnEngine 负责单轮语音问答的端到端单向流水线编排。
type TurnEngine struct{}

// NewEngine 创建语音轮次引擎实例。
func NewEngine() *TurnEngine {
	return &TurnEngine{}
}

// HandleTurn 执行完整的单轮生命周期（ASR -> STT -> LLM/Tool -> TTS -> 连续编码 -> 节拍下发 -> 终局收口）。
// 该函数在当前 Goroutine 同步阻塞执行，返回前确保其创建的所有阶段 Goroutine 完整退出。
func (e *TurnEngine) HandleTurn(
	ctx context.Context,
	req TurnRequest,
	input AudioStream,
	output TurnOutput,
) TurnResult {
	if output == nil {
		return TurnResult{
			TurnId: req.TurnId,
			Status: TurnFailed,
			Err:    errors.New("output is nil"),
		}
	}

	// 1. ASR 阶段
	userText, asrErr := runASRStage(ctx, req, input)
	if ctx.Err() != nil {
		_ = output.End(context.Background(), TurnEndAborted)
		return TurnResult{
			TurnId: req.TurnId,
			Status: TurnAborted,
			Err:    ctx.Err(),
		}
	}

	if asrErr != nil {
		_ = output.End(context.Background(), TurnEndFailed)
		return TurnResult{
			TurnId: req.TurnId,
			Status: TurnFailed,
			Err:    asrErr,
		}
	}

	// manual 模式下无有效识别文本视为无语音正常结束
	if strings.EqualFold(req.Mode, "manual") && userText == "" {
		_ = output.End(context.Background(), TurnEndCompleted)
		return TurnResult{
			TurnId: req.TurnId,
			Status: TurnNoSpeech,
		}
	}

	// 2. 下发 STT
	if err := output.SendSTT(ctx, userText); err != nil {
		if ctx.Err() != nil {
			_ = output.End(context.Background(), TurnEndAborted)
			return TurnResult{
				TurnId:   req.TurnId,
				Status:   TurnAborted,
				UserText: userText,
				Err:      ctx.Err(),
			}
		}
		_ = output.End(context.Background(), TurnEndFailed)
		return TurnResult{
			TurnId:   req.TurnId,
			Status:   TurnFailed,
			UserText: userText,
			Err:      err,
		}
	}

	// 3. 驱动下游 Response -> Encoder -> PaceForward 流水线
	pcmTTSCh := make(chan ai.PCMChunk, 32)
	audioFrameCh := make(chan AudioFrame, 32)

	g, gCtx := errgroup.WithContext(ctx)

	var respRes *responseStageResult

	// Response Worker (LLM -> 分句 -> TTS)
	g.Go(func() error {
		r, err := runResponseStage(gCtx, req, userText, pcmTTSCh)
		if err != nil {
			return err
		}
		respRes = r
		return nil
	})

	// Encoder Worker (PCM -> 连续 Opus 编码 -> AudioFrame)
	g.Go(func() error {
		maxBytes := req.MaxOpusPacketBytes
		if maxBytes <= 0 {
			maxBytes = 1024
		}
		return runEncoderStage(gCtx, maxBytes, pcmTTSCh, audioFrameCh)
	})

	// PaceForward Worker (60ms 节拍转发 -> TurnOutput)
	g.Go(func() error {
		return PaceForward(gCtx, audioFrameCh, output)
	})

	pipelineErr := g.Wait()

	// 汇总 Effect
	var effects []TurnEffect
	if req.EffectsCh != nil {
		for {
			select {
			case eff, ok := <-req.EffectsCh:
				if !ok {
					goto effectsDone
				}
				effects = append(effects, eff)
			default:
				goto effectsDone
			}
		}
	effectsDone:
	}

	// 4. 终局收口
	endCtx, endCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer endCancel()

	if ctx.Err() != nil {
		_ = output.End(endCtx, TurnEndAborted)
		return TurnResult{
			TurnId:   req.TurnId,
			Status:   TurnAborted,
			UserText: userText,
			Effects:  effects,
			Err:      ctx.Err(),
		}
	}

	if pipelineErr != nil {
		_ = output.End(endCtx, TurnEndFailed)
		return TurnResult{
			TurnId:   req.TurnId,
			Status:   TurnFailed,
			UserText: userText,
			Effects:  effects,
			Err:      pipelineErr,
		}
	}

	var (
		assistantText string
		turnMessages  []ai.Message
	)
	if respRes != nil {
		assistantText = respRes.AssistantText
		turnMessages = respRes.TurnMessages
	}

	_ = output.End(endCtx, TurnEndCompleted)
	return TurnResult{
		TurnId:        req.TurnId,
		Status:        TurnCompleted,
		UserText:      userText,
		AssistantText: assistantText,
		TurnMessages:  turnMessages,
		Effects:       effects,
	}
}
