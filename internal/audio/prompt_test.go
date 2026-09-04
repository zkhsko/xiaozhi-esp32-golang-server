package audio

import (
	"sync"
	"testing"
)

func TestGetPromptPCM_Basic(t *testing.T) {
	pcm, err := GetPromptPCM()
	if err != nil {
		t.Fatalf("GetPromptPCM failed: %v", err)
	}

	if len(pcm) == 0 {
		t.Fatalf("expected non-empty PCM data")
	}

	// 16-bit PCM 字节数必须为偶数
	if len(pcm)%2 != 0 {
		t.Fatalf("expected PCM length to be even, got %d", len(pcm))
	}

	// 验证可以被 StreamEncoder 正常分帧和编码
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("NewEncoder failed: %v", err)
	}
	defer enc.Close()

	streamEnc := NewStreamEncoder(enc)
	defer streamEnc.Close()

	pkts, err := streamEnc.Feed(pcm)
	if err != nil {
		t.Fatalf("feed prompt pcm failed: %v", err)
	}

	flushPkts, err := streamEnc.Flush()
	if err != nil {
		t.Fatalf("flush stream encoder failed: %v", err)
	}

	allPkts := append(pkts, flushPkts...)
	if len(allPkts) == 0 {
		t.Fatalf("expected at least one opus packet produced, got 0")
	}

	for i, pkt := range allPkts {
		if len(pkt) == 0 {
			t.Errorf("packet %d is empty", i)
		}
	}
}

func TestGetPromptPCM_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pcm, err := GetPromptPCM()
			if err != nil || len(pcm) == 0 {
				t.Errorf("concurrent GetPromptPCM failed: %v, len: %d", err, len(pcm))
				return
			}
			// 修改本地副本，验证跨协程独立性
			pcm[0] = 0xFF
		}()
	}
	wg.Wait()

	// 再次获取，确保未被协程修改污染
	pcmAfter, err := GetPromptPCM()
	if err != nil {
		t.Fatalf("GetPromptPCM failed: %v", err)
	}
	if pcmAfter[0] == 0xFF {
		t.Errorf("prompt pcm was modified by concurrent test")
	}
}

func TestDecodeEmbeddedPrompt_InvalidData(t *testing.T) {
	// 空数据
	_, err := decodeEmbeddedPrompt(nil)
	if err == nil {
		t.Errorf("expected error for nil data, got nil")
	}

	// 非 Ogg 格式损坏数据
	_, err = decodeEmbeddedPrompt([]byte("not an ogg file"))
	if err == nil {
		t.Errorf("expected error for invalid ogg data, got nil")
	}
}
