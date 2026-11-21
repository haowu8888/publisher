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
)

// publishWeibo publishes a video, or an article as a Weibo article (头条
// 文章). Selectors as of November 2026.
func publishWeibo(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return wbArticle(ctx, b, j)
	}
	return wbVideo(ctx, b, j)
}

// wbStatements are Weibo's content statements (内容声明), by the
// material's statements, which are in Bilibili's words.
var wbStatements = map[string]string{
	"内容无需标注":  "我的内容无需声明",
	"含AI生成内容": "含AI生成内容",
	"含虚构演绎内容": "含虚构演绎内容",
}

// wbVideo uploads a video. The upload page ignores files given to its
// input by script, so the file goes through the chooser its button opens,
// as a user's would. The form shows once the file is chosen.
func wbVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://weibo.com/upload/channel", j)
	if err != nil {
		return err
	}
	const titleSel = `input[placeholder^="填写标题"]`
	if _, err := waitElement(ctx, page, `input[type="file"]`, "upload/channel"); err != nil {
		return err
	}
	j.Report("上传视频")
	if err := wbChoose(ctx, page, m.VideoPath, titleSel); err != nil {
		return err
	}

	j.Report("填写标题和内容")
	if err := wbOriginal(page); err != nil {
		return err
	}
	if s, ok := wbStatements[m.Statement]; ok {
		if err := wbStatement(ctx, page, s); err != nil {
			return err
		}
	}
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return err
	}
	if err := fill(page, title, fitTitle(m.Title, wbVideoLimits.Title)); err != nil {
		return err
	}
	text, err := find(page, `textarea[placeholder^="有什么新鲜事"]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到微博内容输入框")
	}
	if err := fill(page, text, wbText(m.Description, m.Tags)); err != nil {
		return err
	}

	if err := wbUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, wbVideoCover)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	j.Report("提交发布")
	if err := clickVisible(ctx, page, "button", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := wbPublished(ctx, page, nil, "发布成功|再发一条视频"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// wbChoose clicks the upload button and answers the file chooser it opens
// with the file at path, trying once more if startedSel does not show.
func wbChoose(ctx context.Context, page *rod.Page, path, startedSel string) error {
	for try := 0; try < 2; try++ {
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		set, err := page.HandleFileDialog()
		if err != nil {
			return err
		}
		btn, err := findR(page, "button", "^上传视频$", 10*time.Second)
		if err != nil {
			return errors.New("未找到「上传视频」按钮")
		}
		browser.Show(page)
		if err := btn.Click("left", 1); err != nil {
			return err
		}
		if err := set([]string{path}); err != nil {
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

// wbOriginal picks the type (类型) 原创, which Weibo requires.
func wbOriginal(page *rod.Page) error {
	_, err := page.Timeout(evalTimeout).Eval(`() => {
		const label = [...document.querySelectorAll("label.woo-radio-main")].find((l) => l.textContent.trim() === "原创");
		if (!label) throw new Error("未找到类型「原创」");
		if (!label.querySelector("input").checked) label.click();
	}`)
	return err
}

// wbStatement opens the content statement select and picks s.
func wbStatement(ctx context.Context, page *rod.Page, s string) error {
	res, err := page.Timeout(evalTimeout).Eval(`async (s) => {
		const trigger = document.querySelector('[title^="请进行内容声明"]');
		if (!trigger) return "未找到内容声明";
		trigger.click();
		await new Promise((r) => setTimeout(r, 600));
		const pop = trigger.closest(".woo-pop-wrap");
		const opt = [...pop.querySelectorAll('button[class*="_option_"]')].find((b) => b.textContent.trim() === s);
		if (!opt) return "内容声明里没有「" + s + "」";
		opt.click();
		await new Promise((r) => setTimeout(r, 300));
		[...pop.querySelectorAll("button")].find((b) => b.textContent.trim() === "确定")?.click();
		await new Promise((r) => setTimeout(r, 600));
		return pop.querySelector('[class*="_triggerText_"]')?.textContent.trim() === s ? "" : "内容声明没选上";
	}`, s)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// wbText is a post's text: the description, then the tags as topics,
// "#tag#", while they fit.
func wbText(desc string, tags []string) string {
	var topics []string
	for _, t := range tags[:min(len(tags), wbVideoLimits.Tags)] {
		topics = append(topics, "#"+t+"#")
	}
	room := wbVideoLimits.Text - utf8.RuneCountInString(strings.Join(topics, " ")) - 1
	text := truncate(desc, max(room, 0))
	if len(topics) == 0 {
		return text
	}
	return strings.TrimSpace(text + "\n" + strings.Join(topics, " "))
}

// wbProgress reads a video upload's progress, as "45%".
var wbProgress = regexp.MustCompile(`上传中\D{0,6}(\d{1,3}(?:\.\d+)?%)`)

// wbUploaded waits until the page says 上传完成.
func wbUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		text := res.Value.Str()
		if err := watch.check(page, text); err != nil {
			return err
		}
		if strings.Contains(text, "上传完成") {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if m := wbProgress.FindStringSubmatch(text); m != nil {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", m[1], time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// wbVideoCover uploads the cover in the cover editor (编辑封面), which
// crops it to 16:9, and waits until the form shows the new cover.
func wbVideoCover(ctx context.Context, page *rod.Page, path string) error {
	// The form is a layer (.wbpro-layer) too.
	const editor = `() => [...document.querySelectorAll(".wbpro-layer")].filter((e) => e.getClientRects().length && e.querySelector(".wbpro-layer-tit")?.textContent.includes("编辑封面")).pop() ?? null`
	cover := func() string {
		res, err := page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll("a")].find((a) => a.textContent.trim() === "上传封面")?.closest('[class*="_box_"]')?.querySelector("img")?.src ?? ""`)
		if err != nil {
			return ""
		}
		return res.Value.Str()
	}
	before := cover()
	if err := clickVisible(ctx, page, "a", "^上传封面$", 10*time.Second); err != nil {
		return err
	}
	layer, err := page.Timeout(15 * time.Second).ElementByJS(rod.Eval(editor))
	if err != nil {
		return errors.New("封面编辑框没有打开")
	}
	layer = layer.CancelTimeout()
	in, err := layer.Element(`input[type="file"]`)
	if err != nil {
		return err
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// The editor shows the picture from a blob URL once it has it.
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := layer.Eval(`() => [...this.querySelectorAll("img")].some((i) => i.src.startsWith("blob:"))`)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("封面图片没有载入")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if _, err := layer.Eval(`() => [...this.querySelectorAll("button, a, div")].filter((e) => e.offsetParent && e.textContent.trim() === "完成").pop().click()`); err != nil {
		return fmt.Errorf("未找到「完成」按钮: %w", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		if now := cover(); now != "" && now != before {
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

// wbPublished is waitPublished, handing the task over when Weibo asks to
// verify the user (请先验证身份, a slider puzzle) in another tab: that is
// for the user to do.
func wbPublished(ctx context.Context, page *rod.Page, urlMarks []string, text string) error {
	err := waitPublished(ctx, page, urlMarks, text)
	if err != nil && ctx.Err() == nil && wbVerifying(page) {
		return errWbVerify
	}
	return err
}

var errWbVerify = &HandOver{"微博要求验证身份：请在浏览器的验证页拖动滑块，再回到微博的发布页点「发布」，发出后点「我已提交」"}

// wbVerifying reports whether a tab asks the user to verify.
func wbVerifying(page *rod.Page) bool {
	pages, err := page.Browser().Pages()
	if err != nil {
		return false
	}
	for _, p := range pages {
		if info, err := p.Info(); err == nil && strings.Contains(info.URL, "security.weibo.com") {
			return true
		}
	}
	return false
}

// wbArticle writes a Weibo article (头条文章). The editor keeps every
// article as a draft on Weibo's side; 写文章 starts a new one. Pasted
// pictures are uploaded by the editor. The cover is uploaded to the
// user's picture library (图片库) and picked there.
func wbArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://card.weibo.com/article/v5/editor#/draft", j)
	if err != nil {
		return err
	}
	if _, err := waitElement(ctx, page, ".ProseMirror", "card.weibo.com/article"); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, "button", "^写文章$", 10*time.Second); err != nil {
		return err
	}
	for deadline := time.Now().Add(15 * time.Second); ; {
		if info, err := page.Timeout(evalTimeout).Info(); err == nil && wbDraft.MatchString(info.URL) {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("没能新建文章草稿")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}

	// The editor loads the new draft into the form after its URL changes,
	// replacing what was typed before.
	if err := sleep(ctx, 3*time.Second); err != nil {
		return err
	}

	j.Report("填写标题和正文")
	title, err := find(page, `textarea[placeholder="请输入标题"]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, wbArticleLimits.Title)); err != nil {
		return err
	}
	if res, err := title.Eval(`() => this.value`); err != nil || res.Value.Str() == "" {
		return errors.New("标题没填上")
	}
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	editor, err := find(page, ".ProseMirror", 10*time.Second)
	if err != nil {
		return err
	}
	if err := pasteHTML(editor, content, plainText(articleBody(m))); err != nil {
		return err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length + this.querySelectorAll("figure").length`); err != nil || res.Value.Int() == 0 {
		return errors.New("正文没填上")
	}
	if strings.Contains(content, "<img") {
		j.Report("上传正文图片")
		if err := wbImagesStored(ctx, page); err != nil {
			return err
		}
	}

	if m.CoverPath != "" {
		j.Report("设置封面")
		if err := wbArticleCover(ctx, page, m.CoverPath); err != nil {
			return fmt.Errorf("设置封面失败: %w", err)
		}
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后点「下一步」发布")
		return nil
	}
	j.Report("提交发布")
	if err := clickVisible(ctx, page, "button", "^下一步$", 10*time.Second); err != nil {
		return err
	}
	// 下一步 opens .publish-modal (the post that shares the article), unless
	// Weibo first wants the slider check.
	if _, err := find(page, ".publish-modal", 30*time.Second); err != nil {
		if wbVerifying(page) {
			return errWbVerify
		}
		return errors.New("没有出现发布对话框")
	}
	// It checks 「关注@头条文章」 by default.
	page.Timeout(evalTimeout).Eval(`() => document.querySelector(".publish-modal .n-checkbox--checked")?.click()`)
	if err := clickVisible(ctx, page, ".publish-modal button", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := wbPublished(ctx, page, []string{"weibo.com/ttarticle", "/detail/"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功")
}

// wbDraft is the editor's URL on a draft.
var wbDraft = regexp.MustCompile(`#/draft/\d+`)

// wbImagesStored waits until the editor has uploaded every picture of the
// body, none left as a data URL.
func wbImagesStored(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelectorAll('.ProseMirror img[src^="data:"], .ProseMirror img[src^="blob:"]').length`)
		if err == nil && res.Value.Int() == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("正文图片没有全部上传")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// wbArticleCover opens the cover dialog, uploads the picture at path to
// the picture library tab (图片库), picks it, and confirms the crop.
func wbArticleCover(ctx context.Context, page *rod.Page, path string) error {
	if err := clickVisible(ctx, page, "div, span", "^设置文章封面$", 10*time.Second); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, ".n-dialog .n-tabs-tab", "^图片库$", 10*time.Second); err != nil {
		return err
	}
	srcs := func() []string {
		res, err := page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll(".n-dialog .image-item img")].map((i) => i.src)`)
		if err != nil {
			return nil
		}
		var out []string
		for _, v := range res.Value.Arr() {
			out = append(out, v.Str())
		}
		return out
	}
	// The library loads after the tab shows.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".n-dialog")?.innerText.includes("还没有上传过") || !!document.querySelector(".n-dialog .image-item img")`)
		if err == nil && res.Value.Bool() {
			break
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	before := len(srcs())
	in, err := find(page, `.n-dialog input[type="file"]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	for deadline := time.Now().Add(time.Minute); len(srcs()) <= before; {
		if time.Now().After(deadline) {
			return errors.New("封面图片没有上传成功")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	// The newest picture comes first. The list redraws as the upload
	// ends, which can drop a pick made meanwhile: pick until it holds.
	for deadline := time.Now().Add(15 * time.Second); ; {
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const item = document.querySelector(".n-dialog .image-item");
			if (item.classList.contains("is-selected")) return true;
			item.click();
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("没能选中上传的封面")
		}
	}
	if err := clickVisible(ctx, page, ".n-dialog button", "^下一步$", 5*time.Second); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, ".n-dialog button", "^确定$", 15*time.Second); err != nil {
		return err
	}
	// The cropper may not take the first 确定 before it has drawn.
	if err := clickUntilGone(ctx, page, ".n-dialog button", "^确定$", 3*time.Second, 40*time.Second); err != nil {
		return fmt.Errorf("封面裁剪框%w", err)
	}
	return nil
}
