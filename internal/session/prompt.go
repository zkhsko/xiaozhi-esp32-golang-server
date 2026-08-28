package session

import (
	"os"
)

// RenderPrompt 使用标准库 os.Expand 替换系统提示词中的占位符。
func RenderPrompt(prompt string, values map[string]string) string {
	return os.Expand(prompt, func(key string) string {
		return values[key]
	})
}
