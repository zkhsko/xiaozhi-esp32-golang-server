package agentkit

import (
	"time"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// ToolGetCurrentTime 获取当前时间工具名称常量。
const ToolGetCurrentTime = "server.get_current_time"

// GetCurrentTimeInput 定义获取当前时间工具的入参。
type GetCurrentTimeInput struct{}

// GetCurrentTimeOutput 定义获取当前时间工具的结构化返回值。
type GetCurrentTimeOutput struct {
	DateTime  string `json:"datetime"`
	Date      string `json:"date"`
	Time      string `json:"time"`
	Weekday   string `json:"weekday"`
	Timezone  string `json:"timezone"`
	UTCOffset string `json:"utc_offset"`
}

// GetCurrentTimeTool 返回获取当前时间工具的描述与参数定义。
func GetCurrentTimeTool() ai.Tool {
	return ai.Tool{
		Name:        ToolGetCurrentTime,
		Description: "获取服务端当前的日期、时间、星期和时区信息。当用户询问现在几点、今天几号、星期几、当前时间日期等问题时调用此工具",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// ExecuteGetCurrentTime 获取服务端系统当前日期、时间、星期、时区及 UTC 偏移，返回结构化对象。
func ExecuteGetCurrentTime() (*GetCurrentTimeOutput, error) {
	now := time.Now()
	zoneName, _ := now.Zone()
	return &GetCurrentTimeOutput{
		DateTime:  now.Format("2006-01-02 15:04:05"),
		Date:      now.Format("2006-01-02"),
		Time:      now.Format("15:04:05"),
		Weekday:   now.Weekday().String(),
		Timezone:  zoneName,
		UTCOffset: now.Format("-07:00"),
	}, nil
}
