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

// TTSPacketStream 表示单句语音合成生成的下行音频包流。
type TTSPacketStream interface {
	// NextPacket 接收下一个已编码的 Opus 音频包。
	// 当该句全部音频合成并编码完毕时返回 nil, io.EOF。
	// 当流发生错误、超时或被取消时返回 nil, err。
	NextPacket(ctx context.Context) ([]byte, error)

	// Cancel 显式向远端服务端发送取消指令中止当前合成并释放资源。
	Cancel(ctx context.Context) error

	// Close 释放流占用的网络与编解码资源。
	Close() error
}

// TTSSession 表示单轮问答中独占的流式语音合成会话，在单轮生命周期内复用底层长连接。
type TTSSession interface {
	// Synthesize 发起单句语音合成，复用当前会话连接，返回该句的 Opus 数据包流。
	Synthesize(ctx context.Context, text string) (TTSPacketStream, error)

	// Close 关闭底层连接并释放该轮所占用的网络与会话资源。
	Close() error
}

// TTSClient 定义流式语音合成客户端接口。
type TTSClient interface {
	// CreateSession 为单轮问答创建并建立一条 TTS 长连接会话。
	CreateSession(ctx context.Context) (TTSSession, error)
}
