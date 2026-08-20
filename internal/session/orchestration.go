package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// buildLLMMessages 根据系统提示词、会话历史与当前用户识别文本构造发送给大语言模型的消息列表。
func (s *Session) buildLLMMessages(userText string) []ai.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	capacity := 1 + len(s.history)
	if s.cfg != nil && s.cfg.Session.SystemPrompt != "" {
		capacity++
	}

	messages := make([]ai.Message, 0, capacity)

	if s.cfg != nil && s.cfg.Session.SystemPrompt != "" {
		messages = append(messages, ai.Message{
			Role:    ai.RoleSystem,
			Content: s.cfg.Session.SystemPrompt,
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

// orchestrateLLMAndTTS 在后台协程中协同编排流式大语言模型生成、增量分句与回答级流式语音合成。
func (s *Session) orchestrateLLMAndTTS(ctx context.Context, gen uint64, userText string) {
	if s.llmClient == nil && s.ttsClient == nil {
		return
	}
	if s.llmClient == nil || s.ttsClient == nil {
		s.logger.Error("llm client or tts client not configured",
			"session_id", s.SessionID(),
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

	// 1. 创建单条回答级 TTS 合成流
	ttsStream, err := s.ttsClient.CreateStream(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		s.logger.Warn("failed to create tts stream",
			"error", err,
			"session_id", s.SessionID(),
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

	s.mu.Lock()
	s.ttsStream = ttsStream
	s.mu.Unlock()

	// 2. 创建并启动当前轮次的下行 60 ms 节奏调度器
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

	pcmDone := make(chan struct{})

	// 启动后台协程消费 TTS PCM 数据并送入 24 kHz Opus 分帧编码器与节奏调度器
	go s.consumeTTSPCM(ctx, gen, ttsStream, pacer, pcmDone)

	// 3. 构造上下文消息并创建 LLM 流式输出会话
	messages := s.buildLLMMessages(userText)
	tools := s.getMCPTools()
	llmStream, err := s.llmClient.CreateStream(ctx, messages, tools)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		s.logger.Warn("failed to create llm stream",
			"error", err,
			"session_id", s.SessionID(),
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
	defer func() {
		_ = llmStream.Close()
	}()

	splitter := NewSentenceSplitter()
	var assistantText strings.Builder
	sessionID := s.SessionID()

	// sendSentence 先下发设备文本消息 tts.sentence_start，再调用 ttsStream.SendSentence 写入，保证文本严格先于对应音频
	sendSentence := func(sentence string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Generation() > gen {
			return errors.New("generation mismatch")
		}

		startMsgBytes, err := EncodeTTSSentenceStartMessage(sessionID, sentence)
		if err != nil {
			return fmt.Errorf("encode sentence start message: %w", err)
		}

		if err := s.sendTextMessage(startMsgBytes); err != nil {
			return fmt.Errorf("send sentence start text message: %w", err)
		}

		if err := ttsStream.SendSentence(ctx, sentence); err != nil {
			return fmt.Errorf("send sentence to tts: %w", err)
		}

		return nil
	}

	// 3. 消费 LLM 流文本增量并通过分句器切分为完整句子
	for {
		chunk, err := llmStream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
				return
			}
			s.logger.Warn("llm stream read failed",
				"error", err,
				"session_id", sessionID,
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

		if chunk == "" {
			continue
		}

		assistantText.WriteString(chunk)
		sentences := splitter.Feed(chunk)
		for _, sentence := range sentences {
			if err := sendSentence(sentence); err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
					return
				}
				s.logger.Warn("failed to deliver sentence to tts",
					"error", err,
					"session_id", sessionID,
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
	}

	if ctx.Err() != nil || s.Generation() > gen {
		return
	}

	// 检查是否有大模型工具调用 (MCP Tool Calls)
	toolCalls := llmStream.ToolCalls()
	if len(toolCalls) > 0 {
		s.logger.Info("llm returned tool calls",
			"session_id", sessionID,
			"generation", gen,
			"tool_count", len(toolCalls),
		)

		// 将助手的 ToolCalls 追加至上下文
		messages = append(messages, ai.Message{
			Role:      ai.RoleAssistant,
			Content:   assistantText.String(),
			ToolCalls: toolCalls,
		})

		// 逐一执行 MCP Tool 并将结果注入上下文
		for _, tc := range toolCalls {
			if ctx.Err() != nil || s.Generation() > gen {
				return
			}
			resultText, err := s.callMCPTool(ctx, tc.Name, tc.Arguments)
			if err != nil {
				s.logger.Warn("mcp tool call failed during turn",
					"tool_name", tc.Name,
					"error", err,
					"session_id", sessionID,
					"generation", gen,
				)
				resultText = fmt.Sprintf("Error: %v", err)
			}
			messages = append(messages, ai.Message{
				Role:       ai.RoleTool,
				Content:    resultText,
				ToolCallID: tc.ID,
			})
		}

		if ctx.Err() != nil || s.Generation() > gen {
			return
		}

		// 携带工具执行结果再次调用 LLM 生成对用户的最终回答
		llmStream2, err := s.llmClient.CreateStream(ctx, messages, nil)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			s.logger.Warn("failed to create second llm stream after tool call",
				"error", err,
				"session_id", sessionID,
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
		defer func() {
			_ = llmStream2.Close()
		}()

		// 重置当前回答文本以接收最终自然语言回复
		assistantText.Reset()

		for {
			chunk, err := llmStream2.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
					return
				}
				s.logger.Warn("second llm stream read failed",
					"error", err,
					"session_id", sessionID,
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

			if chunk == "" {
				continue
			}

			assistantText.WriteString(chunk)
			sentences := splitter.Feed(chunk)
			for _, sentence := range sentences {
				if err := sendSentence(sentence); err != nil {
					if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
						return
					}
					s.logger.Warn("failed to deliver sentence to tts",
						"error", err,
						"session_id", sessionID,
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
		}
	}

	if ctx.Err() != nil || s.Generation() > gen {
		return
	}

	// 4. LLM 正常结束时，刷新末尾残句
	remaining := splitter.Flush()
	for _, sentence := range remaining {
		if err := sendSentence(sentence); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
				return
			}
			s.logger.Warn("failed to deliver flushed sentence to tts",
				"error", err,
				"session_id", sessionID,
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

	// 5. 调用且只调用一次 Finish 通知 TTS 输入结束
	if err := ttsStream.Finish(ctx); err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
			return
		}
		s.logger.Warn("failed to finish tts stream",
			"error", err,
			"session_id", sessionID,
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

	// 6. 等待 TTS PCM 数据流完全消费并编码入队
	select {
	case <-ctx.Done():
		return
	case <-pcmDone:
	}

	if s.Generation() > gen {
		return
	}

	pipelineSucceeded = true
	pacer.FinishInput(userText, assistantText.String())
}

// consumeTTSPCM 持续消费百炼 TTS 生成的 24 kHz PCM 数据块，通过分帧编码器组装为 60 ms 帧、进行 Opus 编码并送入节奏调度器。
func (s *Session) consumeTTSPCM(ctx context.Context, gen uint64, stream ai.TTSStream, args ...any) {
	var pacer *DownlinkPacer
	var doneCh chan struct{}

	for _, arg := range args {
		switch v := arg.(type) {
		case *DownlinkPacer:
			pacer = v
		case chan struct{}:
			doneCh = v
		}
	}
	if pacer == nil {
		pacer = s.Pacer()
	}

	if doneCh != nil {
		defer close(doneCh)
	}

	if stream == nil {
		return
	}

	maxOpusBytes := audio.DefaultMaxOpusPacketBytes
	if s.cfg != nil && s.cfg.Session.MaxOpusPacketBytes > 0 {
		maxOpusBytes = s.cfg.Session.MaxOpusPacketBytes
	}

	enc, err := audio.NewEncoder(maxOpusBytes)
	if err != nil {
		s.logger.Error("failed to create per-turn opus encoder",
			"error", err,
			"session_id", s.SessionID(),
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
	sessionID := s.SessionID()

	for {
		if ctx.Err() != nil || s.Generation() > gen {
			return
		}

		pcmChunk, err := stream.NextPCM(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if ctx.Err() != nil || s.Generation() > gen {
					return
				}
				// TTS PCM 流正常结束，刷新尾帧（若有残余数据，补静音输出最后一帧 Opus；若无残余则不输出多余包）
				packets, flushErr := streamEncoder.Flush()
				if flushErr != nil {
					if ctx.Err() != nil || s.Generation() > gen {
						return
					}
					s.logger.Warn("failed to flush tts opus encoder",
						"error", flushErr,
						"session_id", sessionID,
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
				for _, pkt := range packets {
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
				return
			}

			if errors.Is(err, context.Canceled) || ctx.Err() != nil || s.Generation() > gen {
				return
			}

			s.logger.Warn("tts stream pcm read failed",
				"error", err,
				"session_id", sessionID,
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

		if len(pcmChunk) == 0 {
			continue
		}

		packets, err := streamEncoder.Feed(pcmChunk)
		if err != nil {
			if ctx.Err() != nil || s.Generation() > gen {
				return
			}
			s.logger.Warn("failed to encode tts pcm to opus",
				"error", err,
				"session_id", sessionID,
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

		for _, pkt := range packets {
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
