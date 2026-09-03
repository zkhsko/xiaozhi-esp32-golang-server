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
	QueueCapacity  int
}

// TTSStream 表示单个回答级的语音合成流式会话。
// 客户端通过该接口按顺序流式写入完整句子，并流式接收合成的 PCM 音频块。
type TTSStream interface {
	// SendSentence 向合成流中按顺序写入一个待合成的完整句子。
	SendSentence(ctx context.Context, text string) error

	// Finish 通知服务端文本输入已全部结束。
	Finish(ctx context.Context) error

	// NextPCM 接收下一个合成的 PCM 音频块（24000 Hz、16-bit、单声道有符号小端）。
	// 当所有音频块接收完毕且任务正常完成时返回 nil, io.EOF。
	// 当流发生错误、超时或被取消时返回 nil, err。
	NextPCM(ctx context.Context) ([]byte, error)

	// Close 关闭并释放流式合成会话的所有网络与内存资源。
	Close() error
}

// TTSClient 定义流式语音合成客户端接口。
type TTSClient interface {
	// CreateStream 创建并启动一条回答级的流式语音合成会话。
	CreateStream(ctx context.Context) (TTSStream, error)
}
