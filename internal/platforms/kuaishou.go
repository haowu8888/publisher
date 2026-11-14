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

// publishKuaishou uploads a video through the creator platform. Kuaishou
// has no title field, so the title leads the description. Selectors as of
// November 2026.
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
	// A video left unpublished by an earlier try is offered above the
	// upload, to go on editing (还有上次未发布的视频): declined, so the
	// form is this video's alone.
	if err := sleep(ctx, time.Second); err != nil {
		return err
	}
	page.Timeout(evalTimeout).Eval(`() => document.querySelector('[class*="_draft-bar_"] [class*="_renounce-btn_"]')?.click()`)
	const editorSel = `div[contenteditable="true"][placeholder*="作品描述"]`
	if err := startUpload(ctx, page, fileSel, "cp.kuaishou.com", m.VideoPath, editorSel); err != nil {
		return err
	}

	j.Report("填写描述和话题")
	editor, err := waitElement(ctx, page, editorSel, "cp.kuaishou.com")
	if err != nil {
		return err
	}
	dismissGuides(page)
	text := strings.TrimSpace(m.Description)
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

	if err := waitUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, kuaishouCover)
	if err != nil {
		return err
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
