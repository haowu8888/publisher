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
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
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
	browser.Show(page)
	page = page.Context(ctx)
	// A user may minimize the browser while the task runs; see Restore.
	go func() {
		for sleep(ctx, 3*time.Second) == nil {
			browser.Restore(page)
		}
	}()
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

// openFresh is openForm for an editor that fills itself with the draft it
// keeps in the browser's storage, as what an earlier task left there: it
// first drops the localStorage keys matching the regular expression stale,
// on blank, a light page of the site's that is not the editor, which could
// save the draft again.
func openFresh(ctx context.Context, b *browser.Browser, blank, url, stale string, j Job) (*rod.Page, error) {
	page, err := openForm(ctx, b, blank, j)
	if err != nil {
		return nil, err
	}
	host := strings.Split(strings.SplitN(blank, "://", 2)[1], "/")[0]
	// A new tab may still be on its way from about:blank.
	for try := 1; ; try++ {
		_, err := page.Timeout(evalTimeout).Eval(`(host, stale) => {
			if (location.host !== host) throw new Error("not there yet");
			const re = new RegExp(stale);
			Object.keys(localStorage).filter((k) => re.test(k)).forEach((k) => localStorage.removeItem(k));
		}`, host, stale)
		if err == nil {
			break
		}
		if try == 5 {
			return nil, fmt.Errorf("清除编辑器的本地草稿失败: %w", err)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	// Navigate returns before the page goes; WaitLoad could still see the
	// blank page.
	loaded := page.WaitNavigation(proto.PageLifecycleEventNameLoad)
	if err := page.Navigate(url); err != nil {
		return nil, err
	}
	loaded()
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
			// Not to fail later waits on it, as for it to show, at once.
			return el.Sleeper(rod.DefaultSleeper), nil
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

// mouseClick is clickVisible with a real mouse click, for buttons that a
// script's click() does nothing to, such as Dayu's, which listen for
// mousedown.
func mouseClick(ctx context.Context, page *rod.Page, sel, text string, wait time.Duration) error {
	for deadline := time.Now().Add(wait); ; {
		el, err := page.Sleeper(rod.NotFoundSleeper).Timeout(evalTimeout).ElementByJS(rod.Eval(`(sel, text) => {
			const re = new RegExp(text);
			return [...document.querySelectorAll(sel)].find((e) => e.getClientRects().length && re.test(e.textContent.trim())) ?? null;
		}`, sel, text))
		if err == nil {
			return realClick(page, el.CancelTimeout())
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("未找到「%s」按钮", strings.Trim(text, "^$"))
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// realClick clicks el with the mouse, as a user would. Its window is shown
// first, a real click waiting for animation frames, as focus says, and it
// is scrolled to the middle: at an edge a fixed bar (知乎's 发布设置) may
// cover it. Either way rod's Click would wait for ever, so it has
// evalTimeout.
func realClick(page *rod.Page, el *rod.Element) error {
	browser.Show(page)
	el = el.Sleeper(rod.DefaultSleeper).Timeout(evalTimeout)
	if _, err := el.Eval(`() => this.scrollIntoView({ block: "center" })`); err != nil {
		return err
	}
	return el.Click(proto.InputMouseButtonLeft, 1)
}

// clickUntilGone clicks the visible element matching sel and text every
// interval until there is none, failing after wait. It is for buttons a
// page ignores at first with no sign of when it will take them, such as a
// cropper's 确定 before it has drawn the picture, and that go away once
// taken.
func clickUntilGone(ctx context.Context, page *rod.Page, sel, text string, interval, wait time.Duration) error {
	for deadline := time.Now().Add(wait); ; {
		if err := sleep(ctx, interval); err != nil {
			return err
		}
		if clickVisible(ctx, page, sel, text, 0) != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("点「%s」没有反应", strings.Trim(text, "^$"))
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

// clearMarker deletes the marker paragraph reading mark from the editor
// sel, as a user would, leaving the cursor where it was: for editors that
// keep their own selection, which a script's does not change. Its line is
// clicked and selected with End and Shift+Home, and deleted only when the
// selection is the marker: a picture above still loading moves the text,
// and the click can land on a long paragraph's line instead.
func clearMarker(ctx context.Context, page *rod.Page, sel, mark string) error {
	for try := 0; ; try++ {
		// Pictures still loading change the layout.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			res, err := page.Timeout(evalTimeout).Eval(`(sel) => [...document.querySelectorAll(sel + " img")].every((i) => i.complete)`, sel)
			if err == nil && res.Value.Bool() {
				break
			}
			if err := sleep(ctx, 500*time.Millisecond); err != nil {
				return err
			}
		}
		el, err := findR(page, sel+` span[data-text]`, regexp.QuoteMeta(mark), 10*time.Second)
		if err != nil {
			return fmt.Errorf("正文里找不到%s", mark)
		}
		if err := realClick(page, el); err != nil {
			if try == 2 {
				return fmt.Errorf("点不到正文里的%s: %w", mark, err)
			}
			continue
		}
		if err := page.Keyboard.Type(input.End); err != nil {
			return err
		}
		if err := page.KeyActions().Press(input.ShiftLeft).Type(input.Home).Release(input.ShiftLeft).Do(); err != nil {
			return err
		}
		res, err := page.Timeout(evalTimeout).Eval(`() => getSelection().toString().trim()`)
		if err != nil {
			return err
		}
		if res.Value.Str() == mark {
			return page.Keyboard.Type(input.Backspace)
		}
		if try == 2 {
			return fmt.Errorf("选不中正文里的%s", mark)
		}
		if err := sleep(ctx, time.Second); err != nil {
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

// focus focuses el by script. rod's Focus first waits for el to keep
// still over a few animation frames, which a page in a minimized window
// never runs, so a task would wait for ever once the user minimized the
// browser.
func focus(el *rod.Element) error {
	_, err := el.Eval(`() => this.focus()`)
	return err
}

// typeInto appends text at the end of a contenteditable editor, as typing
// would.
func typeInto(page *rod.Page, editor *rod.Element, text string) error {
	if err := focus(editor); err != nil {
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
	if err := focus(editor); err != nil {
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
// gaining one of urlMarks (one ending in $ must end its path), or a text
// matching the regular expression text.
// It gives up early when the page starts saying the login is gone.
func waitPublished(ctx context.Context, page *rod.Page, urlMarks []string, text string) error {
	return waitPublishedFor(ctx, page, urlMarks, text, 90*time.Second)
}

// waitPublishedFor is waitPublished, waiting no longer than wait.
func waitPublishedFor(ctx context.Context, page *rod.Page, urlMarks []string, text string, wait time.Duration) error {
	re := regexp.MustCompile(text)
	startNotes(page)
	said := false
	// The success text may be on the page all along, as in Toutiao's hint
	// 发布成功后，可额外获得图文创作收益, so only more of it counts.
	seen := 0
	if res, err := page.Timeout(evalTimeout).Eval(bodyText); err == nil {
		said = loginLost.MatchString(res.Value.Str())
		seen = len(re.FindAllString(res.Value.Str(), -1))
	}
	for deadline := time.Now().Add(wait); ; {
		if info, err := page.Timeout(evalTimeout).Info(); err == nil {
			path, _, _ := strings.Cut(info.URL, "?")
			path, _, _ = strings.Cut(path, "#")
			for _, m := range urlMarks {
				if end, ok := strings.CutSuffix(m, "$"); ok && strings.HasSuffix(path, end) || !ok && strings.Contains(info.URL, m) {
					return nil
				}
			}
		}
		// A success dialog may come and go between two looks, as Sohu's
		// does before it shows the video list.
		if res, err := page.Timeout(evalTimeout).Eval(`() => (window.__pubNotes?.notes || []).join("\n")`); err == nil && re.MatchString(res.Value.Str()) {
			return nil
		}
		if res, err := page.Timeout(evalTimeout).Eval(bodyText); err == nil {
			body := res.Value.Str()
			if len(re.FindAllString(body, -1)) > seen {
				return nil
			}
			if !said && loginLost.MatchString(body) {
				return errors.New("平台提示登录失效，没有发出去：请在「账号」页点「重新登录」扫码后重试")
			}
		}
		if time.Now().After(deadline) {
			if said := newNotes(page); said != "" {
				return fmt.Errorf("提交后未看到成功提示，页面提示过：%s", said)
			}
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

// bodyText is a script returning the text a page shows, with that of
// WeChat Channels' form, which is in a shadow root (see chRoot).
const bodyText = `() => document.body.innerText + "\n" + (` + chRoot + `?.querySelector("body")?.innerText ?? "")`

// noteSel finds the elements that hold a page's messages and dialogs.
const noteSel = `[class*="toast"], [class*="message"], [class*="Message"], [class*="notice"], [class*="dialog"], [class*="modal"], [class*="pop"], [class*="tip"], [class*="error"], [class*="alert"], [role="alert"], [role="dialog"]`

// watchNotes starts recording, in window.__pubNotes, the messages and
// dialogs a page shows from now on that were not on it already. A message
// such as 「请选择信息来源」 is gone in seconds, long before a submit is
// given up on, while the page's fixed hints stay: those would be all that
// pageNotes found then. One started already, before a submit's click,
// keeps going.
const watchNotes = `() => {
	if (window.__pubNotes) return;
	const norm = (t) => (t || "").trim().replace(/\s+/g, " ");
	const roots = [document, ` + chRoot + `].filter(Boolean);
	const base = norm((` + bodyText + `)());
	const notes = [];
	let queued = false;
	const grab = () => {
		queued = false;
		for (const r of roots) for (const e of r.querySelectorAll(` + "`" + noteSel + "`" + `)) {
			if (!e.getClientRects().length) continue;
			const t = norm(e.innerText);
			if (t && t.length < 200 && !base.includes(t) && !notes.includes(t) && notes.length < 50) notes.push(t);
		}
	};
	const ob = new MutationObserver(() => {
		if (!queued) { queued = true; setTimeout(grab, 100); }
	});
	for (const r of roots) ob.observe(r, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ["class", "style"] });
	window.__pubNotes = { ob, notes };
}`

// startNotes runs watchNotes. waitPublished does, but a submit that may
// first be refused, or asks to confirm before waitPublished, should call it
// before its click.
func startNotes(page *rod.Page) { page.Timeout(evalTimeout).Eval(watchNotes) }

// newNotes returns what watchNotes recorded, but for texts that are part
// of a longer one and for Dayu's notice, which comes and goes on its own,
// that the browser is zoomed (by the display scaling).
func newNotes(page *rod.Page) string {
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const all = window.__pubNotes?.notes || [];
		return all.filter((t) => !all.some((o) => o !== t && o.includes(t)) && !/浏览器处于放大状态/.test(t)).slice(0, 4).join("；");
	}`)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// pageNotes returns the text of the messages and dialogs a page shows, as
// what it said to a submit that did not go through.
func pageNotes(page *rod.Page) string {
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const sel = '[class*="toast"], [class*="message"], [class*="Message"], [class*="dialog"], [class*="modal"], [class*="tips"], [role="alert"], [role="dialog"]';
		const roots = [document, ` + chRoot + `].filter(Boolean);
		const texts = roots.flatMap((r) => [...r.querySelectorAll(sel)])
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

// md keeps each line of a paragraph on its own line, as the author's
// editor shows it: lines such as `\- 估值：…`, list items escaped, would
// otherwise run together.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRendererOptions(gmhtml.WithHardWraps()))

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
		data, e := dataURL(locateImage(src, dirs))
		if e != nil {
			err = fmt.Errorf("正文图片读不了: %w", e)
			return tag
		}
		return m[1] + data + m[3]
	})
	return out, err
}

// dataURL reads the picture at path into a data URL.
func dataURL(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if typ == "" {
		typ = "image/png"
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(b), nil
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

// markImages replaces each paragraph of an article's HTML holding a local
// picture with a marker paragraph, 〔图N〕, for an editor that takes
// pictures only through its own upload, and returns the pictures' files,
// resolved against dirs, in order. Pictures on the web stay.
func markImages(content string, dirs []string) (string, []string, error) {
	var paths []string
	marked := imgPara.ReplaceAllStringFunc(content, func(p string) string {
		src := imgSrc.FindStringSubmatch(p)
		if src == nil || strings.Contains(src[2], "://") || strings.HasPrefix(src[2], "data:") {
			return p
		}
		s := html.UnescapeString(src[2])
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
		paths = append(paths, locateImage(filepath.FromSlash(s), dirs))
		return fmt.Sprintf("<p>〔图%d〕</p>", len(paths))
	})
	for _, p := range paths {
		if !fileExists(p) {
			return "", nil, fmt.Errorf("正文图片读不了: %s", p)
		}
	}
	return marked, paths, nil
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

// hasPicture reports whether an article shows a picture: its cover, or one
// in its body.
func hasPicture(m store.Material) bool {
	return m.CoverPath != "" || len(bodyImages(m)) > 0 || strings.Contains(markdownHTML(articleBody(m)), "<img")
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

// halfLen is a text's length as the platforms that count two ASCII
// characters as one Chinese character see it (Mango TV, NetEase, Sohu,
// Yidian): "AI" is 1, "LangChain" 4.5, so "Anthropic" is too short a title
// where 5 characters are the least.
func halfLen(t string) float64 {
	n := 0.0
	for _, r := range t {
		if r < utf8.RuneSelf {
			n += 0.5
		} else {
			n++
		}
	}
	return n
}
