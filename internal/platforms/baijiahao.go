package platforms

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"regexp"
	"time"
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
)

// publishBaijiahao publishes a video or an article through Baijiahao's
// (百家号) editor. Selectors as of November 2026.
func publishBaijiahao(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return bjhArticle(ctx, b, j)
	}
	return bjhVideo(ctx, b, j)
}

// bjhOpen opens the editor for typ, "videoV2" or "news", without the
// draft it keeps in the browser's storage, which it would fill the form
// with: the last material's cover, say, or the last video beside this one.
func bjhOpen(ctx context.Context, b *browser.Browser, typ string, j Job) (*rod.Page, error) {
	return openFresh(ctx, b, "https://baijiahao.baidu.com/robots.txt",
		"https://baijiahao.baidu.com/builder/rc/edit?type="+typ, `^edit-.+-(`+typ+`|localReload)$`, j)
}

// bjhVideo uploads a video. Its form shows once the file is chosen and is
// filled while the video uploads. The video has no title but a short
// description (作品描述), which shows as one: the title goes there, and
// the tags follow it as topics while they fit.
func bjhVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := bjhOpen(ctx, b, "videoV2", j)
	if err != nil {
		return err
	}
	const editorSel = `[data-lexical-editor]`
	if err := startUpload(ctx, page, `input[type="file"][accept*=".mp4"]`, "builder/rc/edit", m.VideoPath, editorSel); err != nil {
		return err
	}

	j.Report("填写作品描述和话题")
	editor, err := waitElement(ctx, page, editorSel, "builder/rc/edit")
	if err != nil {
		return err
	}
	dismissGuides(page)
	title := fitTitle(m.Title, bjhVideoLimits.Title)
	if err := fill(page, editor, title); err != nil {
		return err
	}
	// A topic shows as "#tag#", after a space.
	room := bjhVideoLimits.Title - utf8.RuneCountInString(title)
	for _, tag := range m.Tags {
		if room -= utf8.RuneCountInString(tag) + 3; room < 0 {
			break
		}
		if err := bjhTopic(ctx, page, editor, tag); err != nil {
			return err
		}
	}

	if s, ok := bjhStatements[m.Statement]; ok {
		j.Report("选择创作声明")
		if err := bjhStatement(ctx, page, s); err != nil {
			return err
		}
	}

	if err := bjhUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, bjhVideoCover)
	if err != nil {
		return err
	}
	dismissGuides(page)
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return bjhSubmit(ctx, page, j, note)
}

// bjhTopic types "#tag" at the end of the description and picks the
// suggested topic of that name, or else the first one suggested. Typed
// text no topic matches stays text.
func bjhTopic(ctx context.Context, page *rod.Page, editor *rod.Element, tag string) error {
	if err := typeInto(page, editor, " #"+tag); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		res, err := page.Timeout(evalTimeout).Eval(`(tag) => {
			const items = [...document.querySelectorAll('[class*="topicItem"]')].filter((e) => e.offsetParent);
			const name = (e) => e.querySelector('[class*="topicName"]')?.textContent.replace(/[#\s]/g, "");
			const el = items.find((e) => name(e) === tag) ?? items[0];
			if (el) el.click();
			return !!el;
		}`, tag)
		if err == nil && res.Value.Bool() {
			return sleep(ctx, 500*time.Millisecond)
		}
	}
	return nil
}

// bjhStatements are Baijiahao's creation statements (创作声明), by those
// of the material, which are in Bilibili's words.
var bjhStatements = map[string]string{
	"内容无需标注":  "无需声明",
	"含AI生成内容": "含AI生成内容",
	"含虚构演绎内容": "含虚构演绎内容",
}

// bjhStatement picks the creation statement whose option reads statement,
// in the dialog its field opens.
func bjhStatement(ctx context.Context, page *rod.Page, statement string) error {
	box, err := find(page, `input[placeholder="请选择创作声明"]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到创作声明")
	}
	if err := jsClick(box); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, ".cheetah-modal-body div.cursor-pointer", "^"+regexp.QuoteMeta(statement)+"$", 10*time.Second); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, ".cheetah-modal-footer button", "^确定$", 5*time.Second); err != nil {
		return err
	}
	// The dialog sometimes ignores 确定.
	if err := clickUntilGone(ctx, page, ".cheetah-modal-footer button", "^确定$", 5*time.Second, 25*time.Second); err != nil {
		return errors.New("创作声明对话框没有关闭")
	}
	return nil
}

// bjhUploaded waits until the video upload finishes: the page shows its
// progress as a percentage over the video's thumbnail until then, and the
// covers it takes from the video after.
func bjhUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const pct = [...document.querySelectorAll('[class*="tabCoverOverlay"] *')]
				.map((e) => e.textContent.trim())
				.find((t) => /^\d{1,3}%$/.test(t));
			return { pct: pct || "", covers: !!document.querySelector('.form-cover [data-testid="cover-preview"] img'), text: document.body.innerText };
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		v := res.Value
		if err := watch.check(page, v.Get("text").Str()); err != nil {
			return err
		}
		pct := v.Get("pct").Str()
		if pct == "" && v.Get("covers").Bool() {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if pct != "" {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", pct, time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// bjhVideoCover sets the cover the picture at path fits, the vertical one
// (3:4) or, for a wide picture, the horizontal one (16:9); the other stays
// a frame of the video.
func bjhVideoCover(ctx context.Context, page *rod.Page, path string) error {
	slot := 1
	if wide(path) {
		slot = 0
	}
	return bjhCover(ctx, page, path, `.form-cover [data-testid="cover-preview"]`, slot, `.form-cover [data-testid="cover-preview"] img`)
}

// wide reports whether the picture at path is wider than high.
func wide(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	return err == nil && c.Width > c.Height
}

// bjhCover uploads the picture at path through the cover dialog that the
// slot-th element matching entry opens, which selects what it uploaded,
// and takes the default crop. imgSel is the cover's picture on the form,
// whose source changes once the dialog is done.
func bjhCover(ctx context.Context, page *rod.Page, path, entry string, slot int, imgSel string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(entry, slot, imgSel) => {
		const old = [...document.querySelectorAll(imgSel)].map((i) => i.src).join(" ");
		const el = document.querySelectorAll(entry)[slot];
		if (!el) return null;
		el.click();
		return old;
	}`, entry, slot, imgSel)
	if err != nil || res.Value.Nil() {
		return errors.New("未找到封面入口")
	}
	old := res.Value.Str()
	// The dialog's last file input is its 本地上传.
	const inputs = `[...document.querySelectorAll(".cheetah-modal")].filter((m) => m.getClientRects().length).flatMap((m) => [...m.querySelectorAll('input[type="file"]')])`
	in, err := page.Timeout(15 * time.Second).ElementByJS(rod.Eval(`() => ` + inputs + `.pop() ?? null`))
	// Not wrapped: the queue would report its deadline as the task's.
	if err != nil {
		return errors.New("封面编辑框没有打开")
	}
	pics := func() int {
		res, err := page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll(".cheetah-modal")].filter((m) => m.getClientRects().length).reduce((n, m) => n + m.querySelectorAll("img").length, 0)`)
		if err != nil {
			return -1
		}
		return res.Value.Int()
	}
	before := pics()
	if err := in.CancelTimeout().SetFiles([]string{path}); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); pics() <= before; {
		if time.Now().After(deadline) {
			return errors.New("封面图片没有传上去")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := sleep(ctx, time.Second); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, ".cheetah-modal button", "^确定", 10*time.Second); err != nil {
		return err
	}
	if err := waitGone(ctx, page, ".cheetah-modal-body", 20*time.Second); err != nil {
		return errors.New("封面编辑框没有关闭")
	}
	for deadline := time.Now().Add(20 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(imgSel) => [...document.querySelectorAll(imgSel)].map((i) => i.src).join(" ")`, imgSel)
		if err == nil && res.Value.Str() != old && res.Value.Str() != "" {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面没有换上")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// bjhArticle writes an article: its title, and its body with its pictures,
// which the editor, UEditor, stores on Baidu's servers as it takes them.
// The cover is required: the material's, or else the first picture of the
// body, which the cover dialog offers.
func bjhArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := bjhOpen(ctx, b, "news", j)
	if err != nil {
		return err
	}
	j.Report("填写标题和正文")
	const titleSel = `[data-testid="news-title-input"] [data-lexical-editor]`
	title, err := waitElement(ctx, page, titleSel, "builder/rc/edit")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if err := fill(page, title, fitTitle(m.Title, bjhArticleLimits.Title)); err != nil {
		return err
	}
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	if _, err := page.Timeout(evalTimeout).Eval(`(html) => Object.values(window.UE_V2.instants)[0].setContent(html)`, content); err != nil {
		return fmt.Errorf("未找到正文编辑器，发布页可能已改版: %w", err)
	}
	pics := 0
	if res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector("#ueditor_0").contentDocument.body.querySelectorAll("img").length`); err == nil {
		pics = res.Value.Int()
	}
	if pics > 0 {
		j.Report("上传正文图片")
		if err := bjhImagesStored(ctx, page); err != nil {
			return err
		}
	}

	switch {
	case m.CoverPath != "":
		j.Report("设置封面")
		if err := bjhCover(ctx, page, m.CoverPath, bjhArticleCoverBox, 0, bjhArticleCover); err != nil {
			return fmt.Errorf("设置封面失败: %w", err)
		}
	case pics > 0:
		j.Report("设置封面（正文第一张图）")
		if err := bjhBodyCover(ctx, page); err != nil {
			return fmt.Errorf("设置封面失败: %w", err)
		}
	case j.Mode == store.ModeAuto:
		return errBjhNoCover
	}
	if j.Mode == store.ModeReview {
		if m.CoverPath == "" && pics == 0 {
			j.Report("已填写完成；请设置封面，检查后点「发布」")
		} else {
			j.Report(reviewNote)
		}
		return nil
	}
	return bjhSubmit(ctx, page, j, "")
}

// bjhArticleCoverRow is the article form's cover field, bjhArticleCoverBox
// the box that opens the cover dialog, and bjhArticleCover the cover it
// shows.
const (
	bjhArticleCoverRow = `.cheetah-form-item-row:has(.form-item-cover)`
	bjhArticleCoverBox = bjhArticleCoverRow + ` [class^="FeEditorApp-"][class$="-default"] > [class$="-content"]`
	bjhArticleCover    = bjhArticleCoverRow + ` img[class*="coverImg"]`
)

// bjhImagesStored waits until the editor has stored every picture of the
// body, none left as a data URL.
func bjhImagesStored(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector("#ueditor_0").contentDocument.body.querySelectorAll('img[src^="data:"]').length`)
		if err == nil && res.Value.Int() == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("正文图片上传超时")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// bjhBodyCover makes the first picture of the body the cover: the cover
// dialog selects it from the start.
func bjhBodyCover(ctx context.Context, page *rod.Page) error {
	if err := clickVisible(ctx, page, bjhArticleCoverBox, "^选择封面$", 10*time.Second); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, ".cheetah-modal button", `^确定 \(\d+\)$`, 15*time.Second); err != nil {
		return err
	}
	if err := waitGone(ctx, page, ".cheetah-modal-body", 20*time.Second); err != nil {
		return errors.New("封面编辑框没有关闭")
	}
	if _, err := find(page, bjhArticleCover, 15*time.Second); err != nil {
		return errors.New("封面没有设上")
	}
	return nil
}

// bjhSubmit publishes the filled form.
func bjhSubmit(ctx context.Context, page *rod.Page, j Job, note string) error {
	j.Report("提交发布")
	if err := clickVisible(ctx, page, "button", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"rc/content", "rc/clue"}, "发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

var errBjhNoCover = errors.New("百家号文章必须有封面：素材没有封面，正文里也没有图片")

// checkBaijiahao refuses an article to submit by itself that has no
// picture for the cover it requires, before the form is filled.
func checkBaijiahao(m store.Material, mode store.Mode) error {
	if m.Kind == store.KindArticle && mode == store.ModeAuto && !hasPicture(m) {
		return errBjhNoCover
	}
	return nil
}
