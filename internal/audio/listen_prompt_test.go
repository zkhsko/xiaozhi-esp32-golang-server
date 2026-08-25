package audio

import (
	"sync"
	"testing"
)

// TestListenPrompt_PCMAndOpusPackets 验证内嵌提示音 PCM 与 Opus 包的自动分帧与格式有效性。
func TestListenPrompt_PCMAndOpusPackets(t *testing.T) {
	pcm, err := GetListenPromptPCM()
	if err != nil {
		t.Fatalf("GetListenPromptPCM failed: %v", err)
	}

	if len(pcm) == 0 || len(pcm)%DownlinkBytesPerFrame != 0 {
		t.Fatalf("expected PCM length to be positive multiple of %d, got %d", DownlinkBytesPerFrame, len(pcm))
	}

	expectedFrames := len(pcm) / DownlinkBytesPerFrame

	packets, err := GetListenPromptOpusPackets()
	if err != nil {
		t.Fatalf("GetListenPromptOpusPackets failed: %v", err)
	}

	if len(packets) != expectedFrames {
		t.Fatalf("expected %d opus packets, got %d", expectedFrames, len(packets))
	}

	for i, pkt := range packets {
		if len(pkt) == 0 {
			t.Errorf("packet %d is empty", i)
		}
	}

	frameCount, err := GetListenPromptFrameCount()
	if err != nil {
		t.Fatalf("GetListenPromptFrameCount failed: %v", err)
	}
	if frameCount != expectedFrames {
		t.Fatalf("expected frameCount %d, got %d", expectedFrames, frameCount)
	}
}

// TestListenPrompt_ConcurrentAccess 验证多协程并发获取提示音数据的安全性与独立内存分配。
func TestListenPrompt_ConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pcm, err := GetListenPromptPCM()
			if err != nil || len(pcm) == 0 {
				t.Errorf("concurrent GetListenPromptPCM failed: %v, len: %d", err, len(pcm))
				return
			}
			// 修改本地切片不影响后续调用
			pcm[0] = 0xFF

			pkts, err := GetListenPromptOpusPackets()
			if err != nil || len(pkts) == 0 {
				t.Errorf("concurrent GetListenPromptOpusPackets failed: %v, count: %d", err, len(pkts))
			}
		}()
	}
	wg.Wait()
}
