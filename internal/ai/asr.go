package ai

import "context"

// ASRStream 表示单个语音识别流式会话。
// 客户端通过该接口向服务端流式写入 PCM 音频帧并获取最终识别结果。
type ASRStream interface {
	// WritePCM 流式写入 PCM 二进制音频帧（16000 Hz、16-bit、单声道小端）。
	WritePCM(ctx context.Context, data []byte) error

	// Finish 通知服务端音频流输入已结束。
	Finish(ctx context.Context) error

	// Result 等待并返回服务端最终非空识别文本。
	Result(ctx context.Context) (string, error)

	// Close 关闭并释放流式识别会话的所有网络与内存资源。
	Close() error
}

// ASRClient 定义流式语音识别客户端接口。
type ASRClient interface {
	// CreateStream 创建并启动一条新的流式语音识别会话。
	CreateStream(ctx context.Context) (ASRStream, error)
}
