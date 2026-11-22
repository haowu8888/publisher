package platforms

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// publishNetease publishes on NetEase's creator platform (网易号): a video, or
// an article. Both forms are React ones of the same parts: fields
// (.ne-easy-form-field) named by their label, dialogs (.ne-modal). The
// selectors, as of November 2026, were read from the page's code while the
// account was under review (今日已发布0/0) and checked on the filled forms
// once it passed; a publish itself is yet to be tried.
func publishNetease(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return neArticle(ctx, b, j)
	}
	return neVideo(ctx, b, j)
}

const neHome = "https://mp.163.com/subscribe_v4/index.html#/"

// neKeep is in the forms' URLs; a visitor not logged in goes to
// login.html instead.
const neKeep = "mp.163.com/subscribe_v4"

// neTitleMin is the shortest title NetEase takes.
const neTitleMin = 5

// neVideoStatements are NetEase's statements (声明) on videos, by the
// material's statements, which are in Bilibili's words. A material without
// one gets 无.
var neVideoStatements = map[string]string{
	"内容无需标注":  "无",
	"含AI生成内容": "内容由AI生成",
	"含虚构演绎内容": "虚构演绎，仅供娱乐",
}

// neCategory is the category (分类) a video goes in when NetEase suggests
// one under it; it suggests from the title and the account's field.
const neCategory = "科技"

// neTagMin and neTagMax are how many tags a video needs; each is 10
// characters at most.
const (
	neTagMin = 3
	neTagMax = 5
	neTagLen = 10
)

// checkNetease refuses, before the form is filled, a material to submit by
// itself whose title NetEase would turn down as too short. A review leaves
// that to the user.
func checkNetease(m store.Material, mode store.Mode) error {
	if mode != store.ModeAuto {
		return nil
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(m.Title)); n < neTitleMin {
		return fmt.Errorf("网易号标题至少 %d 个字，「%s」只有 %d 个字", neTitleMin, m.Title, n)
	}
	return nil
}

// neField is a script finding the form field labelled label.
const neField = `(label) => [...document.querySelectorAll(".ne-easy-form-field")]
	.find((f) => f.querySelector(".ne-easy-form-field-label-container")?.innerText.trim() === label)`

var neCount = regexp.MustCompile(`今日已发布\s*(\d+)\s*/\s*(\d+)`)

// neQuota fails when the day's publishes are used up, as the form says in
// sel (今日已发布 n/max). An account under review has none (0/0).
func neQuota(ctx context.Context, page *rod.Page, sel string) error {
	var text string
	for deadline := time.Now().Add(10 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(sel) => document.querySelector(sel)?.textContent ?? ""`, sel)
		if err == nil {
			text = res.Value.Str()
			if neCount.MatchString(text) {
				break
			}
		}
		if time.Now().After(deadline) {
			// Not knowing is no reason not to try.
			return nil
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	m := neCount.FindStringSubmatch(text)
	done, _ := strconv.Atoi(m[1])
	most, _ := strconv.Atoi(m[2])
	switch {
	case most == 0:
		return errors.New("网易号今日可发布 0 条：账号还在审核中，或已被限制发布")
	case done >= most:
		return fmt.Errorf("网易号今日已发布 %d/%d，次数用完了，明天再发", done, most)
	}
	return nil
}

// neVideo uploads a video. The form shows once the file is chosen, with
// the file's name as title, and is filled while the video uploads. It has
// no description; it wants a cover, a category and 3 to 5 tags.
func neVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, neHome+"video-publish", j)
	if err != nil {
		return err
	}
	const fileSel = `input[type=file][accept*="video/mp4"]`
	if _, err := waitElement(ctx, page, fileSel, neKeep); err != nil {
		return err
	}
	if err := neQuota(ctx, page, ".postFooter-publishNum"); err != nil {
		return err
	}
	const titleSel = `.postForm input[placeholder*="个字"]`
	if err := startUpload(ctx, page, fileSel, neKeep, m.VideoPath, `.videoUploadItem`); err != nil {
		return err
	}
	dismissGuides(page)

	j.Report("填写标题")
	title, err := find(page, titleSel, 30*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, neteaseLimits.Title)); err != nil {
		return err
	}
	// The title is checked, and categories suggested from it, on blur.
	if _, err := title.Eval(`() => this.blur()`); err != nil {
		return err
	}

	statement := m.Statement
	if statement == "" {
		statement = "内容无需标注"
	}
	if s, ok := neVideoStatements[statement]; ok {
		statement = s
	}
	if err := neSelect(ctx, page, "声明", statement); err != nil {
		return err
	}

	var notes []string
	j.Report("选择分类")
	if err := neVideoCategory(ctx, page); err != nil {
		if j.Mode == store.ModeAuto {
			return err
		}
		notes = append(notes, "分类请手动选择")
	}
	j.Report("填写标签")
	if n, err := neVideoTags(ctx, page, m.Tags); err != nil {
		return err
	} else if n < neTagMin {
		if j.Mode == store.ModeAuto {
			return fmt.Errorf("网易号视频要 %d–%d 个标签，只填上了 %d 个", neTagMin, neTagMax, n)
		}
		notes = append(notes, fmt.Sprintf("标签要 %d–%d 个，请补上", neTagMin, neTagMax))
	}

	if err := neUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, neVideoCover)
	if err != nil {
		return err
	}
	if m.CoverPath == "" || note != "" {
		// The cover is required: a frame NetEase suggests, then.
		if err := neVideoFrame(ctx, page); err != nil {
			if j.Mode == store.ModeAuto {
				return err
			}
			notes = append(notes, "封面请手动选择")
		}
	}
	if len(notes) > 0 {
		note += "（" + strings.Join(notes, "；") + "）"
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return neSubmit(ctx, page, j, note)
}

// neSelect picks choice in the dropdown of the field labelled label: its
// box opens a list, shown apart from the field, of which the option
// reading choice is clicked.
func neSelect(ctx context.Context, page *rod.Page, label, choice string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(label) => {
		const f = (`+neField+`)(label);
		const box = f?.querySelector("[class*=select], input, [role=button]");
		box?.click();
		return !!box;
	}`, label)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return fmt.Errorf("未找到「%s」", label)
	}
	return nePickOption(ctx, page, label, choice)
}

// nePickOption clicks the shown option reading choice of an open list.
func nePickOption(ctx context.Context, page *rod.Page, what, choice string) error {
	for deadline := time.Now().Add(5 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(choice) => {
			const opts = [...document.querySelectorAll("[class*=option], [class*=Option], [role=option], li")]
				.filter((o) => o.getClientRects().length && o.textContent.trim() === choice);
			const opt = opts.pop();
			opt?.click();
			return !!opt;
		}`, choice)
		if err == nil && res.Value.Bool() {
			return sleep(ctx, 500*time.Millisecond)
		}
		if time.Now().After(deadline) {
			page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
			return fmt.Errorf("「%s」里没有「%s」", what, choice)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// neVideoCategory picks a category NetEase suggests (推荐) under the
// category box, one under neCategory if there is: materials have none,
// and the suggestions, from the title, come after it is filled.
func neVideoCategory(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(want) => {
			const tags = [...document.querySelectorAll(".customCategory .s-recommend .customtag")];
			if (tags.some((t) => t.classList.contains("selected"))) return true;
			const tag = tags.find((t) => t.textContent.trim().startsWith(want)) ?? tags[0];
			tag?.click();
			return false;
		}`, neCategory)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("网易号没有给出推荐分类，请在浏览器里选好分类再发布")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// neVideoTags adds tags: the 添加标签 box takes one typed, ended with
// Enter (a typed space or comma would end it too, so it is not typed key
// by key), and stays open for the next. Those over 10 characters, which
// NetEase cuts short, are left out; NetEase's suggestions make up 3. It
// returns how many there are.
func neVideoTags(ctx context.Context, page *rod.Page, tags []string) (int, error) {
	count := func() int {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelectorAll(".customTag .custom-taginput-tag").length`)
		if err != nil {
			return 0
		}
		return res.Value.Int()
	}
	for _, t := range tags {
		if count() >= neTagMax {
			break
		}
		t = strings.Join(strings.Fields(t), "")
		if t == "" || utf8.RuneCountInString(t) > neTagLen {
			continue
		}
		// The box stays open after a tag; a click on it then closes it.
		if _, err := page.Timeout(evalTimeout).Eval(`() => { document.querySelector(".customTag input") || document.querySelector(".customTag .custom-taginput-addTag")?.click(); }`); err != nil {
			return 0, err
		}
		in, err := find(page, `.customTag input`, 3*time.Second)
		if err != nil {
			return 0, errors.New("未找到标签输入框")
		}
		if err := focus(in); err != nil {
			return 0, err
		}
		if err := page.InsertText(t); err != nil {
			return 0, err
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return 0, err
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return 0, err
		}
	}
	// Suggestions come from the title and the category.
	for deadline := time.Now().Add(10 * time.Second); count() < neTagMin && time.Now().Before(deadline); {
		page.Timeout(evalTimeout).Eval(`() => document.querySelector(".customTag .s-recommend .customtag:not(.selected)")?.click()`)
		if err := sleep(ctx, time.Second); err != nil {
			return 0, err
		}
	}
	return count(), nil
}

// neUploaded waits until the video is uploaded: its item in the list says
// 上传完成, or fails.
func neUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const item = document.querySelector(".videoUploadItem.selected") ?? document.querySelector(".videoUploadItem");
			return {
				done: !!item?.querySelector(".videoUploadItem-progressInfo-success"),
				failed: item?.querySelector(".videoUploadItem-progressInfo-fail")?.textContent.trim() ?? "",
				progress: item?.querySelector(".s-loaded-total")?.textContent.trim() ?? "",
			};
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		v := res.Value
		if f := v.Get("failed").Str(); f != "" {
			return fmt.Errorf("视频上传失败：%s", f)
		}
		if v.Get("done").Bool() {
			return nil
		}
		j.Report(fmt.Sprintf("上传视频中 %s（已用 %s）", v.Get("progress").Str(), time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// neVideoCover sets the video's cover: 上传图片 (or 换图) opens the cover
// dialog, whose 本地上传 tab takes the picture, at least 960x540 wide or
// 720x960 tall as the video is; 确认 then uploads it, and the form shows it.
func neVideoCover(ctx context.Context, page *rod.Page, path string) error {
	const modal = `.video-cover-modal-new`
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		window.publisherOldCover = document.querySelector(".videoPublishNew-cover-preview img")?.src ?? "";
		const open = document.querySelector(".videoPublishNew-cover-upload, .videoPublishNew-cover-preview .s-change");
		if (!open) throw new Error("未找到「上传图片」");
		open.click();
	}`); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, modal+` .ne-tab`, `^本地上传$`, 10*time.Second); err != nil {
		return errors.New("封面对话框没出来")
	}
	in, err := find(page, modal+` input[type=file]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(modal) => ({
			shown: !!document.querySelector(modal + " img[alt='图片']"),
			error: [...document.querySelectorAll(".ne-modal")].some((m) => m.getClientRects().length && m.textContent.includes("尺寸不符合要求")),
		})`, modal)
		if err == nil && res.Value.Get("error").Bool() {
			page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll(".ne-modal button")].find((b) => b.getClientRects().length && /^(确认|确定|知道了)$/.test(b.textContent.trim()))?.click()`)
			page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
			return errors.New("封面图片尺寸太小")
		}
		if err == nil && res.Value.Get("shown").Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("封面图片没有载入")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if err := clickUntilGone(ctx, page, modal+` footer button`, `^确认$`, time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面对话框%w", err)
	}
	return neCoverShown(ctx, page)
}

// neCoverShown waits until the form shows a new cover from NetEase's
// servers.
func neCoverShown(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const src = document.querySelector(".videoPublishNew-cover-preview img")?.src ?? "";
			return src.startsWith("http") && src !== window.publisherOldCover;
		}`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面没有更新")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// neVideoFrame takes the first frame NetEase suggests (推荐封面) as cover,
// unless the form shows one already. The suggestions come once the video
// is looked at on NetEase's side.
func neVideoFrame(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			if (document.querySelector(".videoPublishNew-cover-preview img")?.src?.startsWith("http")) return true;
			document.querySelector(".videoPublishNew-cover-frag li img[src^='http']")?.click();
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("网易号视频必须有封面：素材没有封面，平台也没给出推荐封面")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// neSubmit clicks 发布 and waits for the publish to show.
func neSubmit(ctx context.Context, page *rod.Page, j Job, note string) error {
	j.Report("提交发布")
	// As on Baijiahao, the form now and then ignores 发布 right after it is
	// filled, saying nothing (seen with an article, its pictures just
	// stored); a later click publishes. One that went through leaves the
	// form, so a click is never repeated after the page has answered.
	for try := 1; ; try++ {
		if err := clickVisible(ctx, page, "button.ne-button-color-primary", "^发布$", 10*time.Second); err != nil {
			return err
		}
		err := waitPublishedFor(ctx, page, []string{"content-manage"}, "发布成功", 30*time.Second)
		if err == nil {
			break
		}
		if try == 3 || ctx.Err() != nil || newNotes(page) != "" {
			return err
		}
	}
	return finish(page, j, "发布成功"+note)
}

// neHeading is a heading other than h5, the only one the editor has.
var neHeading = regexp.MustCompile(`(?i)<(/?)h[1-46]>`)

// neArticle writes an article. The title is a textarea and the body a
// Draft.js editor; its pictures go in through the editor's picture dialog,
// which uploads them. The cover is picked from the body's pictures, so
// the material's cover goes first in the body when it is not there.
func neArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, neHome+"article-publish", j)
	if err != nil {
		return err
	}
	const titleSel = `textarea.netease-textarea`
	title, err := waitElement(ctx, page, titleSel, neKeep)
	if err != nil {
		return err
	}
	dismissGuides(page)
	if err := neQuota(ctx, page, ".post-footer__container-counter"); err != nil {
		return err
	}

	j.Report("填写标题")
	if err := fill(page, title, fitTitle(m.Title, neteaseLimits.Title)); err != nil {
		return err
	}

	j.Report("填写正文")
	content := neHeading.ReplaceAllString(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), "<${1}h5>")
	if m.CoverPath != "" && !strings.Contains(content, html.EscapeString(m.CoverPath)) {
		content = `<p><img src="` + html.EscapeString(m.CoverPath) + `"></p>` + content
	}
	editor, err := find(page, `.public-DraftEditor-content`, 10*time.Second)
	if err != nil {
		return errors.New("未找到正文编辑器")
	}
	pics, err := neBody(ctx, page, j, editor, content, plainText(articleBody(m)), articleDirs(m))
	if err != nil {
		return err
	}

	var note string
	if pics > 0 {
		j.Report("设置封面（正文第一张图）")
		if err := neArticleCover(ctx, page); err != nil {
			if j.Mode == store.ModeAuto {
				return fmt.Errorf("设置封面失败: %w", err)
			}
			note = "（封面没设上，请手动选择：" + err.Error() + "）"
		}
	} else if err := neCoverType(page, "auto"); err != nil {
		return err
	}
	if s, ok := neVideoStatements[m.Statement]; ok && s != "无" {
		if err := neArticleStatement(ctx, page, s); err != nil {
			return err
		}
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return neSubmit(ctx, page, j, note)
}

// neBody pastes the article's HTML into the editor, each local picture
// first a marker paragraph. Each marker is then selected as a user would,
// with a click and keys, since the editor keeps its own selection, and
// deleted; the picture dialog then puts the picture where it was. It
// returns how many pictures the body has.
func neBody(ctx context.Context, page *rod.Page, j Job, editor *rod.Element, content, plain string, dirs []string) (int, error) {
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
			return 0, fmt.Errorf("正文图片读不了: %s", p)
		}
	}
	if err := pasteHTML(editor, marked, plain); err != nil {
		return 0, err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length`); err != nil || res.Value.Int() == 0 {
		return 0, errors.New("正文没填上")
	}
	for i, p := range paths {
		i++
		j.Report(fmt.Sprintf("上传正文图片 %d/%d", i, len(paths)))
		mark := fmt.Sprintf("〔图%d〕", i)
		if err := clearMarker(ctx, page, `.public-DraftEditor-content`, mark); err != nil {
			return 0, err
		}
		if err := neInsertPicture(ctx, page, p); err != nil {
			return 0, fmt.Errorf("正文第 %d 张图片%w", i, err)
		}
	}
	res, err := editor.Eval(`() => this.querySelectorAll("img").length`)
	if err != nil {
		return 0, err
	}
	return res.Value.Int(), nil
}

// neInsertPicture puts a picture at the editor's cursor: the toolbar's
// picture button opens a dialog whose file input uploads it, and whose 确定
// is enabled once it has.
func neInsertPicture(ctx context.Context, page *rod.Page, path string) error {
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const before = document.querySelectorAll(".public-DraftEditor-content img").length;
		const btn = [...document.querySelectorAll(".rich-editor-panel-item")].find((b) => b.querySelector("img[src*='icon_image.']"));
		if (!btn) throw new Error("未找到插入图片按钮");
		btn.click();
		return before;
	}`)
	if err != nil {
		return err
	}
	before := res.Value.Int()
	const modal = `.ne-modal.picture-adder`
	in, err := find(page, modal+` input[type=file]`, 10*time.Second)
	if err != nil {
		return errors.New("插入图片的对话框没出来")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(modal) => {
			const ok = document.querySelector(modal + " .s-confirm-btn");
			if (ok && !ok.disabled) { ok.click(); return true; }
			return false;
		}`, modal)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			page.Timeout(evalTimeout).Eval(`(modal) => document.querySelector(modal + " .ne-modal-close")?.click()`, modal)
			return errors.New("没有上传成功")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := waitGone(ctx, page, modal, 10*time.Second); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelectorAll(".public-DraftEditor-content img").length`)
		if err == nil && res.Value.Int() > before {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("没有插进正文")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// neCoverType picks the cover kind: threeImg (三图, the default), custom
// (单图), bigImg (大图) or auto (自动, from the body's pictures).
func neCoverType(page *rod.Page, kind string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(kind) => {
		const radio = document.querySelector(".cover-pic__cover input.ne-switch-base-input[value='" + kind + "']");
		radio?.click();
		return !!radio;
	}`, kind)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return errors.New("未找到封面选项，发布页可能已改版")
	}
	return nil
}

// neArticleCover sets a single cover (单图) from the body's first picture:
// its box opens 选择封面图, a list of the body's pictures, of which those
// too small are disabled; 确认 takes the one clicked. A cropper that may
// follow is confirmed as it is.
func neArticleCover(ctx context.Context, page *rod.Page) error {
	if err := neCoverType(page, "custom"); err != nil {
		return err
	}
	if err := sleep(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		const box = document.querySelector(".cover-pic__single__content__choose");
		if (!box) throw new Error("未找到单图封面框");
		box.click();
	}`); err != nil {
		return err
	}
	const modal = `.cover-picture`
	if err := clickVisible(ctx, page, modal+` .cover-picture__item:not(.disabled)`, ``, 15*time.Second); err != nil {
		return errors.New("选封面的对话框没出来，或正文图片都太小（单图要大于 336x252）")
	}
	if err := clickUntilGone(ctx, page, modal+` .cover-picture__footer div`, `^确认$`, time.Second, 10*time.Second); err != nil {
		return fmt.Errorf("选封面的对话框%w", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			if (document.querySelector(".cover-pic__single__content__choose img")) return true;
			[...document.querySelectorAll(".ne-modal button")]
				.find((b) => b.getClientRects().length && /^(确定|确认|完成|保存)$/.test(b.textContent.trim()))?.click();
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面没有更新")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// neArticleStatement turns on the article's statement (声明), whose
// dropdown shows once it is on, and picks statement.
func neArticleStatement(ctx context.Context, page *rod.Page, statement string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(label) => {
		const sw = (`+neField+`)(label)?.querySelector("button.custom-switcher");
		if (sw && sw.value !== "true") sw.click();
		return !!sw;
	}`, "声明")
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return errors.New("未找到「声明」")
	}
	if err := sleep(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	return neSelect(ctx, page, "声明", statement)
}
