package session

import "fmt"

// State 表示 WebSocket 会话的状态机状态。
type State int

const (
	// StateAwaitHello 连接已建立，等待客户端 hello 握手。
	StateAwaitHello State = iota

	// StateReady 握手成功或上一轮问答结束，等待 listen.start 开始收音。
	StateReady

	// StateTurnActive 正在进行单轮问答（收音、ASR、LLM 或回答播报）。
	StateTurnActive

	// StateClosed 连接已关闭或会话终止。
	StateClosed
)

// String 返回状态名称的字符串表示。
func (s State) String() string {
	switch s {
	case StateAwaitHello:
		return "AWAIT_HELLO"
	case StateReady:
		return "READY"
	case StateTurnActive:
		return "TURN_ACTIVE"
	case StateClosed:
		return "CLOSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}
