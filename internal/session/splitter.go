package session

import (
	"strings"
	"unicode"
)

// minSentenceRunes 定义单句最小字符（rune）数量常量。
// 每次至少累积达到该字符数才允许按标点断句，减少高频细碎短句请求，除非流结束 Flush。
const minSentenceRunes = 20

// maxSentenceRunes 定义单句最大字符（rune）数量常量。
// 当文本流长时间未出现句末标点时，达到该长度即强制切分输出，防止音频延迟过高或单句过长。
const maxSentenceRunes = 60

// SentenceSplitter 实现轻量级增量文本分句器。
// 消费大语言模型输出的流式文本增量，按最小字符阈值、中英文标点与最大长度切分为完整句子，流结束时刷新残余文本。
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
// 若当前暂无就绪的完整句子，返回 nil。
func (s *SentenceSplitter) Feed(chunk string) []string {
	if chunk == "" {
		return nil
	}
	s.buffer = append(s.buffer, []rune(chunk)...)
	return s.split()
}

// Flush 在流结束时刷新并返回缓冲区中剩余的末尾残句，同时清空缓冲区。
// 若缓冲区为空或仅含纯空白字符，返回 nil。
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

// split 循环扫描缓冲区，根据单句最小字数、中英文标点和单句最大字符数提取所有已就绪的句子。
func (s *SentenceSplitter) split() []string {
	var sentences []string

	for len(s.buffer) > 0 {
		// 跳过缓冲区开头的纯空白字符
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

		// 若当前有效字符数未达到单句最小字数且未达最大强制切分限制，则等待更多输入
		if len(s.buffer) < minSentenceRunes {
			break
		}

		splitEnd := -1
		hasContent := false

		for i := 0; i < len(s.buffer); i++ {
			// 长时间无句末标点时，达到最大字符数强制切分
			if i >= maxSentenceRunes {
				splitEnd = maxSentenceRunes
				break
			}

			r := s.buffer[i]

			// 检查是否遇到实质内容字符（非标点且非空白字符）
			if !isSentencePunctuation(r) && !isTrailingPunctuation(r) {
				hasContent = true
			}

			// 必须累积至少达到 minSentenceRunes 个字符后，遇到的标点才作为断句点
			if (i+1) >= minSentenceRunes && hasContent && isSentencePunctuation(r) {
				// 排除形如 3.14 的数字中小数点
				if isPeriodDecimalPoint(s.buffer, i) {
					continue
				}

				// 贪婪吸收后续连续标点及尾随闭合引号/括号
				end := i + 1
				for end < len(s.buffer) && isTrailingPunctuation(s.buffer[end]) {
					end++
				}
				splitEnd = end
				break
			}
		}

		// 未达到切分条件（在 [minSentenceRunes, maxSentenceRunes) 之间无断句标点）
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

// isSentencePunctuation 判断字符是否为中英文断句标点。
func isSentencePunctuation(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '，', '、', '：',
		'!', '?', ';', ',', ':', '\n', '\r',
		'…', '—', '～', '~':
		return true
	case '.':
		return true
	default:
		return false
	}
}

// isTrailingPunctuation 判断字符是否为可跟随在句末标点后的连续标点或尾随闭合符号。
func isTrailingPunctuation(r rune) bool {
	if isSentencePunctuation(r) {
		return true
	}
	switch r {
	case '"', '\'', '”', '’', '）', ')', '」', '』', ']', '}', '>', '》':
		return true
	default:
		return false
	}
}

// isPeriodDecimalPoint 判断指定位置的半角句号是否为浮点数小数点（如 3.14）。
func isPeriodDecimalPoint(buffer []rune, idx int) bool {
	if buffer[idx] != '.' {
		return false
	}
	if idx == 0 || buffer[idx-1] < '0' || buffer[idx-1] > '9' {
		return false
	}
	if idx+1 < len(buffer) && buffer[idx+1] >= '0' && buffer[idx+1] <= '9' {
		return true
	}
	if idx+1 == len(buffer) && len(buffer) < maxSentenceRunes {
		return true
	}
	return false
}
