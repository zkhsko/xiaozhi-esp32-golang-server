package ai

import "context"

// TTSStream 表示单个回答级的语音合成流式会话。
//
// 契约语义：
//   - 单 Stream 单轮回答：一个 Stream 实例仅属于一轮回答并持有一条物理连接，禁止跨轮复用；
//   - 句子禁止并发：同一 Stream 的 SynthesizeSentence 禁止并发调用，相邻句子必须串行执行；
//   - Cancel 不启动第二读取者：Cancel 仅发送取消控制消息并使活跃任务退出，不启动第二个读取协程，无活跃任务时幂等返回；
//   - Close 幂等：一轮正常结束、失败、取消或会话关闭时必须调用 Close，幂等关闭流式合成会话并释放物理连接与资源。
type TTSStream interface {
	// SynthesizeSentence 同步合成单句，并通过 onPCM 回调交付 PCM 数据。
	SynthesizeSentence(
		ctx context.Context,
		text string,
		onPCM func(context.Context, []byte) error,
	) error

	// Cancel 取消当前活跃任务。无活跃任务时幂等返回。不启动第二读取者。
	Cancel(ctx context.Context) error

	// Close 幂等关闭流式合成会话并释放物理连接与资源。
	Close() error
}

// TTSClient 定义流式语音合成客户端接口。
// TTSClient 仅保存不可变的客户端配置，不持有跨轮网络连接。
type TTSClient interface {
	// CreateStream 创建并启动一条回答级的流式语音合成会话。
	CreateStream(ctx context.Context) (TTSStream, error)
}
