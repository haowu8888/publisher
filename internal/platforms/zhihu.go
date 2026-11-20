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
	"github.com/go-rod/rod/lib/input"
)

// publishZhihu publishes on Zhihu (知乎): a video (视频), or an article as a
// column article (文章), its pictures uploaded by the editor. Both take
// topics (话题) from search suggestions. Selectors as of November 2026.
func publishZhihu(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return zhArticle(ctx, b, j)
	}
	return zhVideo(ctx, b, j)
}

// zhTopicMax is how many topics Zhihu takes.
const zhTopicMax = 3

// zhCategory is the field (所属领域) videos go in, which materials do not
// have: the user's videos are on AI.
const zhCategory = "科技互联网"

// zhVideoMarks are Zhihu's video marks (视频标记), by the material's
// statements, which are in Bilibili's words; the others read the same.
var zhVideoMarks = map[string]string{
	"含AI生成内容": "含 AI 生成内容",
}

// zhArticleStatements are Zhihu's statements on articles (创作声明), by
// the material's statements. A material without one keeps 无声明.
var zhArticleStatements = map[string]string{
	"内容无需标注":  "无声明",
	"含AI生成内容": "包含 AI 辅助创作",
	"含虚构演绎内容": "虚构创作",
}

// zhVideo uploads a video. The form shows once the file is chosen, with
// the file's name as title, and is filled while the video uploads.
func zhVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://www.zhihu.com/upload-video", j)
	if err != nil {
		return err
	}
	const titleSel = `textarea[name="title"]`
	// The sign-in page's URL has the form's in its next parameter, escaped.
	if err := startUpload(ctx, page, `input.VideoUploadButton-fileInput`, "zhihu.com/upload-video", m.VideoPath, titleSel); err != nil {
		return err
	}
	dismissGuides(page)

	j.Report("填写标题和介绍")
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, zhihuVideoLimits.Title)); err != nil {
		return err
	}
	const introSel = `.VideoUploadForm .public-DraftEditor-content`
	intro, err := find(page, introSel, 10*time.Second)
	if err != nil {
		return errors.New("未找到介绍输入框")
	}
	if m.Description != "" {
		if err := typeInto(page, intro, truncate(m.Description, zhihuVideoLimits.Text)+" "); err != nil {
			return err
		}
	}
	for i, tag := range m.Tags {
		if i == zhTopicMax {
			break
		}
		if err := zhIntroTopic(ctx, page, intro, tag); err != nil {
			return err
		}
	}

	mark := m.Statement
	if mark == "" {
		mark = "内容无需标注"
	}
	if s, ok := zhVideoMarks[mark]; ok {
		mark = s
	}
	if err := zhVideoMark(ctx, page, mark); err != nil {
		return err
	}
	if err := zhSelect(ctx, page, "所属领域", zhCategory); err != nil {
		return err
	}

	if err := zhUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := setCover(ctx, page, j, zhVideoCover)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(strings.Replace(reviewNote, "「发布」", "「发布视频」", 1) + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, "button.VideoUploadForm-submitButton", "^发布视频$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"/zvideo/", "/creator"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// zhIntroTopic adds a topic at the end of the video's introduction: the
// # button starts one at the cursor, the tag typed after it brings
// suggestions, and the one named as the tag, or else naming it, is taken
// with a press (mousedown), not a click(). A tag with no such suggestion
// stays as typed.
func zhIntroTopic(ctx context.Context, page *rod.Page, intro *rod.Element, tag string) error {
	if err := focus(intro); err != nil {
		return err
	}
	if _, err := intro.Eval(`() => {
		const s = getSelection();
		s.selectAllChildren(this);
		s.collapseToEnd();
	}`); err != nil {
		return err
	}
	if _, err := page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll(".VideoUploadForm button")].find((b) => b.querySelector("svg.ZDI--Hash24"))?.click()`); err != nil {
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
			const items = [...document.querySelectorAll(".Menu-item")].filter((i) => i.querySelector(".topic-name") && i.getClientRects().length);
			const name = (i) => i.querySelector(".topic-name").textContent.replace(/\u200b/g, "").trim();
			// The first suggestions are recommended ones, not yet for the tag.
			const low = (t) => t.toLowerCase();
			const item = items.find((i) => low(name(i)) === low("#" + tag)) ?? items.find((i) => low(name(i)).includes(low(tag)));
			if (!item) return false;
			for (const t of ["mousedown", "mouseup", "click"]) item.dispatchEvent(new MouseEvent(t, { bubbles: true }));
			return true;
		}`, tag)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
			break
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if err := sleep(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	return typeInto(page, intro, " ")
}

// zhVideoMark picks the video mark (视频标记): its dropdown is covered by a
// button opening a dialog of the marks, which takes the one clicked with
// 确认.
func zhVideoMark(ctx context.Context, page *rod.Page, mark string) error {
	const modal = `.VideoUploadForm-videoTypeModal`
	if _, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".VideoUploadForm-videoTypeSelectOverlay").click()`); err != nil {
		return errors.New("未找到「视频标记」")
	}
	if err := clickVisible(ctx, page, modal+` .VideoUploadForm-videoTypeModalOption`, "^"+regexp.QuoteMeta(mark)+"$", 10*time.Second); err != nil {
		return fmt.Errorf("视频标记里没有「%s」", mark)
	}
	if err := clickUntilGone(ctx, page, modal+` button`, `^确认$`, time.Second, 10*time.Second); err != nil {
		return fmt.Errorf("视频标记对话框%w", err)
	}
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const it = [...document.querySelectorAll(".VideoUploadForm-item")].find((i) => i.querySelector(".VideoUploadForm-itemTitle")?.textContent.trim() === "视频标记");
		return it?.querySelector("button.Select-button")?.textContent.trim() ?? "";
	}`)
	if err != nil {
		return err
	}
	if !strings.Contains(res.Value.Str(), mark) {
		return fmt.Errorf("视频标记「%s」没选上", mark)
	}
	return nil
}

// zhSelect picks the choice reading choice in the dropdown of the video
// form's item titled item.
func zhSelect(ctx context.Context, page *rod.Page, item, choice string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(item) => {
		const it = [...document.querySelectorAll(".VideoUploadForm-item")].find((i) => i.querySelector(".VideoUploadForm-itemTitle")?.textContent.trim() === item);
		const button = it?.querySelector("button.Select-button, button.InputButton");
		button?.click();
		return !!button;
	}`, item)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return fmt.Errorf("未找到「%s」", item)
	}
	return zhPickOption(ctx, page, item, choice)
}

// zhPickOption clicks the open dropdown's option reading choice, or
// starting with it, as 包含 AI 辅助创作 作者对内容负责 does.
func zhPickOption(ctx context.Context, page *rod.Page, what, choice string) error {
	var names []string
	for deadline := time.Now().Add(5 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(choice) => {
			const opts = [...document.querySelectorAll(".Popover-content .Select-option")].filter((o) => o.getClientRects().length);
			const text = (o) => o.textContent.replace(/\u200b/g, "").trim();
			const opt = opts.find((o) => text(o) === choice) ?? opts.find((o) => text(o).startsWith(choice));
			if (opt) opt.click();
			return opt ? null : opts.map(text);
		}`, choice)
		if err == nil && res.Value.Nil() {
			return sleep(ctx, 500*time.Millisecond)
		}
		if err == nil {
			names = nil
			for _, v := range res.Value.Arr() {
				names = append(names, v.Str())
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("「%s」里没有「%s」（现有：%s）", what, choice, strings.Join(names, "、"))
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// zhUploaded waits until the video is uploaded: its item in the upload
// list then has the class VideoUploadItem--uploaded.
func zhUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const item = document.querySelector(".VideoUploadItem");
			const pct = [...(item?.querySelectorAll("span, div") ?? [])]
				.filter((e) => e.children.length === 0)
				.map((e) => e.textContent.trim())
				.find((t) => /^\d{1,2}(\.\d+)?%$/.test(t));
			return { text: document.body.innerText, done: !!item?.classList.contains("VideoUploadItem--uploaded"), pct: pct || "" };
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		if err := watch.check(page, res.Value.Get("text").Str()); err != nil {
			return err
		}
		if res.Value.Get("done").Bool() {
			return nil
		}
		j.Report(fmt.Sprintf("上传视频中 %s（已用 %s）", res.Value.Get("pct").Str(), time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// zhVideoCover sets the video's cover: 选择视频封面 → the editor's 本地上传
// tab → its picture input → once it shows the picture, 确认选择. The form
// then shows the cover from zhimg.com.
func zhVideoCover(ctx context.Context, page *rod.Page, path string) error {
	const modal = `.VideoCoverEditor-Modal`
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		window.publisherOldCover = document.querySelector("img.VideoUploadForm-image")?.src ?? "";
		document.querySelector(".VideoUploadForm-imageEditButton").click();
	}`); err != nil {
		return errors.New("未找到「选择视频封面」")
	}
	if err := clickVisible(ctx, page, modal+` .Modal-title div`, `^本地上传$`, 10*time.Second); err != nil {
		return errors.New("封面编辑框没出来")
	}
	in, err := find(page, modal+` input[type=file]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if _, err := find(page, modal+` img[src^="blob:"]`, 30*time.Second); err != nil {
		return errors.New("封面图片没有载入")
	}
	if err := clickUntilGone(ctx, page, modal+` button`, `^确认选择$`, 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面编辑框%w", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const src = document.querySelector("img.VideoUploadForm-image")?.src ?? "";
			return src.startsWith("https://") && src !== window.publisherOldCover;
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

// zhArticle writes a column article. The editor keeps it as a draft on
// Zhihu's side: once typed into, its URL becomes /p/<id>/edit and the
// draft loads again, which turns down edits meanwhile (草稿加载中), so the
// body waits for that. Pasted pictures are uploaded by the editor.
func zhArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://zhuanlan.zhihu.com/write", j)
	if err != nil {
		return err
	}
	const titleSel = `textarea[placeholder^="请输入标题"]`
	title, err := waitElement(ctx, page, titleSel, "zhuanlan.zhihu.com")
	if err != nil {
		return err
	}
	dismissGuides(page)

	j.Report("填写标题")
	titleText := fitTitle(m.Title, zhihuArticleLimits.Title)
	if err := fill(page, title, titleText); err != nil {
		return err
	}
	if err := zhDraftReady(ctx, page); err != nil {
		return err
	}

	j.Report("填写正文")
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	editor, err := find(page, `.public-DraftEditor-content`, 10*time.Second)
	if err != nil {
		return errors.New("未找到正文编辑器")
	}
	if err := zhBody(ctx, page, j, editor, content, plainText(articleBody(m))); err != nil {
		return err
	}
	// The draft reloading may have emptied the title.
	if res, err := page.Timeout(evalTimeout).Eval(`(sel) => document.querySelector(sel)?.value ?? ""`, titleSel); err == nil && res.Value.Str() != titleText {
		title, err := find(page, titleSel, 10*time.Second)
		if err != nil {
			return err
		}
		if err := fill(page, title, titleText); err != nil {
			return err
		}
	}

	note, err := setCover(ctx, page, j, zhArticleCover)
	if err != nil {
		return err
	}
	if s, ok := zhArticleStatements[m.Statement]; ok && s != "无声明" {
		if err := zhArticleStatement(ctx, page, s); err != nil {
			return err
		}
	}
	if len(m.Tags) > 0 {
		j.Report("添加话题")
		if err := zhArticleTopics(ctx, page, m.Tags); err != nil {
			return err
		}
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, "button.Button--primary", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := zhArticlePublished(ctx, page); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// zhDraft is the editor's URL on a draft; zhPublished is an article's.
var (
	zhDraft     = regexp.MustCompile(`zhuanlan\.zhihu\.com/p/\d+/edit`)
	zhPublished = regexp.MustCompile(`zhuanlan\.zhihu\.com/p/\d+([?#]|$)`)
)

// zhDraftReady waits for the draft the title started, and for the editor
// to load it again, answering its 草稿加载中 notice if it showed.
func zhDraftReady(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(20 * time.Second); ; {
		if info, err := page.Timeout(evalTimeout).Info(); err == nil && zhDraft.MatchString(info.URL) {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("没有生成草稿，编辑器可能已改版")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if err := sleep(ctx, 3*time.Second); err != nil {
		return err
	}
	page.Timeout(evalTimeout).Eval(`() => {
		const modal = [...document.querySelectorAll(".Modal")].find((m) => /草稿加载中/.test(m.textContent));
		[...(modal?.querySelectorAll("button") ?? [])].find((b) => b.textContent.trim() === "确定")?.click();
	}`)
	return sleep(ctx, time.Second)
}

// zhBody pastes the article's HTML into the editor. Pictures pasted as
// data URLs fail to import (图片导入失败) when large, but pasted picture
// files upload: each picture is first a marker paragraph, which is then
// selected as a user would, with a click and keys, since the editor keeps
// its own selection, deleted, and replaced by a paste of the picture.
func zhBody(ctx context.Context, page *rod.Page, j Job, editor *rod.Element, content, plain string) error {
	// Pictures on the web are left to the editor.
	var srcs []string
	marked := imgPara.ReplaceAllStringFunc(content, func(p string) string {
		src := imgSrc.FindStringSubmatch(p)
		if src == nil || !strings.HasPrefix(src[2], "data:") {
			return p
		}
		srcs = append(srcs, src[2])
		return fmt.Sprintf("<p>〔图%d〕</p>", len(srcs))
	})
	if err := pasteHTML(editor, marked, plain); err != nil {
		return err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length`); err != nil || res.Value.Int() == 0 {
		return errors.New("正文没填上")
	}
	for i, src := range srcs {
		i++
		j.Report(fmt.Sprintf("上传正文图片 %d/%d", i, len(srcs)))
		mark := fmt.Sprintf("〔图%d〕", i)
		if err := clearMarker(ctx, page, `.public-DraftEditor-content`, mark); err != nil {
			return err
		}
		res, err := page.Timeout(evalTimeout).Eval(`(src) => {
			const ed = document.querySelector(".public-DraftEditor-content");
			const before = ed.querySelectorAll("img").length;
			// The page's policy turns down fetching a data URL.
			const [head, data] = src.split(",");
			const bytes = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
			const blob = new Blob([bytes], { type: head.slice(5).split(";")[0] });
			const dt = new DataTransfer();
			dt.items.add(new File([blob], "image." + (blob.type.split("/")[1] || "png"), { type: blob.type }));
			ed.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
			return before;
		}`, src)
		if err != nil {
			return fmt.Errorf("粘贴正文图片失败: %w", err)
		}
		if err := zhImageStored(ctx, page, res.Value.Int()+1); err != nil {
			return fmt.Errorf("正文第 %d 张图片%w", i, err)
		}
	}
	return nil
}

// zhImageStored waits until the body shows n pictures, none still a blob
// or data URL, as one is while it uploads.
func zhImageStored(ctx context.Context, page *rod.Page, n int) error {
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const ed = document.querySelector(".public-DraftEditor-content");
			const imgs = [...ed.querySelectorAll("img")];
			return { stored: imgs.filter((i) => i.src.startsWith("https://")).length, failed: /图片导入失败/.test(ed.innerText) };
		}`)
		if err == nil {
			if res.Value.Get("failed").Bool() {
				return errors.New("导入失败")
			}
			if res.Value.Get("stored").Int() >= n {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("没有上传成功")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// zhArticleCover uploads the cover under 发布设置; the form shows it once
// stored.
func zhArticleCover(ctx context.Context, page *rod.Page, path string) error {
	in, err := find(page, `input.UploadPicture-input`, 10*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if _, err := find(page, `img[alt="封面图"][src^="https://"]`, time.Minute); err != nil {
		return errors.New("封面图片没有上传成功")
	}
	return nil
}

// zhArticleStatement picks the statement (创作声明) under 发布设置.
func zhArticleStatement(ctx context.Context, page *rod.Page, statement string) error {
	res, err := page.Timeout(evalTimeout).Eval(`() => {
		const label = [...document.querySelectorAll("label")].find((l) => l.textContent.replace(/\u200b/g, "").trim() === "创作声明");
		const button = label?.parentElement.querySelector("button.Select-button");
		button?.click();
		return !!button;
	}`)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return errors.New("未找到「创作声明」")
	}
	return zhPickOption(ctx, page, "创作声明", statement)
}

// zhArticleTopics puts the tags as the article's topics, 3 at most, in
// place of those Zhihu adds by itself from the text (such as 三国 for a
// title saying 第三国), which may come a while after the text: later
// rounds remove those come since.
func zhArticleTopics(ctx context.Context, page *rod.Page, tags []string) error {
	for round := 0; round < 3; round++ {
		// A topic is its name followed by its remove button. Each removal
		// draws the list again, so one at a time.
		res, err := page.Timeout(evalTimeout).Eval(`async (tags) => {
			const norm = (t) => t.replace(/\u200b/g, "").trim().toLowerCase();
			const ours = (name) => tags.some((t) => name === norm(t) || name.includes(norm(t)));
			const topics = () => {
				const label = [...document.querySelectorAll("label")].find((l) => norm(l.textContent) === "文章话题");
				return [...(label?.parentElement.querySelectorAll("div > div + button") ?? [])]
					.filter((b) => !b.closest("label"))
					.map((b) => ({ b, name: norm(b.previousElementSibling.textContent) }));
			};
			for (let i = 0; i < 10; i++) {
				const other = topics().find((t) => !ours(t.name));
				if (!other) break;
				other.b.click();
				await new Promise((r) => setTimeout(r, 300));
			}
			return topics().map((t) => t.name);
		}`, tags)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, v := range res.Value.Arr() {
			have[v.Str()] = true
		}
		for _, tag := range tags {
			if len(have) >= zhTopicMax {
				break
			}
			if have[strings.ToLower(tag)] {
				continue
			}
			if err := zhArticleTopic(ctx, page, tag); err != nil {
				return err
			}
			have[strings.ToLower(tag)] = true
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// zhArticleTopic adds a topic under 发布设置: 添加话题 shows a search box,
// whose suggestions show in a popover; the one named as the tag is taken,
// or else one naming it. A tag with no such suggestion is left out.
func zhArticleTopic(ctx context.Context, page *rod.Page, tag string) error {
	const search = `input[aria-label="搜索话题"]`
	// The box is in the page from the start, hidden until 添加话题.
	res, err := page.Timeout(evalTimeout).Eval(`(sel) => !!document.querySelector(sel)?.getClientRects().length`, search)
	if err != nil {
		return err
	}
	// Without 添加话题 there are 3 topics already, as when Zhihu has just
	// added one; zhArticleTopics then removes it and comes back.
	if !res.Value.Bool() && clickVisible(ctx, page, "button", "^添加话题$", 2*time.Second) != nil {
		return nil
	}
	in, err := find(page, search, 5*time.Second)
	if err != nil {
		return errors.New("未找到话题搜索框")
	}
	if err := fill(page, in, tag); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(tag) => {
			const items = [...document.querySelectorAll(".Popover-content button")].filter((b) => b.getClientRects().length && b.textContent.trim());
			const name = (b) => b.textContent.replace(/\u200b/g, "").trim().toLowerCase();
			const item = items.find((b) => name(b) === tag.toLowerCase()) ?? items.find((b) => name(b).includes(tag.toLowerCase()));
			item?.click();
			return !!item;
		}`, tag)
		if err == nil && res.Value.Bool() {
			return sleep(ctx, 800*time.Millisecond)
		}
		if time.Now().After(deadline) {
			return fill(page, in, "")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// zhArticlePublished waits until the published article shows, at its URL
// without /edit, or the page says it is published.
func zhArticlePublished(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(90 * time.Second); ; {
		if info, err := page.Timeout(evalTimeout).Info(); err == nil && zhPublished.MatchString(info.URL) {
			return nil
		}
		if res, err := page.Timeout(evalTimeout).Eval(bodyText); err == nil && strings.Contains(res.Value.Str(), "发布成功") {
			return nil
		}
		if time.Now().After(deadline) {
			if said := newNotes(page); said != "" {
				return fmt.Errorf("提交后未看到成功提示，页面提示过：%s", said)
			}
			if said := pageNotes(page); said != "" {
				return fmt.Errorf("提交后未看到成功提示，页面显示：%s", said)
			}
			return errors.New("提交后未看到成功提示，请在浏览器中查看")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}
