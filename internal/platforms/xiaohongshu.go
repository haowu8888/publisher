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

const xhsPublishURL = "https://creator.xiaohongshu.com/publish/publish?source=official&target="

// publishXiaohongshu publishes a video as a video note, an article with
// pictures as an image note (上传图文) and one without as a long article
// (写长文), which Xiaohongshu lays out as image cards. All end on the same
// note form. Selectors as of November 2026.
func publishXiaohongshu(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	var page *rod.Page
	var err error
	images := noteImages(m)
	switch {
	case m.Kind == store.KindVideo:
		page, err = xhsVideo(ctx, b, j)
	case len(images) > 0:
		page, err = xhsImages(ctx, b, j, images)
	default:
		page, err = xhsArticle(ctx, b, j)
	}
	if err != nil {
		return err
	}

	j.Report("填写笔记标题和正文")
	title, err := waitElement(ctx, page, `input[placeholder*="填写标题"]`, "creator.xiaohongshu.com")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if err := fill(page, title, fitTitle(m.Title, xhsLimits.Title)); err != nil {
		return err
	}
	editor, err := find(page, `div.tiptap.ProseMirror`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到正文输入框: %w", err)
	}
	desc := m.Description
	switch {
	case m.Kind == store.KindVideo:
	case len(images) > 0:
		// The note's text is the article, a paragraph per line.
		if desc == "" {
			desc = paragraphs(articleBody(m))
		}
	case desc == "":
		desc = truncate(plainText(articleBody(m)), 200)
	}
	desc = xhsLimits.fitText(desc, m.Tags)
	if m.Kind == store.KindArticle && len(images) > 0 {
		if err := pasteHTML(editor, paragraphsHTML(desc), desc); err != nil {
			return err
		}
	} else if desc != "" {
		if err := fill(page, editor, desc); err != nil {
			return err
		}
	}
	for i, tag := range m.Tags {
		if i == xhsLimits.Tags {
			break
		}
		// Typing "#话题" offers matching topics; Enter takes the first.
		if err := typeInto(page, editor, " #"+tag); err != nil {
			return err
		}
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

	if j.Collection != "" {
		j.Report("加入合集")
		if err := xhsCollection(ctx, page, j.Collection); err != nil {
			return err
		}
	}
	skipped := ""
	if j.Quote != "" {
		j.Report("引用笔记")
		if err := xhsQuote(ctx, page, j.Quote); err != nil {
			// The note publishes as well without.
			skipped += "（没引用笔记：" + err.Error() + "）"
		}
	}
	// An image note's cover is its first picture; a video's is set in
	// the cover editor, which shows the video once it is uploaded.
	if m.Kind == store.KindVideo {
		if err := waitUploaded(ctx, page, j); err != nil {
			return err
		}
		if skipped, err = setCover(ctx, page, j, xhsCover); err != nil {
			return err
		}
	}
	// The declaration needs the video's length, known once it is uploaded.
	if j.Original {
		j.Report("声明原创")
		if ok, err := xhsOriginal(ctx, page); err != nil {
			return err
		} else if !ok {
			skipped += "（这篇笔记不能声明原创，如视频短于 10 秒，已跳过）"
		}
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + skipped)
		return nil
	}
	j.Report("提交发布")
	if err := xhsSubmit(page); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"success", "published=true"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+skipped)
}

// xhsCollection adds the note to the user's collection (合集) name.
func xhsCollection(ctx context.Context, page *rod.Page, name string) error {
	btn, err := find(page, ".collection-plugin-button", 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到「加入合集」: %w", err)
	}
	if err := jsClick(btn); err != nil {
		return err
	}
	return pickByName(ctx, page, ".collection-plugin-popover .item", ".item-label", name, "合集", 10*time.Second)
}

// xhsQuote quotes (引用笔记) the account's note titled title: the card
// opens 选择笔记 on the account's notes, newest first, more of which load
// as the list is scrolled to its end; 确认引用 takes the one clicked.
func xhsQuote(ctx context.Context, page *rod.Page, title string) error {
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const card = document.querySelector(".quote-note-container .setting-card-content");
		card?.click();
		return !!card;
	}`)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return errors.New("发布页没有「引用笔记」")
	}
	const modal = `.select-note-modal`
	found := false
	for last, still, deadline := -1, 0, time.Now().Add(time.Minute); !found; {
		res, err := page.Timeout(evalTimeout).Eval(`(modal, title) => {
			const m = document.querySelector(modal);
			if (!m?.getClientRects().length) return -1;
			const card = [...m.querySelectorAll(".note-card")]
				.find((c) => c.querySelector(".note-card__title")?.textContent.trim() === title);
			if (card) {
				if (!card.classList.contains("note-card--selected")) card.click();
				return -2;
			}
			const list = m.querySelector(".select-note-modal__list-wrap");
			if (list) list.scrollTop = list.scrollHeight;
			return m.querySelectorAll(".note-card").length;
		}`, modal, title)
		if err != nil {
			return err
		}
		switch n := res.Value.Int(); {
		case n == -2:
			found = true
			continue
		case n == last && n >= 0:
			still++
		default:
			last, still = n, 0
		}
		// Nothing more has loaded for a while: the list is all there.
		if still >= 4 || time.Now().After(deadline) {
			page.Timeout(evalTimeout).Eval(`(modal) => [...document.querySelectorAll(modal + " button")].find((b) => b.textContent.trim() === "取消")?.click()`, modal)
			if last < 0 {
				return errors.New("选择笔记的对话框没出来")
			}
			return fmt.Errorf("在自己的 %d 篇笔记里没找到「%s」", last, title)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := clickUntilGone(ctx, page, modal+` button`, `^确认引用$`, time.Second, 10*time.Second); err != nil {
		return fmt.Errorf("选择笔记的对话框%w", err)
	}
	res, err = page.Timeout(evalTimeout).Eval(`() => document.querySelector(".quote-note-container__middle")?.textContent ?? ""`)
	if err != nil {
		return err
	}
	if !strings.Contains(res.Value.Str(), title) {
		return errors.New("引用没有生效")
	}
	return nil
}

// xhsOriginal turns on 原创声明, agreeing to its terms when asked. It
// reports false when Xiaohongshu does not offer it for the note, as for a
// video under 10 seconds.
func xhsOriginal(ctx context.Context, page *rod.Page) (bool, error) {
	card, err := findR(page, ".custom-switch-card", "原创声明", 10*time.Second)
	if err != nil {
		return false, fmt.Errorf("未找到「原创声明」: %w", err)
	}
	sw, err := card.Element(".d-switch")
	if err != nil {
		return false, fmt.Errorf("未找到「原创声明」开关: %w", err)
	}
	if cls, _ := sw.Attribute("class"); cls != nil && strings.Contains(*cls, "disabled") {
		return false, nil
	}
	if err := jsClick(sw); err != nil {
		return false, err
	}
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const card = [...document.querySelectorAll(".custom-switch-card")].find((c) => c.textContent.includes("原创声明"));
			if (card?.querySelector(".d-switch-simulator.checked")) return "on";
			const box = document.querySelector(".originalContainer");
			if (!box?.offsetParent) return "";
			const agree = box.querySelector(".d-checkbox");
			if (agree?.querySelector(".unchecked")) agree.click();
			const btn = [...box.querySelectorAll("button")].find((b) => b.textContent.trim() === "声明原创");
			if (btn && !btn.disabled) btn.click();
			return "";
		}`)
		if err == nil && res.Value.Str() == "on" {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, errors.New("没能开启原创声明，请在浏览器中查看页面提示")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return false, err
		}
	}
}

// xhsCover sets a video note's cover to the picture at path, through the
// cover editor's upload.
func xhsCover(ctx context.Context, page *rod.Page, path string) error {
	entry, err := find(page, `.cover-plugin-preview .cover-edit-entry`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到「编辑封面」: %w", err)
	}
	if err := jsClick(entry); err != nil {
		return err
	}
	in, err := find(page, `.cover-modal input.upload-input`, 15*time.Second)
	if err != nil {
		return fmt.Errorf("封面编辑框没有打开: %w", err)
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if _, err := find(page, `.cover-modal .uploaded-thumbnail`, 20*time.Second); err != nil {
		return fmt.Errorf("封面图片没有传上去: %w", err)
	}
	// 完成 turns into a spinner while the cover is stored, which can take
	// most of a minute. Still reading 完成, it was not taken: the editor
	// ignores it while it lays out the picture.
	for deadline := time.Now().Add(2 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const modal = document.querySelector(".cover-modal");
			if (!modal) return true;
			const done = [...modal.querySelectorAll("button")].find((b) => b.offsetParent && b.textContent.trim() === "完成");
			const busy = modal.querySelector(".cover-choose-container")?.getAttribute("aria-busy") === "true";
			if (done && !done.disabled && !busy) done.click();
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面编辑框没有关闭")
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

// xhsVideo starts a video note.
func xhsVideo(ctx context.Context, b *browser.Browser, j Job) (*rod.Page, error) {
	page, err := openForm(ctx, b, xhsPublishURL+"video", j)
	if err != nil {
		return nil, err
	}
	return page, startUpload(ctx, page, `input.upload-input`, "creator.xiaohongshu.com/publish", j.Material.VideoPath, `input[placeholder*="填写标题"]`)
}

// xhsImages starts an image note of an article's pictures, the cover
// first. Xiaohongshu takes 18 at most.
func xhsImages(ctx context.Context, b *browser.Browser, j Job, images []string) (*rod.Page, error) {
	if len(images) > 18 {
		images = images[:18]
	}
	page, err := openForm(ctx, b, xhsPublishURL+"image", j)
	if err != nil {
		return nil, err
	}
	j.Report("上传图片")
	if err := startUpload(ctx, page, `input.upload-input`, "creator.xiaohongshu.com/publish", images[0], `input[placeholder*="填写标题"]`); err != nil {
		return nil, err
	}
	if len(images) > 1 {
		// The note form's own input adds pictures to those there.
		in, err := find(page, `input[type=file][multiple]`, 15*time.Second)
		if err != nil {
			return nil, fmt.Errorf("未找到添加图片的入口: %w", err)
		}
		if err := in.SetFiles(images[1:]); err != nil {
			return nil, err
		}
	}
	// The count beside 图片编辑 reads "n/18" once they are all in.
	want := fmt.Sprintf("%d/18", len(images))
	for deadline := time.Now().Add(2 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".title-area .status")?.textContent.trim() ?? ""`)
		// Each picture is busy until uploaded; a note published before
		// then does not go out.
		if err == nil && res.Value.Str() == want {
			busy, err := page.Timeout(evalTimeout).Eval(`() => !!document.querySelector('.img-list .img-container[aria-busy="true"]')`)
			if err == nil && !busy.Value.Bool() {
				break
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("图片没有全部上传成功（应为 %s）", want)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	return page, nil
}

// xhsArticle writes a long article, lays it out with the default template
// and moves on to the note form.
func xhsArticle(ctx context.Context, b *browser.Browser, j Job) (*rod.Page, error) {
	m := j.Material
	page, err := openForm(ctx, b, xhsPublishURL+"article", j)
	if err != nil {
		return nil, err
	}
	if _, err := waitElement(ctx, page, `span.title`, "creator.xiaohongshu.com/publish"); err != nil {
		return nil, err
	}
	if err := clickText(page, "button, div, span", "^新的创作$", 15*time.Second); err != nil {
		return nil, err
	}

	j.Report("填写长文标题和正文")
	title, err := find(page, `textarea[placeholder="输入标题"]`, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("未找到长文标题框: %w", err)
	}
	if err := fill(page, title, fitTitle(m.Title, 64)); err != nil {
		return nil, err
	}
	body, err := find(page, `div.tiptap.ProseMirror`, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("未找到长文正文框: %w", err)
	}
	if err := pasteHTML(body, markdownHTML(m.Body), m.Body); err != nil {
		return nil, err
	}
	if err := sleep(ctx, time.Second); err != nil {
		return nil, err
	}

	j.Report("排版长文")
	if err := clickText(page, "button", "^一键排版$", 10*time.Second); err != nil {
		return nil, err
	}
	if err := clickText(page, "button", "^下一步$", 30*time.Second); err != nil {
		return nil, err
	}
	return page, nil
}

// xhsSubmit clicks 发布. The button sits in the closed shadow root of an
// xhs-publish-btn element, out of reach of page scripts and of a search by
// text, so it goes through DevTools, which sees into closed roots.
func xhsSubmit(page *rod.Page) error {
	host, err := find(page, "xhs-publish-btn", 10*time.Second)
	if err != nil {
		return clickText(page, "button, div", "^发布$", 5*time.Second)
	}
	if v, _ := host.Attribute("submit-disabled"); v != nil && *v == "true" {
		return errors.New("「发布」按钮不可用，请在浏览器中查看页面提示")
	}
	root, err := host.ShadowRoot()
	if err != nil {
		return fmt.Errorf("未找到「发布」按钮: %w", err)
	}
	btn, err := root.Element("button.bg-red")
	if err != nil {
		return fmt.Errorf("未找到「发布」按钮: %w", err)
	}
	return jsClick(btn)
}
