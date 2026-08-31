package session

import (
	"context"
	"errors"
	"io"
	"sync"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// buildLLMMessages 根据系统提示词、会话历史与当前用户识别文本构造发送给大语言模型的消息列表。
func (s *Session) buildLLMMessages(userText string) []ai.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sysPrompt := s.systemPrompt

	capacity := 1 + len(s.history)
	if sysPrompt != "" {
		capacity++
	}

	messages := make([]ai.Message, 0, capacity)

	if sysPrompt != "" {
		messages = append(messages, ai.Message{
			Role:    ai.RoleSystem,
			Content: sysPrompt,
		})
	}

	if len(s.history) > 0 {
		messages = append(messages, s.history...)
	}

	messages = append(messages, ai.Message{
		Role:    ai.RoleUser,
		Content: userText,
	})

	return messages
}

// AppendHistory 将一轮成功的用户文本与助手完整回复追加至会话历史，并按上限滚动淘汰最旧轮次。
func (s *Session) AppendHistory(userText, assistantText string) {
	if userText == "" || assistantText == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.history = append(s.history,
		ai.Message{Role: ai.RoleUser, Content: userText},
		ai.Message{Role: ai.RoleAssistant, Content: assistantText},
	)

	maxTurns := DefaultMaxHistoryTurns
	if s.cfg != nil && s.cfg.Session.MaxHistoryTurns > 0 {
		maxTurns = s.cfg.Session.MaxHistoryTurns
	}

	maxMessages := maxTurns * 2
	if len(s.history) > maxMessages {
		trimmed := make([]ai.Message, maxMessages)
		copy(trimmed, s.history[len(s.history)-maxMessages:])
		s.history = trimmed
	}
}

// ClearHistory 清空当前会话的对话历史。
func (s *Session) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = nil
}

// History 返回当前会话的历史消息列表副本。
func (s *Session) History() []ai.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.history) == 0 {
		return nil
	}
	copied := make([]ai.Message, len(s.history))
	copy(copied, s.history)
	return copied
}

// stopTTS 停止并清理当前轮次的 TTS 流。
func (s *Session) stopTTS() {
	s.mu.Lock()
	stream := s.ttsStream
	s.ttsStream = nil
	s.mu.Unlock()

	if stream != nil {
		_ = stream.Close()
	}
}

// orchestrateLLMAndTTS 在后台协程中协同编排流式大语言模型生成、增量分句与逐句精准音字同步语音合成。
func (s *Session) orchestrateLLMAndTTS(ctx context.Context, gen uint64, userText string) {
	if s.llmClient == nil && s.ttsClient == nil {
		return
	}
	if s.llmClient == nil || s.ttsClient == nil {
		s.logger.Error("llm client or tts client not configured",
			"session_id", s.SessionId(),
			"generation", gen,
		)
		s.postEvent(event{
			kind:       eventKindError,
			generation: gen,
			err:        errors.New("ai clients not configured"),
			fatal:      true,
		})
		return
	}

	if err := ctx.Err(); err != nil {
		return
	}

	// 1. 创建并启动当前轮次的下行 60 ms 节奏调度器
	queueCap := DefaultWriteQueueCapacity
	if s.cfg != nil && s.cfg.Session.DownlinkOpusQueueCapacity > 0 {
		queueCap = s.cfg.Session.DownlinkOpusQueueCapacity
	}
	s.mu.RLock()
	factory := s.tickerFactory
	s.mu.RUnlock()

	pacer := NewDownlinkPacer(ctx, s, gen, queueCap, factory)
	s.mu.Lock()
	s.pacer = pacer
	s.mu.Unlock()

	go pacer.Run()

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

	// 启动后台协程逐句消费分句通道并执行 TTS 与 Pacer 音字严格入队
	go s.consumeSentencesTTS(ctx, gen, sentenceCh, pacer, pcmDone)

	// 2. 构造上下文消息与工具列表并执行流式生成
	messages := s.buildLLMMessages(userText)
	tools := s.availableTools(gen)
	splitter := NewSentenceSplitter()
	sessionId := s.SessionId()

	sendSentence := func(sentence string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Generation() > gen {
			return errors.New("generation mismatch")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case sentenceCh <- sentence:
		}

		return nil
	}

	currentIteration := 0
	finalText, err := s.llmClient.Generate(
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
		if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
			return
		}
		s.logger.Warn("llm generate failed",
			"error", err,
			"session_id", sessionId,
			"generation", gen,
		)
		s.postEvent(event{
			kind:       eventKindError,
			generation: gen,
			err:        err,
			fatal:      true,
		})
		return
	}

	if ctx.Err() != nil || s.Generation() > gen {
		return
	}

	// 3. LLM 正常结束时，刷新末尾残句
	remaining := splitter.Flush()
	for _, sentence := range remaining {
		if err := sendSentence(sentence); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
				return
			}
			s.logger.Warn("failed to deliver flushed sentence",
				"error", err,
				"session_id", sessionId,
				"generation", gen,
			)
			s.postEvent(event{
				kind:       eventKindError,
				generation: gen,
				err:        err,
				fatal:      true,
			})
			return
		}
	}

	// 4. 关闭分句通道，通知后台消费协程文本已全部提交
	closeSentenceCh()

	if ctx.Err() != nil || s.Generation() > gen {
		return
	}

	// 5. 等待后台 TTS 合成与分帧编码完全结束
	select {
	case <-ctx.Done():
		return
	case pcmErr := <-pcmDone:
		if pcmErr != nil {
			return
		}
	}

	if s.Generation() > gen {
		return
	}

	pipelineSucceeded = true
	pacer.FinishInput(userText, finalText)
}

// synthesizeSentence 为单个句子建立流式 TTS 会话，顺序流式拉取 PCM 数据、进行分帧 Opus 编码并写入下行节奏器。
// 保持严格单并发合成，当前句子拉取完毕后再拉取下一句。
func (s *Session) synthesizeSentence(ctx context.Context, gen uint64, sentence string, streamEncoder *audio.StreamEncoder, pacer *DownlinkPacer) error {
	if s.ttsClient == nil {
		return nil
	}

	stream, err := s.ttsClient.CreateStream(ctx)
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

	hasEnqueuedSentenceStart := false

	for {
		if ctx.Err() != nil || s.Generation() > gen {
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

		if !hasEnqueuedSentenceStart {
			if pacer != nil {
				if err := pacer.EnqueueSentenceStart(sentence); err != nil {
					return err
				}
			}
			hasEnqueuedSentenceStart = true
		}

		packets, err := streamEncoder.Feed(pcmChunk)
		if err != nil {
			return err
		}

		for _, pkt := range packets {
			if s.Generation() > gen {
				return errors.New("generation mismatch")
			}
			s.handleEncodedOpusPacket(gen, pkt)
			if pacer != nil {
				if err := pacer.Enqueue(pkt); err != nil {
					return err
				}
			}
		}
	}
}

// consumeSentencesTTS 严格保持单并发合成与流式分帧编码，边生成边将单句字幕通知与 Opus 音频包实时压入下行节奏器。
func (s *Session) consumeSentencesTTS(ctx context.Context, gen uint64, sentenceCh <-chan string, pacer *DownlinkPacer, done chan<- error) {
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

	if s.ttsClient == nil {
		return
	}

	maxOpusBytes := audio.DefaultMaxOpusPacketBytes
	if s.cfg != nil && s.cfg.Session.MaxOpusPacketBytes > 0 {
		maxOpusBytes = s.cfg.Session.MaxOpusPacketBytes
	}

	enc, err := audio.NewEncoder(maxOpusBytes)
	if err != nil {
		consumeErr = err
		s.logger.Error("failed to create per-turn opus encoder",
			"error", err,
			"session_id", s.SessionId(),
			"generation", gen,
		)
		s.postEvent(event{
			kind:       eventKindError,
			generation: gen,
			err:        err,
			fatal:      true,
		})
		return
	}
	defer enc.Close()

	streamEncoder := audio.NewStreamEncoder(enc)
	sessionId := s.SessionId()

	for sentence := range sentenceCh {
		if ctx.Err() != nil || s.Generation() > gen {
			return
		}
		if sentence == "" {
			continue
		}

		if err := s.synthesizeSentence(ctx, gen, sentence, streamEncoder, pacer); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
				return
			}
			consumeErr = err
			s.logger.Warn("tts sentence stream error",
				"error", err,
				"session_id", sessionId,
				"generation", gen,
			)
			s.postEvent(event{
				kind:       eventKindError,
				generation: gen,
				err:        err,
				fatal:      true,
			})
			return
		}
	}

	if ctx.Err() != nil || s.Generation() > gen {
		return
	}

	s.mu.RLock()
	isCloseAfterTurn := s.closeAfterTurn
	s.mu.RUnlock()

	// 若为 auto 模式且启用了提示音且本轮非关闭连接操作，在末尾追加提示音 PCM
	if s.Mode() == ListenModeAuto && s.cfg != nil && s.cfg.Session.ListenPromptEnabled && !isCloseAfterTurn {
		promptPCM, pErr := audio.GetListenPromptPCM()
		if pErr != nil {
			consumeErr = pErr
			s.logger.Warn("failed to get listen prompt pcm for turn tail",
				"error", pErr,
				"session_id", sessionId,
				"generation", gen,
			)
		} else if len(promptPCM) > 0 {
			promptPackets, pEncodeErr := streamEncoder.Feed(promptPCM)
			if pEncodeErr != nil {
				consumeErr = pEncodeErr
				s.logger.Warn("failed to encode prompt pcm to opus",
					"error", pEncodeErr,
					"session_id", sessionId,
					"generation", gen,
				)
			} else {
				for _, pkt := range promptPackets {
					if s.Generation() > gen {
						return
					}
					s.handleEncodedOpusPacket(gen, pkt)
					if pacer != nil {
						if err := pacer.Enqueue(pkt); err != nil {
							return
						}
					}
				}
			}
		}
	}

	// 所有分句与提示音 PCM 输入完毕，统一刷新最后一包 Opus 尾帧
	flushPackets, flushErr := streamEncoder.Flush()
	if flushErr != nil {
		consumeErr = flushErr
		s.logger.Warn("failed to flush tts opus encoder",
			"error", flushErr,
			"session_id", sessionId,
			"generation", gen,
		)
		s.postEvent(event{
			kind:       eventKindError,
			generation: gen,
			err:        flushErr,
			fatal:      true,
		})
		return
	}
	for _, pkt := range flushPackets {
		if s.Generation() > gen {
			return
		}
		s.handleEncodedOpusPacket(gen, pkt)
		if pacer != nil {
			if err := pacer.Enqueue(pkt); err != nil {
				return
			}
		}
	}
}

// handleEncodedOpusPacket 处理编码产出的单个 24 kHz 60 ms Opus 数据包。
func (s *Session) handleEncodedOpusPacket(gen uint64, packet []byte) {
	if s.Generation() > gen {
		return
	}
	s.mu.RLock()
	cb := s.onEncodedOpus
	s.mu.RUnlock()
	if cb != nil {
		cb(gen, packet)
	}
}
