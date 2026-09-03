package ai

import (
	"context"
	"time"
)

// TTSOptions 封装创建流式语音合成客户端所需的配置参数。
type TTSOptions struct {
	Provider       string
	Endpoint       string
	APIKey         string
	Model          string
	Voice          string
	ProxyURL       string
	ConnectTimeout time.Duration
}

// PCMChunk 表示 TTS 输出的 24 kHz 单声道 16-bit PCM 片段。
type PCMChunk struct {
	Data          []byte
	SentenceStart string
}

// TTSSession 表示单轮问答中独占的流式语音合成会话，在单轮生命周期内复用底层长连接。
type TTSSession interface {
	// Synthesize 发起单句语音合成，复用当前会话长连接，流式输出 PCM 片段到 pcm 通道。
	// 该方法为同步调用，返回前结束该句所有内部读写任务。
	Synthesize(
		ctx context.Context,
		text string,
		pcm chan<- PCMChunk,
	) error

	// Close 关闭底层长连接并释放该轮所占用的网络与会话资源。
	Close() error
}

// TTSClient 定义流式语音合成客户端接口。
type TTSClient interface {
	// CreateSession 为单轮问答创建并建立一条 TTS 长连接会话。
	CreateSession(ctx context.Context) (TTSSession, error)
}
