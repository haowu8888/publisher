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
)

// publishDouyin uploads a video, or an article's pictures as an image post
// (图文) with the article as its text, through the creator center. After
// the files are chosen the page moves on to the publish form, where a
// video's upload goes on while the form is filled. Selectors as of
// November 2026.
func publishDouyin(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	limits, url := douyinLimits, "https://creator.douyin.com/creator-micro/content/upload"
	if m.Kind == store.KindArticle {
		limits, url = douyinImageLimits, url+"?default-tab=3"
	}
	page, err := openForm(ctx, b, url, j)
	if err != nil {
		return err
	}
	const titleSel = `input[placeholder*="作品标题"]`
	desc := m.Description
	if m.Kind == store.KindArticle {
		if err := douyinImages(ctx, page, j, titleSel); err != nil {
			return err
		}
		if desc == "" {
			desc = paragraphs(articleBody(m))
		}
	} else if err := startUpload(ctx, page, `input[type="file"]`, "creator-micro", m.VideoPath, titleSel); err != nil {
		return err
	}

	j.Report("填写标题")
	title, err := waitElement(ctx, page, titleSel, "creator-micro")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if err := fill(page, title, fitTitle(m.Title, limits.Title)); err != nil {
		return err
	}

	j.Report("填写简介和话题")
	editor, err := find(page, `div.zone-container[contenteditable="true"]`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到简介输入框: %w", err)
	}
	if desc != "" {
		if err := typeInto(page, editor, limits.fitText(desc, m.Tags)+" "); err != nil {
			return err
		}
	}
	for i, tag := range m.Tags {
		if i == limits.Tags {
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
		if err := douyinCollection(ctx, page, m.Kind, j.Collection); err != nil {
			return err
		}
	}

	// An image post's cover is its first picture, the material's cover.
	note := ""
	if m.Kind == store.KindVideo {
		if err := douyinNoSync(ctx, page); err != nil {
			return err
		}
		if err := waitUploaded(ctx, page, j); err != nil {
			return err
		}
		if note, err = setCover(ctx, page, j, douyinCover); err != nil {
			return err
		}
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

// douyinNoSync picks not to post the video to Toutiao too (同时发布到今日头条,
// on by default), which is published to on its own. Accounts not linked to
// Toutiao have no such choice.
func douyinNoSync(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(5 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const off = [...document.querySelectorAll("label[class*=radio-item]")].find((e) => e.textContent.trim() === "不同时发布");
			if (!off) return false;
			if (off.dataset.checked !== "true") off.click();
			return off.dataset.checked === "true";
		}`)
		if err != nil {
			return err
		}
		if res.Value.Bool() || time.Now().After(deadline) {
			return nil
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// douyinCollection adds the post to the user's collection (合集) name.
// The image post's form has a select of its own.
func douyinCollection(ctx context.Context, page *rod.Page, kind store.Kind, name string) error {
	box, option := `[class*="select-collection"]`, ".collection-option"
	if kind == store.KindArticle {
		box, option = `[class*="mix-sel-wrap"] .semi-select:not([class*="select-mix-type"])`, ".semi-select-option"
	}
	sel, err := find(page, box, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到合集选择框: %w", err)
	}
	if err := jsClick(sel); err != nil {
		return err
	}
	return pickByName(ctx, page, option, `[class*="option-title"]`, name, "合集", 10*time.Second)
}

// douyinProgress matches the image upload's progress: a percentage and
// the count of pictures uploaded, as in "37%" and "1/4".
var douyinProgress = regexp.MustCompile(`\d+(\.\d+)?%|\d+/\d+`)

// douyinImages starts an image post of an article's pictures, the cover
// first, and waits until they are all uploaded. Douyin takes 35 at most.
func douyinImages(ctx context.Context, page *rod.Page, j Job, titleSel string) error {
	images := noteImages(j.Material)
	if len(images) > 35 {
		images = images[:35]
	}
	j.Report("上传图片")
	const sel = `input[type="file"][accept^="image"]`
	if _, err := waitElement(ctx, page, sel, "creator-micro"); err != nil {
		return err
	}
	// The widget ignores files given before its script is ready.
	if err := sleep(ctx, 2*time.Second); err != nil {
		return err
	}
	in, err := page.Element(sel)
	if err != nil {
		return err
	}
	if err := in.SetFiles(images); err != nil {
		return err
	}
	if _, err := waitElement(ctx, page, titleSel, "creator-micro"); err != nil {
		return err
	}
	// Uploads can take minutes on a slow line, so give up only when the
	// progress (the current picture's percentage, "1/4") stops moving.
	want := fmt.Sprintf("已添加%d张图片", len(images))
	var progress string
	failed := -1 // failure messages the page already showed
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`)
		if err == nil {
			body := res.Value.Str()
			if strings.Contains(body, want) && !strings.Contains(body, "上传中") {
				return nil
			}
			if n := len(uploadFailed.FindAllString(body, -1)); failed < 0 {
				failed = n
			} else if n > failed {
				return errors.New("图片上传失败，请在浏览器中查看页面提示后重试")
			}
			if p := strings.Join(douyinProgress.FindAllString(body, -1), " "); p != progress {
				progress, deadline = p, time.Now().Add(3*time.Minute)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("图片上传 3 分钟没有进展，没有全部上传成功（应为 %d 张）", len(images))
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// checkPictures refuses an article without pictures on platforms that
// post articles as pictures (图文): Douyin and Kuaishou.
func checkPictures(m store.Material, mode store.Mode) error {
	if m.Kind == store.KindArticle && len(noteImages(m)) == 0 {
		return errors.New("抖音、快手的文章发成图文，需要至少一张图片（封面或文章图片）")
	}
	return nil
}

// reviewNote is the last step of a review mode publish.
const reviewNote = "已填写完成，请在浏览器中检查后点「发布」"
