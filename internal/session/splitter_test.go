package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestSentenceSplitter_MinLengthRequirement(t *testing.T) {
	splitter := NewSentenceSplitter()

	// 1. 少于 20 字遇到标点：不应该切分
	res := splitter.Feed("你好，")
	if len(res) != 0 {
		t.Fatalf("expected 0 sentences for short chunk with punctuation, got: %v", res)
	}

	res = splitter.Feed("我是小智。")
	if len(res) != 0 {
		t.Fatalf("expected 0 sentences for total length < 20 runes, got: %v", res)
	}

	// 2. 累积超过 20 字并在 20 字后遇到标点：应该切分出第一句
	res = splitter.Feed("很高兴能够为你提供帮助，请问你今天有什么问题想咨询吗？")
	if len(res) == 0 {
		t.Fatalf("expected at least 1 sentence when reaching >= 20 runes with punctuation, got 0")
	}

	// 验证切出的第一句字数 >= 20
	firstSentence := res[0]
	runes := []rune(firstSentence)
	if len(runes) < minSentenceRunes {
		t.Fatalf("expected sentence runes >= %d, got %d (%q)", minSentenceRunes, len(runes), firstSentence)
	}

	// 3. Flush 刷新末尾不足 20 字的残余文本
	flushed := splitter.Flush()
	if len(flushed) > 0 {
		for _, s := range flushed {
			if strings.TrimSpace(s) == "" {
				t.Fatalf("expected non-empty flushed sentence")
			}
		}
	}
}

func TestSentenceSplitter_FlushUnderMinRunes(t *testing.T) {
	splitter := NewSentenceSplitter()

	// 只有 5 个字
	res := splitter.Feed("好的，收到。")
	if len(res) != 0 {
		t.Fatalf("expected no split during feed, got: %v", res)
	}

	// 结束时 Flush 必须刷出
	flushed := splitter.Flush()
	expected := []string{"好的，收到。"}
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

	// 构造 70 个没有标点的纯汉字
	longText := strings.Repeat("字", 70)
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
