package platforms

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"publisher/internal/store"

	"github.com/go-rod/rod"
)

// mdImage is a picture in Markdown: ![alt](src "title"), its source maybe
// in angle brackets.
var mdImage = regexp.MustCompile(`!\[([^\]]*)\]\(\s*(<[^>]*>|[^)\s]+)((?:\s+"[^"]*")?)\s*\)`)

// articleMarkdown is an article's Markdown body for an editor that takes
// Markdown, its local pictures in it as data URLs, which uploadImages then
// puts on the platform's servers. A body that shows none of its pictures
// gets those of its folder spread through it, as spreadImages does.
func articleMarkdown(m store.Material) (string, error) {
	src := articleBody(m)
	if !mdImage.MatchString(src) {
		src = spreadMarkdownImages(src, bodyImages(m))
	}
	dirs := articleDirs(m)
	var err error
	out := mdImage.ReplaceAllStringFunc(src, func(s string) string {
		g := mdImage.FindStringSubmatch(s)
		p := strings.TrimSuffix(strings.TrimPrefix(g[2], "<"), ">")
		if strings.Contains(p, "://") || strings.HasPrefix(p, "data:") {
			return s
		}
		if u, e := url.PathUnescape(p); e == nil {
			p = u
		}
		data, e := dataURL(locateImage(p, dirs))
		if e != nil {
			err = fmt.Errorf("正文图片读不了: %w", e)
			return s
		}
		return "![" + g[1] + "](" + data + g[3] + ")"
	})
	return out, err
}

// spreadMarkdownImages places pictures evenly between the top-level blocks
// of Markdown, those apart by blank lines outside code fences, as
// spreadImages does in HTML.
func spreadMarkdownImages(src string, images []string) string {
	if len(images) == 0 {
		return src
	}
	var blocks []string
	var cur []string
	fenced := false
	for _, line := range strings.Split(strings.TrimSpace(src), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
		}
		if strings.TrimSpace(line) == "" && !fenced {
			if len(cur) > 0 {
				blocks = append(blocks, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, strings.Join(cur, "\n"))
	}
	if len(blocks) == 0 {
		blocks = []string{""}
	}
	after := make([][]string, len(blocks))
	for i, p := range images {
		k := max(1, ((i+1)*len(blocks)+len(images))/(len(images)+1)) - 1
		after[k] = append(after[k], "![](<"+p+">)")
	}
	var out []string
	for i, b := range blocks {
		if b != "" {
			out = append(out, b)
		}
		out = append(out, after[i]...)
	}
	return strings.Join(out, "\n\n")
}

// uploadImages puts each data URL picture of text, HTML or Markdown, on
// the platform's servers through upload, a script taking a File to the
// URL it is stored at, as the editor's own picture upload does, and
// returns text with those URLs.
func uploadImages(page *rod.Page, text, upload string) (string, error) {
	if !strings.Contains(text, "data:image/") {
		return text, nil
	}
	res, err := page.Timeout(5*time.Minute).Eval(`async (text) => {
		const upload = `+upload+`;
		const done = new Map();
		const re = /data:(image\/[\w.+-]+);base64,[A-Za-z0-9+\/=]+/g;
		for (const [data, type] of text.matchAll(re)) {
			if (done.has(data)) continue;
			const bytes = Uint8Array.from(atob(data.split(",")[1]), (c) => c.charCodeAt(0));
			const ext = type.split("/")[1].replace("jpeg", "jpg").replace("svg+xml", "svg");
			const url = await upload(new File([bytes], "image" + (done.size + 1) + "." + ext, { type }));
			if (!url) throw new Error("没有返回图片地址");
			done.set(data, url);
		}
		return text.replace(re, (data) => done.get(data));
	}`, text)
	if err != nil {
		return "", fmt.Errorf("上传正文图片失败: %w", err)
	}
	return res.Value.Str(), nil
}
