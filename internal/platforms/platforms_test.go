package platforms

import "testing"

func TestFitTitle(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"跟你聊天的是人还是 AI？", 20, "跟你聊天的是人还是 AI？"},
		{"作业、周报、文案全交给 AI，人会不会越用越笨？", 20, "作业、周报、文案全交给 AI"},
		{"Claude Code：Anthropic 的 AI 编程工具", 20, "Claude Code"},
		{"下周面试心里没底？三步让 AI 当面试官来练手吧", 20, "下周面试心里没底？三步让 AI"},
		{"一二三四五六七八九十一二三四五六七八九十一二", 20, "一二三四五六七八九十一二三四五六七八九十"},
	} {
		if got := fitTitle(c.in, c.n); got != c.want {
			t.Errorf("fitTitle(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// TestFitText leaves room in the text for the tags typed after it.
func TestFitText(t *testing.T) {
	l := Limits{Text: 20, Tags: 2}
	got := l.fitText("一二三四五六七八九十一二三四五六七八九十", []string{"AI", "教程", "不算"})
	// 20, less a space, less "#AI " and "#教程 ".
	if want := "一二三四五六七八九十一"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
