package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

func TestTurnEngine_PromptToneContinuousEncoding(t *testing.T) {
	promptPCM, err := audio.GetPromptPCM()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"auto", "manual"} {
		for _, enabled := range []bool{false, true} {
			for _, pcmBytes := range []int{audio.DownlinkBytesPerFrame, audio.DownlinkBytesPerFrame - 2} {
				t.Run(fmt.Sprintf("%s/%v/%d", mode, enabled, pcmBytes), func(t *testing.T) {
					// 无论正文是否填满一帧，都只允许在正文与尾音之后执行最终 Flush。
					combinedPCM := make([]byte, pcmBytes)
					if enabled {
						combinedPCM = append(combinedPCM, promptPCM...)
					}
					enc, err := audio.NewEncoder(audio.DefaultMaxOpusPacketBytes)
					if err != nil {
						t.Fatal(err)
					}
					defer enc.Close()
					stream := audio.NewStreamEncoder(enc)
					defer stream.Close()
					expected, err := stream.Feed(combinedPCM)
					if err != nil {
						t.Fatal(err)
					}
					tail, err := stream.Flush()
					if err != nil {
						t.Fatal(err)
					}
					expected = append(expected, tail...)

					input := make(chan []byte)
					close(input)
					output := &mockTurnOutput{}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					result := NewEngine().HandleTurn(ctx, TurnRequest{
						TurnId: 1, Mode: mode, PromptToneEnabled: enabled,
						ASRClient: &mockASRClient{text: "你好"},
						LLMClient: &mockLLMClient{chunks: []ai.LLMChunk{{Text: "你好。"}}},
						TTSClient: &mockTTSClient{pcmBytes: pcmBytes},
					}, input, output)
					if result.Status != TurnCompleted {
						t.Fatalf("turn failed: %v", result.Err)
					}
					if len(output.audioFrames) == 0 || len(output.audioFrames) != len(expected) {
						t.Fatalf("audio frames = %d, want %d", len(output.audioFrames), len(expected))
					}
					for i, frame := range output.audioFrames {
						if !bytes.Equal(frame.OpusData, expected[i]) {
							t.Fatalf("frame %d differs from continuously encoded PCM", i)
						}
						if i == 0 {
							if len(frame.SentenceStarts) != 1 || frame.SentenceStarts[0] != "你好。" {
								t.Fatalf("unexpected first-frame subtitle: %v", frame.SentenceStarts)
							}
						} else if len(frame.SentenceStarts) != 0 {
							t.Fatalf("frame %d contains an extra subtitle: %v", i, frame.SentenceStarts)
						}
					}
					if result.AssistantText != "你好。" || !output.ended || output.endReason != TurnEndCompleted {
						t.Fatalf("unexpected result: %+v, end=%v", result, output.endReason)
					}
				})
			}
		}
	}
}

func TestTurnEngine_PromptToneRequiresSynthesizedText(t *testing.T) {
	input := make(chan []byte)
	close(input)
	output := &mockTurnOutput{}
	tts := &mockTTSClient{}
	result := NewEngine().HandleTurn(context.Background(), TurnRequest{
		TurnId: 1, Mode: "auto", PromptToneEnabled: true,
		ASRClient: &mockASRClient{text: "你好"},
		LLMClient: &mockLLMClient{chunks: []ai.LLMChunk{{Text: " "}}, finalText: "未合成的最终文本"},
		TTSClient: tts,
	}, input, output)
	if result.Status != TurnCompleted || result.AssistantText != "未合成的最终文本" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if tts.sessionCount != 0 || len(output.audioFrames) != 0 {
		t.Fatalf("unsynthesized text must not produce a prompt: sessions=%d, frames=%d", tts.sessionCount, len(output.audioFrames))
	}
}

func TestTurnEngine_ResponseFailureDoesNotAppendPrompt(t *testing.T) {
	failure := errors.New("response failed")
	for _, stage := range []string{"llm", "tts", "abort"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			synthesized := make(chan struct{})
			llm := &mockLLMClient{
				chunks: []ai.LLMChunk{{Text: "已经合成的正文。"}},
				afterChunks: func() {
					select {
					case <-synthesized:
					case <-ctx.Done():
					}
				},
			}
			tts := &mockTTSClient{afterSynthesize: func() { close(synthesized) }}
			wantStatus := TurnFailed
			switch stage {
			case "llm":
				llm.err = failure
			case "tts":
				tts.err = failure
			case "abort":
				wantStatus = TurnAborted
				tts.afterSynthesize = func() {
					cancel()
					close(synthesized)
				}
			}

			input := make(chan []byte)
			close(input)
			output := &mockTurnOutput{}
			result := NewEngine().HandleTurn(ctx, TurnRequest{
				TurnId: 1, Mode: "auto", PromptToneEnabled: true,
				ASRClient: &mockASRClient{text: "你好"}, LLMClient: llm, TTSClient: tts,
			}, input, output)
			if result.Status != wantStatus {
				t.Fatalf("status = %v, want %v: %v", result.Status, wantStatus, result.Err)
			}
			if stage != "abort" && !errors.Is(result.Err, failure) {
				t.Fatalf("unexpected error: %v", result.Err)
			}
			if len(output.audioFrames) > 1 {
				t.Fatalf("failed response appended extra audio: %d frames", len(output.audioFrames))
			}
		})
	}
}
