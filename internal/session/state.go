package session

import "fmt"

// State 表示 WebSocket 会话的状态机状态。
type State int

const (
	// StateConnected 连接已建立，等待客户端 hello 握手。
	StateConnected State = iota

	// StateReady 握手成功或上一轮问答结束，等待 listen.start 开始收音。
	StateReady

	// StateListening 已收到 listen.start，正在收音与 ASR 处理中。
	StateListening

	// StateProcessing 收到 ASR 最终识别文本，已下发 STT，LLM 处理中。
	StateProcessing

	// StateSpeaking 已发送 tts.start，正在按句下发 TTS 音频或独立提示音。
	StateSpeaking

	// StateClosed 连接已关闭或会话终止。
	StateClosed
)

// String 返回状态名称的字符串表示。
func (s State) String() string {
	switch s {
	case StateConnected:
		return "CONNECTED"
	case StateReady:
		return "READY"
	case StateListening:
		return "LISTENING"
	case StateProcessing:
		return "PROCESSING"
	case StateSpeaking:
		return "SPEAKING"
	case StateClosed:
		return "CLOSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}
