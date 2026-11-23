package platforms

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"publisher/internal/store"
)

// TestInlineImages puts a local picture, named relative to the article and
// percent-encoded by the Markdown renderer, into the HTML; a picture on the
// web stays a link.
func TestInlineImages(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "图 片"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "图 片", "1.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := inlineImages(markdownHTML("![a](<图 片/1.png>)\n\n![b](https://x.test/b.jpg)"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `src="data:image/png;base64,cG5n"`) {
		t.Errorf("local picture not inlined: %s", out)
	}
	if !strings.Contains(out, `src="https://x.test/b.jpg"`) {
		t.Errorf("web picture changed: %s", out)
	}
	if _, err := inlineImages(`<img src="missing.png">`, dir); err == nil {
		t.Error("a missing picture should fail")
	}
}

func TestParagraphs(t *testing.T) {
	got := paragraphs("第一段 **加粗**\n\n- 一\n- 二\n\n![图](a.png)\n\n> 引用")
	if want := "第一段 加粗\n一\n二\n引用"; got != want {
		t.Errorf("paragraphs = %q, want %q", got, want)
	}
}

// TestLineBreaks keeps a paragraph's lines apart, as escaped list items.
func TestLineBreaks(t *testing.T) {
	src := "开头：\n\\- **融资**：813亿\n\\- 估值：1.5万亿"
	if got := markdownHTML(src); strings.Count(got, "<br") != 2 {
		t.Errorf("markdownHTML = %q, want 2 line breaks", got)
	}
	if got, want := paragraphs(src), "开头：\n- 融资：813亿\n- 估值：1.5万亿"; got != want {
		t.Errorf("paragraphs = %q, want %q", got, want)
	}
}

func TestNoteImages(t *testing.T) {
	m := store.Material{CoverPath: "c.jpg", Images: []string{"1.jpg", "c.jpg", "2.jpg"}}
	if got := noteImages(m); !slices.Equal(got, []string{"c.jpg", "1.jpg", "2.jpg"}) {
		t.Errorf("noteImages = %q", got)
	}
}

// TestSpreadImages puts pictures between top-level blocks, evenly, and
// leaves a body with pictures of its own alone.
func TestSpreadImages(t *testing.T) {
	body := markdownHTML("one\n\n- a\n\n  in item\n\n- b\n\ntwo\n\nthree\n\nfour")
	got := spreadImages(body, []string{`D:\a\1.jpg`, `D:\a\2.jpg`})
	if n := strings.Count(got, "<img"); n != 2 {
		t.Fatalf("got %d pictures:\n%s", n, got)
	}
	if strings.Contains(got, "<p><img src=\"D:\\a\\1.jpg\"></p>\n</li>") || strings.Index(got, "1.jpg") > strings.Index(got, "2.jpg") {
		t.Errorf("misplaced:\n%s", got)
	}
	if !strings.HasSuffix(got, "<p>four</p>\n") {
		t.Errorf("a picture after the last block:\n%s", got)
	}
	if mine := `<p><img src="x.png"></p>`; spreadImages(mine, []string{"y.png"}) != mine {
		t.Error("changed a body with its own pictures")
	}
	if got := bodyImages(store.Material{CoverPath: "c.jpg", Images: []string{"c.jpg", "1.jpg"}}); len(got) != 1 || got[0] != "1.jpg" {
		t.Errorf("bodyImages: %q", got)
	}
}

// TestArticleBody makes a line naming a picture of the article's folder
// that picture, found there without the folder in the name.
func TestArticleBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "文章"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "文章", "3.JPG"), []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := store.Material{ArticlePath: filepath.Join(dir, "文章.md"), Body: "开头\n\n3.JPG\n\n9.jpg 不存在\n\n9.jpg\n\n![](3.JPG)"}
	body := articleBody(m)
	if want := "开头\n\n![](<3.JPG>)\n\n9.jpg 不存在\n\n9.jpg\n\n![](3.JPG)"; body != want {
		t.Errorf("got %q, want %q", body, want)
	}
	out, err := inlineImages(markdownHTML(body), articleDirs(m)...)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, `src="data:image/jpeg;base64,anBn"`); n != 2 {
		t.Errorf("got %d inlined pictures: %s", n, out)
	}
	if got := paragraphs(body); got != "开头\n9.jpg 不存在\n9.jpg" {
		t.Errorf("paragraphs: %q", got)
	}
}

// TestArticleMarkdown puts the local pictures of the Markdown into it as
// data URLs, leaving one on the web as it is, and spreads the folder's
// pictures through a body that shows none.
func TestArticleMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	if err := os.WriteFile(filepath.Join(dir, "1.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := store.Material{ArticlePath: path, Body: "开头\n\n![一](<1.png> \"标题\")\n\n![二](https://x.test/b.jpg)"}
	got, err := articleMarkdown(m)
	if err != nil {
		t.Fatal(err)
	}
	if want := "开头\n\n![一](data:image/png;base64,cG5n \"标题\")\n\n![二](https://x.test/b.jpg)"; got != want {
		t.Errorf("articleMarkdown = %q, want %q", got, want)
	}

	m = store.Material{ArticlePath: path, Body: "一\n\n```\n代码\n\n块\n```\n\n二", Images: []string{filepath.Join(dir, "1.png")}}
	if got, err = articleMarkdown(m); err != nil {
		t.Fatal(err)
	}
	if want := "一\n\n```\n代码\n\n块\n```\n\n![](data:image/png;base64,cG5n)\n\n二"; got != want {
		t.Errorf("articleMarkdown = %q, want %q", got, want)
	}
}
