package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// responseStageResult 包含 response 阶段汇总的助手回复文本。
type responseStageResult struct {
	AssistantText string
}

// runResponseStage 执行 LLM 生成、流式分句与串行 TTS 合成。
func runResponseStage(
	ctx context.Context,
	req TurnRequest,
	userText string,
	pcmTTSCh chan<- ai.PCMChunk,
) (*responseStageResult, error) {
	if req.LLMClient == nil {
		return nil, errors.New("llm client is nil")
	}
	if req.TTSClient == nil {
		return nil, errors.New("tts client is nil")
	}

	// 组装 LLM 请求消息列表
	messages := make([]ai.Message, 0, len(req.History)+2)
	if req.SystemPrompt != "" {
		messages = append(messages, ai.Message{
			Role:    ai.RoleSystem,
			Content: req.SystemPrompt,
		})
	}
	if len(req.History) > 0 {
		messages = append(messages, req.History...)
	}
	messages = append(messages, ai.Message{
		Role:    ai.RoleUser,
		Content: userText,
	})

	sentenceCh := make(chan string, 16)
	g, gCtx := errgroup.WithContext(ctx)

	var assistantTextBuilder strings.Builder

	// LLM 与分句 Worker
	g.Go(func() error {
		defer close(sentenceCh)

		tools := req.Tools
		if req.ToolSnapshot != nil {
			tools = req.ToolSnapshot(gCtx)
		}

		llmReq := ai.LLMRequest{
			Messages: messages,
			Tools:    tools,
			MaxTurns: 8,
		}

		chunks := make(chan ai.LLMChunk, 32)
		var (
			llmRes      ai.LLMResult
			llmErr      error
			llmFinished = make(chan struct{})
		)

		go func() {
			defer close(llmFinished)
			defer close(chunks)
			llmRes, llmErr = req.LLMClient.Generate(gCtx, llmReq, chunks)
		}()

		splitter := NewSentenceSplitter()
		var (
			chunkCount       int
			currentIteration = -1
		)

		for chunk := range chunks {
			chunkCount++
			if currentIteration != -1 && chunk.Iteration != currentIteration {
				// 迭代切换，刷新分句器残余
				for _, s := range splitter.Flush() {
					select {
					case sentenceCh <- s:
					case <-gCtx.Done():
						return gCtx.Err()
					}
				}
			}
			currentIteration = chunk.Iteration

			sentences := splitter.Feed(chunk.Text)
			for _, s := range sentences {
				select {
				case sentenceCh <- s:
				case <-gCtx.Done():
					return gCtx.Err()
				}
			}
		}

		// 刷新最后残余
		for _, s := range splitter.Flush() {
			select {
			case sentenceCh <- s:
			case <-gCtx.Done():
				return gCtx.Err()
			}
		}

		<-llmFinished
		if llmErr != nil {
			return fmt.Errorf("llm generate: %w", llmErr)
		}

		// finalText 兜底逻辑
		if chunkCount == 0 {
			fText := strings.TrimSpace(llmRes.FinalText)
			if fText == "" {
				return errors.New("empty llm response (no chunks and empty finalText)")
			}
			select {
			case sentenceCh <- fText:
			case <-gCtx.Done():
				return gCtx.Err()
			}
		}

		return nil
	})

	// TTS Worker
	g.Go(func() error {
		defer close(pcmTTSCh)

		var ttsSession ai.TTSSession
		defer func() {
			if ttsSession != nil {
				_ = ttsSession.Close()
			}
		}()

		sentenceTimeout := req.TTSSentenceTimeout
		if sentenceTimeout <= 0 {
			sentenceTimeout = 30 * time.Second
		}

		for sentence := range sentenceCh {
			trimmed := strings.TrimSpace(sentence)
			if trimmed == "" {
				continue
			}

			if assistantTextBuilder.Len() > 0 {
				assistantTextBuilder.WriteString(" ")
			}
			assistantTextBuilder.WriteString(trimmed)

			if ttsSession == nil {
				sess, err := req.TTSClient.CreateSession(gCtx)
				if err != nil {
					return fmt.Errorf("create tts session: %w", err)
				}
				ttsSession = sess
			}

			sentCtx, sentCancel := context.WithTimeout(gCtx, sentenceTimeout)
			err := ttsSession.Synthesize(sentCtx, trimmed, pcmTTSCh)
			sentCancel()
			if err != nil {
				return fmt.Errorf("synthesize sentence: %w", err)
			}
		}

		// 若合成了有效回复文本，在语音输出流末尾追加提示音
		if assistantTextBuilder.Len() > 0 {
			promptPCM, err := audio.GetPromptPCM()
			if err != nil {
				return fmt.Errorf("get prompt pcm: %w", err)
			}
			if len(promptPCM) > 0 {
				select {
				case pcmTTSCh <- ai.PCMChunk{Data: promptPCM}:
				case <-gCtx.Done():
					return gCtx.Err()
				}
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &responseStageResult{
		AssistantText: assistantTextBuilder.String(),
	}, nil
}
