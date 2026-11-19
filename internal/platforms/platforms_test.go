package platforms

import (
	"testing"

	"publisher/internal/store"
)

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

func TestShortTitle(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"AI 测试，标题!@ 短", "AI 测试 标题 短"},
		{"Claude Code：Anthropic 的 AI 编程工具", "Claude Code"},
		{"《三体》里的 AI？", "《三体》里的 AI？"},
		{"你好，AI", ""},
		{"一二三四五六七八九十一二三四五六七八九十", "一二三四五六七八九十一二三四五六"},
	} {
		if got := shortTitle(c.in); got != c.want {
			t.Errorf("shortTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCheckDayu refuses, ahead of the form, the articles Dayu would turn
// down when submitted by themselves.
func TestCheckDayu(t *testing.T) {
	art := func(title, body, cover string) store.Material {
		return store.Material{Kind: store.KindArticle, Title: title, Body: body, CoverPath: cover}
	}
	for _, c := range []struct {
		name string
		m    store.Material
		mode store.Mode
		ok   bool
	}{
		{"cover", art("一个够长的标题", "正文", `C:\a.png`), store.ModeAuto, true},
		{"picture in body", art("一个够长的标题", "![](https://example.com/a.png)", ""), store.ModeAuto, true},
		{"no picture", art("一个够长的标题", "正文", ""), store.ModeAuto, false},
		{"short title", art("短标题", "正文", `C:\a.png`), store.ModeAuto, false},
		{"review", art("短", "正文", ""), store.ModeReview, true},
		{"video", store.Material{Kind: store.KindVideo, Title: "短"}, store.ModeAuto, true},
	} {
		if err := checkDayu(c.m, c.mode); (err == nil) != c.ok {
			t.Errorf("%s: checkDayu = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// TestIqiyiText drops what iQiyi's form turns down, emoji and other
// symbols, and keeps Chinese, ASCII and the punctuation it takes.
func TestIqiyiText(t *testing.T) {
	for in, want := range map[string]string{
		"把检索、模型串起来，好用但复杂 🔗":     "把检索、模型串起来，好用但复杂",
		"《AI 词典》：过拟合？100% 讲清楚！": "《AI 词典》：过拟合？100% 讲清楚！",
		"温度 30℃ → 很热 ✨\n第二行":    "温度 30  很热 \n第二行",
	} {
		if got := iqiyiText(in); got != want {
			t.Errorf("iqiyiText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMgtvTagLen counts ASCII as half a character, as Mango TV does.
func TestMgtvTagLen(t *testing.T) {
	for in, want := range map[string]float64{"AI": 1, "人工智能": 4, "LangChain": 4.5, "AI开发": 3} {
		if got := mgtvTagLen(in); got != want {
			t.Errorf("mgtvTagLen(%q) = %v, want %v", in, got, want)
		}
	}
}
