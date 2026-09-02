package ai_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"xiaozhi-esp32-golang-server/internal/ai"
)

type mockTTSStream struct {
	synthesizeFn func(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error
	cancelFn     func(ctx context.Context) error
	closeFn      func() error
}

func (m *mockTTSStream) SynthesizeSentence(ctx context.Context, text string, onPCM func(context.Context, []byte) error) error {
	if m.synthesizeFn != nil {
		return m.synthesizeFn(ctx, text, onPCM)
	}
	return nil
}

func (m *mockTTSStream) Cancel(ctx context.Context) error {
	if m.cancelFn != nil {
		return m.cancelFn(ctx)
	}
	return nil
}

func (m *mockTTSStream) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

type mockTTSClient struct {
	createStreamFn func(ctx context.Context) (ai.TTSStream, error)
}

func (m *mockTTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	if m.createStreamFn != nil {
		return m.createStreamFn(ctx)
	}
	return &mockTTSStream{}, nil
}

var (
	_ ai.TTSStream = (*mockTTSStream)(nil)
	_ ai.TTSClient = (*mockTTSClient)(nil)
)

func TestTTSContract_SynthesizeSentenceAndCallbacks(t *testing.T) {
	ctx := context.Background()
	samplePCM := []byte{0x01, 0x02, 0x03, 0x04}
	expectedText := "你好，世界！"

	var receivedText string
	var pcmCollector [][]byte

	stream := &mockTTSStream{
		synthesizeFn: func(sCtx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			receivedText = text
			if err := onPCM(sCtx, samplePCM); err != nil {
				return err
			}
			return nil
		},
	}

	client := &mockTTSClient{
		createStreamFn: func(cCtx context.Context) (ai.TTSStream, error) {
			return stream, nil
		},
	}

	createdStream, err := client.CreateStream(ctx)
	if err != nil {
		t.Fatalf("CreateStream failed: %v", err)
	}

	err = createdStream.SynthesizeSentence(ctx, expectedText, func(pCtx context.Context, data []byte) error {
		pcmCollector = append(pcmCollector, data)
		return nil
	})
	if err != nil {
		t.Fatalf("SynthesizeSentence failed: %v", err)
	}

	if receivedText != expectedText {
		t.Errorf("expected text %q, got %q", expectedText, receivedText)
	}
	if len(pcmCollector) != 1 || !bytes.Equal(pcmCollector[0], samplePCM) {
		t.Errorf("unexpected PCM data received: %v", pcmCollector)
	}
}

func TestTTSContract_CallbackErrorPropagation(t *testing.T) {
	ctx := context.Background()
	expectedErr := errors.New("pcm callback failure")

	stream := &mockTTSStream{
		synthesizeFn: func(sCtx context.Context, text string, onPCM func(context.Context, []byte) error) error {
			return onPCM(sCtx, []byte{0x00})
		},
	}

	err := stream.SynthesizeSentence(ctx, "测试文本", func(pCtx context.Context, data []byte) error {
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}

func TestTTSContract_CancelAndClose(t *testing.T) {
	ctx := context.Background()
	cancelCalled := false
	closeCalled := false

	stream := &mockTTSStream{
		cancelFn: func(cCtx context.Context) error {
			cancelCalled = true
			return nil
		},
		closeFn: func() error {
			closeCalled = true
			return nil
		},
	}

	if err := stream.Cancel(ctx); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	if !cancelCalled {
		t.Errorf("expected Cancel to be called")
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !closeCalled {
		t.Errorf("expected Close to be called")
	}
}
