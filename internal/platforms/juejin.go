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
)

// juejinEditor opens a new draft; the editor saves drafts as it goes.
const juejinEditor = "https://juejin.cn/editor/drafts/new?v=2"

// juejinCategory is the category (分类) articles go in, which materials do
// not have: the user's articles are on AI.
const juejinCategory = "人工智能"

// juejinTagMax is how many tags an article takes. They must be Juejin's,
// so a tag Juejin does not have is left out, and juejinCategory, a tag
// too, taken when none is in.
const juejinTagMax = 3

// juejinSummaryMin is the shortest summary (摘要) Juejin takes; it takes 100
// characters at most.
const (
	juejinSummaryMin = 50
	juejinSummaryMax = 100
)

// checkJuejin refuses, before the form is filled, an article to submit by
// itself too short for the summary Juejin requires. A review leaves that
// to the user.
func checkJuejin(m store.Material, mode store.Mode) error {
	if mode != store.ModeAuto {
		return nil
	}
	if n := utf8.RuneCountInString(juejinSummary(m)); n < juejinSummaryMin {
		return fmt.Errorf("掘金要求摘要至少 %d 字，这篇文章的简介和正文合起来只有 %d 字", juejinSummaryMin, n)
	}
	return nil
}

// juejinSummary is the material's description, or else the start of its
// body.
func juejinSummary(m store.Material) string {
	s := strings.TrimSpace(m.Description)
	if utf8.RuneCountInString(s) < juejinSummaryMin {
		s = plainText(articleBody(m))
	}
	return truncate(s, juejinSummaryMax)
}

// publishJuejin writes an article in Juejin's Markdown editor (bytemd, on
// CodeMirror). Its pictures are pasted into it as files, which it uploads
// and links by itself. The settings are in the panel 发布 opens, which a
// review leaves open for the user to submit.
func publishJuejin(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, juejinEditor, j)
	if err != nil {
		return err
	}
	title, err := waitElement(ctx, page, `input.title-input`, "juejin.cn/editor")
	if err != nil {
		return err
	}
	if _, err := find(page, `.CodeMirror`, 30*time.Second); err != nil {
		return errors.New("未找到正文编辑器")
	}
	j.Report("填写标题")
	if err := fill(page, title, fitTitle(m.Title, juejinLimits.Title)); err != nil {
		return err
	}

	j.Report("填写正文")
	body, err := articleMarkdown(m)
	if err != nil {
		return err
	}
	if _, err := page.Timeout(5*time.Minute).Eval(juejinBody, body); err != nil {
		return fmt.Errorf("填写正文失败: %w", err)
	}

	j.Report("填写发布设置")
	if err := clickVisible(ctx, page, `.publish-popup > button`, "^发布$", 10*time.Second); err != nil {
		return err
	}
	if _, err := find(page, `.publish-popup .panel .category-list`, 10*time.Second); err != nil {
		return errors.New("发布设置没有弹出")
	}
	if err := clickVisible(ctx, page, `.publish-popup .category-list .item`, "^"+juejinCategory+"$", 5*time.Second); err != nil {
		return fmt.Errorf("分类里没有「%s」", juejinCategory)
	}
	if err := juejinTags(ctx, page, m.Tags); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, juejinCover)
	if err != nil {
		return err
	}
	summary, err := find(page, `.publish-popup .summary textarea`, 5*time.Second)
	if err != nil {
		return errors.New("未找到摘要输入框")
	}
	if err := fill(page, summary, juejinSummary(m)); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后在发布设置里点「确定并发布」" + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, `.publish-popup .footer button`, "^确定并发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"/published"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// juejinBody is a script setting the editor's Markdown: each data URL
// picture in it becomes a marker, which is then deleted and the picture
// pasted where it was as a file; the editor puts a link to it at the
// cursor once it is uploaded (it would not replace a selection).
const juejinBody = `async (text) => {
	const wait = (ms) => new Promise((r) => setTimeout(r, ms));
	const cm = document.querySelector(".CodeMirror").CodeMirror;
	const files = [];
	const md = text.replace(/!\[[^\]]*\]\((data:(image\/[\w.+-]+);base64,([A-Za-z0-9+\/=]+))[^)]*\)/g, (all, data, type, b64) => {
		files.push({ type, b64 });
		return "〔图" + files.length + "〕";
	});
	cm.setValue(md);
	for (const [i, f] of files.entries()) {
		const mark = "〔图" + (i + 1) + "〕";
		const at = cm.getValue().indexOf(mark);
		if (at < 0) throw new Error("正文里找不到" + mark);
		const links = () => (cm.getValue().match(/!\[[^\]]*\]\(https?:/g) ?? []).length;
		const before = links();
		cm.focus();
		cm.replaceRange("", cm.posFromIndex(at), cm.posFromIndex(at + mark.length));
		cm.setCursor(cm.posFromIndex(at));
		const bytes = Uint8Array.from(atob(f.b64), (c) => c.charCodeAt(0));
		const dt = new DataTransfer();
		dt.items.add(new File([bytes], "image" + (i + 1) + "." + f.type.split("/")[1].replace("jpeg", "jpg"), { type: f.type }));
		cm.getInputField().dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
		for (let t = 0; links() === before; t++) {
			if (t === 240) throw new Error("第 " + (i + 1) + " 张图片 2 分钟还没传完");
			await wait(500);
		}
	}
	if (!cm.getValue().trim()) throw new Error("正文没填上");
}`

// juejinTags picks the tags: typing in the tag box lists Juejin's tags
// like it, of which the one named so is taken.
func juejinTags(ctx context.Context, page *rod.Page, tags []string) error {
	in, err := find(page, `.publish-popup .form-item:nth-child(3) .tag-input input.byte-select__input`, 5*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	count := func() int {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelectorAll(".publish-popup .form-item:nth-child(3) .byte-select__tag").length`)
		if err != nil {
			return 0
		}
		return res.Value.Int()
	}
	add := func(t string) error {
		if err := jsClick(in); err != nil {
			return err
		}
		if err := fill(page, in, t); err != nil {
			return err
		}
		if err := sleep(ctx, 1500*time.Millisecond); err != nil {
			return err
		}
		_, err := page.Timeout(evalTimeout).Eval(`(tag) => [...document.querySelectorAll(".byte-select-option")]
			.find((o) => o.getClientRects().length && o.textContent.trim().toLowerCase() === tag.toLowerCase())?.click()`, t)
		return err
	}
	for _, t := range tags {
		if count() == juejinTagMax {
			break
		}
		if err := add(strings.TrimSpace(t)); err != nil {
			return err
		}
	}
	if count() == 0 {
		if err := add(juejinCategory); err != nil {
			return err
		}
	}
	// Not emptied by fill: Backspace in the empty box drops the last tag.
	if count() == 0 {
		return errors.New("文章标签没加上")
	}
	return nil
}

// juejinCover uploads the cover, which Juejin takes as it is. The upload
// now and then never finishes, so it is tried again, three times at most.
func juejinCover(ctx context.Context, page *rod.Page, path string) error {
	for try := 1; ; try++ {
		page.Timeout(evalTimeout).Eval(`() => document.querySelector(".publish-popup .coverselector_container .delete-btn")?.click()`)
		in, err := find(page, `.publish-popup .coverselector_container input[type=file]`, 5*time.Second)
		if err != nil {
			return errors.New("未找到封面上传框")
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
		if _, err := find(page, `.publish-popup .coverselector_container img.preview-image[src^="http"]`, 30*time.Second); err == nil {
			return nil
		}
		if try == 3 {
			return errors.New("封面没有上传成功")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}
