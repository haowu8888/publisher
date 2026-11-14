package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"publisher/internal/store"
)

// newMaterial makes a material of the video or article at p, or returns
// false for another kind of file, as a video's caption. A video takes its title, description and
// tags from a caption file beside it ("x_caption.txt" or "x.txt"); an
// article's first "# " heading becomes its title, and its lines of "#tag"s
// its tags.
func newMaterial(p string) (store.Material, bool, error) {
	m := store.Material{ID: store.NewID(), Title: baseName(p), Tags: []string{}, CoverPath: siblingCover(p), CreatedAt: time.Now()}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
	switch {
	case slices.Contains(videoExts, ext):
		// Most videos here need no label; the user can change it.
		m.Kind, m.VideoPath, m.Statement = store.KindVideo, p, statements[0]
		if c, ok := readCaption(p); ok {
			m.Title, m.Description, m.Tags = c.title, c.description, c.tags
		}
	case slices.Contains(articleExts, ext):
		if ext == "txt" && isCaption(p) {
			return m, false, nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return m, false, err
		}
		title, body := splitTitle(string(b))
		if title != "" {
			m.Title = title
		}
		body, m.Tags = articleTags(body)
		m.Kind, m.ArticlePath, m.Body = store.KindArticle, p, body
		// The user's articles go to the official account as picture posts.
		m.WeixinType = "images"
		var cover string
		m.Images, cover = articleImages(p)
		if m.CoverPath == "" {
			m.CoverPath = cover
		}
	default:
		return m, false, nil
	}
	return m, true, nil
}

// articleImages returns the pictures in the folder beside the article at
// p with its name, in natural order, and its cover: the one named
// "cover", else the first.
func articleImages(p string) (images []string, cover string) {
	dir := strings.TrimSuffix(p, filepath.Ext(p))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, ""
	}
	for _, e := range entries {
		if e.IsDir() || !isImage(e.Name()) {
			continue
		}
		f := filepath.Join(dir, e.Name())
		if strings.EqualFold(baseName(f), "cover") {
			cover = f
			continue
		}
		images = append(images, f)
	}
	slices.SortFunc(images, func(a, b string) int {
		if naturalLess(filepath.Base(a), filepath.Base(b)) {
			return -1
		}
		return 1
	})
	if cover == "" && len(images) > 0 {
		cover = images[0]
	}
	return images, cover
}

// isImage reports whether a file name is of a picture platforms take.
func isImage(name string) bool {
	return slices.Contains(imageExts, strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")))
}

var imageExts = []string{"jpg", "jpeg", "png", "webp"}

type caption struct {
	title, description string
	tags               []string
}

// readCaption reads the caption file of the video at p: its text is the
// description, lines of "#tag"s are the tags.
func readCaption(p string) (caption, bool) {
	stem := strings.TrimSuffix(p, filepath.Ext(p))
	for _, name := range []string{stem + "_caption.txt", stem + ".txt"} {
		if b, err := os.ReadFile(name); err == nil {
			return parseCaption(string(b)), true
		}
	}
	return caption{}, false
}

// isCaption reports whether the text file at p is the caption of a video
// beside it, where readCaption looks for one, and so no article: dropped
// on the window with its video, it would be published as one.
func isCaption(p string) bool {
	stem := strings.TrimSuffix(p, filepath.Ext(p))
	for _, video := range []string{stem, strings.TrimSuffix(stem, "_caption")} {
		for _, ext := range videoExts {
			if _, err := os.Stat(video + "." + ext); err == nil {
				return true
			}
		}
	}
	return false
}

func parseCaption(s string) caption {
	var text []string
	var tags []string
	for line := range strings.Lines(strings.TrimPrefix(s, "\ufeff")) {
		if isTagLine(line) {
			tags = append(tags, strings.Fields(line)...)
			continue
		}
		text = append(text, strings.TrimRight(line, "\r\n"))
	}
	desc := strings.TrimSpace(strings.Join(text, "\n"))
	return caption{title: captionTitle(desc), description: desc, tags: cleanTags(tags)}
}

// isTagLine reports whether a line holds only "#tag"s. A Markdown heading
// has a space after its "#"s, so "# 标题" and "## 小节" are not tags.
func isTagLine(line string) bool {
	fields := strings.Fields(line)
	return len(fields) > 0 && !slices.ContainsFunc(fields, func(f string) bool {
		return !strings.HasPrefix(f, "#") || strings.TrimLeft(f, "#") == ""
	})
}

// articleTags takes an article's lines of "#tag"s, as "#AI #MCP" at its
// end, out of its body and returns them as its tags. Lines in code blocks
// stay, as "#!/bin/bash". Markdown editors save a "#" that starts a line
// as "\#" to keep it from being a heading, so that is a tag's "#" too.
func articleTags(body string) (string, []string) {
	var kept, tags []string
	fence := ""
	for line := range strings.Lines(body) {
		trimmed := strings.TrimSpace(line)
		unescaped := strings.ReplaceAll(line, `\#`, "#")
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
		case isTagLine(unescaped):
			tags = append(tags, strings.Fields(unescaped)...)
			continue
		}
		kept = append(kept, line)
	}
	if len(tags) == 0 {
		return body, []string{}
	}
	return strings.TrimRight(strings.Join(kept, ""), "\r\n\t ") + "\n", cleanTags(tags)
}

// captionTitle picks a title from a caption: its first sentence, with the
// next ones while it is under 10 characters, cut back to whole clauses when
// it runs over 30 (or 40, should 30 leave too little).
func captionTitle(s string) string {
	var title string
	for rest := strings.TrimSpace(s); rest != "" && utf8.RuneCountInString(title) < 10; {
		var sentence string
		sentence, rest = cutSentence(rest)
		title = trimEnd(title + sentence)
	}
	if utf8.RuneCountInString(title) <= 30 {
		return title
	}
	c := wholeClauses(title, 30)
	if utf8.RuneCountInString(c) < 12 {
		c = wholeClauses(title, 40)
	}
	if utf8.RuneCountInString(c) < 8 {
		return title
	}
	return c
}

// cutSentence splits s after its first sentence, which ends at "。？！" or
// an emoji.
func cutSentence(s string) (sentence, rest string) {
	ended := false
	for i, r := range s {
		end := strings.ContainsRune("。？！?!", r) || isEmoji(r)
		if ended && !end {
			return s[:i], s[i:]
		}
		ended = ended || end
	}
	return s, ""
}

// wholeClauses returns as many of s's leading clauses as fit in n
// characters.
func wholeClauses(s string, n int) string {
	var out string
	count := 0
	for i, r := range s {
		if count > n {
			break
		}
		if strings.ContainsRune("，；：", r) {
			out = s[:i]
		}
		count++
	}
	if count <= n {
		return s
	}
	return out
}

// trimEnd drops trailing spaces, full stops and emoji; a title ends with
// "？" or "！" but not "。".
func trimEnd(s string) string {
	return strings.TrimRightFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '。' || isEmoji(r) })
}

func isEmoji(r rune) bool {
	return r >= 0x1F000 || (r >= 0x2600 && r <= 0x27BF) || r == 0xFE0F || r == 0x200D
}

// naturalLess orders names with their numbers by value, so "ep42" comes
// before "ep100".
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if ra != rb {
			return ra < rb
		}
		a, b = a[sa:], b[sb:]
	}
	return len(a) < len(b)
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
