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

// publishSohu publishes through Sohu Video's creator center (搜狐视频创作中心):
// a video, or an article as an image post (图文), which has no title: the
// title goes first in its text, as on Toutiao's micro posts. Both forms
// are tiptap editors and Vue widgets. Selectors as of November 2026.
func publishSohu(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return sohuPost(ctx, b, j)
	}
	return sohuVideo(ctx, b, j)
}

const sohuCenter = "https://tv.sohu.com/s/center/index.html#/"

// sohuStatements are Sohu's statements (创作内容声明), by the material's
// statements, which are in Bilibili's words; the others read the same.
var sohuStatements = map[string]string{
	"内容无需标注": "无需标注",
}

// sohuVideo uploads a video. The form shows once the file is chosen, and
// is filled while the video uploads.
func sohuVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, sohuCenter, j)
	if err != nil {
		return err
	}
	const titleSel = `.form-item-title-video .ProseMirror`
	if err := startUpload(ctx, page, `input[type="file"][accept^="video"]`, "s/center", m.VideoPath, titleSel); err != nil {
		return err
	}
	dismissGuides(page)
	j.Report("填写标题")
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, sohuVideoLimits.Title)); err != nil {
		return err
	}
	if err := sohuVideoTopics(ctx, page, j); err != nil {
		return err
	}
	if m.Description != "" {
		j.Report("填写简介")
		desc, err := find(page, `textarea.textarea-inner`, 10*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(m.Description, sohuVideoLimits.Text)); err != nil {
			return err
		}
	}
	if err := sohuStatement(page, m.Statement); err != nil {
		return err
	}
	if err := sohuUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, sohuCover)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return sohuSubmit(ctx, page, j, "^发布$", note)
}

// sohuVideoTopics types the tags into the topic box, each ended with Enter.
// Sohu takes 5 at most, of 100 characters in all.
func sohuVideoTopics(ctx context.Context, page *rod.Page, j Job) error {
	if len(j.Material.Tags) == 0 {
		return nil
	}
	j.Report("填写话题")
	in, err := find(page, `.subjectComponents input.input-topic`, 10*time.Second)
	if err != nil {
		return errors.New("未找到话题输入框")
	}
	n := 0
	for i, t := range j.Material.Tags {
		if i == 5 || n+utf8.RuneCountInString(t) > 100 {
			break
		}
		n += utf8.RuneCountInString(t)
		if err := focus(in); err != nil {
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
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// sohuStatement picks the material's statement; a material without one
// gets 无需标注, which the form asks for.
func sohuStatement(page *rod.Page, statement string) error {
	if statement == "" {
		statement = "内容无需标注"
	}
	if s, ok := sohuStatements[statement]; ok {
		statement = s
	}
	res, err := page.Timeout(evalTimeout).Eval(`async (s) => {
		const item = [...document.querySelectorAll(".radio_item")].find((i) => i.textContent.trim() === s);
		if (!item) return "创作内容声明里没有「" + s + "」";
		const on = () => item.querySelector(".radio_selected");
		if (!on()) item.click();
		await new Promise((r) => setTimeout(r, 300));
		return on() ? "" : "创作内容声明「" + s + "」没选上";
	}`, statement)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// sohuProgress reads the upload bar's width, as "45%".
var sohuProgress = regexp.MustCompile(`width:\s*(\d{1,3}(?:\.\d+)?%)`)

// sohuUploaded waits until the video is uploaded: the form then says 上传
// 成功 by the cover.
func sohuUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => ({
			text: document.body.innerText,
			done: !!document.querySelector(".detail-status.status-suc"),
			bar: document.querySelector(".upInfo-progress .progress-bar")?.getAttribute("style") ?? "",
		})`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		if err := watch.check(page, res.Value.Get("text").Str()); err != nil {
			return err
		}
		if res.Value.Get("done").Bool() {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if m := sohuProgress.FindStringSubmatch(res.Value.Get("bar").Str()); m != nil {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", m[1], time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// sohuCover sets the video's cover: 修改封面 → the dialog's picture input →
// once it shows the upload, 确认. It is not cropped.
func sohuCover(ctx context.Context, page *rod.Page, path string) error {
	const dialog = `.alertBox-modifyCover`
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		window.publisherOldCover = document.querySelector(".video-pic img")?.src ?? "";
		document.querySelector(".video-pic .pic-edit").click();
	}`); err != nil {
		return err
	}
	in, err := find(page, dialog+` input.uploadImg`, 10*time.Second)
	if err != nil {
		return errors.New("封面对话框没出来")
	}
	if _, err := page.Timeout(evalTimeout).Eval(`(d) => { window.publisherOldPhoto = document.querySelector(d + " img.photo")?.src ?? "" }`, dialog); err != nil {
		return err
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(d) => {
			const img = document.querySelector(d + " img.photo");
			return !!img && img.src !== window.publisherOldPhoto && img.complete && img.naturalWidth > 0;
		}`, dialog)
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
	if err := clickUntilGone(ctx, page, dialog+` .btns a`, `^确认$`, 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面对话框%w", err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const src = document.querySelector(".video-pic img")?.src;
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

// sohuPost writes an article as an image post: its pictures (the cover
// first, 9 at most), its title and body as the text, and its tags as
// topics.
func sohuPost(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, sohuCenter+"upload/topic", j)
	if err != nil {
		return err
	}
	editor, err := waitElement(ctx, page, `#tiptap-editor .ProseMirror`, "s/center")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if images := noteImages(m); len(images) > 0 {
		if err := sohuImages(ctx, page, j, images); err != nil {
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
	text = sohuPostLimits.fitText(text, m.Tags)
	if err := pasteHTML(editor, paragraphsHTML(text), text); err != nil {
		return err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length`); err != nil || res.Value.Int() == 0 {
		return errors.New("正文没填上")
	}
	for i, tag := range m.Tags {
		if i == sohuPostLimits.Tags {
			break
		}
		if err := sohuTopic(ctx, page, tag); err != nil {
			return err
		}
	}
	if err := sohuStatement(page, m.Statement); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote)
		return nil
	}
	return sohuSubmit(ctx, page, j, "^发布图文$", "")
}

// sohuImages adds the pictures, all at once, and waits until each is
// uploaded.
func sohuImages(ctx context.Context, page *rod.Page, j Job, images []string) error {
	if len(images) > 9 {
		images = images[:9]
	}
	j.Report("上传图片")
	in, err := find(page, `.form-item-image input.uploadImg`, 10*time.Second)
	if err != nil {
		return errors.New("未找到图片上传框")
	}
	if err := in.SetFiles(images); err != nil {
		return err
	}
	var watch uploadWatch
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => ({
			text: document.body.innerText,
			ok: document.querySelectorAll(".form-img-list .img-box.ok").length,
		})`)
		if err == nil {
			if err := watch.check(page, res.Value.Get("text").Str()); err != nil {
				return errors.New("图片上传失败，请在浏览器中查看页面提示后重试")
			}
			if res.Value.Get("ok").Int() >= len(images) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("图片没有全部上传成功（应为 %d 张）", len(images))
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// sohuTopic adds a topic at the end of the text: 话题 puts a "#" at the
// cursor, the tag typed after it brings suggestions, the typed one first,
// and a suggestion takes a press (mousedown), not a click().
func sohuTopic(ctx context.Context, page *rod.Page, tag string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		const ed = document.querySelector("#tiptap-editor .ProseMirror");
		ed.focus();
		const s = getSelection();
		s.selectAllChildren(ed);
		s.collapseToEnd();
		document.querySelector(".subject-btn .btn").click();
	}`); err != nil {
		return err
	}
	if err := sleep(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	if err := page.InsertText(tag); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(tag) => {
			const items = [...document.querySelectorAll(".topic-suggestion-item")].filter((i) => i.getClientRects().length);
			const item = items.find((i) => i.textContent.trim() === "#" + tag) ?? items[0];
			if (!item) return false;
			for (const t of ["mousedown", "mouseup", "click"]) item.dispatchEvent(new MouseEvent(t, { bubbles: true }));
			return true;
		}`, tag)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("话题「%s」没有候选", tag)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return sleep(ctx, 500*time.Millisecond)
}

// sohuSubmit publishes the filled form with the button reading text.
func sohuSubmit(ctx context.Context, page *rod.Page, j Job, text, note string) error {
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, ".form-button .button, span.button", text, 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"#/manage", "#/works", "#/video/list"}, "发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}
