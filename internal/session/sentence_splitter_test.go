package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestSentenceSplitter_MinThreshold_NoCutBeforeFiveChars(t *testing.T) {
	s := NewSentenceSplitter()

	// 3 个非空白 rune（'你', '好', '。'）未达到 5 字阈值，不切句
	got := s.Feed("你好。")
	if len(got) != 0 {
		t.Fatalf("expected 0 sentences, got %v", got)
	}

	// Flush 输出最终短残余
	flushed := s.Flush()
	expected := []string{"你好。"}
	if !reflect.DeepEqual(flushed, expected) {
		t.Fatalf("expected %v, got %v", expected, flushed)
	}
}

func TestSentenceSplitter_MinThreshold_StrongPunctuationBeforeFiveCharsIgnored(t *testing.T) {
	s := NewSentenceSplitter()

	// "好。" 仅 2 个非空白 rune，'。' 出现在 5 字前被忽略；
	// 后续到达 "你好世界！"，'！' 处非空白 rune 计数为 7（>= 5），在此处切句
	got := s.Feed("好。你好世界！")
	expected := []string{"好。你好世界！"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}

	// 缓冲区此时应为空
	flushed := s.Flush()
	if len(flushed) != 0 {
		t.Fatalf("expected empty flush, got %v", flushed)
	}
}

func TestSentenceSplitter_MinThreshold_ExactFiveChars(t *testing.T) {
	s := NewSentenceSplitter()

	// '一'(1), '二'(2), '三'(3), '四'(4), '。'(5) 恰好 5 个非空白 rune
	got := s.Feed("一二三四。")
	expected := []string{"一二三四。"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestSentenceSplitter_ChineseStrongPunctuation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "句号切句",
			input:    "今天天气真好。明天天气也不错。",
			expected: []string{"今天天气真好。", "明天天气也不错。"},
		},
		{
			name:     "感叹号切句",
			input:    "太棒了大家！大家加油干！",
			expected: []string{"太棒了大家！", "大家加油干！"},
		},
		{
			name:     "问号切句",
			input:    "今天星期几呢？现在几点了？",
			expected: []string{"今天星期几呢？", "现在几点了？"},
		},
		{
			name:     "分号切句",
			input:    "第一步是准备；第二步是执行。",
			expected: []string{"第一步是准备；", "第二步是执行。"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSentenceSplitter()
			got := s.Feed(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestSentenceSplitter_EnglishStrongPunctuation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "英文句号",
			input:    "Hello world. How are you today.",
			expected: []string{"Hello world.", "How are you today."},
		},
		{
			name:     "英文感叹号",
			input:    "Great job everyone! Keep it up!",
			expected: []string{"Great job everyone!", "Keep it up!"},
		},
		{
			name:     "英文问号",
			input:    "What time is it? Are you ready?",
			expected: []string{"What time is it?", "Are you ready?"},
		},
		{
			name:     "英文分号",
			input:    "First step is ready; second step follows.",
			expected: []string{"First step is ready;", "second step follows."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSentenceSplitter()
			got := s.Feed(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestSentenceSplitter_NewlineDelimiter(t *testing.T) {
	s := NewSentenceSplitter()

	// 换行作为强分隔符，输出时执行 TrimSpace 去除换行符
	got := s.Feed("第一行内容啊\n第二行内容啊\n")
	expected := []string{"第一行内容啊", "第二行内容啊"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}

	// 短文本加换行：未满 5 字时不切分
	s.Reset()
	got = s.Feed("你好\n")
	if len(got) != 0 {
		t.Fatalf("expected 0 sentences for short newline, got %v", got)
	}

	// 补足文本后按下一换行切出完整句子
	got = s.Feed("世界真美好\n")
	expected = []string{"你好\n世界真美好"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestSentenceSplitter_WeakPunctuation_UnderEightyCharsNotCut(t *testing.T) {
	s := NewSentenceSplitter()

	// 包含弱分隔符（逗号、顿号、冒号），但未达到 80 字且无强分隔符，不应切句
	input := "你好，亲爱的朋友、各位伙伴：今天我们要讨论一个重要议题"
	got := s.Feed(input)
	if len(got) != 0 {
		t.Fatalf("expected 0 sentences for weak delimiters under 80 chars, got %v", got)
	}

	// Flush 输出完整未切分句子
	flushed := s.Flush()
	expected := []string{input}
	if !reflect.DeepEqual(flushed, expected) {
		t.Fatalf("expected %v, got %v", expected, flushed)
	}
}

func TestSentenceSplitter_EightyChars_CutAtLastWeakDelimiter(t *testing.T) {
	s := NewSentenceSplitter()

	// 构造 85 个非空白字符，其中在第 30 字和第 60 字处有弱标点
	part1 := strings.Repeat("甲", 29) + "，" // 30 字，第 30 字为弱分隔符
	part2 := strings.Repeat("乙", 29) + "：" // 30 字，第 60 字为弱分隔符
	part3 := strings.Repeat("丙", 25)        // 25 字，总共 85 字

	got := s.Feed(part1 + part2 + part3)
	// 达到 80 字时，应在不超过 80 字的最后一个弱分隔符（第 60 字 '：'）切分
	expected := []string{part1 + part2}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}

	// 剩余的 part3（25 字）在 Flush 时输出
	flushed := s.Flush()
	expectedFlushed := []string{part3}
	if !reflect.DeepEqual(flushed, expectedFlushed) {
		t.Fatalf("expected %v, got %v", expectedFlushed, flushed)
	}
}

func TestSentenceSplitter_EightyChars_AllWeakDelimitersSupported(t *testing.T) {
	weakDelims := []rune{'，', '、', '：', ',', ':'}

	for _, delim := range weakDelims {
		t.Run(string(delim), func(t *testing.T) {
			s := NewSentenceSplitter()
			prefix := strings.Repeat("字", 49) + string(delim) // 50 字
			suffix := strings.Repeat("符", 35)                // 35 字，总共 85 字

			got := s.Feed(prefix + suffix)
			expected := []string{prefix}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("delim %c: expected %v, got %v", delim, expected, got)
			}

			flushed := s.Flush()
			expectedFlushed := []string{suffix}
			if !reflect.DeepEqual(flushed, expectedFlushed) {
				t.Fatalf("delim %c: expected flushed %v, got %v", delim, expectedFlushed, flushed)
			}
		})
	}
}

func TestSentenceSplitter_EightyChars_HardCutWhenNoWeakDelimiter(t *testing.T) {
	s := NewSentenceSplitter()

	// 构造 85 个连续字符，没有任何标点
	input := strings.Repeat("哈", 85)
	got := s.Feed(input)

	// 应在第 80 个非空白 rune 硬切
	expected := []string{strings.Repeat("哈", 80)}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}

	// 剩余 5 个字在 Flush 时输出
	flushed := s.Flush()
	expectedFlushed := []string{strings.Repeat("哈", 5)}
	if !reflect.DeepEqual(flushed, expectedFlushed) {
		t.Fatalf("expected %v, got %v", expectedFlushed, flushed)
	}
}

func TestSentenceSplitter_RuneCountingNotBytes(t *testing.T) {
	s := NewSentenceSplitter()

	// 中文字符每个 3 字节，"你好" 仅 2 个 rune（6 字节）
	// 按非空白 rune 计数，2 < 5 不应因标点切句
	got := s.Feed("你好！")
	if len(got) != 0 {
		t.Fatalf("expected 0 sentences for 2 runes (6 bytes), got %v", got)
	}

	// 包含 Emoji（👋 4 字节 1 rune）和全角空格（\u3000）
	// '你'(1), '好'(2), '👋'(3), '\u3000'(全角空白不计), '世'(4), '界'(5), '！'(6)
	s.Reset()
	got = s.Feed("你好👋\u3000世界！")
	expected := []string{"你好👋\u3000世界！"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestSentenceSplitter_IncrementalStreamingChunks(t *testing.T) {
	s := NewSentenceSplitter()

	var allSentences []string

	chunks := []string{
		"今", "天", "天", "气", "真", "好", "，",
		"我", "们", "一", "起", "去", "公", "园", "散", "步", "吧", "！",
		"好", "的", "，", "没", "问", "题", "。",
		"现", "在", "就", "出", "发",
	}

	for _, chunk := range chunks {
		sentences := s.Feed(chunk)
		allSentences = append(allSentences, sentences...)
	}

	flushed := s.Flush()
	allSentences = append(allSentences, flushed...)

	expected := []string{
		"今天天气真好，我们一起去公园散步吧！",
		"好的，没问题。",
		"现在就出发",
	}

	if !reflect.DeepEqual(allSentences, expected) {
		t.Fatalf("expected %v, got %v", expected, allSentences)
	}
}

func TestSentenceSplitter_MultipleSentencesInSingleChunk(t *testing.T) {
	s := NewSentenceSplitter()

	input := "第一句话完成了。第二句话也结束了！第三句话问候你？第四句话说明白；最后一句完成。"
	got := s.Feed(input)
	expected := []string{
		"第一句话完成了。",
		"第二句话也结束了！",
		"第三句话问候你？",
		"第四句话说明白；",
		"最后一句完成。",
	}

	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestSentenceSplitter_WhitespaceAndEmptyFiltering(t *testing.T) {
	s := NewSentenceSplitter()

	// 纯空白输入
	got := s.Feed("   \n\t  \u3000  ")
	if len(got) != 0 {
		t.Fatalf("expected 0 sentences for whitespace, got %v", got)
	}

	// 纯空白 Flush 不产生空句子
	flushed := s.Flush()
	if len(flushed) != 0 {
		t.Fatalf("expected 0 sentences for whitespace flush, got %v", flushed)
	}

	// 首尾有大量空白和换行，应 TrimSpace 输出
	got = s.Feed("   \n\n  你好世界啊！  \t\n  ")
	expected := []string{"你好世界啊！"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestSentenceSplitter_Reset(t *testing.T) {
	s := NewSentenceSplitter()

	s.Feed("正在输入但未完成的一段很长的话语")
	s.Reset()

	flushed := s.Flush()
	if len(flushed) != 0 {
		t.Fatalf("expected 0 sentences after reset, got %v", flushed)
	}
}

func TestSentenceSplitter_LongTextMultiCutLoop(t *testing.T) {
	s := NewSentenceSplitter()

	// 一次性输入包含超过 160 字的文本，测试循环多次切分
	// 句 1: 50 字 + 弱标点 '，' + 40 字 -> 达到 80 字时在 '，' (50 字) 切分
	// 句 2: 剩余 40 字 + 30 字 + 弱标点 '，' + 20 字 -> 达到 80 字时在 '，' (70 字) 切分
	// 句 3: 剩余 20 字 -> Flush 输出
	chunk1 := strings.Repeat("壹", 49) + "，" // 50 字
	chunk2 := strings.Repeat("贰", 40)        // 40 字 (当前 90 字)
	chunk3 := strings.Repeat("叁", 29) + "，" // 30 字
	chunk4 := strings.Repeat("肆", 20)        // 20 字

	got := s.Feed(chunk1 + chunk2 + chunk3 + chunk4)
	expectedFeed := []string{
		chunk1,
		chunk2 + chunk3,
	}
	if !reflect.DeepEqual(got, expectedFeed) {
		t.Fatalf("expected %v, got %v", expectedFeed, got)
	}

	flushed := s.Flush()
	expectedFlushed := []string{chunk4}
	if !reflect.DeepEqual(flushed, expectedFlushed) {
		t.Fatalf("expected %v, got %v", expectedFlushed, flushed)
	}
}

func TestSentenceSplitter_ConsecutiveStrongPunctuation(t *testing.T) {
	s := NewSentenceSplitter()

	// 连续强标点："！？"
	// 第一个强标点 '！' 在 6 字处满足条件并切分；
	// 剩余缓冲区首字符为 '？'，随后紧跟文本，在其后强标点切句
	got := s.Feed("太棒了大家！？这是真的吗？")
	expected := []string{
		"太棒了大家！",
		"？这是真的吗？",
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestSentenceSplitter_Boundary79And80Chars(t *testing.T) {
	s := NewSentenceSplitter()

	// 79 个字（包含第 40 字弱标点）：未达到 80 字，不因弱标点切分
	prefix := strings.Repeat("甲", 39) + "，" // 40 字
	suffix79 := strings.Repeat("乙", 39)       // 39 字，总计 79 字
	got := s.Feed(prefix + suffix79)
	if len(got) != 0 {
		t.Fatalf("expected 0 sentences at 79 chars, got %v", got)
	}

	// 注入第 80 个字，触发 80 字边界弱分隔符切分
	got = s.Feed("丙")
	expected := []string{prefix}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}

	// 剩余为 39 个 "乙" + 1 个 "丙" = 40 字
	flushed := s.Flush()
	expectedFlushed := []string{suffix79 + "丙"}
	if !reflect.DeepEqual(flushed, expectedFlushed) {
		t.Fatalf("expected %v, got %v", expectedFlushed, flushed)
	}
}

func TestSentenceSplitter_IterationFlush_Separation(t *testing.T) {
	s := NewSentenceSplitter()

	// 模拟 Iteration 1：LLM 输出短文本未满 5 字，Iteration 切换时执行 Flush
	got1 := s.Feed("好的")
	if len(got1) != 0 {
		t.Fatalf("expected 0 sentences in feed, got %v", got1)
	}
	flushed1 := s.Flush()
	expected1 := []string{"好的"}
	if !reflect.DeepEqual(flushed1, expected1) {
		t.Fatalf("expected %v, got %v", expected1, flushed1)
	}

	// 模拟 Iteration 2：新 Iteration 从干净缓冲开始
	got2 := s.Feed("我们重新开始一次查询。")
	expected2 := []string{"我们重新开始一次查询。"}
	if !reflect.DeepEqual(got2, expected2) {
		t.Fatalf("expected %v, got %v", expected2, got2)
	}
	flushed2 := s.Flush()
	if len(flushed2) != 0 {
		t.Fatalf("expected empty flush in iteration 2, got %v", flushed2)
	}
}

func TestSentenceSplitter_EmptyFeedAndMultipleFlushIdempotent(t *testing.T) {
	s := NewSentenceSplitter()

	// 空字符串 Feed
	if got := s.Feed(""); len(got) != 0 {
		t.Fatalf("expected 0 sentences for empty feed, got %v", got)
	}

	// 初始空 Flush
	if flushed := s.Flush(); len(flushed) != 0 {
		t.Fatalf("expected 0 sentences for empty flush, got %v", flushed)
	}

	// Feed 少量内容后连续 Flush 两次，第二次必须为空
	s.Feed("残余短句")
	flushedFirst := s.Flush()
	if !reflect.DeepEqual(flushedFirst, []string{"残余短句"}) {
		t.Fatalf("expected [残余短句], got %v", flushedFirst)
	}

	flushedSecond := s.Flush()
	if len(flushedSecond) != 0 {
		t.Fatalf("expected second flush to be empty, got %v", flushedSecond)
	}
}

