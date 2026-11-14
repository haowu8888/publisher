package platforms

import (
	"context"
	"errors"
	"fmt"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
)

// publishDouyin uploads a video through the creator center. After the file
// is chosen the page moves on to the publish form, where the upload goes
// on while the form is filled. Selectors as of November 2026.
func publishDouyin(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://creator.douyin.com/creator-micro/content/upload", j)
	if err != nil {
		return err
	}
	const titleSel = `input[placeholder*="作品标题"]`
	if err := startUpload(ctx, page, `input[type="file"]`, "creator-micro", m.VideoPath, titleSel); err != nil {
		return err
	}

	j.Report("填写标题")
	title, err := waitElement(ctx, page, titleSel, "creator-micro")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if err := fill(page, title, fitTitle(m.Title, douyinLimits.Title)); err != nil {
		return err
	}

	j.Report("填写简介和话题")
	editor, err := find(page, `div.zone-container[contenteditable="true"]`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到简介输入框: %w", err)
	}
	if m.Description != "" {
		if err := typeInto(page, editor, douyinLimits.fitText(m.Description, m.Tags)+" "); err != nil {
			return err
		}
	}
	for i, tag := range m.Tags {
		if i == douyinLimits.Tags {
			break
		}
		// A space after "#话题" turns it into a topic.
		if err := typeInto(page, editor, "#"+tag+" "); err != nil {
			return err
		}
		if err := sleep(ctx, 800*time.Millisecond); err != nil {
			return err
		}
	}

	if j.Collection != "" {
		j.Report("添加到合集")
		if err := douyinCollection(ctx, page, j.Collection); err != nil {
			return err
		}
	}

	if err := waitUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, douyinCover)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	j.Report("提交发布")
	if err := clickText(page, "button", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"content/manage"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// douyinCover sets the vertical cover (竖封面 3:4) to the picture at
// path, through the cover editor's upload. On 完成 the editor offers to
// set the horizontal cover too, which is declined: Douyin makes one of the
// vertical cover.
func douyinCover(ctx context.Context, page *rod.Page, path string) error {
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const tip = [...document.querySelectorAll('[class*="cover-tip"]')].find((e) => e.textContent.includes("竖封面"));
		const entry = tip?.parentElement?.parentElement?.querySelector('[class*="filter-"]');
		if (entry) entry.click();
		return !!entry;
	}`)
	if err != nil || !res.Value.Bool() {
		return errors.New("未找到竖封面的「选择封面」")
	}
	const upload = `.semi-upload[class*="upload-"] input.semi-upload-hidden-input`
	in, err := find(page, upload, 15*time.Second)
	if err != nil {
		return fmt.Errorf("封面编辑框没有打开: %w", err)
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// The upload button shows the picture once the editor has taken it.
	if _, err := find(page, `[class*="upload-"] > [class*="bg-"][style*="background-image"]`, 20*time.Second); err != nil {
		return fmt.Errorf("封面图片没有传上去: %w", err)
	}
	if err := clickVisible(ctx, page, "button", "^完成$", 10*time.Second); err != nil {
		return err
	}
	for deadline := time.Now().Add(20 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(upload) => {
			const later = [...document.querySelectorAll("button")].find((b) => b.offsetParent && b.textContent.trim() === "暂不设置");
			if (later) later.click();
			return !document.querySelector(upload);
		}`, upload)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面编辑框没有关闭")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// douyinCollection adds the video to the user's collection (合集) name.
func douyinCollection(ctx context.Context, page *rod.Page, name string) error {
	sel, err := find(page, `[class*="select-collection"]`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到合集选择框: %w", err)
	}
	if err := jsClick(sel); err != nil {
		return err
	}
	return pickByName(ctx, page, ".collection-option", `[class*="option-title"]`, name, "合集", 10*time.Second)
}

// reviewNote is the last step of a review mode publish.
const reviewNote = "已填写完成，请在浏览器中检查后点「发布」"
