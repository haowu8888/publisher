package platforms

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// publishYidian publishes on Yidian's creator platform (一点号): a video
// (发视频, for accounts let to), or an article. The pages are Vue ones whose
// templates are in the page's code, which the selectors, as of November
// 2026, were read from while the account was under review (the editors
// then show but do not work); both have since been published with.
func publishYidian(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return ydArticle(ctx, b, j)
	}
	return ydVideo(ctx, b, j)
}

const ydHome = "https://mp.yidianzixun.com/"

// ydTitleMin is the shortest title Yidian takes.
const ydTitleMin = 5

// ydStatements are Yidian's statements (内容声明), by the material's
// statements, which are in Bilibili's words. A material without one gets
// 无需声明.
var ydStatements = map[string]string{
	"内容无需标注":  "无需声明",
	"含AI生成内容": "内容由AI生成",
	"含虚构演绎内容": "虚构情节内容",
}

// ydCategory is the category (分类) videos go in, by its levels, which
// materials do not have: the user's videos are on AI. The cascader takes
// only a last level.
var ydCategory = []string{"科技", "互联网"}

// ydTagMax is how many tags a video takes, each 8 characters at most.
const (
	ydTagMax = 8
	ydTagLen = 8
)

// checkYidian refuses, before the form is filled, a material to submit by
// itself whose title Yidian would turn down as too short. A review leaves
// that to the user.
func checkYidian(m store.Material, mode store.Mode) error {
	if mode != store.ModeAuto {
		return nil
	}
	if n := halfLen(strings.TrimSpace(m.Title)); n < ydTitleMin {
		return fmt.Errorf("一点号标题至少 %d 个字（两个英文字母算一个字），「%s」只有 %g 个字", ydTitleMin, m.Title, n)
	}
	return nil
}

// ydOpen opens an editor of the creator platform. The editors keep what
// was typed in localStorage (editor_<editor>) and fill themselves with it,
// so that is dropped first, on the site's favicon, which is light. A
// visitor not logged in stays on the URL, shown the sign-up page.
func ydOpen(ctx context.Context, b *browser.Browser, editor, sel string, j Job) (*rod.Page, *rod.Element, error) {
	page, err := openFresh(ctx, b, ydHome+"favicon.ico", ydHome+"#/Writing/"+editor, `^editor_`, j)
	if err != nil {
		return nil, nil, err
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => window.hasUID === false || /注册一点号/.test(document.body.innerText)`)
		if err == nil && res.Value.Bool() {
			page.Close()
			return nil, nil, ErrNotLoggedIn
		}
		if el, err := page.Sleeper(rod.NotFoundSleeper).Element(sel); err == nil {
			return page, el.Sleeper(rod.DefaultSleeper), nil
		}
		if time.Now().After(deadline) {
			// The Home page shows in place of an editor the account may not
			// use, as 发视频 for one not let to.
			return nil, nil, fmt.Errorf("未找到 %s：账号可能还没有这项发布权限，或发布页已改版", sel)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return nil, nil, err
		}
	}
}

// ydStatement picks the statement (内容声明), a row of radio buttons.
func ydStatement(page *rod.Page, statement string) error {
	if statement == "" {
		statement = "内容无需标注"
	}
	if s, ok := ydStatements[statement]; ok {
		statement = s
	}
	res, err := page.Timeout(evalTimeout).Eval(`(want) => {
		const item = [...document.querySelectorAll(".content-statement-wrap .radio-container .item")]
			.find((i) => i.querySelector(".text")?.childNodes[0]?.textContent.trim() === want);
		if (item && !item.classList.contains("checked")) item.click();
		return !!item;
	}`, statement)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return fmt.Errorf("内容声明里没有「%s」", statement)
	}
	return nil
}

// ydVideo uploads a video. Its form shows once the file is chosen, and is
// filled while the video uploads.
func ydVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	const fileSel = `.video-editor-container .upload-before input.upload-input`
	page, _, err := ydOpen(ctx, b, "videoEditor", fileSel, j)
	if err != nil {
		return err
	}
	const form = `.video-edit-table`
	if err := startUpload(ctx, page, fileSel, "mp.yidianzixun.com", m.VideoPath, form); err != nil {
		return err
	}
	dismissGuides(page)

	j.Report("填写标题和简介")
	title, err := find(page, form+` .title input.text`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, yidianVideoLimits.Title)); err != nil {
		return err
	}
	if m.Description != "" {
		desc, err := find(page, form+` .summary textarea`, 10*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(m.Description, yidianVideoLimits.Text)); err != nil {
			return err
		}
	}
	var notes []string
	j.Report("选择分类")
	if err := ydCascade(ctx, page, form+` .cate .cascader-input`, ydCategory); err != nil {
		if j.Mode == store.ModeAuto {
			return err
		}
		notes = append(notes, "分类请手动选择："+err.Error())
	}
	if len(m.Tags) > 0 {
		j.Report("填写标签")
		if err := ydTags(ctx, page, form+` .tags input.tag-input`, m.Tags); err != nil {
			return err
		}
	}
	if err := ydStatement(page, m.Statement); err != nil {
		return err
	}

	if err := ydUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, ydVideoCover)
	if err != nil {
		return err
	}
	if len(notes) > 0 {
		note += "（" + strings.Join(notes, "；") + "）"
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return ydSubmit(ctx, page, j, `.publish-footer button.mp-btn-primary`, note)
}

// ydCascade picks a category by its levels in a cascader: its box opens a
// menu, whose items are named with their initial first (K - 科技), and
// pointing at an item opens the next level. A click takes the path pointed
// at, so each level is pointed at (mouseenter), and the last clicked; an
// item that has a next level takes no click.
func ydCascade(ctx context.Context, page *rod.Page, sel string, levels []string) error {
	box, err := find(page, sel, 10*time.Second)
	if err != nil {
		return errors.New("未找到分类选择框")
	}
	if err := jsClick(box); err != nil {
		return err
	}
	res, err := page.Timeout(evalTimeout).Eval(`async (levels) => {
		const wait = (ms) => new Promise((r) => setTimeout(r, ms));
		const lists = () => [...document.querySelectorAll(".cascader-menu-container ul.cascader-menu-list")].filter((u) => u.getClientRects().length);
		let item;
		for (const [i, name] of levels.entries()) {
			for (let t = 0; t < 10 && !lists()[i]; t++) await wait(300);
			const next = [...(lists()[i]?.children ?? [])].find((li) => li.textContent.trim().replace(/^[A-Z] - /, "") === name);
			if (!next) return "分类里没有「" + name + "」";
			item = next;
			item.dispatchEvent(new MouseEvent("mouseenter"));
			await wait(300);
		}
		item.click();
		return "";
	}`, levels)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
		return errors.New(msg)
	}
	if err := sleep(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	value, err := box.Eval(`() => this.value`)
	if err != nil {
		return err
	}
	if !strings.Contains(value.Value.Str(), levels[len(levels)-1]) {
		page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
		return fmt.Errorf("分类「%s」没选上", strings.Join(levels, "/"))
	}
	return nil
}

// ydTags types the tags into the tag box, each ended with Enter. Those
// over 8 characters, which the box cuts short, are left out.
func ydTags(ctx context.Context, page *rod.Page, sel string, tags []string) error {
	in, err := find(page, sel, 10*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	n := 0
	for _, t := range tags {
		if n == ydTagMax {
			break
		}
		t = strings.TrimSpace(t)
		if t == "" || utf8.RuneCountInString(t) > ydTagLen {
			continue
		}
		n++
		if err := focus(in); err != nil {
			return err
		}
		if err := page.InsertText(t); err != nil {
			return err
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return err
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// ydUploaded waits until the video is uploaded: the form then says 上传成功.
func ydUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const t = document.querySelector(".video-edit-table .uploading");
			return {
				done: !!t?.querySelector(".status.success"),
				failed: t?.querySelector(".status.failed")?.textContent.trim() || (t?.querySelector(".failed-container") ? "视频重复或不符合要求" : ""),
				progress: t?.querySelector(".status.upload")?.textContent.trim() ?? "",
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
		j.Report(fmt.Sprintf("%s（已用 %s）", strings.Replace(v.Get("progress").Str(), "上传", "上传视频中 ", 1), time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// ydVideoCover sets the video's cover: 上传封面 (or 更换) opens a dialog whose
// 本地上传 tab uploads the picture; it then shows in a cropper, cut to the
// video's shape, which 确定 takes.
func ydVideoCover(ctx context.Context, page *rod.Page, path string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		window.publisherOldCover = document.querySelector(".video-edit-table img.cover-img")?.src ?? "";
		const open = document.querySelector(".video-edit-table .cover-container")
			?? [...document.querySelectorAll(".video-edit-table .cover-image-operate .edit-btn")].find((b) => b.textContent.trim() === "更换");
		if (!open) throw new Error("未找到「上传封面」");
		open.click();
	}`); err != nil {
		return err
	}
	const dialog = `.upload-video-pic`
	page.Timeout(evalTimeout).Eval(`(d) => [...document.querySelectorAll(d + " .tab-banner-item")].find((t) => t.textContent.trim() === "本地上传")?.click()`, dialog)
	in, err := find(page, dialog+` .upload-box-before input.upload-input`, 10*time.Second)
	if err != nil {
		return errors.New("封面对话框没出来")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if _, err := find(page, dialog+` .upload-box-after .mp-crop-container img`, 30*time.Second); err != nil {
		return errors.New("封面图片没有上传成功")
	}
	if err := clickUntilGone(ctx, page, dialog+` .after-btns .confirm-btn`, `^确定$`, 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面对话框%w", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const src = document.querySelector(".video-edit-table img.cover-img")?.src ?? "";
			return !!src && src !== window.publisherOldCover;
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

// ydSubmit clicks the editor's 发布, enabled once all is uploaded, answers
// the 你确定要发布吗？ dialog it opens, and waits for the page saying
// 发布成功.
func ydSubmit(ctx context.Context, page *rod.Page, j Job, sel, note string) error {
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, sel+`:not(.mp-btn-disabled)`, "^发布$", 30*time.Second); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, `.dialog.showDialogBody .btn-container button.mp-btn-primary`, "^确定$", 10*time.Second); err != nil {
		return errors.New("没有弹出「你确定要发布吗？」，发布页可能已改版")
	}
	if err := waitPublished(ctx, page, []string{"successGuide"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// ydArticle writes an article. The body is a MediumEditor; pictures in it
// are uploaded first, through the page's own upload as its picture dialog
// does, and pasted from Yidian's servers, which the editor keeps as they
// are (it would fetch others from their URL, which a data URL is not). The
// cover is picked from the body's pictures, or uploaded beside them; a
// body without pictures takes the material's cover first.
func ydArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, title, err := ydOpen(ctx, b, "articleEditor", `input.post-title`, j)
	if err != nil {
		return err
	}
	dismissGuides(page)
	const edSel = `.editor-content[contenteditable=true]`
	editor, err := find(page, edSel, 30*time.Second)
	if err != nil {
		return errors.New("正文编辑器没有加载出来（账号还在审核中时是这样）")
	}

	j.Report("填写标题")
	if err := fill(page, title, fitTitle(m.Title, yidianArticleLimits.Title)); err != nil {
		return err
	}

	j.Report("填写正文")
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	coverApart := m.CoverPath != "" && strings.Contains(content, "<img")
	if m.CoverPath != "" && !coverApart {
		cover, err := inlineImages(`<p><img src="`+html.EscapeString(m.CoverPath)+`"></p>`, articleDirs(m)...)
		if err != nil {
			return err
		}
		content = cover + content
	}
	if strings.Contains(content, `src="data:`) {
		j.Report("上传正文图片")
		res, err := page.Timeout(2*time.Minute).Eval(ydUploadImages, content)
		if err != nil {
			return fmt.Errorf("上传正文图片失败: %w", err)
		}
		content = res.Value.Str()
	}
	if _, err := editor.Eval(`(html, plain) => {
		this.focus();
		const r = document.createRange();
		r.selectNodeContents(this);
		r.collapse(false);
		const s = getSelection();
		s.removeAllRanges();
		s.addRange(r);
		const dt = new DataTransfer();
		dt.setData("text/html", html);
		dt.setData("text/plain", plain);
		this.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
	}`, content, plainText(articleBody(m))); err != nil {
		return err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length`); err != nil || res.Value.Int() == 0 {
		return errors.New("正文没填上")
	}

	note := ""
	if strings.Contains(content, "<img") {
		j.Report("设置封面")
		cover := ""
		if coverApart {
			cover = m.CoverPath
		}
		if err := ydArticleCover(ctx, page, cover); err != nil {
			if j.Mode == store.ModeAuto {
				return fmt.Errorf("设置封面失败: %w", err)
			}
			note = "（封面没设上，请手动选择：" + err.Error() + "）"
		}
	} else if err := ydCoverType(page, "默认"); err != nil {
		return err
	}
	if err := ydStatement(page, m.Statement); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return ydSubmit(ctx, page, j, `.footer-operation button.mp-btn-primary`, note)
}

// ydUploadImages is a script uploading the data URL pictures of an
// article's HTML as the editor's picture dialog does, returning the HTML
// with them on Yidian's servers.
const ydUploadImages = `async (html) => {
	const doc = new DOMParser().parseFromString(html, "text/html");
	for (const img of doc.querySelectorAll("img[src^='data:']")) {
		const [head, data] = img.getAttribute("src").split(",");
		const type = head.slice(5).split(";")[0];
		const bytes = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
		const form = new FormData();
		form.append("upfile", new File([bytes], "image." + (type.split("/")[1] || "png"), { type }));
		const res = await fetch("/upload?action=uploadimage&picType=wemedia_cnt", { method: "POST", body: form, credentials: "include" });
		const body = await res.json().catch(() => ({}));
		if (!res.ok || !body.url) throw new Error("图片上传失败：" + (body.reason || body.message || res.status));
		img.setAttribute("src", body.url);
	}
	return doc.body.innerHTML;
}`

// ydCoverType picks the cover kind: 单图, 三图（仅在wifi下显示） or 默认 (the
// system picks from the body's pictures).
func ydCoverType(page *rod.Page, kind string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(kind) => {
		const item = [...document.querySelectorAll(".cover-type .radio-container .item")]
			.find((i) => i.querySelector(".text")?.textContent.trim().startsWith(kind));
		if (item && !item.classList.contains("checked")) item.click();
		return !!item;
	}`, kind)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return errors.New("未找到封面选项，发布页可能已改版")
	}
	return nil
}

// ydArticleCover sets a single cover (单图): the body's pictures show below
// the cover box, each with 设为封面图, beside an upload box adding one to
// them. The picture at path is uploaded there; without one, the body's
// first picture is taken. A wide picture is then shown in a cropper, which
// 裁切 takes; a tall one Yidian crops by itself.
func ydArticleCover(ctx context.Context, page *rod.Page, path string) error {
	if err := ydCoverType(page, "单图"); err != nil {
		return err
	}
	const sel = `.cover-selector .pre-img-item .setting-btn`
	if err := waitCount(ctx, page, sel, 1, 30*time.Second); err != nil {
		return errors.New("封面下方没有列出正文图片")
	}
	pick := "first"
	if path != "" {
		res, err := page.Timeout(evalTimeout).Eval(`(sel) => document.querySelectorAll(sel).length`, sel)
		if err != nil {
			return err
		}
		in, err := find(page, `.cover-selector .upload-img-container input[type=file]`, 5*time.Second)
		if err != nil {
			return errors.New("未找到封面上传框")
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
		if err := waitCount(ctx, page, sel, res.Value.Int()+1, 30*time.Second); err != nil {
			return errors.New("封面图片没有上传成功")
		}
		pick = "last"
	}
	if _, err := page.Timeout(evalTimeout).Eval(`(sel, pick) => {
		const btns = [...document.querySelectorAll(sel)];
		(pick === "last" ? btns.pop() : btns[0]).click();
	}`, sel, pick); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const set = [...document.querySelectorAll(".cover-container .cover-setter")].find((s) => s.getClientRects().length);
			if (set?.querySelector(".cover-item.setted img.preview")?.src) return true;
			[...document.querySelectorAll(".cover-crop-container .mp-crop-container button")]
				.find((b) => b.getClientRects().length && b.textContent.trim() === "裁切")?.click();
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
