package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"publisher/internal/store"
)

func TestParseCaption(t *testing.T) {
	c := importRules(store.Import{}).parseCaption("\ufeff跟你聊天的是人还是 AI？七十多年前的图灵测试，AI 已经过了吗？🤖\n\n#AI #人工智能 #图灵测试 #ChatGPT #科技史 #涨知识\n")
	if c.title != "跟你聊天的是人还是 AI？" {
		t.Errorf("title = %q", c.title)
	}
	if c.description != "跟你聊天的是人还是 AI？七十多年前的图灵测试，AI 已经过了吗？🤖" {
		t.Errorf("description = %q", c.description)
	}
	if want := []string{"AI", "人工智能", "图灵测试", "ChatGPT", "科技史", "涨知识"}; !slices.Equal(c.tags, want) {
		t.Errorf("tags = %q", c.tags)
	}
}

// TestArticleTags takes the lines of "#tag"s out of an article, but not its
// headings or the lines of its code blocks.
func TestArticleTags(t *testing.T) {
	body, tags := articleTags("正文第一段。\n\n## 小节\n\n```bash\n#!/bin/bash\n#注释\n```\n\n#AI #MCP  #人工智能\n#涨知识 #AI\n\n")
	if want := []string{"AI", "MCP", "人工智能", "涨知识"}; !slices.Equal(tags, want) {
		t.Errorf("tags = %q", tags)
	}
	if want := "正文第一段。\n\n## 小节\n\n```bash\n#!/bin/bash\n#注释\n```\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	// An editor's escaped "#" is a tag's too.
	body, tags = articleTags("正文。  \n还是正文！\n\n\\#AI #Optimus \\#人形机器人\n")
	if want := []string{"AI", "Optimus", "人形机器人"}; !slices.Equal(tags, want) || body != "正文。  \n还是正文！\n" {
		t.Errorf("escaped: %q %q", body, tags)
	}
	// Without tags the body stays as it is.
	if body, tags := articleTags("# 不是标签\n##\n正文\n"); body != "# 不是标签\n##\n正文\n" || len(tags) != 0 || tags == nil {
		t.Errorf("no tags: %q %q", body, tags)
	}
}

func TestNewMaterialReadsArticleTags(t *testing.T) {
	md := filepath.Join(t.TempDir(), "mcp.md")
	if err := os.WriteFile(md, []byte("# AI 圈天天在说的 MCP 是什么？\n\n正文……\n\n#AI #MCP #人工智能 #涨知识\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok, err := newMaterial(md, store.Import{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if m.Title != "AI 圈天天在说的 MCP 是什么？" || m.Body != "正文……\n" || !slices.Equal(m.Tags, []string{"AI", "MCP", "人工智能", "涨知识"}) {
		t.Errorf("material = %q %q %q", m.Title, m.Body, m.Tags)
	}
}

func TestCaptionTitle(t *testing.T) {
	for in, want := range map[string]string{
		// Too short alone: the next sentence joins, an emoji ending it.
		"输入一句话，两分钟后得到一首带歌词、带人声的完整歌曲 🎵 AI 写歌工具 Suno 怎么玩？": "输入一句话，两分钟后得到一首带歌词、带人声的完整歌曲",
		"下周面试心里没底？三步让 AI 当面试官。":                          "下周面试心里没底？三步让 AI 当面试官",
		// Over 30: whole clauses, but not just the name before "：".
		"Claude Code：Anthropic 的 AI 编程工具，2025 年 2 月推出预览、5 月正式上线。用大白话说需求 💻": "Claude Code：Anthropic 的 AI 编程工具",
		"Cline：2024 年诞生的开源 AI 编程助手，最早叫 Claude Dev。装在 VS Code 里":            "Cline：2024 年诞生的开源 AI 编程助手",
		"扣子（Coze）：字节跳动 2024 年 2 月上线的智能体搭建平台。用拖拽搭 AI 应用":                    "扣子（Coze）：字节跳动 2024 年 2 月上线的智能体搭建平台",
		"没有标点也没有分句的一段很长很长很长很长很长很长很长很长很长的文字":                                "没有标点也没有分句的一段很长很长很长很长很长很长很长很长很长的文字",
	} {
		if got := captionTitle(in, 10, 30); got != want {
			t.Errorf("captionTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewMaterialReadsCaptionAndCover(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"ep42_mcp.mp4":         "",
		"ep42_mcp_cover.jpg":   "",
		"ep42_mcp_caption.txt": "AI 圈天天在说的 MCP 是什么？一个比喻讲明白 🔌\n\n#AI #MCP\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, ok, err := newMaterial(filepath.Join(dir, "ep42_mcp.mp4"), store.Import{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if m.Title != "AI 圈天天在说的 MCP 是什么？" || m.CoverPath != filepath.Join(dir, "ep42_mcp_cover.jpg") || len(m.Tags) != 2 || m.Statement == "" {
		t.Errorf("material = %+v", m)
	}
}

func TestNaturalLess(t *testing.T) {
	names := []string{"ep100_a.mp4", "ep42_b.mp4", "ep9.mp4", "ep41_c.mp4", "ep042_d.mp4"}
	slices.SortFunc(names, func(a, b string) int {
		if naturalLess(a, b) {
			return -1
		}
		return 1
	})
	if want := []string{"ep9.mp4", "ep41_c.mp4", "ep42_b.mp4", "ep042_d.mp4", "ep100_a.mp4"}; !slices.Equal(names, want) {
		t.Errorf("sorted = %q", names)
	}
}

// TestArticleImages imports an article with a folder of pictures beside it:
// they come in natural order, and the one named cover is the cover.
func TestArticleImages(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "文章.md")
	pics := filepath.Join(dir, "文章")
	if err := os.Mkdir(pics, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"10.png", "2.png", "1.jpg", "cover.jpg", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(pics, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(md, []byte("# 标题\n\n正文 ![](文章/1.jpg)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok, err := newMaterial(md, store.Import{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var names []string
	for _, p := range m.Images {
		names = append(names, filepath.Base(p))
	}
	if want := []string{"1.jpg", "2.png", "10.png"}; !slices.Equal(names, want) {
		t.Errorf("images = %q", names)
	}
	if m.CoverPath != filepath.Join(pics, "cover.jpg") {
		t.Errorf("cover = %q", m.CoverPath)
	}

	// Without a cover picture, the first one is the cover.
	os.Remove(filepath.Join(pics, "cover.jpg"))
	if m, _, _ = newMaterial(md, store.Import{}); m.CoverPath != filepath.Join(pics, "1.jpg") {
		t.Errorf("cover without cover.jpg = %q", m.CoverPath)
	}
}

// TestImportRules reads files by the settings' rules in place of the
// defaults.
func TestImportRules(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"ep1.mp4":       "",
		"ep1-封面.png":    "",
		"ep1_cover.jpg": "",
		"ep1-字幕.txt":    "很长的第一句话已经超过了十个字。第二句。\n#AI\n",
		"文章.md":         "# 标题\n\n正文\n\n#AI\n",
		"文章/首图.jpg":     "",
		"文章/1.jpg":      "",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	im := store.Import{CoverSuffix: "-封面", CaptionSuffix: "-字幕", Statement: statements[1], ImageCover: "首图", Weixin: "article"}
	v, _, _ := newMaterial(filepath.Join(dir, "ep1.mp4"), im)
	if v.CoverPath != filepath.Join(dir, "ep1-封面.png") || v.Title != "很长的第一句话已经超过了十个字" || v.Statement != statements[1] || !slices.Equal(v.Tags, []string{"AI"}) {
		t.Errorf("video = %+v", v)
	}
	if !importRules(im).isCaption(filepath.Join(dir, "ep1-字幕.txt")) {
		t.Error("caption with its suffix taken for an article")
	}
	a, _, _ := newMaterial(filepath.Join(dir, "文章.md"), im)
	if a.CoverPath != filepath.Join(dir, "文章", "首图.jpg") || a.WeixinType != "" || a.Title != "标题" {
		t.Errorf("article = %+v", a)
	}

	// Everything off: names for titles, text kept as it is.
	im = store.Import{NoCaption: true, NoHeading: true, NoTags: true, NoImages: true}
	v, _, _ = newMaterial(filepath.Join(dir, "ep1.mp4"), im)
	if v.Title != "ep1" || v.Description != "" || len(v.Tags) != 0 {
		t.Errorf("video, rules off = %+v", v)
	}
	a, _, _ = newMaterial(filepath.Join(dir, "文章.md"), im)
	if a.Title != "文章" || a.Body != "# 标题\n\n正文\n\n#AI\n" || len(a.Tags) != 0 || len(a.Images) != 0 || a.CoverPath != "" {
		t.Errorf("article, rules off = %+v", a)
	}

	// The file name for the title, the caption still for the rest.
	v, _, _ = newMaterial(filepath.Join(dir, "ep1.mp4"), store.Import{CaptionSuffix: "-字幕", NameTitle: true})
	if v.Title != "ep1" || v.Description == "" {
		t.Errorf("video, name title = %+v", v)
	}
}
