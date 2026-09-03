package voice

import (
	"context"
	"time"
)

// PaceForward 负责以 60 ms 目标节拍将 Opus 音频帧转发给 TurnOutput。
func PaceForward(
	ctx context.Context,
	frames <-chan AudioFrame,
	output TurnOutput,
) error {
	const frameInterval = 60 * time.Millisecond

	// 读取首帧立即发送
	var firstFrame AudioFrame
	var ok bool
	select {
	case <-ctx.Done():
		return ctx.Err()
	case firstFrame, ok = <-frames:
		if !ok {
			return nil
		}
	}

	if err := output.SendAudio(ctx, firstFrame); err != nil {
		return err
	}

	nextTick := time.Now().Add(frameInterval)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-frames:
			if !ok {
				return nil
			}

			// 等待至目标节拍点
			waitDuration := time.Until(nextTick)
			if waitDuration > 0 {
				timer := time.NewTimer(waitDuration)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}

			if err := output.SendAudio(ctx, frame); err != nil {
				return err
			}

			now := time.Now()
			if now.After(nextTick.Add(frameInterval)) {
				// 落后超过一个周期，不突发追赶，从当前时点重新建立节拍
				nextTick = now.Add(frameInterval)
			} else {
				nextTick = nextTick.Add(frameInterval)
			}
		}
	}
}
