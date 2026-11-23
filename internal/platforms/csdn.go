package platforms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// csdnEditor is CSDN's Markdown editor; not_checkout opens it empty rather
// than on the last draft.
const csdnEditor = "https://editor.csdn.net/md/?not_checkout=1"

// csdnTitleMin is the shortest title CSDN takes.
const csdnTitleMin = 5

// csdnTagMax is how many tags an article takes here. CSDN needs one, so an
// article none of whose tags CSDN has gets csdnTag.
const (
	csdnTagMax = 5
	csdnTag    = "人工智能"
)

// csdnStatements are CSDN's statements (创作声明), by the material's, which
// are in Bilibili's words. Others get 无声明.
var csdnStatements = map[string]string{
	"含AI生成内容": "部分内容由AI辅助生成",
}

// csdnUpload is a script uploading a picture through the editor's own
// upload, without the watermark the user may have it add.
const csdnUpload = `async (file) => {
	const res = await window.csdn.upload.uploadImg({ appName: "direct_blog", file, imageTemplate: "" });
	const url = res?.[0]?.data?.data?.imageUrl;
	if (!url) throw new Error("图片上传失败：" + (res?.[0]?.data?.msg ?? JSON.stringify(res).slice(0, 100)));
	return url;
}`

// checkCSDN refuses, before the form is filled, an article to submit by
// itself whose title CSDN would turn down as too short. A review leaves
// that to the user.
func checkCSDN(m store.Material, mode store.Mode) error {
	if mode != store.ModeAuto || m.Kind != store.KindArticle {
		return nil
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(m.Title)); n < csdnTitleMin {
		return fmt.Errorf("CSDN 标题至少 %d 个字，「%s」只有 %d 个字", csdnTitleMin, m.Title, n)
	}
	return nil
}

// publishCSDN uploads a video, or writes an article.
func publishCSDN(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindVideo {
		return csdnVideo(ctx, b, j)
	}
	return csdnArticle(ctx, b, j)
}

// csdnArticle writes an article in CSDN's Markdown editor, a StackEdit
// one: the body's pictures are uploaded first, and the Markdown pasted as
// text in place of the editor's sample. The settings are in the dialog 发布文章
// opens, which a review leaves open for the user to submit.
func csdnArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, csdnEditor, j)
	if err != nil {
		return err
	}
	editor, err := waitElement(ctx, page, `pre.editor__inner`, "editor.csdn.net")
	if err != nil {
		return err
	}

	j.Report("上传正文图片")
	body, err := articleMarkdown(m)
	if err != nil {
		return err
	}
	if _, err := find(page, `button.btn-publish`, 30*time.Second); err != nil {
		return errors.New("编辑器没有加载完")
	}
	if body, err = uploadImages(page, body, csdnUpload); err != nil {
		return err
	}

	j.Report("填写标题")
	if _, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".article-bar__title-display")?.click()`); err != nil {
		return err
	}
	title, err := find(page, `input.article-bar__title`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, csdnLimits.Title)); err != nil {
		return err
	}

	j.Report("填写正文")
	// The editor keeps its own text; select all and delete as typing
	// would, then paste, which it takes as it is, lines and all.
	if _, err := editor.Eval(`(text) => {
		this.focus();
		document.execCommand("selectAll");
		document.execCommand("delete");
		const dt = new DataTransfer();
		dt.setData("text/plain", text);
		this.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
	}`, body); err != nil {
		return err
	}
	if err := sleep(ctx, time.Second); err != nil {
		return err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length`); err != nil || res.Value.Int() < utf8.RuneCountInString(strings.TrimSpace(body))/2 {
		return errors.New("正文没填上")
	}

	j.Report("填写发布设置")
	if err := clickVisible(ctx, page, `button.btn-publish`, "^发布文章$", 10*time.Second); err != nil {
		return err
	}
	if _, err := find(page, `.modal__publish-article`, 10*time.Second); err != nil {
		return errors.New("发布设置没有弹出")
	}
	if err := csdnTags(ctx, page, m.Tags); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, csdnCover)
	if err != nil {
		return err
	}
	if err := csdnSettings(ctx, page, m.Statement); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后在发布设置里点「发布文章」" + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, `.modal__publish-article .modal__button-bar button`, "^发布文章$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"/success/"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// csdnTags adds the tags in the tag box: each is typed, CSDN searches its
// tags for it, and Enter adds it when one is named so. An account below
// blog level 3 may not make new tags, so a tag CSDN does not have is left
// out, and csdnTag added when none is in.
func csdnTags(ctx context.Context, page *rod.Page, tags []string) error {
	if err := clickVisible(ctx, page, `.modal__publish-article .mark_selection .tag__btn-tag`, "添加文章标签", 10*time.Second); err != nil {
		return err
	}
	in, err := find(page, `.mark_selection_box input.el-input__inner`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	count := func() int {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelectorAll(".modal__publish-article .mark_selection_title_el_tag .el-tag").length`)
		if err != nil {
			return 0
		}
		return res.Value.Int()
	}
	add := func(t string) error {
		if err := fill(page, in, ""); err != nil {
			return err
		}
		if err := page.InsertText(t); err != nil {
			return err
		}
		// The search waits for a pause in typing.
		if err := sleep(ctx, 1500*time.Millisecond); err != nil {
			return err
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return err
		}
		return sleep(ctx, 500*time.Millisecond)
	}
	// The box starts with the tags of the article before.
	if _, err := page.Timeout(evalTimeout).Eval(`async () => {
		for (const x of document.querySelectorAll(".modal__publish-article .mark_selection_title_el_tag .el-tag .el-tag__close")) {
			x.click();
			await new Promise((r) => setTimeout(r, 200));
		}
	}`); err != nil {
		return err
	}
	for _, t := range tags {
		if count() == csdnTagMax {
			break
		}
		if err := add(t); err != nil {
			return err
		}
	}
	if count() == 0 {
		if err := add(csdnTag); err != nil {
			return err
		}
	}
	fill(page, in, "")
	page.Timeout(evalTimeout).Eval(`() => document.querySelector(".mark_selection_box .modal__close-button")?.click()`)
	if count() == 0 {
		return errors.New("文章标签没加上")
	}
	return nil
}

// csdnCover uploads the cover, which opens a cropper whose 确认上传 takes
// it, cut to the cover's shape. The dialog may come with a cover already,
// as the one used last, in place of the upload box; it is removed first.
func csdnCover(ctx context.Context, page *rod.Page, path string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".modal__publish-article .preview-box.hasImg .btn-remove-coverimg")?.click()`); err != nil {
		return err
	}
	in, err := find(page, `.modal__publish-article .cover-upload-box input.el-upload__input`, 5*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if err := clickUntilGone(ctx, page, `.vue-image-crop-upload .vicp-operate-btn`, "^确认上传$", 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面裁剪框%w", err)
	}
	if _, err := find(page, `.modal__publish-article .preview-box img.preview[src*="csdnimg.cn"]`, 30*time.Second); err != nil {
		return errors.New("封面没有上传成功")
	}
	return nil
}

// csdnSettings picks 原创 and the statement.
func csdnSettings(ctx context.Context, page *rod.Page, statement string) error {
	want, ok := csdnStatements[statement]
	if !ok {
		want = "无声明"
	}
	res, err := page.Timeout(evalTimeout).Eval(`async (want) => {
		const modal = document.querySelector(".modal__publish-article");
		const original = modal.querySelector("input#original");
		if (!original) return "未找到「原创」";
		if (!original.checked) modal.querySelector("label[for=original]").click();
		const sel = modal.querySelector(".creation-statement-select");
		if (!sel) return "未找到「创作声明」";
		if (sel.querySelector("input").value === want || (!sel.querySelector("input").value && want === "无声明")) return "";
		sel.querySelector(".el-input").click();
		await new Promise((r) => setTimeout(r, 500));
		const item = [...sel.querySelectorAll(".el-select-dropdown__item")].find((i) => i.textContent.trim() === want);
		if (!item) return "创作声明里没有「" + want + "」";
		item.click();
		return "";
	}`, want)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// csdnVideoUpload is the video upload page; once given a file it goes to
// the video's form (videoEditorial).
const csdnVideoUpload = "https://mp.csdn.net/mp_others/creation/videoUpload"

// csdnVideoTagMax is how many tags a video takes, each csdnVideoTagLen
// characters at most; unlike an article's, they need not be CSDN's.
const (
	csdnVideoTagMax = 3
	csdnVideoTagLen = 10
)

// csdnVideo uploads a video. Its form is filled while it uploads; it
// needs a description, which is the title for a material without one.
func csdnVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, csdnVideoUpload, j)
	if err != nil {
		return err
	}
	const fileSel = `input.el-upload__input[accept^=".mp4"]`
	if _, err := waitElement(ctx, page, fileSel, "mp.csdn.net/mp_others"); err != nil {
		return err
	}
	const titleSel = `.Management input.el-input__inner[maxlength="30"]`
	if err := csdnStartUpload(ctx, page, fileSel, m.VideoPath, titleSel); err != nil {
		return err
	}

	j.Report("填写标题和简介")
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, csdnVideoLimits.Title)); err != nil {
		return err
	}
	desc, err := find(page, `.VideoManagement_description textarea`, 10*time.Second)
	if err != nil {
		return errors.New("未找到简介输入框")
	}
	text := m.Description
	if strings.TrimSpace(text) == "" {
		text = m.Title
	}
	if err := fill(page, desc, truncate(text, csdnVideoLimits.Text)); err != nil {
		return err
	}
	j.Report("填写标签")
	if err := csdnVideoTags(ctx, page, m.Tags); err != nil {
		return err
	}

	if err := csdnVideoUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, csdnVideoCover)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, `.Management-bottom button`, "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"/manage"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// csdnStartUpload gives the upload page the video, and waits for the
// form it then goes to, which can take a while to load. It gives the file
// once only, unlike startUpload: the form has a file input matching sel
// too, and a second file would start the upload over.
func csdnStartUpload(ctx context.Context, page *rod.Page, sel, path, formSel string) error {
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
	for deadline := time.Now().Add(time.Minute); ; {
		if ok, _, _ := page.Timeout(evalTimeout).Has(formSel); ok {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("选了视频 1 分钟还没出现填写表单，发布页可能已改版")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// csdnVideoTags types the tags into the tag box, each ended with Enter;
// those too long for it are left out, and csdnTag added when none is in.
func csdnVideoTags(ctx context.Context, page *rod.Page, tags []string) error {
	in, err := find(page, `.video_mark_selection input.el-input__inner`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	count := func() int {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelectorAll(".video_mark_selection_title_el_tag .el-tag").length`)
		if err != nil {
			return 0
		}
		return res.Value.Int()
	}
	add := func(t string) error {
		if err := fill(page, in, t); err != nil {
			return err
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return err
		}
		return sleep(ctx, 500*time.Millisecond)
	}
	for _, t := range tags {
		if count() == csdnVideoTagMax {
			break
		}
		if t = strings.TrimSpace(t); t == "" || utf8.RuneCountInString(t) > csdnVideoTagLen {
			continue
		}
		if err := add(t); err != nil {
			return err
		}
	}
	if count() == 0 {
		if err := add(csdnTag); err != nil {
			return err
		}
	}
	fill(page, in, "")
	if count() == 0 {
		return errors.New("视频标签没加上")
	}
	return nil
}

// csdnVideoUploaded waits until the video is uploaded: the upload box then
// says 上传成功.
func csdnVideoUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => ({
			status: document.querySelector(".Management-top .gement li.text")?.textContent.trim() ?? "",
			progress: document.querySelector(".Management-top .el-progress")?.getAttribute("aria-valuenow") ?? "",
		})`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		status := res.Value.Get("status").Str()
		switch {
		case strings.Contains(status, "成功"):
			return nil
		case strings.Contains(status, "失败"):
			return fmt.Errorf("视频上传失败：%s", status)
		}
		j.Report(fmt.Sprintf("上传视频中 %s%%（已用 %s）", res.Value.Get("progress").Str(), time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// csdnVideoCover replaces the cover, a frame of the video: the picture goes
// into a cropper (图片剪裁), whose 确认 uploads the part cut out.
func csdnVideoCover(ctx context.Context, page *rod.Page, path string) error {
	in, err := find(page, `.essential-uploader input.el-upload__input`, 5*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	old, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".essential-uploader img.right-img")?.src ?? ""`)
	if err != nil {
		return err
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// The cropper (vue-cropper) starts on a 16:9 box of 80% of the
	// picture, and 确认 silently does nothing when that is under 960 wide,
	// as for a tall picture 1080 wide; the box is made as large as fits.
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const cr = document.querySelector(".VideoManagement")?.__vue__?.$refs.cropper;
			if (!cr?.trueWidth || !document.querySelector(".essential-img .el-dialog__wrapper .dialog-footer button")?.offsetParent) return false;
			const w = cr.trueWidth * cr.scale, h = cr.trueHeight * cr.scale;
			cr.changeCrop(Math.min(w, h * 16 / 9), Math.min(w, h * 16 / 9) * 9 / 16);
			return true;
		}`)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("封面裁剪框没出来")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := clickUntilGone(ctx, page, `.essential-img .el-dialog__wrapper .dialog-footer button`, "^确认$", 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面裁剪框%w（图片可能小于 960x540）", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".essential-uploader img.right-img")?.src ?? ""`)
		if err == nil && res.Value.Str() != old.Value.Str() && res.Value.Str() != "" {
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
