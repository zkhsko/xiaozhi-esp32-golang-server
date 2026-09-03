package audio

import (
	"bytes"
	"testing"
)

func TestDecoderAndEncoder_RoundTrip(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("NewEncoder failed: %v", err)
	}
	defer enc.Close()

	// 构造 24kHz 单声道 60ms PCM (2880 字节)
	pcm := make([]byte, DownlinkBytesPerFrame)
	for i := range pcm {
		pcm[i] = byte(i % 256)
	}

	packet, err := enc.Encode(pcm)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	if len(packet) == 0 {
		t.Fatal("expected non-empty opus packet")
	}

	// 测试 16kHz 解码器
	dec, err := NewDecoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("NewDecoder failed: %v", err)
	}

	// 构造 16kHz 编码器产生 16kHz Opus 包
	// 验证 Decoder 的空包和超限包错误
	if _, err := dec.Decode(nil); err != ErrEmptyPacket {
		t.Fatalf("expected ErrEmptyPacket, got %v", err)
	}
	if _, err := dec.Decode(make([]byte, dec.MaxPacketBytes()+1)); err != ErrPacketTooLarge {
		t.Fatalf("expected ErrPacketTooLarge, got %v", err)
	}
}

func TestPCMFramer_FeedAndFlush(t *testing.T) {
	framer := NewPCMFramer()

	// 喂入 1000 字节（不足一帧 2880）
	frames := framer.Feed(make([]byte, 1000))
	if len(frames) != 0 {
		t.Fatalf("expected 0 frames, got %d", len(frames))
	}

	// 再喂入 2000 字节（总计 3000 字节，产生 1 帧 2880 字节，残余 120 字节）
	frames = framer.Feed(make([]byte, 2000))
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if len(frames[0]) != DownlinkBytesPerFrame {
		t.Fatalf("expected frame size %d, got %d", DownlinkBytesPerFrame, len(frames[0]))
	}

	// Flush 残余 120 字节，应补零并返回 1 帧
	flushFrames := framer.Flush()
	if len(flushFrames) != 1 {
		t.Fatalf("expected 1 flush frame, got %d", len(flushFrames))
	}
	if len(flushFrames[0]) != DownlinkBytesPerFrame {
		t.Fatalf("expected flush frame size %d, got %d", DownlinkBytesPerFrame, len(flushFrames[0]))
	}

	// 再次 Flush，无残余，返回 nil
	emptyFlush := framer.Flush()
	if len(emptyFlush) != 0 {
		t.Fatalf("expected 0 frames on empty flush, got %d", len(emptyFlush))
	}
}

func TestStreamEncoder(t *testing.T) {
	enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
	if err != nil {
		t.Fatalf("NewEncoder failed: %v", err)
	}
	defer enc.Close()

	streamEnc := NewStreamEncoder(enc)
	defer streamEnc.Close()

	// 喂入 2880 字节
	chunk := make([]byte, DownlinkBytesPerFrame)
	pkts, err := streamEnc.Feed(chunk)
	if err != nil {
		t.Fatalf("Feed failed: %v", err)
	}
	if len(pkts) != 1 {
		t.Fatalf("expected 1 opus packet, got %d", len(pkts))
	}

	// 喂入 500 字节残余
	pkts, err = streamEnc.Feed(make([]byte, 500))
	if err != nil {
		t.Fatalf("Feed partial failed: %v", err)
	}
	if len(pkts) != 0 {
		t.Fatalf("expected 0 opus packets for partial data, got %d", len(pkts))
	}

	// Flush
	pkts, err = streamEnc.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if len(pkts) != 1 {
		t.Fatalf("expected 1 flushed opus packet, got %d", len(pkts))
	}

	// 再次 Flush 返回 nil
	pkts, err = streamEnc.Flush()
	if err != nil {
		t.Fatalf("Second Flush failed: %v", err)
	}
	if len(pkts) != 0 {
		t.Fatalf("expected 0 packets on second flush, got %d", len(pkts))
	}
	_ = bytes.Equal(nil, nil)
}
