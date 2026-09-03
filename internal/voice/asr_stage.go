package voice

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// runASRStage 执行 ASR 阶段：上行 Opus 解码与流式语音识别。
func runASRStage(
	ctx context.Context,
	req TurnRequest,
	input AudioStream,
) (string, error) {
	if req.ASRClient == nil {
		return "", fmt.Errorf("asr client is nil")
	}

	decoder, err := audio.NewDecoder(req.MaxOpusPacketBytes)
	if err != nil {
		return "", fmt.Errorf("create opus decoder: %w", err)
	}

	pcmASRCh := make(chan []byte, 32)
	stageCtx, stageCancel := context.WithCancel(ctx)
	defer stageCancel()

	g, gCtx := errgroup.WithContext(stageCtx)

	// Decoder Worker: 从 input 读取 Opus 包解码为 PCM 写入 pcmASRCh
	g.Go(func() error {
		defer close(pcmASRCh)
		for {
			select {
			case <-gCtx.Done():
				return nil
			case pkt, ok := <-input:
				if !ok {
					return nil
				}
				if len(pkt) == 0 {
					continue
				}
				pcm, decErr := decoder.Decode(pkt)
				if decErr != nil {
					return fmt.Errorf("decode uplink opus: %w", decErr)
				}
				select {
				case pcmASRCh <- pcm:
				case <-gCtx.Done():
					return nil
				}
			}
		}
	})

	var userText string

	// ASR Worker: 调用 ASR 客户端消费 PCM
	g.Go(func() error {
		mode := ai.ASRModeAuto
		if strings.EqualFold(req.Mode, "manual") {
			mode = ai.ASRModeManual
		}

		asrReq := ai.ASRRequest{
			Mode:       mode,
			SampleRate: 16000,
			Channels:   1,
		}

		text, recErr := req.ASRClient.Recognize(gCtx, asrReq, pcmASRCh)
		if recErr != nil {
			return recErr
		}
		userText = strings.TrimSpace(text)
		// ASR 识别成功完成，解除 Decoder Worker 等待
		stageCancel()
		return nil
	})

	err = g.Wait()

	// ASR 阶段结束时通知输入闭环
	if req.OnInputClosed != nil {
		req.OnInputClosed()
	}

	if err != nil {
		return "", err
	}

	return userText, nil
}
