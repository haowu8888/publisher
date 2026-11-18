package platforms

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// publishDayu publishes a video or an article through Dayu's (大鱼号,
// UC's) creator platform. Selectors as of November 2026.
func publishDayu(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return dayuArticle(ctx, b, j)
	}
	return dayuVideo(ctx, b, j)
}

// dayuOpen opens the editor for kind, "video" or "article", without the
// draft it keeps in the browser's storage (wmdraft_<account>), which it
// would fill the form with.
func dayuOpen(ctx context.Context, b *browser.Browser, kind string, j Job) (*rod.Page, error) {
	return openFresh(ctx, b, "https://mp.dayu.com/404.html",
		"https://mp.dayu.com/dashboard/"+kind+"/write", `^wmdraft_`, j)
}

// dayuCategory is the category (分类) videos go in, which Dayu requires and
// materials do not have: the user's videos are on AI.
const dayuCategory = "科技"

// dayuStatements are Dayu's information sources (信息来源), by the
// material's statements, which are in Bilibili's words.
var dayuStatements = map[string]string{
	"内容无需标注":  "无需标注",
	"含AI生成内容": "AI生成",
	"含虚构演绎内容": "虚构演绎",
}

// dayuVideo uploads a video. Its form shows once the file is chosen, with
// the file's name as title and description, and is filled while the video
// uploads.
func dayuVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := dayuOpen(ctx, b, "video", j)
	if err != nil {
		return err
	}
	const titleSel = `.w-form-field-content-editable[placeholder*="标题"]`
	if err := startUpload(ctx, page, `input[type="file"][accept*=".mp4"]`, "dashboard/video", m.VideoPath, titleSel); err != nil {
		return err
	}
	dismissGuides(page)

	j.Report("填写标题和简介")
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return err
	}
	if err := fill(page, title, fitTitle(m.Title, dayuVideoLimits.Title)); err != nil {
		return err
	}
	desc, err := page.Timeout(10 * time.Second).ElementByJS(rod.Eval(`() => [...document.querySelectorAll(".w-form-field")]
		.find((f) => f.querySelector(":scope > label")?.textContent.includes("视频简介"))?.querySelector("textarea") ?? null`))
	if err != nil {
		return errors.New("未找到视频简介")
	}
	desc = desc.CancelTimeout()
	if err := fill(page, desc, truncate(m.Description, dayuVideoLimits.Text)); err != nil {
		return err
	}

	j.Report("选择分类")
	if err := dayuCategoryPick(ctx, page, dayuCategory); err != nil {
		return err
	}
	if len(m.Tags) > 0 {
		j.Report("填写标签")
		if err := dayuTags(ctx, page, m.Tags); err != nil {
			return err
		}
	}
	if err := dayuStatement(page, m.Statement); err != nil {
		return err
	}

	if err := dayuUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, dayuVideoCover)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(strings.Replace(reviewNote, "发布", "发表", 1) + note)
		return nil
	}
	return dayuSubmit(ctx, page, j, note)
}

// dayuCategoryPick opens the category select and picks name.
func dayuCategoryPick(ctx context.Context, page *rod.Page, name string) error {
	res, err := page.Timeout(evalTimeout).Eval(`async (name) => {
		const field = [...document.querySelectorAll(".w-form-field")].find((f) => f.querySelector(":scope > label")?.textContent.includes("视频分类"));
		if (!field) return "未找到视频分类";
		field.querySelector(".widgets-selects_container").click();
		await new Promise((r) => setTimeout(r, 500));
		const opt = [...field.querySelectorAll(".widgets-selects_select_container a")].find((a) => a.textContent.trim() === name);
		if (!opt) return "视频分类里没有「" + name + "」";
		opt.click();
		await new Promise((r) => setTimeout(r, 500));
		return field.querySelector(".widgets-selects_container").textContent.trim() === name ? "" : "视频分类没选上";
	}`, name)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// dayuTags types the tags into the tag box, each ended with Enter, which
// leaves the tag as an input of its own and adds a new one for the next.
// A tag takes 10 characters at most; longer ones are left out.
func dayuTags(ctx context.Context, page *rod.Page, tags []string) error {
	box, err := find(page, ".article-write_video-tags", 5*time.Second)
	if err != nil {
		return errors.New("未找到视频标签")
	}
	if err := jsClick(box); err != nil {
		return err
	}
	for _, t := range tags {
		if len([]rune(t)) > 10 {
			continue
		}
		in, err := page.Timeout(5 * time.Second).ElementByJS(rod.Eval(`() => [...document.querySelectorAll(".article-write_video-tags input")].pop() ?? null`))
		if err != nil {
			return errors.New("未找到视频标签输入框")
		}
		if err := in.CancelTimeout().Focus(); err != nil {
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

// dayuStatement picks the information source (信息来源) for the
// material's statement. It is required, so a material without one, as
// articles mostly are, gets 无需标注. The video form and the article form
// lay the choices out differently.
func dayuStatement(page *rod.Page, statement string) error {
	s, ok := dayuStatements[statement]
	if !ok {
		s = "无需标注"
	}
	res, err := page.Timeout(evalTimeout).Eval(`(s) => {
		const radio = document.querySelector('.source-remark-detail input[value="' + s + '"], .article-write-source-remark input[value="' + s + '"]');
		if (radio && !radio.checked) radio.closest("label").click();
		return !!radio && radio.checked;
	}`, s)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return fmt.Errorf("信息来源「%s」没选上", s)
	}
	return nil
}

// dayuUploading reads a video upload's progress, as "45%".
var dayuUploading = regexp.MustCompile(`(\d{1,3}(?:\.\d+)?%)`)

// dayuUploaded waits until the video is uploaded: the page then says 视频上传
// 成功 while Dayu processes it, which a submit need not wait for.
func dayuUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const box = document.querySelector(".article-write_video-container, [class*=video-upload], [class*=upload-progress]");
			return { text: document.body.innerText, box: box ? box.innerText : "" };
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		text := res.Value.Get("text").Str()
		if err := watch.check(page, text); err != nil {
			return err
		}
		if strings.Contains(text, "上传成功") {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if pct := dayuUploading.FindString(res.Value.Get("box").Str()); pct != "" {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", pct, time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// dayuVideoCover sets the video's cover (16:9, cropped from the picture)
// and, for an upright picture, its upright cover too.
func dayuVideoCover(ctx context.Context, page *rod.Page, path string) error {
	if err := dayuCrop(ctx, page, `#coverImg input[type="file"]`, `#form_main_cover_img`, path); err != nil {
		return err
	}
	if wide(path) {
		return nil
	}
	const vertical = `.article-write_box-vertical-cover`
	return dayuCrop(ctx, page, vertical+` input[type="file"]`, vertical+` .article-write_box-form-coverImg > img`, path)
}

// dayuCrop gives the cover input sel the picture at path, saves the crop
// dialog that opens, and waits until the cover shown, img, is the
// uploaded one.
func dayuCrop(ctx context.Context, page *rod.Page, sel, img, path string) error {
	in, err := find(page, sel, 10*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if err := dayuSave(ctx, page); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(img) => !!document.querySelector(img)?.src.includes("/upload/")`, img)
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

// dayuSave saves the cover crop dialog. The dialog ignores 保存 until it
// has drawn the picture, and shows no sign of having done so, then takes a
// few seconds to store the crop: click until it closes.
func dayuSave(ctx context.Context, page *rod.Page) error {
	const save = ".article-material-image-dialog_btn button"
	if _, err := page.Timeout(30 * time.Second).ElementByJS(rod.Eval(`(sel) => [...document.querySelectorAll(sel)].find((b) => b.offsetParent && b.textContent.trim() === "保存") ?? null`, save)); err != nil {
		return errors.New("封面裁剪框没出来")
	}
	for deadline := time.Now().Add(time.Minute); ; {
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
		if clickVisible(ctx, page, save, "^保存$", 0) != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面裁剪框点「保存」没有反应")
		}
	}
}

// dayuSubmit publishes the filled form (发表).
func dayuSubmit(ctx context.Context, page *rod.Page, j Job, note string) error {
	j.Report("提交发表")
	// 发表 listens for mousedown, which a script's click() does not send.
	btn, err := findR(page, "button.w-btn_primary", "^发表$", 10*time.Second)
	if err != nil {
		return errors.New("未找到「发表」按钮")
	}
	if err := btn.Click("left", 1); err != nil {
		return err
	}
	// 发表 may first ask to confirm, e.g. 「视频画质过低提醒」 for a low
	// bitrate, or that no location was added: go on with the material as it
	// is. Then it shows a preview to 确认发表.
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		if info, err := page.Info(); err == nil && regexp.MustCompile(`dashboard/(contents|works)`).MatchString(info.URL) {
			break
		}
		if res, err := page.Timeout(evalTimeout).Eval(`() => /发表成功|发布成功|提交成功/.test(document.body.innerText)`); err == nil && res.Value.Bool() {
			break
		}
		if el, err := page.Sleeper(rod.NotFoundSleeper).Timeout(evalTimeout).ElementByJS(rod.Eval(`() => [...document.querySelectorAll(".widgets-pop button")]
			.find((b) => b.getClientRects().length && /^(继续上传|继续发表|确认发表)$/.test(b.innerText.trim())) || null`)); err == nil {
			el.CancelTimeout().Click("left", 1)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := waitPublished(ctx, page, []string{"dashboard/contents", "dashboard/works"}, "发表成功|发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发表成功"+note)
}

// dayuArticle writes an article. Its cover can only be picked from the
// body's pictures, so the material's cover goes first in the body when the
// body does not show it already.
func dayuArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := dayuOpen(ctx, b, "article", j)
	if err != nil {
		return err
	}
	j.Report("填写标题和正文")
	title, err := waitElement(ctx, page, "input.article-write_box-title-input", "dashboard/article")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if err := fill(page, title, fitTitle(m.Title, dayuArticleLimits.Title)); err != nil {
		return err
	}
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	if m.CoverPath != "" {
		cover, err := inlineImages(`<p><img src="`+html.EscapeString(m.CoverPath)+`"></p>`, articleDirs(m)...)
		if err != nil {
			return err
		}
		if src := imgSrc.FindStringSubmatch(cover); src != nil && !strings.Contains(content, src[2]) {
			content = cover + content
		}
	}
	pics, err := dayuBody(ctx, page, j, content)
	if err != nil {
		return err
	}

	switch {
	case pics > 0:
		j.Report("设置封面（正文第一张图）")
		if err := dayuArticleCover(ctx, page); err != nil {
			return fmt.Errorf("设置封面失败: %w", err)
		}
	case j.Mode == store.ModeAuto:
		return errors.New("大鱼号文章的封面只能从正文图片里选：素材没有封面，正文里也没有图片")
	}
	if err := dayuStatement(page, m.Statement); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(strings.Replace(reviewNote, "发布", "发表", 1))
		return nil
	}
	return dayuSubmit(ctx, page, j, "")
}

// dayuBody writes the article's HTML into the UEditor body and returns how
// many pictures it has. The editor drops pictures given as data URLs, but
// uploads pasted picture files: each picture is first a marker paragraph,
// which a paste of the picture then replaces.
func dayuBody(ctx context.Context, page *rod.Page, j Job, content string) (int, error) {
	srcs := imgSrc.FindAllStringSubmatch(content, -1)
	n := 0
	marked := imgPara.ReplaceAllStringFunc(content, func(string) string {
		n++
		return fmt.Sprintf("<p>〔图%d〕</p>", n)
	})
	if _, err := page.Timeout(evalTimeout).Eval(`(html) => UE.instants.ueditorInstant0.setContent(html)`, marked); err != nil {
		return 0, fmt.Errorf("未找到正文编辑器，发布页可能已改版: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	for i := 1; i <= n; i++ {
		j.Report(fmt.Sprintf("上传正文图片 %d/%d", i, n))
		if _, err := page.Timeout(evalTimeout).Eval(`async (mark, src) => {
			const ed = UE.instants.ueditorInstant0;
			const p = [...ed.document.querySelectorAll("p")].find((e) => e.textContent.trim() === mark);
			if (!p) throw new Error("正文里找不到" + mark);
			p.innerHTML = "<br>";
			ed.focus();
			const r = ed.document.createRange();
			r.setStart(p, 0);
			r.collapse(true);
			const s = ed.window.getSelection();
			s.removeAllRanges();
			s.addRange(r);
			const blob = await (await fetch(src)).blob();
			const dt = new DataTransfer();
			dt.items.add(new File([blob], "image." + (blob.type.split("/")[1] || "png"), { type: blob.type }));
			ed.body.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
		}`, fmt.Sprintf("〔图%d〕", i), srcs[min(i, len(srcs))-1][2]); err != nil {
			return 0, fmt.Errorf("粘贴正文图片失败: %w", err)
		}
		if err := dayuImagesStored(ctx, page, i); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// imgPara is a paragraph of a picture alone, as markdownHTML and
// spreadImages write them, or a bare picture.
var imgPara = regexp.MustCompile(`<p>\s*<img[^>]*>\s*</p>|<img[^>]*>`)

// dayuImagesStored waits until the body shows n pictures stored by Dayu,
// not the placeholder (spacer.gif) shown while one uploads.
func dayuImagesStored(ctx context.Context, page *rod.Page, n int) error {
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => [...UE.instants.ueditorInstant0.document.querySelectorAll("img")].filter((i) => i.src.includes("/dayu/image") && i.naturalWidth > 1).length`)
		if err == nil && res.Value.Int() >= n {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("正文第 %d 张图片没有上传成功", n)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// dayuArticleCover sets a single cover (单封面) picked from the body: its
// first picture, which the dialog chooses first.
func dayuArticleCover(ctx context.Context, page *rod.Page) error {
	if _, err := page.Timeout(evalTimeout).Eval(`async () => {
		const opt = [...document.querySelectorAll(".article-write-article-cover_normal-option")].find((o) => o.textContent.includes("单封面"));
		if (!opt) throw new Error("未找到单封面选项");
		opt.querySelector(".w-radio").click();
		await new Promise((r) => setTimeout(r, 500));
		// 从正文中选择, shown on hovering the cover box.
		document.querySelector(".article-write-article-cover-cover-item_choose button").click();
	}`); err != nil {
		return err
	}
	const pop = `.widgets-pop`
	if err := clickVisible(ctx, page, pop+` .article-material-image_image-choose img`, ``, 15*time.Second); err != nil {
		return fmt.Errorf("选封面的对话框没出来: %w", err)
	}
	// 下一步 is ignored until the dialog has loaded the picture.
	for deadline := time.Now().Add(30 * time.Second); ; {
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		if clickVisible(ctx, page, pop+` button`, `^下一步$`, 0) != nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("选封面的对话框点「下一步」没有反应")
		}
	}
	if err := dayuSave(ctx, page); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => !!document.querySelector(".article-write-article-cover img")?.src.includes("/upload/")`)
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
