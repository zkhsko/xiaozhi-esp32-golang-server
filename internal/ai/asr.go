package ai

import "context"

// ASRMode 定义语音识别模式。
type ASRMode string

const (
	// ASRModeAuto 由服务端或云端 VAD 自动判断说话结束。
	ASRModeAuto ASRMode = "auto"

	// ASRModeManual 由客户端显式 listen.stop 触发输入结束。
	ASRModeManual ASRMode = "manual"
)

// ASRRequest 封装单轮语音识别的请求参数。
type ASRRequest struct {
	Mode       ASRMode
	SampleRate int
	Channels   int
}

// ASRClient 定义消费 PCM 音频流并返回最终识别结果的语音识别客户端接口。
type ASRClient interface {
	// Recognize 消费 16 kHz 单声道 16-bit signed PCM 帧流，返回最终文本识别结果。
	Recognize(
		ctx context.Context,
		req ASRRequest,
		pcm <-chan []byte,
	) (string, error)
}
