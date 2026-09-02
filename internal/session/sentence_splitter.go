package session

import (
	"strings"
	"unicode"
)

const (
	// MinSentenceNonSpaceRunes 最小切句非空白 Unicode rune 阈值。
	// 缓冲字符数未满 5 个字符时不因任何标点切句。
	MinSentenceNonSpaceRunes = 5

	// MaxSentenceNonSpaceRunes 单句最大非空白 Unicode rune 上限。
	// 达到 80 字时优先在最后一个弱分隔符处切句，无弱分隔符则在第 80 个非空白 rune 硬切。
	MaxSentenceNonSpaceRunes = 80
)

// SentenceSplitter 实现 Unicode 增量句子切分器。
//
// 切句规则优先级（参考 voice-stream-implementation-plan.md 第 6 节）：
//  1. 缓冲字符数（非空白 rune 计数）小于 5 时，不因任何标点切句。
//  2. 达到 5 字后，优先选择最早可用的强分隔符进行切分（截取到该强分隔符并归入当前句）。
//  3. 若缓冲达到 80 字（非空白 rune 计数达到 80）：优先选择不超过上限的最后一个弱分隔符切分（截取到该弱分隔符并归入当前句）；
//     若没有弱分隔符，则在第 80 个非空白 rune 硬切。
//  4. 循环检查缓冲是否仍满足切句条件（例如一次送入长文本可能切出多句）。
//  5. Flush 用于 LLM 整体结束或 Iteration 切换：输出任意长度残余。
//  6. 所有切出的句子在输出前执行 strings.TrimSpace；TrimSpace 后为空的不产生句子。
//  7. Reset 可清空当前缓冲。
//
// 组件为无外部依赖、无全局状态、高内聚的纯内存切分器。
type SentenceSplitter struct {
	buffer []rune
}

// NewSentenceSplitter 创建新的增量句子切分器实例。
func NewSentenceSplitter() *SentenceSplitter {
	return &SentenceSplitter{
		buffer: make([]rune, 0, 128),
	}
}

// Feed 向切分器缓冲追加增量文本，并尝试循环切分出已满足条件的完整句子。
// 所有切出的句子在返回前均已执行 strings.TrimSpace，若 TrimSpace 后为空则过滤。
func (s *SentenceSplitter) Feed(text string) []string {
	if text == "" {
		return nil
	}

	s.buffer = append(s.buffer, []rune(text)...)
	return s.drainSentences()
}

// Flush 在 LLM 整体回答结束或 Iteration 切换时调用。
// 先循环切出所有满足条件的完整句子，若缓冲区仍有残余文本，则将其整体作为尾句输出。
// 输出前同样执行 strings.TrimSpace 并过滤空句，调用后缓冲区彻底清空。
func (s *SentenceSplitter) Flush() []string {
	sentences := s.drainSentences()

	if len(s.buffer) > 0 {
		residue := strings.TrimSpace(string(s.buffer))
		s.buffer = s.buffer[:0]
		if residue != "" {
			sentences = append(sentences, residue)
		}
	}

	return sentences
}

// Reset 清空切分器内部当前缓冲。
func (s *SentenceSplitter) Reset() {
	s.buffer = s.buffer[:0]
}

// drainSentences 循环从缓冲区中切出所有满足条件的句子。
func (s *SentenceSplitter) drainSentences() []string {
	var sentences []string
	for {
		sentence := s.cutOne()
		if sentence == "" {
			break
		}
		trimmed := strings.TrimSpace(sentence)
		if trimmed != "" {
			sentences = append(sentences, trimmed)
		}
	}
	return sentences
}

// cutOne 尝试从当前缓冲区头部切分出单条满足条件的句子。若当前不满足切分条件则返回空字符串。
func (s *SentenceSplitter) cutOne() string {
	totalNonSpace := 0
	for _, r := range s.buffer {
		if !unicode.IsSpace(r) {
			totalNonSpace++
		}
	}

	// 规则 1：缓冲字符数（非空白 rune 计数）小于 5 时，不因任何标点切句。
	if totalNonSpace < MinSentenceNonSpaceRunes {
		return ""
	}

	currentNonSpace := 0
	lastWeakDelimIndex := -1

	for i, r := range s.buffer {
		if !unicode.IsSpace(r) {
			currentNonSpace++
		}

		// 规则 2：达到 5 字后，优先选择最早可用的强分隔符进行切分（5 <= currentNonSpace <= 80）。
		if isStrongDelimiter(r) && currentNonSpace >= MinSentenceNonSpaceRunes && currentNonSpace <= MaxSentenceNonSpaceRunes {
			return s.extractSentence(i)
		}

		// 记录不超过上限 80 字范围内的弱分隔符位置。
		if isWeakDelimiter(r) && currentNonSpace <= MaxSentenceNonSpaceRunes {
			lastWeakDelimIndex = i
		}

		// 规则 3：若缓冲达到 80 字（非空白 rune 计数达到 80）：
		// 优先选择不超过上限的最后一个弱分隔符切分；若没有弱分隔符，则在第 80 个非空白 rune 硬切。
		if currentNonSpace == MaxSentenceNonSpaceRunes {
			if lastWeakDelimIndex != -1 {
				return s.extractSentence(lastWeakDelimIndex)
			}
			return s.extractSentence(i)
		}
	}

	// 达到 5 字但尚未达到 80 字且无强分隔符，继续等待后续增量输入。
	return ""
}

// extractSentence 截取 0..cutIndex（含）的文本作为切出句子，并将剩余 rune 移动到缓冲区头部。
func (s *SentenceSplitter) extractSentence(cutIndex int) string {
	sentence := string(s.buffer[:cutIndex+1])
	n := copy(s.buffer, s.buffer[cutIndex+1:])
	s.buffer = s.buffer[:n]
	return sentence
}

// isStrongDelimiter 判断当前字符是否为强分隔符（。！？；.!?;\n）。
func isStrongDelimiter(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?', ';', '\n':
		return true
	default:
		return false
	}
}

// isWeakDelimiter 判断当前字符是否为弱分隔符（，、：,、:）。
func isWeakDelimiter(r rune) bool {
	switch r {
	case '，', '、', '：', ',', ':':
		return true
	default:
		return false
	}
}
