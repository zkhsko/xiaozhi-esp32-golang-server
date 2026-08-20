package session

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// stripWhitespace 移除字符串中的所有空白字符（空格、制表符、换行、回车等）。
func stripWhitespace(s string) string {
	var builder strings.Builder
	for _, r := range s {
		if !unicode.IsSpace(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// TestSentenceSplitter_PunctuationAcrossChunks 验证标点符号跨越多个增量 chunk 时的正确切分行为。
func TestSentenceSplitter_PunctuationAcrossChunks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chunks   []string
		expected []string
	}{
		{
			name: "text and punctuation in separate chunks",
			chunks: []string{
				"你好",
				"。",
			},
			expected: []string{"你好。"},
		},
		{
			name: "multiple sentences with scattered punctuation",
			chunks: []string{
				"今天天气",
				"真好，我们",
				"一起去",
				"公园吧！",
			},
			expected: []string{
				"今天天气真好，",
				"我们一起去公园吧！",
			},
		},
		{
			name: "english sentence split by words and comma period",
			chunks: []string{
				"Hello",
				", ",
				"world",
				"!",
				" How",
				" are",
				" you",
				"?",
			},
			expected: []string{
				"Hello,",
				"world!",
				"How are you?",
			},
		},
		{
			name: "continuous punctuation in separate chunk",
			chunks: []string{
				"真的吗？？",
				"太棒了！！",
			},
			expected: []string{
				"真的吗？？",
				"太棒了！！",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			splitter := NewSentenceSplitter()
			var actual []string

			for _, chunk := range tt.chunks {
				sentences := splitter.Feed(chunk)
				if len(sentences) > 0 {
					actual = append(actual, sentences...)
				}
			}

			final := splitter.Flush()
			if len(final) > 0 {
				actual = append(actual, final...)
			}

			if !reflect.DeepEqual(actual, tt.expected) {
				t.Fatalf("expected sentences %v, got %v", tt.expected, actual)
			}
		})
	}
}

// TestSentenceSplitter_MultipleSentencesInSingleChunk 验证单次 Feed 增量包含多个完整句子时的切分行为。
func TestSentenceSplitter_MultipleSentencesInSingleChunk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		chunk    string
		expected []string
	}{
		{
			name:  "multiple chinese punctuation sentences in single chunk",
			chunk: "你好。世界！今天天气好吗？很好；我们出发吧，走：看风景！",
			expected: []string{
				"你好。",
				"世界！",
				"今天天气好吗？",
				"很好；",
				"我们出发吧，",
				"走：",
				"看风景！",
			},
		},
		{
			name:  "mixed chinese and english multiple sentences",
			chunk: "Hello! 你好。How are you? 天气真不错；See you tomorrow.",
			expected: []string{
				"Hello!",
				"你好。",
				"How are you?",
				"天气真不错；",
				"See you tomorrow.",
			},
		},
		{
			name:  "newlines as sentence boundaries",
			chunk: "第一行内容\n第二行内容\r\n第三行内容\r第四行内容",
			expected: []string{
				"第一行内容",
				"第二行内容",
				"第三行内容",
				"第四行内容",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			splitter := NewSentenceSplitter()
			actual := splitter.Feed(tt.chunk)

			final := splitter.Flush()
			if len(final) > 0 {
				actual = append(actual, final...)
			}

			if !reflect.DeepEqual(actual, tt.expected) {
				t.Fatalf("expected sentences %v, got %v", tt.expected, actual)
			}
		})
	}
}

// TestSentenceSplitter_MaxLengthGuard 验证无句末标点时按照最大字符数常量（30 字符）强制切分。
func TestSentenceSplitter_MaxLengthGuard(t *testing.T) {
	t.Parallel()

	t.Run("chinese text without punctuation exactly 75 runes", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()

		// 75 个中文字符
		text := "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五"
		runes := []rune(text)
		if len(runes) != 75 {
			t.Fatalf("test data rune count must be 75, got %d", len(runes))
		}

		s1 := splitter.Feed(text)
		// 应该在前 60 字符产生 2 句（每句 30 字符），剩余 15 字符留在缓冲区
		if len(s1) != 2 {
			t.Fatalf("expected 2 sentences from Feed, got %d: %v", len(s1), s1)
		}
		if []rune(s1[0]) == nil || len([]rune(s1[0])) != 30 {
			t.Fatalf("first sentence length expected 30 runes, got %d (%s)", len([]rune(s1[0])), s1[0])
		}
		if len([]rune(s1[1])) != 30 {
			t.Fatalf("second sentence length expected 30 runes, got %d (%s)", len([]rune(s1[1])), s1[1])
		}

		s2 := splitter.Flush()
		if len(s2) != 1 {
			t.Fatalf("expected 1 sentence from Flush, got %d: %v", len(s2), s2)
		}
		if len([]rune(s2[0])) != 15 {
			t.Fatalf("flush sentence length expected 15 runes, got %d (%s)", len([]rune(s2[0])), s2[0])
		}

		// 验证字符总数和拼接内容完全一致
		all := append(s1, s2...)
		joined := strings.Join(all, "")
		if joined != text {
			t.Fatalf("reconstructed text does not match original: got %q, want %q", joined, text)
		}
	})

	t.Run("ascii text without punctuation", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()

		// 65 个英文字符
		text := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789012"
		runes := []rune(text)
		if len(runes) != 65 {
			t.Fatalf("test data rune count must be 65, got %d", len(runes))
		}

		s1 := splitter.Feed(text)
		if len(s1) != 2 {
			t.Fatalf("expected 2 sentences, got %d: %v", len(s1), s1)
		}
		if len(s1[0]) != 30 || len(s1[1]) != 30 {
			t.Fatalf("sentence lengths should be 30, 30, got %d, %d", len(s1[0]), len(s1[1]))
		}

		s2 := splitter.Flush()
		if len(s2) != 1 || len(s2[0]) != 5 {
			t.Fatalf("flush sentence length should be 5, got %v", s2)
		}
	})

	t.Run("exact boundary checks at 29, 30, 31 runes", func(t *testing.T) {
		t.Parallel()

		// 29 runes
		sp29 := NewSentenceSplitter()
		r29 := sp29.Feed(strings.Repeat("a", 29))
		if len(r29) != 0 {
			t.Fatalf("expected 0 sentences for 29 runes, got %d", len(r29))
		}
		f29 := sp29.Flush()
		if len(f29) != 1 || len(f29[0]) != 29 {
			t.Fatalf("expected 1 sentence of 29 runes from flush, got %v", f29)
		}

		// 30 runes
		sp30 := NewSentenceSplitter()
		r30 := sp30.Feed(strings.Repeat("b", 30))
		if len(r30) != 1 || len(r30[0]) != 30 {
			t.Fatalf("expected 1 sentence of 30 runes from feed, got %v", r30)
		}
		f30 := sp30.Flush()
		if len(f30) != 0 {
			t.Fatalf("expected 0 sentences from flush for exact 30 runes, got %v", f30)
		}

		// 31 runes
		sp31 := NewSentenceSplitter()
		r31 := sp31.Feed(strings.Repeat("c", 31))
		if len(r31) != 1 || len(r31[0]) != 30 {
			t.Fatalf("expected 1 sentence of 30 runes from feed, got %v", r31)
		}
		f31 := sp31.Flush()
		if len(f31) != 1 || len(f31[0]) != 1 {
			t.Fatalf("expected 1 sentence of 1 rune from flush, got %v", f31)
		}
	})
}

// TestSentenceSplitter_WhitespaceHandling 验证纯空白增量跳过、纯空白残句不输出以及空白修剪。
func TestSentenceSplitter_WhitespaceHandling(t *testing.T) {
	t.Parallel()

	t.Run("empty string chunks", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()
		res := splitter.Feed("")
		if len(res) != 0 {
			t.Fatalf("expected nil or empty for empty string, got %v", res)
		}
		fl := splitter.Flush()
		if len(fl) != 0 {
			t.Fatalf("expected nil or empty flush for empty splitter, got %v", fl)
		}
	})

	t.Run("pure spaces and newlines chunks", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()
		res1 := splitter.Feed("   ")
		res2 := splitter.Feed("\t\t\n\r\n   ")
		if len(res1) != 0 || len(res2) != 0 {
			t.Fatalf("expected 0 sentences for pure whitespace, got %v, %v", res1, res2)
		}
		fl := splitter.Flush()
		if len(fl) != 0 {
			t.Fatalf("expected 0 sentences on flush for pure whitespace, got %v", fl)
		}
	})

	t.Run("text surrounded by whitespace and newlines", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()
		res := splitter.Feed("  \n\t 你好！ \n  世界。  \n\t")
		expected := []string{"你好！", "世界。"}
		if !reflect.DeepEqual(res, expected) {
			t.Fatalf("expected %v, got %v", expected, res)
		}
		fl := splitter.Flush()
		if len(fl) != 0 {
			t.Fatalf("expected empty flush, got %v", fl)
		}
	})
}

// TestSentenceSplitter_Flush 验证流结束时 Flush 刷新末尾残句及幂等性。
func TestSentenceSplitter_Flush(t *testing.T) {
	t.Parallel()

	t.Run("sentence ending without punctuation", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()
		res := splitter.Feed("这是最后一句没有标点的话")
		if len(res) != 0 {
			t.Fatalf("expected 0 sentences from Feed, got %v", res)
		}

		fl1 := splitter.Flush()
		expected := []string{"这是最后一句没有标点的话"}
		if !reflect.DeepEqual(fl1, expected) {
			t.Fatalf("expected %v from first flush, got %v", expected, fl1)
		}

		// 第二次 Flush 应该返回 nil（已清空）
		fl2 := splitter.Flush()
		if len(fl2) != 0 {
			t.Fatalf("expected nil from second flush, got %v", fl2)
		}
	})

	t.Run("sentence with punctuation flushed immediately in feed", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()
		res := splitter.Feed("这是完整的一句话。")
		expected := []string{"这是完整的一句话。"}
		if !reflect.DeepEqual(res, expected) {
			t.Fatalf("expected %v from feed, got %v", expected, res)
		}

		fl := splitter.Flush()
		if len(fl) != 0 {
			t.Fatalf("expected nil from flush after full sentence, got %v", fl)
		}
	})

	t.Run("reuse after flush", func(t *testing.T) {
		t.Parallel()
		splitter := NewSentenceSplitter()

		splitter.Feed("第一轮对话残句")
		fl1 := splitter.Flush()
		if !reflect.DeepEqual(fl1, []string{"第一轮对话残句"}) {
			t.Fatalf("unexpected fl1: %v", fl1)
		}

		r2 := splitter.Feed("第二轮新句子。")
		if !reflect.DeepEqual(r2, []string{"第二轮新句子。"}) {
			t.Fatalf("unexpected r2: %v", r2)
		}
		fl2 := splitter.Flush()
		if len(fl2) != 0 {
			t.Fatalf("unexpected fl2: %v", fl2)
		}
	})
}

// TestSentenceSplitter_TextReconstructionConsistency 综合复杂文本拼接还原一致性验证（所有产出句子去除空白后拼接与原文去除空白严格一致）。
func TestSentenceSplitter_TextReconstructionConsistency(t *testing.T) {
	t.Parallel()

	complexTexts := []string{
		"你好，我是小智语音助手！请问有什么可以帮助您的？今天的天气非常晴朗，气温大概是25.5度。建议您多出去走走，放松心情。",
		"人工智能（Artificial Intelligence）正在深刻改变世界。例如：大语言模型（LLM）、语音合成（TTS）以及自动语音识别（ASR）等技术！您觉得呢？是不是很酷？",
		"长文本测试：这是一段非常非常长的文本没有太多的标点符号但是中间偶尔会有一些逗号比如这里，然后又是长篇大论继续往下说直到最后才给出一个句号。",
		"代码与特殊符号测试：`fmt.Println(\"Hello, World!\")` —— 这是 Go 语言经典的入门程序；不仅简单，而且执行效率极高！",
		"多重连续标点测试：“真的吗？？？！！！”他惊讶地问道：“这怎么可能？！？！”大家面面相觑……",
		"浮点数与版本号测试：当前系统版本是 v2.0.1，圆周率约等于 3.1415926，自然对数底 e 约为 2.71828。各项指标正常！",
	}

	chunkSizes := []int{1, 2, 3, 5, 10, 20, 50, 100}

	for textIdx, text := range complexTexts {
		for _, chunkSize := range chunkSizes {
			splitter := NewSentenceSplitter()
			var collected []string

			// 按固定 chunk 大小分割输入
			runes := []rune(text)
			for i := 0; i < len(runes); i += chunkSize {
				end := i + chunkSize
				if end > len(runes) {
					end = len(runes)
				}
				chunk := string(runes[i:end])
				sentences := splitter.Feed(chunk)
				if len(sentences) > 0 {
					collected = append(collected, sentences...)
				}
			}

			final := splitter.Flush()
			if len(final) > 0 {
				collected = append(collected, final...)
			}

			// 验证拼接一致性：所有产出句子去除空白后拼接，必须与原文去除空白后完全一致
			originalClean := stripWhitespace(text)
			var joinedBuilder strings.Builder
			for _, s := range collected {
				joinedBuilder.WriteString(s)
			}
			reconstructedClean := stripWhitespace(joinedBuilder.String())

			if originalClean != reconstructedClean {
				t.Fatalf("text[%d] chunkSize=%d reconstruction mismatch:\nwant: %s\ngot:  %s\nsentences: %v",
					textIdx, chunkSize, originalClean, reconstructedClean, collected)
			}
		}
	}
}

// TestSentenceSplitter_DecimalNumbers 验证数字中的小数点不会被误切分为英文句号。
func TestSentenceSplitter_DecimalNumbers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:  "decimal number inside sentence",
			input: "圆周率大约是3.14159，自然常数是2.718。",
			expected: []string{
				"圆周率大约是3.14159，",
				"自然常数是2.718。",
			},
		},
		{
			name:  "decimal number across chunks",
			input: "商品价格是99.9元。",
			expected: []string{
				"商品价格是99.9元。",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			splitter := NewSentenceSplitter()
			actual := splitter.Feed(tt.input)
			final := splitter.Flush()
			if len(final) > 0 {
				actual = append(actual, final...)
			}
			if !reflect.DeepEqual(actual, tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, actual)
			}
		})
	}
}

// TestSentenceSplitter_ConcurrentInstances 验证多协程独立使用各自分句器实例时的并发安全与隔离性。
func TestSentenceSplitter_ConcurrentInstances(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			splitter := NewSentenceSplitter()

			s1 := splitter.Feed("你好，")
			s2 := splitter.Feed("这是并发测试！")
			fl := splitter.Flush()

			var all []string
			all = append(all, s1...)
			all = append(all, s2...)
			all = append(all, fl...)

			expected := []string{"你好，", "这是并发测试！"}
			if !reflect.DeepEqual(all, expected) {
				t.Errorf("worker %d expected %v, got %v", workerID, expected, all)
			}
		}(i)
	}
	wg.Wait()
}
