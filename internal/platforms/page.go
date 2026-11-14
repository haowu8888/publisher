package platforms

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// openForm opens a platform's publish page in a new tab, brought to the
// front for the user to watch.
//
// Until ctx ends it answers the page's native dialogs, which would
// otherwise stop the page, and with it every call to the page, until the
// user closed them: Douyin, for one, reloads after a failed submit, and
// Chrome then asks whether to leave the filled form.
func openForm(ctx context.Context, b *browser.Browser, url string, j Job) (*rod.Page, error) {
	j.Report("打开发布页")
	page, err := b.Page(url)
	if err != nil {
		return nil, err
	}
	page.Activate()
	page = page.Context(ctx)
	go page.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
		if e.Message != "" && e.Type != proto.PageDialogTypeBeforeunload {
			j.Report("页面提示：" + e.Message)
		}
		_ = proto.PageHandleJavaScriptDialog{Accept: true}.Call(page)
	})()
	if err := page.WaitLoad(); err != nil {
		return nil, err
	}
	return page, nil
}

// loggedOut reports whether a page meant to stay on a URL containing keep
// went to a login page instead, or shows one in place, as Douyin does once
// the server ends a session whose cookie is still there.
func loggedOut(page *rod.Page, keep string) bool {
	info, err := page.Timeout(evalTimeout).Info()
	if err != nil {
		return false
	}
	if !strings.Contains(info.URL, keep) || strings.Contains(info.URL, "login") || strings.Contains(info.URL, "passport") {
		return true
	}
	res, err := page.Timeout(evalTimeout).Eval(`() => /扫码登录/.test(document.body.innerText)`)
	return err == nil && res.Value.Bool()
}

// loadFailed is what a page shows in place of a form it could not load,
// as Kuaishou's 应用加载失败，请刷新重试.
var loadFailed = regexp.MustCompile(`加载失败.{0,6}刷新`)

// waitElement waits up to 30 seconds for sel on a page whose URL should
// keep containing keep. Login redirects happen in the page's script after
// load, so it watches for one meanwhile, and closes the page on finding it.
// A page that says it failed to load is loaded once more.
func waitElement(ctx context.Context, page *rod.Page, sel, keep string) (*rod.Element, error) {
	reloaded := false
	for deadline := time.Now().Add(30 * time.Second); ; {
		if loggedOut(page, keep) {
			page.Close()
			return nil, ErrNotLoggedIn
		}
		if el, err := page.Sleeper(rod.NotFoundSleeper).Element(sel); err == nil {
			return el, nil
		}
		if !reloaded {
			if res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`); err == nil && loadFailed.MatchString(res.Value.Str()) {
				reloaded = true
				if err := page.Timeout(time.Minute).Reload(); err != nil {
					return nil, fmt.Errorf("发布页加载失败，刷新也没成功: %w", err)
				}
				deadline = time.Now().Add(30 * time.Second)
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("未找到 %s，发布页可能已改版", sel)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return nil, err
		}
	}
}

// startUpload gives the file input sel the file at path, and waits for
// startedSel, which shows that the page took it. Upload widgets often
// ignore a file given before their script is ready, so it waits a little
// first and gives the file once more if nothing happens.
func startUpload(ctx context.Context, page *rod.Page, sel, keep, path, startedSel string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("文件不可读: %w", err)
	}
	if _, err := waitElement(ctx, page, sel, keep); err != nil {
		return err
	}
	for try := 0; try < 2; try++ {
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		in, err := page.Element(sel)
		if err != nil {
			return err
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
			if ok, _, _ := page.Has(startedSel); ok {
				return nil
			}
			if err := sleep(ctx, time.Second); err != nil {
				return err
			}
		}
	}
	return errors.New("页面没有开始上传，发布页可能已改版")
}

// evalTimeout bounds a script run in a page, which never returns while
// the page is stuck.
const evalTimeout = 15 * time.Second

var percent = regexp.MustCompile(`^([0-9]|[1-9][0-9])(\.[0-9]+)?%$`)

// uploadFailed is what pages say of an upload that will not finish.
var uploadFailed = regexp.MustCompile(`上传失败|上传出错|上传错误`)

// uploadWatch tells when a page starts saying that its upload failed, so
// the task ends then rather than waiting out its hour. What the page said
// at the first look is its own wording, not news: only more of it counts.
type uploadWatch struct {
	looked bool
	said   int
}

func (w *uploadWatch) check(page *rod.Page, body string) error {
	said := len(uploadFailed.FindAllString(body, -1))
	if !w.looked {
		w.looked, w.said = true, said
	}
	if said <= w.said {
		return nil
	}
	if notes := pageNotes(page); notes != "" {
		return fmt.Errorf("视频上传失败，页面显示：%s", notes)
	}
	return errors.New("视频上传失败，请在浏览器中查看页面提示后重试")
}

// waitUploaded waits until a video upload finishes on pages that offer to
// upload again once it has (Douyin, Kuaishou, Xiaohongshu), showing its
// progress meanwhile.
func waitUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const text = document.body.innerText;
			const pct = [...document.querySelectorAll("span, div, p")]
				.filter((e) => e.children.length === 0)
				.map((e) => e.textContent.trim())
				.find((t) => /^\d{1,2}(\.\d+)?%$/.test(t));
			return { again: text.includes("重新上传"), cancel: text.includes("取消上传"), pct: pct || "", text };
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		v := res.Value
		pct := v.Get("pct").Str()
		if err := watch.check(page, v.Get("text").Str()); err != nil {
			return err
		}
		if v.Get("again").Bool() && !v.Get("cancel").Bool() && !percent.MatchString(pct) {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if pct != "" {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", pct, time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// find waits up to wait for sel. Unlike page.Timeout(wait).Element, the
// element it returns does not keep that deadline for later actions.
func find(page *rod.Page, sel string, wait time.Duration) (*rod.Element, error) {
	el, err := page.Timeout(wait).Element(sel)
	if err != nil {
		return nil, err
	}
	return el.CancelTimeout(), nil
}

// findR is find for an element whose text matches the regular expression
// text.
func findR(page *rod.Page, sel, text string, wait time.Duration) (*rod.Element, error) {
	el, err := page.Timeout(wait).ElementR(sel, text)
	if err != nil {
		return nil, err
	}
	return el.CancelTimeout(), nil
}

// jsClick clicks el from script, so overlays such as feature tours cannot
// take the click.
func jsClick(el *rod.Element) error {
	_, err := el.Eval(`() => this.click()`)
	return err
}

// clickText clicks the first element matching sel whose whole text matches
// the regular expression text.
func clickText(page *rod.Page, sel, text string, wait time.Duration) error {
	el, err := findR(page, sel, text, wait)
	if err != nil {
		return fmt.Errorf("未找到「%s」按钮: %w", strings.Trim(text, "^$"), err)
	}
	return jsClick(el)
}

// clickVisible clicks the first shown element matching sel whose whole
// text matches the regular expression text, waiting up to wait for one.
// Pages keep closed dialogs in the document, whose buttons share the text
// of the open one's.
func clickVisible(ctx context.Context, page *rod.Page, sel, text string, wait time.Duration) error {
	for deadline := time.Now().Add(wait); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(sel, text) => {
			const re = new RegExp(text);
			const el = [...document.querySelectorAll(sel)].find((e) => e.offsetParent && re.test(e.textContent.trim()));
			if (el) el.click();
			return !!el;
		}`, sel, text)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("未找到「%s」按钮", strings.Trim(text, "^$"))
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// pickByName clicks the visible element matching sel whose label, the text
// of labelSel inside it, is name, waiting up to wait for the choices to
// load. what names the choices in the error, which lists those there are.
func pickByName(ctx context.Context, page *rod.Page, sel, labelSel, name, what string, wait time.Duration) error {
	var names []string
	for deadline := time.Now().Add(wait); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(sel, labelSel, name) => {
			const label = (e) => e.querySelector(labelSel)?.textContent.trim() ?? "";
			const items = [...document.querySelectorAll(sel)].filter((e) => e.offsetParent);
			const el = items.find((e) => label(e) === name);
			if (el) el.click();
			return el ? null : items.map(label);
		}`, sel, labelSel, name)
		if err == nil {
			if res.Value.Nil() {
				return nil
			}
			names = names[:0]
			for _, v := range res.Value.Arr() {
				names = append(names, v.Str())
			}
		}
		if time.Now().After(deadline) {
			if len(names) == 0 {
				return fmt.Errorf("未找到%s「%s」：账号下还没有%s", what, name, what)
			}
			return fmt.Errorf("未找到%s「%s」，现有：%s", what, name, strings.Join(names, "、"))
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// waitCount waits up to wait for at least n elements matching sel.
func waitCount(ctx context.Context, page *rod.Page, sel string, n int, wait time.Duration) error {
	for deadline := time.Now().Add(wait); ; {
		if els, err := page.Timeout(evalTimeout).Elements(sel); err == nil && len(els) >= n {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %s 超时", sel)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// waitGone waits up to wait until nothing matching sel is shown any more,
// as a dialog that has closed.
func waitGone(ctx context.Context, page *rod.Page, sel string, wait time.Duration) error {
	for deadline := time.Now().Add(wait); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(sel) => ![...document.querySelectorAll(sel)].some((e) => e.getClientRects().length)`, sel)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 没有关闭", sel)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// setCover puts the material's cover, if it has one, on a video's form
// with set. A cover that cannot be set is no reason not to publish: the
// platform then takes a frame of the video, as it does without a cover,
// and the note returned says so in the publish's last step. Escape closes
// the cover editor a failed try may have left over the form.
func setCover(ctx context.Context, page *rod.Page, j Job, set func(ctx context.Context, page *rod.Page, path string) error) (note string, err error) {
	if j.Material.CoverPath == "" {
		return "", nil
	}
	j.Report("设置封面")
	err = set(ctx, page, j.Material.CoverPath)
	if err == nil || ctx.Err() != nil {
		return "", ctx.Err()
	}
	page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
	return "（封面没设上，用的是平台默认封面：" + err.Error() + "）", nil
}

// dismissGuides closes the feature tours platforms pop over their forms.
func dismissGuides(page *rod.Page) {
	page.Timeout(evalTimeout).Eval(`() => document.querySelectorAll("button, span, div").forEach((e) => {
		if (e.children.length <= 1 && /^(我知道了|知道了|跳过)$/.test(e.textContent.trim())) e.click();
	})`)
}

// typeInto appends text at the end of a contenteditable editor, as typing
// would.
func typeInto(page *rod.Page, editor *rod.Element, text string) error {
	if err := editor.Focus(); err != nil {
		return err
	}
	if _, err := editor.Eval(`() => {
		const r = document.createRange();
		r.selectNodeContents(this);
		r.collapse(false);
		const s = getSelection();
		s.removeAllRanges();
		s.addRange(r);
	}`); err != nil {
		return err
	}
	return page.InsertText(text)
}

// pasteHTML pastes rich text into an editor, which keeps headings, lists
// and the like where typing would not.
func pasteHTML(editor *rod.Element, htmlText, plain string) error {
	if err := editor.Focus(); err != nil {
		return err
	}
	_, err := editor.Eval(`(html, plain) => {
		const dt = new DataTransfer();
		dt.setData("text/html", html);
		dt.setData("text/plain", plain);
		this.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
	}`, htmlText, plain)
	return err
}

// finish reports the last step of a publish that submitted itself and
// closes its tab, which nobody needs to see: tabs would pile up in a
// browser that stays open. A failed publish leaves its tab to look at.
func finish(page *rod.Page, j Job, step string) error {
	j.Report(step)
	page.Timeout(evalTimeout).Close()
	return nil
}

// loginLost is what platforms say when a submit finds the login gone.
var loginLost = regexp.MustCompile(`登[录陆](已)?(失效|过期)|请重新登[录陆]`)

// waitPublished waits for a page to show that a submit worked: its URL
// gaining one of urlMarks, or a text matching the regular expression text.
// It gives up early when the page starts saying the login is gone.
func waitPublished(ctx context.Context, page *rod.Page, urlMarks []string, text string) error {
	return waitPublishedFor(ctx, page, urlMarks, text, 90*time.Second)
}

// waitPublishedFor is waitPublished, waiting no longer than wait.
func waitPublishedFor(ctx context.Context, page *rod.Page, urlMarks []string, text string, wait time.Duration) error {
	re := regexp.MustCompile(text)
	said := false
	if res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`); err == nil {
		said = loginLost.MatchString(res.Value.Str())
	}
	for deadline := time.Now().Add(wait); ; {
		if info, err := page.Timeout(evalTimeout).Info(); err == nil {
			for _, m := range urlMarks {
				if strings.Contains(info.URL, m) {
					return nil
				}
			}
		}
		if res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`); err == nil {
			body := res.Value.Str()
			if re.MatchString(body) {
				return nil
			}
			if !said && loginLost.MatchString(body) {
				return errors.New("平台提示登录失效，没有发出去：请在「账号」页点「重新登录」扫码后重试")
			}
		}
		if time.Now().After(deadline) {
			if said := pageNotes(page); said != "" {
				return fmt.Errorf("提交后未看到成功提示，页面显示：%s", said)
			}
			return errors.New("提交后未看到成功提示，请在浏览器中查看")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// pageNotes returns the text of the messages and dialogs a page shows, as
// what it said to a submit that did not go through.
func pageNotes(page *rod.Page) string {
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const sel = '[class*="toast"], [class*="message"], [class*="Message"], [class*="dialog"], [class*="modal"], [class*="tips"], [role="alert"], [role="dialog"]';
		const texts = [...document.querySelectorAll(sel)]
			.filter((e) => e.getClientRects().length > 0)
			.map((e) => e.innerText.trim().replace(/\s+/g, " "))
			.filter((t) => t && t.length < 200);
		return [...new Set(texts)].filter((t, i, all) => !all.some((o, j) => j !== i && o !== t && o.includes(t))).slice(0, 4).join("；");
	}`)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// markdownHTML renders an article's Markdown body.
func markdownHTML(src string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<p>" + html.EscapeString(src) + "</p>"
	}
	return buf.String()
}

var imgSrc = regexp.MustCompile(`(<img[^>]*?\ssrc=")([^"]*)(")`)

// inlineImages puts the local pictures an article's HTML shows into it as
// data URLs, resolving relative paths against dirs in turn, the article's
// folders: editors that take pasted pictures store them on their own
// servers, and could not read the files. Pictures on the web stay as they
// are.
func inlineImages(htmlText string, dirs ...string) (string, error) {
	var err error
	out := imgSrc.ReplaceAllStringFunc(htmlText, func(tag string) string {
		m := imgSrc.FindStringSubmatch(tag)
		src := html.UnescapeString(m[2])
		if strings.Contains(src, "://") || strings.HasPrefix(src, "data:") {
			return tag
		}
		if p, e := url.PathUnescape(src); e == nil {
			src = p
		}
		b, e := os.ReadFile(locateImage(src, dirs))
		if e != nil {
			err = fmt.Errorf("正文图片读不了: %w", e)
			return tag
		}
		typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(src)))
		if typ == "" {
			typ = "image/png"
		}
		return m[1] + "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(b) + m[3]
	})
	return out, err
}

// locateImage resolves a relative picture path against the first of dirs
// that has it, or else the first of dirs.
func locateImage(src string, dirs []string) string {
	if filepath.IsAbs(src) || len(dirs) == 0 {
		return src
	}
	for _, dir := range dirs {
		if p := filepath.Join(dir, src); fileExists(p) {
			return p
		}
	}
	return filepath.Join(dirs[0], src)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// articleDirs are where an article's pictures are looked for: beside it,
// then in the folder with its name.
func articleDirs(m store.Material) []string {
	return []string{filepath.Dir(m.ArticlePath), strings.TrimSuffix(m.ArticlePath, filepath.Ext(m.ArticlePath))}
}

// imageLine is a line of Markdown holding only a picture's file name.
var imageLine = regexp.MustCompile(`(?mi)^[ \t]*([^\s()\[\]<>]+\.(?:jpe?g|png|webp|gif))[ \t]*$`)

// articleBody is an article's Markdown with each line naming one of its
// pictures, as "3.jpg", made that picture, so the writer need not spell out
// the image syntax and the folder. A name no picture has stays text.
func articleBody(m store.Material) string {
	dirs := articleDirs(m)
	return imageLine.ReplaceAllStringFunc(m.Body, func(line string) string {
		name := strings.TrimSpace(line)
		if !fileExists(locateImage(name, dirs)) {
			return line
		}
		return "![](<" + filepath.ToSlash(name) + ">)"
	})
}

// blockEnd ends a top-level block of goldmark's HTML; one followed by a
// closing tag is nested, as a paragraph in a list item.
var blockEnd = regexp.MustCompile(`</(p|h[1-6]|ul|ol|blockquote|pre|table)>\n`)

// spreadImages places pictures evenly between the blocks of an article's
// HTML, for a body that shows none of its own: the article's folder holds
// its pictures, but its Markdown need not say where they go.
func spreadImages(htmlText string, images []string) string {
	if len(images) == 0 || strings.Contains(htmlText, "<img") {
		return htmlText
	}
	var ends []int
	for _, loc := range blockEnd.FindAllStringIndex(htmlText, -1) {
		if !strings.HasPrefix(htmlText[loc[1]:], "</") {
			ends = append(ends, loc[1])
		}
	}
	if len(ends) == 0 {
		ends = []int{len(htmlText)}
	}
	var b strings.Builder
	last := 0
	for i, p := range images {
		// After block k of len(ends), k spread from the first to the last.
		k := max(1, ((i+1)*len(ends)+len(images))/(len(images)+1)) - 1
		at := ends[k]
		b.WriteString(htmlText[last:at])
		b.WriteString(`<p><img src="` + html.EscapeString(p) + `"></p>` + "\n")
		last = at
	}
	b.WriteString(htmlText[last:])
	return b.String()
}

// bodyImages are the pictures an article's body shows when its Markdown
// places none: those of its folder but the cover.
func bodyImages(m store.Material) []string {
	var out []string
	for _, p := range m.Images {
		if p != m.CoverPath {
			out = append(out, p)
		}
	}
	return out
}

// noteImages returns the pictures of an image post: the cover first, then
// the article's pictures, each once.
func noteImages(m store.Material) []string {
	var out []string
	for _, p := range append([]string{m.CoverPath}, m.Images...) {
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

var (
	tags   = regexp.MustCompile(`<[^>]+>`)
	spaces = regexp.MustCompile(`\s+`)
	breaks = regexp.MustCompile(`(?i)</(p|h[1-6]|li|blockquote|pre)>|<br\s*/?>`)
	blanks = regexp.MustCompile(`\n[ \t]*\n[\s]*`)
)

// paragraphs returns the text of an article's Markdown body, a paragraph
// per line, for posts that take plain text.
func paragraphs(src string) string {
	text := breaks.ReplaceAllString(markdownHTML(src), "\n")
	text = html.UnescapeString(tags.ReplaceAllString(text, ""))
	return strings.TrimSpace(blanks.ReplaceAllString(text, "\n"))
}

// paragraphsHTML is text with a <p> per line, to paste into an editor
// that would take a typed newline for a submit or ignore it.
func paragraphsHTML(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("<p>" + html.EscapeString(line) + "</p>")
	}
	return b.String()
}

// plainText returns the text of an article's Markdown body, on one line.
func plainText(src string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(tags.ReplaceAllString(markdownHTML(src), " ")), " "))
}

// hashtags joins tags as "#a #b ".
func hashtags(tags []string) string {
	var b strings.Builder
	for _, t := range tags {
		b.WriteString("#" + t + " ")
	}
	return b.String()
}
