package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestSentenceSplitter_MinLengthRequirement(t *testing.T) {
	splitter := NewSentenceSplitter()

	// 1. 少于 5 字遇到标点：不应该切分（例如 3 个字）
	res := splitter.Feed("你好，")
	if len(res) != 0 {
		t.Fatalf("expected 0 sentences for short chunk with punctuation, got: %v", res)
	}

	// 2. 累积达到 5 字并在 5 字及之后遇到标点：应该切分出第一句（"你好，我是小智。" 共 8 字）
	res = splitter.Feed("我是小智。")
	if len(res) != 1 {
		t.Fatalf("expected 1 sentence when reaching >= 5 runes with punctuation, got: %v", res)
	}
	if res[0] != "你好，我是小智。" {
		t.Fatalf("expected '你好，我是小智。', got: %q", res[0])
	}

	// 验证切出的第一句字数 >= 5
	firstSentence := res[0]
	runes := []rune(firstSentence)
	if len(runes) < minSentenceRunes {
		t.Fatalf("expected sentence runes >= %d, got %d (%q)", minSentenceRunes, len(runes), firstSentence)
	}

	// 3. Flush 刷新末尾不足 5 字的残余文本
	_ = splitter.Feed("好的")
	flushed := splitter.Flush()
	if len(flushed) != 1 || flushed[0] != "好的" {
		t.Fatalf("expected flushed ['好的'], got: %v", flushed)
	}
}

func TestSentenceSplitter_FlushUnderMinRunes(t *testing.T) {
	splitter := NewSentenceSplitter()

	// 只有 3 个字
	res := splitter.Feed("好的。")
	if len(res) != 0 {
		t.Fatalf("expected no split during feed for < 5 runes, got: %v", res)
	}

	// 结束时 Flush 必须刷出
	flushed := splitter.Flush()
	expected := []string{"好的。"}
	if !reflect.DeepEqual(flushed, expected) {
		t.Fatalf("expected %v, got %v", expected, flushed)
	}

	// 再次 Flush 应为空
	flushedAgain := splitter.Flush()
	if len(flushedAgain) != 0 {
		t.Fatalf("expected empty on second flush, got %v", flushedAgain)
	}
}

func TestSentenceSplitter_MaxLengthForceSplit(t *testing.T) {
	splitter := NewSentenceSplitter()

	// 构造 90 个没有标点的纯汉字
	longText := strings.Repeat("字", 90)
	res := splitter.Feed(longText)

	if len(res) != 1 {
		t.Fatalf("expected 1 force-split sentence, got: %v", res)
	}

	if len([]rune(res[0])) != maxSentenceRunes {
		t.Fatalf("expected max runes %d, got %d", maxSentenceRunes, len([]rune(res[0])))
	}

	// 剩余 10 个字留在 buffer，Flush 出来
	flushed := splitter.Flush()
	if len(flushed) != 1 || len([]rune(flushed[0])) != 10 {
		t.Fatalf("expected 10 runes in flushed, got %v", flushed)
	}
}

func TestSentenceSplitter_DecimalPointNotSplit(t *testing.T) {
	splitter := NewSentenceSplitter()

	// 3.14 的小数点不应当作为句末标点
	input := "圆周率的近似值是3.14159265358979323846，这是一个非常神奇的常数。"
	res := splitter.Feed(input)

	if len(res) == 0 {
		t.Fatalf("expected split sentence, got 0")
	}

	for _, s := range res {
		if strings.HasSuffix(s, "3.") {
			t.Fatalf("sentence incorrectly split at decimal point: %q", s)
		}
	}
}

func TestSentenceSplitter_TrailingPunctuation(t *testing.T) {
	splitter := NewSentenceSplitter()

	input := "请大家准时参加今晚在三楼举行的例行工作研讨会非常感谢大家的积极配合！！”"
	res := splitter.Feed(input)

	if len(res) != 1 {
		t.Fatalf("expected 1 sentence, got %d: %v", len(res), res)
	}

	// 确认双感叹号和闭合双引号都被完整吸收在句子末尾
	if !strings.HasSuffix(res[0], "！！”") {
		t.Fatalf("expected sentence to end with '！！”', got %q", res[0])
	}
}

func TestSentenceSplitter_MultiplePunctuationAndLanguages(t *testing.T) {
	splitter := NewSentenceSplitter()

	// 英文测试：Hello world!
	res := splitter.Feed("Hello")
	if len(res) != 0 {
		t.Fatalf("expected 0, got %v", res)
	}
	res = splitter.Feed(" world! How are you doing today?")
	if len(res) != 2 {
		t.Fatalf("expected 2 sentences, got %d: %v", len(res), res)
	}
	if res[0] != "Hello world!" {
		t.Fatalf("expected 'Hello world!', got %q", res[0])
	}
	if res[1] != "How are you doing today?" {
		t.Fatalf("expected 'How are you doing today?', got %q", res[1])
	}

	// 空 Feed
	res = splitter.Feed("")
	if len(res) != 0 {
		t.Fatalf("expected 0 for empty chunk, got %v", res)
	}

	// Flush 空
	flushed := splitter.Flush()
	if len(flushed) != 0 {
		t.Fatalf("expected empty flush, got %v", flushed)
	}
}
