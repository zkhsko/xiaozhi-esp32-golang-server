package voice

import (
	"context"
	"fmt"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// appendReadyPrompt 在正文成功合成且会话继续时追加尾音，不关闭 PCM 流。
func appendReadyPrompt(ctx context.Context, enabled bool, response *responseStageResult, pcm chan<- ai.PCMChunk) error {
	if !enabled || !response.HasSynthesizedText {
		return nil
	}
	for _, effect := range response.Effects {
		if effect.Type == EffectCloseSession {
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	promptPCM, err := audio.GetPromptPCM()
	if err != nil {
		return fmt.Errorf("get prompt pcm: %w", err)
	}

	select {
	case pcm <- ai.PCMChunk{Data: promptPCM}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
