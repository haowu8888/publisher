package platforms

import (
	"context"
	"fmt"
	"strings"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
)

// publishKuaishou uploads a video, or an article's pictures as an image
// post (图文) with the article as its text, through the creator platform.
// Kuaishou has no title field, so the title leads the description.
// Selectors as of November 2026.
func publishKuaishou(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://cp.kuaishou.com/article/publish/video", j)
	if err != nil {
		return err
	}
	const fileSel = `input[type="file"][accept*="video"]`
	if _, err := waitElement(ctx, page, fileSel, "cp.kuaishou.com"); err != nil {
		return err
	}
	if err := kuaishouNoDraft(ctx, page); err != nil {
		return err
	}
	editorSel := `div[contenteditable="true"][placeholder*="作品描述"]`
	text := strings.TrimSpace(m.Description)
	if m.Kind == store.KindArticle {
		editorSel = `div[contenteditable="true"][class*="_description_"]`
		if err := kuaishouImages(ctx, page, j, editorSel); err != nil {
			return err
		}
		if text == "" {
			text = paragraphs(articleBody(m))
		}
	} else if err := startUpload(ctx, page, fileSel, "cp.kuaishou.com", m.VideoPath, editorSel); err != nil {
		return err
	}

	j.Report("填写描述和话题")
	editor, err := waitElement(ctx, page, editorSel, "cp.kuaishou.com")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if !strings.Contains(text, strings.TrimSpace(m.Title)) {
		text = strings.TrimSpace(m.Title + "\n" + text)
	}
	if err := typeInto(page, editor, kuaishouLimits.fitText(text, m.Tags)+" "); err != nil {
		return err
	}
	for i, tag := range m.Tags {
		if i == kuaishouLimits.Tags { // Kuaishou takes at most 4 topics.
			break
		}
		if err := typeInto(page, editor, "#"+tag+" "); err != nil {
			return err
		}
		if err := sleep(ctx, 800*time.Millisecond); err != nil {
			return err
		}
	}

	// An image post's cover is its first picture, the material's cover.
	note := ""
	if m.Kind == store.KindVideo {
		if err := waitUploaded(ctx, page, j); err != nil {
			return err
		}
		if note, err = setCover(ctx, page, j, kuaishouCover); err != nil {
			return err
		}
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	j.Report("提交发布")
	if err := clickText(page, "div, button", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"/manage"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// kuaishouNoDraft declines what an earlier try left unpublished, which the
// upload tab offers to go on editing (还有上次未发布的视频/图集), so the
// form is this material's alone: given files would join the draft's.
func kuaishouNoDraft(ctx context.Context, page *rod.Page) error {
	if err := sleep(ctx, time.Second); err != nil {
		return err
	}
	page.Timeout(evalTimeout).Eval(`() => {
		const bar = [...document.querySelectorAll('[class*="_draft-bar_"]')].find((e) => e.offsetParent);
		[...(bar?.querySelectorAll("*") ?? [])].find((e) => e.children.length === 0 && e.textContent.trim() === "放弃")?.click();
	}`)
	return nil
}

// kuaishouImages starts an image post of an article's pictures, the cover
// first, from the 上传图文 tab. Kuaishou takes 31 at most. The upload takes
// files only after its button was clicked, which opens no file chooser
// from a script.
func kuaishouImages(ctx context.Context, page *rod.Page, j Job, editorSel string) error {
	images := noteImages(j.Material)
	if len(images) > 31 {
		images = images[:31]
	}
	j.Report("上传图片")
	if err := clickVisible(ctx, page, ".ant-tabs-tab-btn", "^上传图文$", 10*time.Second); err != nil {
		return err
	}
	const sel = `input[type="file"][accept*="image"]`
	if _, err := waitElement(ctx, page, sel, "cp.kuaishou.com"); err != nil {
		return err
	}
	if err := kuaishouNoDraft(ctx, page); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, "button", "^上传图片$", 10*time.Second); err != nil {
		return err
	}
	in, err := page.Element(sel)
	if err != nil {
		return err
	}
	if err := in.SetFiles(images); err != nil {
		return err
	}
	if _, err := waitElement(ctx, page, editorSel, "cp.kuaishou.com"); err != nil {
		return err
	}
	want := fmt.Sprintf("%d/31", len(images))
	for deadline := time.Now().Add(2 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`)
		if err == nil && strings.Contains(res.Value.Str(), want) && !strings.Contains(res.Value.Str(), "上传中") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("图片没有全部上传成功（应为 %d 张）", len(images))
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// kuaishouCover sets the cover to the picture at path, whole as it is,
// through the cover editor's 上传封面 tab.
func kuaishouCover(ctx context.Context, page *rod.Page, path string) error {
	entry, err := find(page, `[class*="_cover-full-editor_"]`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到「封面设置」: %w", err)
	}
	if err := jsClick(entry); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, `.ant-modal [class*="_header-title-item_"]`, "^上传封面$", 15*time.Second); err != nil {
		return err
	}
	in, err := find(page, `.ant-modal input[type="file"][accept*="image"]`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到封面的上传入口: %w", err)
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// The editor offers to clear the picture once it has it, a moment
	// before it has drawn it: confirmed by then, the cover comes out black.
	if _, err := findR(page, ".ant-modal *", "^清空上传$", 20*time.Second); err != nil {
		return fmt.Errorf("封面图片没有传上去: %w", err)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const c = document.querySelector('.ant-modal canvas[class*="_cutter-raw_"]');
			const d = c?.getContext("2d")?.getImageData(0, 0, c.width, c.height).data ?? [];
			for (let i = 0; i < d.length; i += 4 * 97) if (d[i] > 48 || d[i + 1] > 48 || d[i + 2] > 48) return true;
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			break
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if err := sleep(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	// Ant Design spaces out a button's two characters.
	if err := clickVisible(ctx, page, ".ant-modal button", `^确\s*认$`, 10*time.Second); err != nil {
		return err
	}
	return waitGone(ctx, page, ".ant-modal", 30*time.Second)
}
