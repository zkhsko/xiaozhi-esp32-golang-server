package voice

import (
	"strings"
	"unicode"
)

// minSentenceRunes 定义单句最小字符数量。
const minSentenceRunes = 5

// maxSentenceRunes 定义单句最大字符数量。
const maxSentenceRunes = 80

// SentenceSplitter 实现轻量级增量文本分句器。
type SentenceSplitter struct {
	buffer []rune
}

// NewSentenceSplitter 创建并初始化增量文本分句器。
func NewSentenceSplitter() *SentenceSplitter {
	return &SentenceSplitter{
		buffer: make([]rune, 0, maxSentenceRunes*2),
	}
}

// Feed 接收文本增量片段，按中英文标点与最大长度切分并返回当前已就绪的完整句子列表。
func (s *SentenceSplitter) Feed(chunk string) []string {
	if chunk == "" {
		return nil
	}
	s.buffer = append(s.buffer, []rune(chunk)...)
	return s.split()
}

// Flush 在流结束或迭代切换时刷新并返回缓冲区中剩余的末尾残句，同时清空缓冲区。
func (s *SentenceSplitter) Flush() []string {
	if len(s.buffer) == 0 {
		return nil
	}
	rem := string(s.buffer)
	s.buffer = s.buffer[:0]

	trimmed := strings.TrimSpace(rem)
	if trimmed == "" {
		return nil
	}
	return []string{trimmed}
}

// split 循环扫描缓冲区提取所有已就绪的句子。
func (s *SentenceSplitter) split() []string {
	var sentences []string

	for len(s.buffer) > 0 {
		start := 0
		for start < len(s.buffer) && unicode.IsSpace(s.buffer[start]) {
			start++
		}
		if start > 0 {
			s.buffer = s.buffer[start:]
			if len(s.buffer) == 0 {
				break
			}
		}

		if len(s.buffer) < minSentenceRunes {
			break
		}

		splitEnd := -1
		hasContent := false

		for i := 0; i < len(s.buffer); i++ {
			if i >= maxSentenceRunes {
				splitEnd = maxSentenceRunes
				break
			}

			r := s.buffer[i]

			if !isSentencePunctuation(r) && !isTrailingPunctuation(r) {
				hasContent = true
			}

			if (i+1) >= minSentenceRunes && hasContent && isSentencePunctuation(r) {
				if isPeriodDecimalPoint(s.buffer, i) {
					continue
				}

				end := i + 1
				for end < len(s.buffer) && isTrailingPunctuation(s.buffer[end]) {
					end++
				}
				splitEnd = end
				break
			}
		}

		if splitEnd == -1 {
			break
		}

		part := string(s.buffer[:splitEnd])
		s.buffer = s.buffer[splitEnd:]

		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			sentences = append(sentences, trimmed)
		}
	}

	return sentences
}

func isSentencePunctuation(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '…', '.', '!', '?', ';':
		return true
	default:
		return false
	}
}

func isTrailingPunctuation(r rune) bool {
	switch r {
	case '”', '’', '」', '』', '"', '\'', ')', '）', ']', '】', '}', '》', '>', '。', '！', '？', '；', '…', '.', '!', '?', ';':
		return true
	default:
		return false
	}
}

func isPeriodDecimalPoint(runes []rune, index int) bool {
	if runes[index] != '.' {
		return false
	}
	if index > 0 && index+1 < len(runes) {
		prev := runes[index-1]
		next := runes[index+1]
		if unicode.IsDigit(prev) && unicode.IsDigit(next) {
			return true
		}
	}
	return false
}
