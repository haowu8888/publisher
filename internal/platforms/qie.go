package platforms

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// publishQie publishes a video or an article through Qie's (企鹅号,
// Tencent's 腾讯内容开放平台) creator platform. The two forms share their
// widgets: a contenteditable title, tags, the 自主声明 dialog and the AI
// 生成声明 one. Selectors as of November 2026.
func publishQie(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return qieArticle(ctx, b, j)
	}
	return qieVideo(ctx, b, j)
}

// qieTitle is the title box, shown on the video form once the file is
// chosen, with the file's name in it.
const qieTitle = `.omui-articletitle__input1 [contenteditable="true"]`

// qieTitleMin is the fewest characters Qie takes in a title.
const qieTitleMin = 5

// qieStatements are Qie's own statements (自主声明), by the material's
// statements, which are in Bilibili's words. AI content is declared in
// the AI 生成声明 dialog instead.
var qieStatements = map[string]string{
	"内容无需标注":  "无需标注",
	"含虚构演绎内容": "虚构演绎，仅供娱乐",
}

// qieAI is the material statement that its pictures or video are made by
// AI.
const qieAI = "含AI生成内容"

// qieVideo uploads a video. Its form shows once the file is chosen, and
// is filled while the video uploads.
func qieVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://om.qq.com/main/creation/video", j)
	if err != nil {
		return err
	}
	if err := startUpload(ctx, page, `input[type="file"]`, "creation/video", m.VideoPath, qieTitle); err != nil {
		return err
	}
	dismissGuides(page)
	if err := qieFillTitle(ctx, page, m.Title); err != nil {
		return err
	}
	if m.Description != "" {
		j.Report("填写简介")
		desc, err := find(page, `#-desc textarea`, 10*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(m.Description, qieVideoLimits.Text)); err != nil {
			return err
		}
	}
	if err := qieTags(ctx, page, j); err != nil {
		return err
	}
	if err := qieStatement(ctx, page, m.Statement); err != nil {
		return err
	}
	if err := qieUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, qieVideoCover)
	if err != nil {
		return err
	}
	if err := qieAIMark(ctx, page, m.Statement == qieAI); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return qieSubmit(ctx, page, j, note)
}

// qieFillTitle fills the title box with the material's title. The box
// tells the form of a change only when its text differs from what it last
// had, 300 ms after typing stops, and after 撤销 it still has the autosaved
// title while the form's is empty: the same article again would leave the
// form without a title (请输入5至64个字的标题). So it is emptied first.
func qieFillTitle(ctx context.Context, page *rod.Page, title string) error {
	el, err := find(page, qieTitle, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, el, ""); err != nil {
		return err
	}
	if err := sleep(ctx, 600*time.Millisecond); err != nil {
		return err
	}
	if err := fill(page, el, fitTitle(title, qieLimits.Title)); err != nil {
		return err
	}
	if err := sleep(ctx, 600*time.Millisecond); err != nil {
		return err
	}
	if res, err := el.Eval(`() => this.textContent.trim()`); err != nil || res.Value.Str() == "" {
		return errors.New("标题没填上")
	}
	return nil
}

// qieTags types the tags into the tag box, each ended with Enter. Qie
// takes 9 at most, of 8 characters at most; longer ones are left out.
func qieTags(ctx context.Context, page *rod.Page, j Job) error {
	var tags []string
	for _, t := range j.Material.Tags {
		if utf8.RuneCountInString(t) <= 8 && len(tags) < 9 {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return nil
	}
	j.Report("填写标签")
	in, err := find(page, `[id$="-tag"] input.omui-suggestion__value`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	for _, t := range tags {
		if err := in.Focus(); err != nil {
			return err
		}
		if err := page.InsertText(t); err != nil {
			return err
		}
		if err := sleep(ctx, 300*time.Millisecond); err != nil {
			return err
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return err
		}
		if err := sleep(ctx, 300*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// qieDialog is the open dialog: pages keep closed ones in the document.
const qieDialog = `[...document.querySelectorAll(".omui-dialog")].find((d) => d.getClientRects().length)`

// qieStatement picks the material's statement in the 自主声明 dialog, if
// Qie has it. A material without one gets 无需标注.
func qieStatement(ctx context.Context, page *rod.Page, statement string) error {
	if statement == "" {
		statement = "内容无需标注"
	}
	s, ok := qieStatements[statement]
	if !ok {
		return nil
	}
	// The form may show the statement made last time, which 撤销 leaves: it
	// is only shown, and 发布 asks for one (请选择自主声明), so it is made anew.
	if _, err := page.Timeout(evalTimeout).Eval(`async () => {
		const remove = document.querySelector('[id$="-selfDeclaration"] .omui-contentbutton__remove');
		if (!remove) return;
		remove.click();
		await new Promise((r) => setTimeout(r, 800));
	}`); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, `[id$="-selfDeclaration"] button`, `^添加内容自主声明$`, 5*time.Second); err != nil {
		return err
	}
	res, err := page.Timeout(evalTimeout).Eval(`async (s) => {
		const wait = (ms) => new Promise((r) => setTimeout(r, ms));
		// The dialog shows a while before its choices.
		let d, radio;
		for (let i = 0; i < 40 && !radio; i++) {
			await wait(250);
			d = `+qieDialog+`;
			radio = d && [...d.querySelectorAll("label.omui-radio")].find((l) => l.textContent.trim() === s);
		}
		if (!d) return "自主声明的对话框没出来";
		if (!radio) return "自主声明里没有「" + s + "」";
		radio.click();
		await wait(300);
		[...d.querySelectorAll("button")].find((b) => b.textContent.trim() === "确认")?.click();
		await wait(800);
		return document.querySelector('[id$="-selfDeclaration"]').textContent.includes(s) ? "" : "自主声明「" + s + "」没选上";
	}`, s)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// qieAIMark answers the AI 生成声明, which the form asks for once it has
// the video or the pictures: each of them shows in the dialog, to be ticked
// when made by AI, and an untouched dialog declares none.
func qieAIMark(ctx context.Context, page *rod.Page, ai bool) error {
	res, err := page.Timeout(evalTimeout).Eval(`async (ai) => {
		const wait = (ms) => new Promise((r) => setTimeout(r, ms));
		const link = document.querySelector('[id$="-resourceAigcMarkInfo"] a');
		if (!link) return "";
		link.click();
		let d;
		for (let i = 0; i < 20 && !(d = `+qieDialog+`); i++) await wait(250);
		if (!d || !d.textContent.includes("AI生成声明")) return "AI生成声明的对话框没出来";
		for (let i = 0; i < 40 && !d.querySelector(".omui-thumb"); i++) await wait(250);
		if (ai) {
			// The thumbnails take a press, not a script's click().
			for (const t of d.querySelectorAll(".omui-thumb:not(.is--selected)")) {
				const r = t.getBoundingClientRect();
				const o = { bubbles: true, clientX: r.x + r.width / 2, clientY: r.y + r.height / 2 };
				for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) {
					t.dispatchEvent(new (type.startsWith("pointer") ? PointerEvent : MouseEvent)(type, o));
				}
				await wait(200);
			}
			if (d.querySelector(".omui-thumb:not(.is--selected)")) return "AI生成声明没勾上";
		}
		[...d.querySelectorAll("button")].find((b) => b.textContent.trim() === "提交")?.click();
		await wait(1000);
		return `+qieDialog+` ? "AI生成声明没提交上" : "";
	}`, ai)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// qieUploading reads a video upload's progress, as "45%".
var qieUploading = regexp.MustCompile(`(\d{1,3}(?:\.\d+)?%)`)

// qieUploaded waits until the video is uploaded: the form then says 视频添加
// 成功 above its title.
func qieUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const box = document.querySelector("#-title")?.parentElement?.parentElement;
			return { text: document.body.innerText, box: box ? box.innerText.slice(0, 300) : "" };
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		text := res.Value.Get("text").Str()
		if err := watch.check(page, text); err != nil {
			return err
		}
		if strings.Contains(text, "视频添加成功") {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if pct := qieUploading.FindString(res.Value.Get("box").Str()); pct != "" {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", pct, time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// qieVideoCover sets the video's cover, cropped from the picture: the
// cover box → 本地上传 → 下一步 → 完成 in the crop dialog. An upright video
// takes it as its 3:4 and 9:16 covers, a wide one as its 16:9.
func qieVideoCover(ctx context.Context, page *rod.Page, path string) error {
	if err := qieCover(ctx, page, `#-poster .omui-thumb, #-poster [class*=commonThumb]`, `#-poster img.omui-thumb__img`, path, "下一步"); err != nil {
		return err
	}
	if err := clickUntilGone(ctx, page, `.omui-dialog button`, `^完成$`, 2*time.Second, time.Minute); err != nil {
		return fmt.Errorf("封面裁剪框%w", err)
	}
	return qieCoverShown(ctx, page, `#-poster img.omui-thumb__img`)
}

// qieCover opens the cover dialog with the element box, uploads the
// picture at path in its 本地上传 tab, and once the dialog shows it takes it
// with the button reading next.
func qieCover(ctx context.Context, page *rod.Page, box, img, path, next string) error {
	// The box may take a click or two while the form settles, as when it
	// has just taken a picture of the body as the article's cover.
	res, err := page.Timeout(30*time.Second).Eval(`async (box, img) => {
		const wait = (ms) => new Promise((r) => setTimeout(r, ms));
		window.publisherOldCovers = [...document.querySelectorAll(img)].map((i) => i.src);
		let d;
		for (let i = 0; i < 60 && !d; i++) {
			if (i % 8 === 0) {
				const el = document.querySelector(box);
				if (el) {
					const action = [...el.querySelectorAll(".omui-thumb__action span")].find((s) => s.textContent.trim() === "更换");
					(action ?? el).click();
				}
			}
			await wait(250);
			d = `+qieDialog+`;
		}
		if (!d) return "封面对话框没出来";
		const tab = [...d.querySelectorAll(".omui-tab__label")].find((t) => t.textContent.includes("本地上传"));
		if (!tab) return "封面对话框里没有「本地上传」";
		tab.click();
		await wait(800);
		return "";
	}`, box, img)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	in, err := find(page, `.omui-dialog input[type="file"][accept^="image/"]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// Taken before it shows the upload, the dialog would close without it.
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const d = ` + qieDialog + `;
			const panel = d && [...d.querySelectorAll(".omui-tab__panel")].find((p) => p.getClientRects().length);
			return !!panel && [...panel.querySelectorAll("img")].some((i) => i.getClientRects().length && i.naturalWidth > 0);
		}`)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("封面图片没有上传成功")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := clickUntilGone(ctx, page, `.omui-dialog button`, "^"+next+"$", 2*time.Second, time.Minute); err != nil {
		return fmt.Errorf("封面对话框%w", err)
	}
	return nil
}

// qieCoverShown waits until the form shows a cover other than those it
// showed when qieCover opened the dialog.
func qieCoverShown(ctx context.Context, page *rod.Page, img string) error {
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(img) => [...document.querySelectorAll(img)].some((i) => i.src && !(window.publisherOldCovers ?? []).includes(i.src))`, img)
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

// qieSubmit publishes the filled form.
func qieSubmit(ctx context.Context, page *rod.Page, j Job, note string) error {
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, "button.omui-button--primary", "^发布$", 10*time.Second); err != nil {
		return err
	}
	// 发布 may first ask to confirm, as for a low-resolution video.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if clickVisible(ctx, page, ".omui-dialog button", "^(确认发布|继续发布|确认|确定)$", 0) == nil {
			break
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := waitPublished(ctx, page, []string{"management"}, "发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// qieArticle writes an article.
func qieArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://om.qq.com/main/creation/article", j)
	if err != nil {
		return err
	}
	j.Report("填写标题和正文")
	if _, err := waitElement(ctx, page, qieTitle, "creation/article"); err != nil {
		return err
	}
	if err := qieDropCache(ctx, page); err != nil {
		return err
	}
	dismissGuides(page)
	if err := qieFillTitle(ctx, page, m.Title); err != nil {
		return err
	}
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	editor, err := find(page, ".ProseMirror", 10*time.Second)
	if err != nil {
		return errors.New("未找到正文编辑器")
	}
	if err := pasteHTML(editor, content, plainText(articleBody(m))); err != nil {
		return err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length + this.querySelectorAll("img").length`); err != nil || res.Value.Int() == 0 {
		return errors.New("正文没填上")
	}
	if strings.Contains(content, "<img") {
		j.Report("上传正文图片")
		if err := qieImagesStored(ctx, page); err != nil {
			return err
		}
	}
	if m.CoverPath != "" {
		j.Report("设置封面")
		if err := qieArticleCover(ctx, page, m.CoverPath); err != nil {
			return fmt.Errorf("设置封面失败: %w", err)
		}
	}
	if err := qieTags(ctx, page, j); err != nil {
		return err
	}
	if err := qieStatement(ctx, page, m.Statement); err != nil {
		return err
	}
	if err := qieAIMark(ctx, page, m.Statement == qieAI); err != nil {
		return err
	}
	if err := qieKeepSingle(ctx, page, m.CoverPath != ""); err != nil {
		return fmt.Errorf("设置封面失败: %w", err)
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote)
		return nil
	}
	return qieSubmit(ctx, page, j, "")
}

// qieKeepSingle turns the cover back to 单图 when the form has turned it
// to 三图: it does so by itself a while after the body's pictures are
// stored, once there are three, over the cover set, and may leave a box
// empty (请完成三图封面设置). An own cover is kept single in any case; it
// is the single cover again once picked.
func qieKeepSingle(ctx context.Context, page *rod.Page, own bool) error {
	res, err := page.Timeout(evalTimeout).Eval(`async (own) => {
		const c = document.querySelector('[id$="-coverinfo"]');
		const radios = c ? [...c.querySelectorAll("label.omui-radio")] : [];
		const checked = () => radios.find((l) => l.querySelector("input")?.checked)?.textContent.trim();
		if (checked() !== "三图") return "";
		if (!own && c.querySelectorAll("img.omui-thumb__img").length >= 3) return "";
		radios.find((l) => l.textContent.trim() === "单图")?.click();
		await new Promise((r) => setTimeout(r, 800));
		return checked() === "单图" ? "" : "封面没能改回单图";
	}`, own)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	if own {
		return qieCoverShown(ctx, page, `[id$="-coverinfo"] img.omui-thumb__img`)
	}
	return nil
}

// qieDropCache undoes the editor's autosave. The editor keeps what was
// last in it on Tencent's server (editorCache), not in the browser, and
// fills a new article with it, saying 已载入自动保存的内容 with 撤销 for a few
// seconds: an earlier task's article would be under this one's. 撤销 empties
// the form and drops the autosave.
func qieDropCache(ctx context.Context, page *rod.Page) error {
	res, err := page.Timeout(30*time.Second).Eval(`async (titleSel) => {
		const wait = (ms) => new Promise((r) => setTimeout(r, ms));
		const title = () => document.querySelector(titleSel)?.textContent.trim() ?? "";
		const body = () => document.querySelector(".ProseMirror")?.innerText.trim() ?? "";
		let tip;
		for (let i = 0; i < 24 && !tip; i++) {
			await wait(250);
			tip = [...document.querySelectorAll(".omui-countdowntip")].find((t) => t.getClientRects().length);
		}
		if (!tip) return title() || body() ? "编辑器里有上次的内容，又没有「撤销」" : "";
		tip.querySelector("a")?.click();
		for (let i = 0; i < 20 && (title() || body()); i++) await wait(250);
		return title() || body() ? "点了「撤销」，编辑器里还留着上次的内容" : "";
	}`, qieTitle)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// qieImagesStored waits until the editor has stored the body's pictures,
// pasted as data URLs, on Tencent's servers (inews.gtimg.com). ProseMirror
// keeps empty pictures of its own (ProseMirror-separator) beside them.
func qieImagesStored(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(2 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll(".ProseMirror img")].filter((i) => /^(data|blob):/.test(i.src)).length`)
		if err == nil && res.Value.Int() == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("正文图片没有全部上传成功")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// qieArticleCover sets a single cover (单图) from the picture at path. The
// form takes the body's first picture by itself; 更换 replaces it, and the
// dialog takes the upload with 确认, without cropping.
func qieArticleCover(ctx context.Context, page *rod.Page, path string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`async () => {
		const radio = [...document.querySelectorAll('[id$="-coverinfo"] label.omui-radio')].find((l) => l.textContent.trim() === "单图");
		if (!radio) throw new Error("未找到单图封面选项");
		radio.click();
		await new Promise((r) => setTimeout(r, 500));
	}`); err != nil {
		return err
	}
	const img = `[id$="-coverinfo"] img.omui-thumb__img`
	if err := qieCover(ctx, page, `[id$="-coverinfo"] [class*=addCoverBtn], [id$="-coverinfo"] .omui-thumb`, img, path, "确认"); err != nil {
		return err
	}
	return qieCoverShown(ctx, page, img)
}

// checkQie refuses, before the form is filled, a material to submit by
// itself whose title Qie would turn down as too short. A review leaves
// that to the user.
func checkQie(m store.Material, mode store.Mode) error {
	if mode != store.ModeAuto {
		return nil
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(m.Title)); n < qieTitleMin {
		return fmt.Errorf("企鹅号标题至少 %d 个字，「%s」只有 %d 个字", qieTitleMin, m.Title, n)
	}
	return nil
}
