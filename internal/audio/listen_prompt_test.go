package audio

import (
	"bytes"
	"sync"
	"testing"
)

func TestGetListenPromptOpusPackets_Success(t *testing.T) {
	packets, err := GetListenPromptOpusPackets()
	if err != nil {
		t.Fatalf("GetListenPromptOpusPackets returned unexpected error: %v", err)
	}

	if len(packets) == 0 {
		t.Fatal("expected non-empty prompt opus packets")
	}

	for i, pkt := range packets {
		if len(pkt) == 0 {
			t.Fatalf("packet %d is empty", i)
		}
		if len(pkt) > DefaultMaxOpusPacketBytes {
			t.Fatalf("packet %d size %d exceeds max %d", i, len(pkt), DefaultMaxOpusPacketBytes)
		}
	}
}

func TestGetListenPromptOpusPackets_DeepCopy(t *testing.T) {
	packets1, err := GetListenPromptOpusPackets()
	if err != nil {
		t.Fatalf("GetListenPromptOpusPackets first call failed: %v", err)
	}

	packets2, err := GetListenPromptOpusPackets()
	if err != nil {
		t.Fatalf("GetListenPromptOpusPackets second call failed: %v", err)
	}

	if len(packets1) != len(packets2) {
		t.Fatalf("expected packet length %d, got %d", len(packets1), len(packets2))
	}

	for i := range packets1 {
		if !bytes.Equal(packets1[i], packets2[i]) {
			t.Fatalf("packet %d content mismatch", i)
		}
	}

	// 修改 packets1[0] 的第一个字节，验证不污染 packets2
	originalByte := packets1[0][0]
	packets1[0][0] ^= 0xFF
	if packets2[0][0] != originalByte {
		t.Fatal("modifying returned packet corrupted subsequent or parallel copies")
	}
}

func TestGetListenPromptOpusPackets_Concurrent(t *testing.T) {
	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			pkts, err := GetListenPromptOpusPackets()
			if err != nil {
				t.Errorf("concurrent call failed: %v", err)
				return
			}
			if len(pkts) == 0 {
				t.Errorf("concurrent call returned empty packets")
			}
		}()
	}

	wg.Wait()
}
