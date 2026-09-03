package voice

import (
	"context"
	"fmt"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// runEncoderStage 消费 TTS 输出的 PCM 块，执行 24 kHz 连续 Opus 编码并组装 AudioFrame 投递给下行通道。
func runEncoderStage(
	ctx context.Context,
	maxOpusPacketBytes int,
	pcmTTSCh <-chan ai.PCMChunk,
	audioFrameCh chan<- AudioFrame,
) error {
	defer close(audioFrameCh)

	enc, err := audio.NewEncoder(maxOpusPacketBytes)
	if err != nil {
		return fmt.Errorf("create opus encoder: %w", err)
	}
	defer enc.Close()

	streamEnc := audio.NewStreamEncoder(enc)
	defer streamEnc.Close()

	var pendingSentenceStarts []string

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-pcmTTSCh:
			if !ok {
				// PCM 流结束，执行最终 Flush
				flushPkts, flushErr := streamEnc.Flush()
				if flushErr != nil {
					return fmt.Errorf("flush stream encoder: %w", flushErr)
				}
				for _, pkt := range flushPkts {
					frame := AudioFrame{
						OpusData: pkt,
					}
					if len(pendingSentenceStarts) > 0 {
						frame.SentenceStarts = append([]string(nil), pendingSentenceStarts...)
						pendingSentenceStarts = nil
					}
					select {
					case audioFrameCh <- frame:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}

			if chunk.SentenceStart != "" {
				pendingSentenceStarts = append(pendingSentenceStarts, chunk.SentenceStart)
			}

			if len(chunk.Data) == 0 {
				continue
			}

			pkts, feedErr := streamEnc.Feed(chunk.Data)
			if feedErr != nil {
				return fmt.Errorf("feed pcm to stream encoder: %w", feedErr)
			}

			for _, pkt := range pkts {
				frame := AudioFrame{
					OpusData: pkt,
				}
				if len(pendingSentenceStarts) > 0 {
					frame.SentenceStarts = append([]string(nil), pendingSentenceStarts...)
					pendingSentenceStarts = nil
				}
				select {
				case audioFrameCh <- frame:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}
