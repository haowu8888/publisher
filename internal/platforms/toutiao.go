package platforms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

// publishToutiao publishes an article as a micro post (微头条) of its text
// and pictures, through Toutiao's creator platform (头条号). Videos go to
// Toutiao with Douyin's 同时发布到今日头条 (douyinSync) instead.
// Selectors as of November 2026.
func publishToutiao(ctx context.Context, b *browser.Browser, j Job) error {
	return ttPost(ctx, b, j)
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
	// An account whose details are not completed may post 微头条 but not
	// videos or articles: 发布 then only opens a dialog saying so. Should
	// micro posts come to need them too, this says why.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if res, err := page.Timeout(evalTimeout).Eval(bodyText); err == nil && strings.Contains(res.Value.Str(), "账号信息未完善") {
			return errors.New("头条号账号信息未完善，暂时不能发布：请在头条号后台点「立即完善」，用今日头条 App「我的」-设置-扫一扫完成后重试")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	// A posted 微头条 goes to the list of them, profile_v4/weitoutiao,
	// whose URL the form's (…/weitoutiao/publish) contains, so it must end
	// it; a dialog asking to sync works to other apps may cover the list.
	if err := waitPublished(ctx, page, []string{"/manage", "/content", "/weitoutiao$"}, "发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功")
}
