package platforms

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// publishToutiao publishes a video, or an article as a micro post (微头条)
// of its text and pictures, through Toutiao's creator platform (头条号).
// Selectors as of November 2026.
func publishToutiao(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return ttPost(ctx, b, j)
	}
	return ttVideo(ctx, b, j)
}

// ttVideo uploads a video. The form shows once the file is chosen and is
// filled while the video uploads.
func ttVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://mp.toutiao.com/profile_v4/xigua/upload-video", j)
	if err != nil {
		return err
	}
	const titleSel = `.form-item-title input`
	if err := startUpload(ctx, page, `input[type="file"][accept*="video"]`, "profile_v4", m.VideoPath, titleSel); err != nil {
		return err
	}

	j.Report("填写标题")
	title, err := waitElement(ctx, page, titleSel, "profile_v4")
	if err != nil {
		return err
	}
	if err := fill(page, title, fitTitle(m.Title, ttVideoLimits.Title)); err != nil {
		return err
	}

	// A vertical video's form has neither topics nor a description.
	if ok, _, _ := page.Has(".form-item-hash_tag input"); ok && len(m.Tags) > 0 {
		j.Report("填写话题")
		if err := ttVideoTags(ctx, page, m.Tags); err != nil {
			return err
		}
	}

	if ok, _, _ := page.Has("textarea.abstract"); ok && m.Description != "" {
		j.Report("填写简介")
		el, err := page.Element("textarea.abstract")
		if err != nil {
			return err
		}
		if err := fill(page, el, truncate(m.Description, ttVideoLimits.Text)); err != nil {
			return err
		}
	}

	if s, ok := ttStatements[m.Statement]; ok {
		j.Report("选择作品声明")
		if err := clickVisible(ctx, page, ".form-item-source label", "^"+regexp.QuoteMeta(s)+"$", 10*time.Second); err != nil {
			return err
		}
	}

	if err := ttUploaded(ctx, page, j); err != nil {
		return err
	}
	// The cover is required: a frame of the video without one of the
	// material's.
	note, err := setCover(ctx, page, j, ttCover)
	if err != nil {
		return err
	}
	if m.CoverPath == "" {
		j.Report("设置封面（视频画面）")
		if err := ttCover(ctx, page, ""); err != nil {
			page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
			note = "（封面没设上，请手动设置：" + err.Error() + "）"
		}
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	if note != "" {
		// Toutiao takes no video without a cover.
		return errors.New("封面没设上，没有提交" + note)
	}
	return ttSubmit(ctx, page, j)
}

// ttVideoTags adds tags as the video's topics, ten at most: Enter takes
// what is typed.
func ttVideoTags(ctx context.Context, page *rod.Page, tags []string) error {
	in, err := page.Element(".form-item-hash_tag input")
	if err != nil {
		return err
	}
	for i, tag := range tags {
		if i == 10 {
			break
		}
		if err := fill(page, in, tag); err != nil {
			return err
		}
		// Enter before the suggestions load is not taken.
		if err := sleep(ctx, 1500*time.Millisecond); err != nil {
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

// ttStatements are Toutiao's work statements (作品声明), by the creation
// statements of the material, which are in Bilibili's words.
var ttStatements = map[string]string{
	"含AI生成内容": "AI生成",
	"含虚构演绎内容": "虚构演绎，故事经历",
}

// ttUploaded waits until the video upload finishes: the page says 上传成功
// then, and shows its progress until, staying at 100% a while as the
// server takes in the video.
func ttUploaded(ctx context.Context, page *rod.Page, j Job) error {
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
		if strings.Contains(text, "上传成功") {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if m := ttPercent.FindStringSubmatch(text); m != nil {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", m[1], time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// ttPercent finds the upload's progress, as "上传中... 45%"; the page shows
// other percentages.
var ttPercent = regexp.MustCompile(`上传中\D{0,6}(\d{1,3}(?:\.\d+)?%)`)

// ttCover sets the video's cover to the picture at path, or with path
// empty to the frame the cover dialog offers first. The cover editor wants
// the crop confirmed (完成裁剪), then asks whether it is done.
func ttCover(ctx context.Context, page *rod.Page, path string) error {
	const dialog = ".m-poster-upgrade"
	if _, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".form-item-poster .fake-upload-trigger, .form-item-poster .image-modify-btn:last-child").click()`); err != nil {
		return errors.New("未找到封面入口")
	}
	if _, err := find(page, dialog, 10*time.Second); err != nil {
		return errors.New("封面编辑框没有打开")
	}
	if path == "" {
		if err := clickVisible(ctx, page, dialog+" *", "^下一步$", 10*time.Second); err != nil {
			return err
		}
	} else {
		if err := clickVisible(ctx, page, dialog+" *", "^本地上传$", 10*time.Second); err != nil {
			return err
		}
		in, err := find(page, dialog+` input[type="file"]`, 10*time.Second)
		if err != nil {
			return errors.New("未找到封面上传入口")
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
	}
	// The editor ignores 确定 until it has drawn the picture, which it
	// shows no sign of, so 确定 is clicked again until the editor asks
	// whether it is done. A picture of the video's shape needs no crop.
	for deadline := time.Now().Add(40 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			// Not offsetParent, which a fixed dialog has none of.
			const shown = (sel) => [...document.querySelectorAll(sel)].find((e) => e.getClientRects().length);
			if (shown(".m-dialog-edit")) return true;
			const clip = shown(".clip-btn");
			if (clip) clip.click();
			else if (shown(".btn-sure") && !shown(".btn-sure").disabled) shown(".btn-sure").click();
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("封面编辑器一直没有响应「确定」")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	if err := clickVisible(ctx, page, ".m-dialog-edit button", "^确定$", 10*time.Second); err != nil {
		return err
	}
	if err := waitGone(ctx, page, ".m-xigua-dialog", 20*time.Second); err != nil {
		return errors.New("封面编辑框没有关闭")
	}
	if _, err := find(page, `.form-item-poster .xigua-poster-editor .bg`, 15*time.Second); err != nil {
		return errors.New("封面没有设上")
	}
	return nil
}

// ttPost writes an article as a micro post (微头条): its pictures, the
// cover first, and its text, which leads with the title, as micro posts
// have none, and ends with the tags as topics.
func ttPost(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	// The editor would add what an earlier task left in it.
	page, err := openFresh(ctx, b, "https://mp.toutiao.com/robots.txt",
		"https://mp.toutiao.com/profile_v4/weitoutiao/publish", `^wtt-mp(form|editor)draft$`, j)
	if err != nil {
		return err
	}
	editor, err := waitElement(ctx, page, ".ProseMirror", "weitoutiao")
	if err != nil {
		return err
	}
	if images := noteImages(m); len(images) > 0 {
		if err := ttImages(ctx, page, j, images); err != nil {
			return err
		}
	}

	j.Report("填写正文和话题")
	text := m.Description
	if text == "" {
		text = paragraphs(articleBody(m))
	}
	if !strings.Contains(text, strings.TrimSpace(m.Title)) {
		text = strings.TrimSpace(m.Title + "\n" + text)
	}
	text = ttPostLimits.fitText(text, m.Tags)
	if err := pasteHTML(editor, paragraphsHTML(text), text); err != nil {
		return err
	}
	for i, tag := range m.Tags {
		if i == ttPostLimits.Tags {
			break
		}
		if err := ttTopic(ctx, page, editor, tag); err != nil {
			return err
		}
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote)
		return nil
	}
	return ttSubmit(ctx, page, j)
}

// ttImages adds pictures to the micro post through its picture panel,
// which takes 18 at most.
func ttImages(ctx context.Context, page *rod.Page, j Job, images []string) error {
	if len(images) > 18 {
		images = images[:18]
	}
	j.Report("上传图片")
	if err := clickVisible(ctx, page, "button.syl-toolbar-button", "^图片$", 10*time.Second); err != nil {
		return err
	}
	in, err := find(page, `.upload-image-panel input[type="file"]`, 10*time.Second)
	if err != nil {
		return errors.New("图片上传框没有打开")
	}
	if err := in.SetFiles(images); err != nil {
		return err
	}
	want := fmt.Sprintf("已上传 %d 张图片", len(images))
	for deadline := time.Now().Add(2 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`)
		if err == nil && strings.Contains(res.Value.Str(), want) {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("图片没有全部上传成功（应为 %d 张）", len(images))
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := clickVisible(ctx, page, `[class*="drawer"] button`, "^确定$", 10*time.Second); err != nil {
		return err
	}
	for deadline := time.Now().Add(20 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`)
		if err == nil && strings.Contains(res.Value.Str(), fmt.Sprintf("共 %d 张", len(images))) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("图片没有加到微头条里")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// ttTopic adds the topic tag at the end of the micro post, through the
// topic picker: the topic of that name it finds, or else the first. A
// topic it finds none for is left out.
func ttTopic(ctx context.Context, page *rod.Page, editor *rod.Element, tag string) error {
	if err := typeInto(page, editor, " "); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, "button.syl-toolbar-button", "^话题$", 10*time.Second); err != nil {
		return err
	}
	search, err := find(page, `section.mention-tooltip-modal input[placeholder="搜索话题"]`, 10*time.Second)
	if err != nil {
		return errors.New("话题选择框没有打开")
	}
	if err := fill(page, search, tag); err != nil {
		return err
	}
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		res, err := page.Timeout(evalTimeout).Eval(`(tag) => {
			const items = [...document.querySelectorAll("section.mention-tooltip-modal .forum-list-item")].filter((e) => e.offsetParent);
			const name = (e) => e.querySelector(".forum-list-item-text")?.textContent.trim();
			const el = items.find((e) => name(e) === tag) ?? items[0];
			if (el) el.click();
			return !!el;
		}`, tag)
		if err == nil && res.Value.Bool() {
			return sleep(ctx, 500*time.Millisecond)
		}
	}
	return page.Keyboard.Type(input.Escape)
}

// ttSubmit publishes the filled form.
func ttSubmit(ctx context.Context, page *rod.Page, j Job) error {
	j.Report("提交发布")
	if err := clickVisible(ctx, page, "button.byte-btn-primary", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"/manage", "/content"}, "发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功")
}
