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

// chRoot is, in a page's script, the shadow root that holds WeChat
// Channels' pages: the site shows them through wujie, a micro frontend, so
// document.querySelector, rod's Element and document.body.innerText do not
// reach the form.
const chRoot = `document.querySelector("wujie-app")?.shadowRoot`

// publishWeixinChannels uploads a video through the Channels assistant
// (视频号助手). The form shows from the start; it is filled while the
// video uploads. Channels has no title but a short title (短标题), so the
// title also leads the description. Selectors as of November 2026.
func publishWeixinChannels(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	if m.Kind == store.KindArticle {
		return chImages(ctx, b, j)
	}
	page, err := openForm(ctx, b, "https://channels.weixin.qq.com/platform/post/create", j)
	if err != nil {
		return err
	}
	if err := chUpload(ctx, page, m.VideoPath); err != nil {
		return err
	}

	j.Report("填写描述和话题")
	editor, err := chWait(ctx, page, `.input-editor[contenteditable]`)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(m.Description)
	if !strings.Contains(text, strings.TrimSpace(m.Title)) {
		text = strings.TrimSpace(m.Title + "\n" + text)
	}
	if err := chDescribe(ctx, page, editor, channelsLimits.fitText(text, m.Tags), m.Tags); err != nil {
		return err
	}

	if short := shortTitle(m.Title); short != "" {
		j.Report("填写短标题")
		in, err := chFind(page, `input[placeholder*="短标题"]`, 10*time.Second)
		if err != nil {
			return fmt.Errorf("未找到短标题输入框: %w", err)
		}
		if err := fill(page, in, short); err != nil {
			return err
		}
	}

	if err := chFillCollection(ctx, page, j); err != nil {
		return err
	}

	if err := chUploaded(ctx, page, j); err != nil {
		return err
	}
	j.Report("位置设为不显示")
	if err := chNoLocation(ctx, page); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, chCover)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后点「发表」" + note)
		return nil
	}
	return chSubmit(ctx, page, j, note)
}

// chImages posts an article as a picture post (图文, 发表动态): its
// pictures, cover first, up to 18, a title of up to 22 characters, and
// the body's paragraphs as the description, its tags as topics. Channels
// picks background music for the pictures itself; that is left.
func chImages(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://channels.weixin.qq.com/platform/post/finderNewLifeCreate", j)
	if err != nil {
		return err
	}
	images := noteImages(m)
	if len(images) > chImagesMax {
		images = images[:chImagesMax]
	}
	j.Report("上传图片")
	const sel = `input[type="file"][accept^="image"]`
	if _, err := chWait(ctx, page, sel); err != nil {
		return err
	}
	// The widget ignores files given before its script is ready.
	if err := sleep(ctx, 2*time.Second); err != nil {
		return err
	}
	in, err := chFind(page, sel, 10*time.Second)
	if err != nil {
		return err
	}
	if err := in.SetFiles(images); err != nil {
		return err
	}

	j.Report("填写标题")
	title, err := chWait(ctx, page, `input[placeholder^="填写标题"]`)
	if err != nil {
		return err
	}
	if err := fill(page, title, fitTitle(m.Title, channelsImageLimits.Title)); err != nil {
		return err
	}
	j.Report("填写描述和话题")
	editor, err := chWait(ctx, page, `.input-editor[contenteditable]`)
	if err != nil {
		return err
	}
	text := m.Description
	if text == "" {
		text = paragraphs(articleBody(m))
	}
	if err := chDescribe(ctx, page, editor, channelsImageLimits.fitText(strings.TrimSpace(text), m.Tags), m.Tags); err != nil {
		return err
	}
	if err := chFillCollection(ctx, page, j); err != nil {
		return err
	}
	if err := chImagesUploaded(ctx, page, len(images)); err != nil {
		return err
	}
	j.Report("位置设为不显示")
	if err := chNoLocation(ctx, page); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后点「发表」")
		return nil
	}
	return chSubmit(ctx, page, j, "")
}

// chImagesMax is the most pictures a picture post takes.
const chImagesMax = 18

// chImagesUploaded waits until the form lists n pictures and none is
// still uploading, which leaves 发表 disabled.
func chImagesUploaded(ctx context.Context, page *rod.Page, n int) error {
	var watch uploadWatch
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const root = ` + chRoot + `;
			const submit = [...(root?.querySelectorAll("button") ?? [])].find((b) => b.textContent.trim() === "发表");
			const thumbs = [...(root?.querySelectorAll(".post-img-list-wrap .thumb-item") ?? [])];
			return {
				count: thumbs.length,
				busy: thumbs.some((t) => t.querySelector("[class*=loading], [class*=progress]")) || !submit || submit.className.includes("disabled"),
				text: root?.querySelector("body")?.innerText ?? "",
			};
		}`)
		if err == nil {
			v := res.Value
			if err := watch.check(page, v.Get("text").Str()); err != nil {
				return err
			}
			if v.Get("count").Int() >= n && !v.Get("busy").Bool() {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("图片 3 分钟内没有全部上传成功（应为 %d 张）", n)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// chDescribe types text into the description editor, then each tag as a
// topic, up to Channels' 10. A topic ends at a symbol, so "#GPT-6.2" would
// be #GPT-6 and a stray ".2"; such tags are left out.
func chDescribe(ctx context.Context, page *rod.Page, editor *rod.Element, text string, tags []string) error {
	if err := typeInto(page, editor, text+" "); err != nil {
		return err
	}
	n := 0
	for _, tag := range tags {
		if n == channelsLimits.Tags {
			break
		}
		if !chTopic.MatchString(tag) {
			continue
		}
		n++
		// A space after "#话题" turns it into a topic.
		if err := typeInto(page, editor, "#"+tag+" "); err != nil {
			return err
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// chTopic matches the tags that make whole topics.
var chTopic = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

// chFillCollection adds the post to the job's collection, if it has one;
// videos and picture posts share the account's collections.
func chFillCollection(ctx context.Context, page *rod.Page, j Job) error {
	if j.Collection == "" {
		return nil
	}
	j.Report("添加到合集")
	return chCollection(ctx, page, j.Collection)
}

// chSubmit clicks 发表 and waits for the post list.
func chSubmit(ctx context.Context, page *rod.Page, j Job, note string) error {
	j.Report("提交发表")
	startNotes(page)
	if err := chClick(ctx, page, "button", "^发表$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"post/list", "finderNewLifePostList"}, "发表成功"); err != nil {
		return err
	}
	return finish(page, j, "发表成功"+note)
}

// chFind waits up to wait for sel in the Channels page's shadow root.
func chFind(page *rod.Page, sel string, wait time.Duration) (*rod.Element, error) {
	el, err := page.Timeout(wait).ElementByJS(rod.Eval(`(sel) => `+chRoot+`?.querySelector(sel)`, sel))
	if err != nil {
		return nil, err
	}
	return el.CancelTimeout(), nil
}

// chWait is waitElement for the Channels page's shadow root: it waits up
// to 30 seconds for sel, watching for a login page meanwhile.
func chWait(ctx context.Context, page *rod.Page, sel string) (*rod.Element, error) {
	for deadline := time.Now().Add(30 * time.Second); ; {
		if loggedOut(page, "channels.weixin.qq.com/platform") {
			page.Close()
			return nil, ErrNotLoggedIn
		}
		if el, err := chFind(page, sel, time.Second); err == nil {
			return el, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("未找到 %s，发布页可能已改版", sel)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return nil, err
		}
	}
}

// chText returns the text the Channels page shows.
func chText(page *rod.Page) (string, error) {
	res, err := page.Timeout(evalTimeout).Eval(`() => ` + chRoot + `?.querySelector("body")?.innerText ?? ""`)
	if err != nil {
		return "", err
	}
	return res.Value.Str(), nil
}

// chUpload is startUpload for the Channels form: the page shows 取消上传
// once it has taken the file.
func chUpload(ctx context.Context, page *rod.Page, path string) error {
	if !fileExists(path) {
		return fmt.Errorf("文件不可读: %s", path)
	}
	const sel = `input[type="file"][accept*="video"]`
	if _, err := chWait(ctx, page, sel); err != nil {
		return err
	}
	for try := 0; try < 2; try++ {
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		in, err := chFind(page, sel, 10*time.Second)
		if err != nil {
			return err
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
			// A small video may be up before the first look.
			if text, err := chText(page); err == nil && (strings.Contains(text, "取消上传") || strings.Contains(text, "删除")) {
				return nil
			}
			if err := sleep(ctx, time.Second); err != nil {
				return err
			}
		}
	}
	return errors.New("页面没有开始上传，发布页可能已改版")
}

// chUploaded is waitUploaded for the Channels form, which enables 发表
// and drops 取消上传 once the video is up.
func chUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const root = ` + chRoot + `;
			const text = root?.querySelector("body")?.innerText ?? "";
			const submit = [...(root?.querySelectorAll("button") ?? [])].find((b) => b.textContent.trim() === "发表");
			const pct = [...(root?.querySelectorAll("span, div, p") ?? [])]
				.filter((e) => e.children.length === 0)
				.map((e) => e.textContent.trim())
				.find((t) => /^\d{1,2}(\.\d+)?%$/.test(t));
			return { done: !!submit && !submit.className.includes("disabled") && !text.includes("取消上传"), pct: pct || "", text };
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		v := res.Value
		if err := watch.check(page, v.Get("text").Str()); err != nil {
			return err
		}
		if v.Get("done").Bool() {
			return nil
		}
		step := fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second))
		if pct := v.Get("pct").Str(); pct != "" {
			step = fmt.Sprintf("上传视频中 %s（已用 %s）", pct, time.Since(start).Round(time.Second))
		}
		j.Report(step)
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// chClick is clickVisible for the Channels page's shadow root.
func chClick(ctx context.Context, page *rod.Page, sel, text string, wait time.Duration) error {
	for deadline := time.Now().Add(wait); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(sel, text) => {
			const re = new RegExp(text);
			const el = [...(`+chRoot+`?.querySelectorAll(sel) ?? [])].find((e) => e.offsetParent && re.test(e.textContent.trim()));
			if (el) el.click();
			return !!el;
		}`, sel, text)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("未找到「%s」按钮", strings.Trim(text, "^$"))
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// chCollection adds the video to the user's collection (合集) name.
func chCollection(ctx context.Context, page *rod.Page, name string) error {
	sel, err := chFind(page, ".post-album-display", 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到合集选择框: %w", err)
	}
	if err := jsClick(sel); err != nil {
		return err
	}
	var names []string
	for deadline := time.Now().Add(10 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(name) => {
			const items = [...(`+chRoot+`?.querySelectorAll(".post-album-wrap .option-item") ?? [])].filter((e) => e.offsetParent);
			const label = (e) => e.querySelector(".name")?.textContent.trim() ?? "";
			const el = items.find((e) => label(e) === name);
			if (el) el.click();
			return el ? null : items.map(label);
		}`, name)
		if err == nil {
			if res.Value.Nil() {
				break
			}
			names = names[:0]
			for _, v := range res.Value.Arr() {
				names = append(names, v.Str())
			}
		}
		if time.Now().After(deadline) {
			if len(names) == 0 {
				return fmt.Errorf("未找到合集「%s」：账号下还没有合集", name)
			}
			return fmt.Errorf("未找到合集「%s」，现有：%s", name, strings.Join(names, "、"))
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	// The choice shows in place of 选择合集.
	for deadline := time.Now().Add(5 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => ` + chRoot + `?.querySelector(".post-album-display")?.textContent.trim() ?? ""`)
		if err == nil && res.Value.Str() == name {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("合集「%s」没有选上", name)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// chNoLocation sets the location to 不显示位置: Channels fills in the
// city it locates the browser in. Done once the video is up, in case the
// page locates it again then.
func chNoLocation(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const root = ` + chRoot + `;
			const shown = root?.querySelector(".position-display")?.innerText.trim();
			if (shown === "不显示位置") return true;
			const item = [...(root?.querySelectorAll(".location-item") ?? [])].find((e) => e.offsetParent && e.innerText.trim() === "不显示位置");
			if (item) item.click();
			else if (root?.querySelector(".location-filter-wrap")?.style.display === "none") root.querySelector(".position-display-wrap")?.click();
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("位置没能设成「不显示位置」，发布页可能已改版")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// chCover sets the profile card cover (个人主页卡片 3:4) to the picture at
// path, through its editor's 上传封面. The share card (分享卡片 4:3) keeps
// a frame of the video: a 4:3 cut of a vertical cover would lose most of it.
func chCover(ctx context.Context, page *rod.Page, path string) error {
	entry, err := chFind(page, ".vertical-cover-wrap .edit-btn", 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到个人主页卡片的「编辑」: %w", err)
	}
	if err := jsClick(entry); err != nil {
		return err
	}
	in, err := chFind(page, `.single-cover-uploader-wrap input[type="file"]`, 15*time.Second)
	if err != nil {
		return fmt.Errorf("封面编辑框没有打开: %w", err)
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// The uploader shows the picture once the editor has it.
	if _, err := chFind(page, `.single-cover-uploader-wrap img`, 20*time.Second); err != nil {
		return fmt.Errorf("封面图片没有传上去: %w", err)
	}
	if err := sleep(ctx, time.Second); err != nil {
		return err
	}
	if err := chClick(ctx, page, ".weui-desktop-dialog button", "^确认$", 10*time.Second); err != nil {
		return err
	}
	// The editor uploads the cut, which takes some seconds, then closes.
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => ![...(` + chRoot + `?.querySelectorAll(".weui-desktop-dialog") ?? [])].some((d) => d.querySelector(".single-cover-uploader-wrap") && d.offsetParent)`)
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

// shortTitleMax is the most characters a short title takes. Limits has
// no title for Channels: the whole title goes into the description.
const shortTitleMax = 16

// shortTitleOff are the characters a short title may not have: besides
// letters, digits and spaces, Channels takes only 《》“”‘’"'：:+？?%℃.
var shortTitleOff = regexp.MustCompile(`[^\p{L}\p{N} 《》“”‘’"'：:+？?%℃]+`)

// shortTitle makes the title a short title (短标题): 6 to 16 characters,
// clause breaks made spaces as Channels suggests, other symbols dropped.
// A title too short for one gives none; the field is optional.
func shortTitle(title string) string {
	s := shortTitleOff.ReplaceAllString(fitTitle(title, shortTitleMax), " ")
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) < 6 {
		return ""
	}
	return s
}
