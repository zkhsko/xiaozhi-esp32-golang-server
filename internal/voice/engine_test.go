package voice

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/audio"
)

// mockASRClient 实现测试用 ASRClient
type mockASRClient struct {
	text string
	err  error
}

func (m *mockASRClient) Recognize(ctx context.Context, req ai.ASRRequest, pcm <-chan []byte) (string, error) {
	// 排空 pcm
	for range pcm {
	}
	if m.err != nil {
		return "", m.err
	}
	return m.text, nil
}

// mockLLMClient 实现测试用 LLMClient
type mockLLMClient struct {
	chunks    []ai.LLMChunk
	finalText string
	err       error
}

func (m *mockLLMClient) Generate(ctx context.Context, req ai.LLMRequest, chunks chan<- ai.LLMChunk) (ai.LLMResult, error) {
	if m.err != nil {
		return ai.LLMResult{}, m.err
	}
	if chunks != nil {
		for _, c := range m.chunks {
			select {
			case chunks <- c:
			case <-ctx.Done():
				return ai.LLMResult{}, ctx.Err()
			}
		}
	}
	return ai.LLMResult{FinalText: m.finalText}, nil
}

// mockTTSSession 实现测试用 TTSSession
type mockTTSSession struct {
	closed     bool
	synthCount int
}

func (s *mockTTSSession) Synthesize(ctx context.Context, text string, pcm chan<- ai.PCMChunk) error {
	s.synthCount++
	// 生成 2880 字节的假 PCM
	chunk := ai.PCMChunk{
		Data:          make([]byte, 2880),
		SentenceStart: text,
	}
	select {
	case pcm <- chunk:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *mockTTSSession) Close() error {
	s.closed = true
	return nil
}

// mockTTSClient 实现测试用 TTSClient
type mockTTSClient struct {
	mu           sync.Mutex
	sessionCount int
	lastSession  *mockTTSSession
}

func (m *mockTTSClient) CreateSession(ctx context.Context) (ai.TTSSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionCount++
	sess := &mockTTSSession{}
	m.lastSession = sess
	return sess, nil
}

// mockTurnOutput 实现测试用 TurnOutput
type mockTurnOutput struct {
	mu          sync.Mutex
	sttText     string
	audioFrames []AudioFrame
	ended       bool
	endReason   TurnEndReason
}

func (m *mockTurnOutput) SendSTT(ctx context.Context, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sttText = text
	return nil
}

func (m *mockTurnOutput) SendAudio(ctx context.Context, frame AudioFrame) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audioFrames = append(m.audioFrames, frame)
	return nil
}

func (m *mockTurnOutput) End(ctx context.Context, reason TurnEndReason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ended = true
	m.endReason = reason
	return nil
}

func TestTurnEngine_Auto_NormalCompletion(t *testing.T) {
	engine := NewEngine()

	asr := &mockASRClient{text: "今天天气怎么样？"}
	llm := &mockLLMClient{
		chunks: []ai.LLMChunk{
			{Text: "今天天气非常晴朗，适合外出散步。"},
		},
	}
	tts := &mockTTSClient{}
	output := &mockTurnOutput{}

	inCh := make(chan []byte, 1)
	close(inCh)

	var inputClosed atomic.Bool

	req := TurnRequest{
		TurnId:    1,
		Mode:      "auto",
		ASRClient: asr,
		LLMClient: llm,
		TTSClient: tts,
		OnInputClosed: func() {
			inputClosed.Store(true)
		},
	}

	res := engine.HandleTurn(context.Background(), req, inCh, output)

	if res.Status != TurnCompleted {
		t.Fatalf("expected TurnCompleted, got %v, err: %v", res.Status, res.Err)
	}
	if res.UserText != "今天天气怎么样？" {
		t.Fatalf("expected userText %q, got %q", "今天天气怎么样？", res.UserText)
	}
	if !inputClosed.Load() {
		t.Fatal("expected OnInputClosed to be called")
	}
	if output.sttText != "今天天气怎么样？" {
		t.Fatalf("expected output STT %q, got %q", "今天天气怎么样？", output.sttText)
	}
	if len(output.audioFrames) == 0 {
		t.Fatal("expected at least 1 audio frame output")
	}
	if tts.sessionCount != 1 {
		t.Fatalf("expected exactly 1 tts session created, got %d", tts.sessionCount)
	}
	if !output.ended || output.endReason != TurnEndCompleted {
		t.Fatalf("expected output ended with TurnEndCompleted, got %v, reason=%v", output.ended, output.endReason)
	}
}

func TestTurnEngine_Manual_NoSpeech(t *testing.T) {
	engine := NewEngine()

	asr := &mockASRClient{text: ""}
	llm := &mockLLMClient{}
	tts := &mockTTSClient{}
	output := &mockTurnOutput{}

	inCh := make(chan []byte)
	close(inCh)

	req := TurnRequest{
		TurnId:    2,
		Mode:      "manual",
		ASRClient: asr,
		LLMClient: llm,
		TTSClient: tts,
	}

	res := engine.HandleTurn(context.Background(), req, inCh, output)

	if res.Status != TurnNoSpeech {
		t.Fatalf("expected TurnNoSpeech, got %v", res.Status)
	}
	if output.sttText != "" {
		t.Fatalf("expected no STT sent, got %q", output.sttText)
	}
	if len(output.audioFrames) != 0 {
		t.Fatalf("expected 0 audio frames, got %d", len(output.audioFrames))
	}
	if tts.sessionCount != 0 {
		t.Fatalf("expected 0 tts sessions, got %d", tts.sessionCount)
	}
}

func TestTurnEngine_Auto_EmptyASR_Failed(t *testing.T) {
	engine := NewEngine()

	asr := &mockASRClient{err: errors.New("empty asr text in auto mode")}
	llm := &mockLLMClient{}
	tts := &mockTTSClient{}
	output := &mockTurnOutput{}

	inCh := make(chan []byte)
	close(inCh)

	req := TurnRequest{
		TurnId:    3,
		Mode:      "auto",
		ASRClient: asr,
		LLMClient: llm,
		TTSClient: tts,
	}

	res := engine.HandleTurn(context.Background(), req, inCh, output)

	if res.Status != TurnFailed {
		t.Fatalf("expected TurnFailed, got %v", res.Status)
	}
	if output.endReason != TurnEndFailed {
		t.Fatalf("expected TurnEndFailed, got %v", output.endReason)
	}
}

func TestTurnEngine_FinalTextFallback(t *testing.T) {
	engine := NewEngine()

	asr := &mockASRClient{text: "你好"}
	// 没有任何 chunks，只有 finalText
	llm := &mockLLMClient{
		finalText: "这是最后的兜底回复文本。",
	}
	tts := &mockTTSClient{}
	output := &mockTurnOutput{}

	inCh := make(chan []byte)
	close(inCh)

	req := TurnRequest{
		TurnId:    4,
		Mode:      "auto",
		ASRClient: asr,
		LLMClient: llm,
		TTSClient: tts,
	}

	res := engine.HandleTurn(context.Background(), req, inCh, output)

	if res.Status != TurnCompleted {
		t.Fatalf("expected TurnCompleted, got %v, err: %v", res.Status, res.Err)
	}
	if res.AssistantText != "这是最后的兜底回复文本。" {
		t.Fatalf("expected assistant text %q, got %q", "这是最后的兜底回复文本。", res.AssistantText)
	}
}

func TestTurnEngine_Abort(t *testing.T) {
	engine := NewEngine()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消

	asr := &mockASRClient{text: "测试打断"}
	llm := &mockLLMClient{}
	tts := &mockTTSClient{}
	output := &mockTurnOutput{}

	inCh := make(chan []byte)

	req := TurnRequest{
		TurnId:    5,
		Mode:      "auto",
		ASRClient: asr,
		LLMClient: llm,
		TTSClient: tts,
	}

	res := engine.HandleTurn(ctx, req, inCh, output)

	if res.Status != TurnAborted {
		t.Fatalf("expected TurnAborted, got %v", res.Status)
	}
	if output.endReason != TurnEndAborted {
		t.Fatalf("expected TurnEndAborted, got %v", output.endReason)
	}
}

func TestPaceForward_Basic(t *testing.T) {
	output := &mockTurnOutput{}
	framesCh := make(chan AudioFrame, 3)

	framesCh <- AudioFrame{OpusData: []byte("frame1"), SentenceStarts: []string{"句1"}}
	framesCh <- AudioFrame{OpusData: []byte("frame2")}
	close(framesCh)

	err := PaceForward(context.Background(), framesCh, output)
	if err != nil {
		t.Fatalf("PaceForward failed: %v", err)
	}

	if len(output.audioFrames) != 2 {
		t.Fatalf("expected 2 frames sent, got %d", len(output.audioFrames))
	}
	if len(output.audioFrames[0].SentenceStarts) != 1 || output.audioFrames[0].SentenceStarts[0] != "句1" {
		t.Fatalf("expected sentence start '句1', got %v", output.audioFrames[0].SentenceStarts)
	}
}

func TestEncoderStage_ContinuousPcm(t *testing.T) {
	pcmCh := make(chan ai.PCMChunk, 2)
	audioFrameCh := make(chan AudioFrame, 5)

	// 喂入 2880 字节的 PCM
	pcmCh <- ai.PCMChunk{
		Data:          make([]byte, audio.DownlinkBytesPerFrame),
		SentenceStart: "首句",
	}
	close(pcmCh)

	err := runEncoderStage(context.Background(), audio.DefaultMaxOpusPacketBytes, pcmCh, audioFrameCh)
	if err != nil {
		t.Fatalf("runEncoderStage failed: %v", err)
	}

	var frames []AudioFrame
	for f := range audioFrameCh {
		frames = append(frames, f)
	}

	if len(frames) != 1 {
		t.Fatalf("expected 1 audio frame, got %d", len(frames))
	}
	if len(frames[0].SentenceStarts) != 1 || frames[0].SentenceStarts[0] != "首句" {
		t.Fatalf("expected SentenceStarts ['首句'], got %v", frames[0].SentenceStarts)
	}
}
