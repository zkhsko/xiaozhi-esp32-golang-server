package voice

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// TurnStatus 定义单轮问答的终态。
type TurnStatus int

const (
	// TurnCompleted 问答完整成功交付（音频与 tts.stop 已成功发送）。
	TurnCompleted TurnStatus = iota

	// TurnAborted 用户打断或取消。
	TurnAborted

	// TurnFailed 链路出现错误（ASR/LLM/TTS/编解码/下行故障）。
	TurnFailed

	// TurnNoSpeech manual 模式下未检测到有效语音。
	TurnNoSpeech
)

func (s TurnStatus) String() string {
	switch s {
	case TurnCompleted:
		return "COMPLETED"
	case TurnAborted:
		return "ABORTED"
	case TurnFailed:
		return "FAILED"
	case TurnNoSpeech:
		return "NO_SPEECH"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}

// TurnEffectType 定义工具执行副作用类型。
type TurnEffectType int

const (
	// EffectCloseSession 工具请求关闭会话。
	EffectCloseSession TurnEffectType = 1
)

// TurnEffect 封装工具执行产生的不可变副作用值。
type TurnEffect struct {
	Type TurnEffectType
}

// TurnResult 封装单轮问答退出的唯一权威结果。
type TurnResult struct {
	TurnId        uint64
	Status        TurnStatus
	UserText      string
	AssistantText string
	Effects       []TurnEffect
	Err           error
}

// AudioFrame 封装待节拍下发的单包 60 ms Opus 数据及其关联的句首字幕标记。
type AudioFrame struct {
	OpusData       []byte
	SentenceStarts []string
}

// TurnEndReason 定义轮次终止原因。
type TurnEndReason int

const (
	TurnEndCompleted TurnEndReason = iota
	TurnEndAborted
	TurnEndFailed
)

// TurnOutput 定义单轮输出管理接口。
type TurnOutput interface {
	// SendSTT 下发 ASR 识别文本。
	SendSTT(ctx context.Context, text string) error

	// SendAudio 下发单包 Opus 音频（首次调用负责原子下发 tts.start 及首句字幕）。
	SendAudio(ctx context.Context, frame AudioFrame) error

	// End 终结单轮输出，根据 tts.start 是否真实写出严格补发 tts.stop。
	End(ctx context.Context, reason TurnEndReason) error
}

// AudioStream 定义上行 16 kHz 60 ms Opus 包输入通道。
type AudioStream <-chan []byte

// TurnRequest 封装启动单轮处理的完整上下文与依赖。
type TurnRequest struct {
	TurnId             uint64
	Mode               string // "auto" or "manual"
	SystemPrompt       string
	History            []ai.Message
	Tools              []ai.Tool
	ASRClient          ai.ASRClient
	LLMClient          ai.LLMClient
	TTSClient          ai.TTSClient
	EffectsCh          <-chan TurnEffect
	MaxOpusPacketBytes int
	TTSSentenceTimeout time.Duration
	Logger             *slog.Logger
	OnInputClosed      func()
}
