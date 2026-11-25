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
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// publishSohuhao publishes a video or an article on Sohu's creator
// platform (搜狐号, mp.sohu.com), apart from Sohu Video's (搜狐视频). It
// takes them only from an account with its real name verified (实名认证);
// one without may post moments (动态) only.
func publishSohuhao(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return shArticle(ctx, b, j)
	}
	return shVideo(ctx, b, j)
}

const shHome = "https://mp.sohu.com/mpfe/v4/contentManagement/"

// shTitleMin is the shortest title Sohu takes.
const shTitleMin = 5

// shStatements are Sohu's statements (必选声明), by the material's, which
// are in Bilibili's words. A material without one gets 无需声明.
var shStatements = map[string]string{
	"内容无需标注":  "无需声明",
	"含AI生成内容": "含有AI生成内容",
	"含虚构演绎内容": "含有虚构演绎内容",
}

// shChannel is the channel (频道分类) videos go in, by its levels, which
// materials do not have: the user's videos are on AI.
var shChannel = []string{"科技", "黑科技"}

// shTopicMax is how many topics (话题) an article joins. Sohu's topics are
// its campaigns, so a tag joins only one of the same name, or with it in
// the name.
const shTopicMax = 3

// checkSohuhao refuses, before the form is filled, a material to submit by
// itself whose title Sohu would turn down as too short. A review leaves
// that to the user.
func checkSohuhao(m store.Material, mode store.Mode) error {
	if mode != store.ModeAuto {
		return nil
	}
	if n := halfLen(strings.TrimSpace(m.Title)); n < shTitleMin {
		return fmt.Errorf("搜狐号标题至少 %d 个字（两个英文字母算一个字），「%s」只有 %g 个字", shTitleMin, m.Title, n)
	}
	return nil
}

// shOpen opens an editor and returns the element sel. The editors keep
// what was typed in localStorage (auto-save-html) and fill themselves with
// it, so that is dropped first, on the site's favicon, which is light.
func shOpen(ctx context.Context, b *browser.Browser, editor, sel string, j Job) (*rod.Page, *rod.Element, error) {
	page, err := openFresh(ctx, b, "https://mp.sohu.com/favicon.ico", shHome+"news/"+editor, `^auto-save`, j)
	if err != nil {
		return nil, nil, err
	}
	el, err := waitElement(ctx, page, sel, "mp.sohu.com/mpfe/v4/contentManagement")
	if err != nil && !errors.Is(err, ErrNotLoggedIn) {
		// The first visit after the browser starts, which signs in to
		// mp.sohu.com with the passport, lands on the home page instead.
		if info, e := page.Timeout(evalTimeout).Info(); e == nil && !strings.Contains(info.URL, "news/"+editor) {
			loaded := page.WaitNavigation(proto.PageLifecycleEventNameLoad)
			if err := page.Navigate(shHome + "news/" + editor); err != nil {
				return nil, nil, err
			}
			loaded()
			el, err = waitElement(ctx, page, sel, "mp.sohu.com/mpfe/v4/contentManagement")
		}
	}
	if err != nil {
		return nil, nil, err
	}
	// The welcome dialog of a new account would cover the form.
	page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll(".el-dialog__wrapper")]
		.filter((d) => d.getClientRects().length && /欢迎入驻搜狐号/.test(d.textContent))
		.forEach((d) => d.querySelector(".el-dialog__headerbtn")?.click())`)
	return page, el, nil
}

// shQuota refuses to go on when the account may post no more today, or not
// until its real name is verified, as the editor says below its buttons.
func shQuota(ctx context.Context, page *rod.Page) error {
	// It says 还能发 0 篇 until the count loads, so a 0 counts only once it
	// stays.
	var rest string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".button-list .rest-number")?.textContent.trim() ?? ""`)
		if err == nil {
			rest = res.Value.Str()
		}
		if rest != "" && !strings.Contains(rest, "还能发 0 ") {
			break
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	switch {
	case strings.Contains(rest, "未实名"):
		return errors.New("搜狐号账号还没实名认证，只能发动态：请先在搜狐号后台完成实名认证")
	case strings.Contains(rest, "还能发 0 "):
		return fmt.Errorf("搜狐号今天的发布次数已用完（%s）", rest)
	}
	return nil
}

// shArticle writes an article. The body is a Quill editor, which uploads
// the pictures pasted into it to Sohu by itself.
func shArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, title, err := shOpen(ctx, b, "addarticle", `input[placeholder^="请输入标题"]`, j)
	if err != nil {
		return err
	}
	if err := shQuota(ctx, page); err != nil {
		return err
	}
	editor, err := find(page, `.ql-editor`, 10*time.Second)
	if err != nil {
		return errors.New("未找到正文编辑器")
	}

	j.Report("填写标题")
	if err := fill(page, title, fitTitle(m.Title, sohuhaoArticleLimits.Title)); err != nil {
		return err
	}
	j.Report("填写正文")
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	if err := pasteHTML(editor, content, plainText(articleBody(m))); err != nil {
		return err
	}
	if res, err := editor.Eval(`() => this.innerText.trim().length`); err != nil || res.Value.Int() == 0 {
		return errors.New("正文没填上")
	}
	if err := shImagesUploaded(ctx, editor, j); err != nil {
		return err
	}

	var notes []string
	if m.CoverPath != "" || strings.Contains(content, "<img") {
		j.Report("设置封面")
		if err := shCover(ctx, page, m.CoverPath); err != nil {
			if j.Mode == store.ModeAuto {
				return fmt.Errorf("设置封面失败: %w", err)
			}
			notes = append(notes, "封面没设上，请手动选择："+err.Error())
		}
	}
	if len(m.Tags) > 0 {
		j.Report("选择话题")
		if err := shTopics(ctx, page, m.Tags); err != nil {
			notes = append(notes, "话题没选上："+err.Error())
		}
	}
	if err := shStatement(page, m.Statement); err != nil {
		return err
	}
	note := ""
	if len(notes) > 0 {
		note = "（" + strings.Join(notes, "；") + "）"
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return shSubmit(ctx, page, j, note)
}

// shVideo uploads a video. Its form shows once the file is chosen, and is
// filled while the video uploads; its title starts as the file's name.
func shVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	const fileSel = `.upload-area input.chunkUploader-input`
	page, _, err := shOpen(ctx, b, "addvideo", fileSel, j)
	if err != nil {
		return err
	}
	const titleSel = `.publish-title input[placeholder^="请输入标题"]`
	if err := startUpload(ctx, page, fileSel, "mp.sohu.com", m.VideoPath, titleSel); err != nil {
		return err
	}
	if err := shQuota(ctx, page); err != nil {
		return err
	}

	j.Report("填写标题和简介")
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, sohuhaoVideoLimits.Title)); err != nil {
		return err
	}
	// A description under 5 characters is refused.
	if utf8.RuneCountInString(m.Description) >= 5 {
		desc, err := find(page, `.abstract textarea.abstract-main-textarea`, 10*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(m.Description, sohuhaoVideoLimits.Text)); err != nil {
			return err
		}
	}
	var notes []string
	if len(m.Tags) > 0 {
		j.Report("选择话题")
		if err := shTopics(ctx, page, m.Tags); err != nil {
			notes = append(notes, "话题没选上："+err.Error())
		}
	}
	j.Report("选择频道分类")
	if err := shPickChannel(ctx, page, shChannel); err != nil {
		if j.Mode == store.ModeAuto {
			return err
		}
		notes = append(notes, "频道分类请手动选择："+err.Error())
	}
	if err := shStatement(page, m.Statement); err != nil {
		return err
	}

	if err := shVideoUploaded(ctx, page, j); err != nil {
		return err
	}
	if m.CoverPath != "" {
		j.Report("设置封面")
		if err := shCover(ctx, page, m.CoverPath); err != nil {
			if j.Mode == store.ModeAuto {
				return fmt.Errorf("设置封面失败: %w", err)
			}
			notes = append(notes, "封面没设上，请手动上传："+err.Error())
		}
	}
	note := ""
	if len(notes) > 0 {
		note = "（" + strings.Join(notes, "；") + "）"
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	return shSubmit(ctx, page, j, note)
}

// shVideoUploaded waits until the video is uploaded, as its status line
// says: 视频上传成功！ once it is.
func shVideoUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => ({
			status: document.querySelector(".video-uploading-status")?.textContent.trim() ?? "",
			progress: document.querySelector(".video-uploading-manager .title-progress-fill")?.style.width ?? "",
		})`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		status := res.Value.Get("status").Str()
		switch {
		case strings.Contains(status, "成功"):
			return nil
		case strings.Contains(status, "失败"):
			return fmt.Errorf("视频上传失败：%s", status)
		}
		j.Report(fmt.Sprintf("上传视频中 %s（已用 %s）", res.Value.Get("progress").Str(), time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// shPickChannel picks the video's channel in its two dropdowns, the first
// of which Sohu may have set already.
func shPickChannel(ctx context.Context, page *rod.Page, levels []string) error {
	for i, name := range levels {
		res, err := page.Timeout(evalTimeout).Eval(`async (i, name) => {
			const drop = document.querySelectorAll(".channel-select-row .plugin-dropdown")[i];
			if (!drop) return "未找到频道分类";
			if (drop.querySelector(".select-text")?.textContent.trim() === name) return "";
			drop.querySelector(".select-main").click();
			await new Promise((r) => setTimeout(r, 500));
			const item = [...drop.querySelectorAll(".select-list li")].find((li) => li.textContent.trim() === name);
			if (!item) return "频道分类里没有「" + name + "」";
			item.click();
			await new Promise((r) => setTimeout(r, 500));
			return drop.querySelector(".select-text")?.textContent.trim() === name ? "" : "频道分类「" + name + "」没选上";
		}`, i, name)
		if err != nil {
			return err
		}
		if msg := res.Value.Str(); msg != "" {
			return errors.New(msg)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// shImagesUploaded waits until the editor has put the pasted pictures on
// Sohu's servers: until then they show from their data URLs.
func shImagesUploaded(ctx context.Context, editor *rod.Element, j Job) error {
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := editor.Timeout(evalTimeout).Eval(`() => {
			const imgs = [...this.querySelectorAll("img")];
			return { total: imgs.length, left: imgs.filter((i) => !/^(https?:)?\/\//.test(i.getAttribute("src") ?? "")).length };
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		total, left := res.Value.Get("total").Int(), res.Value.Get("left").Int()
		if left == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("正文图片 3 分钟还没传完（%d/%d）", total-left, total)
		}
		j.Report(fmt.Sprintf("上传正文图片 %d/%d", total-left, total))
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// shCover sets the cover in the dialog 上传图片 opens: the picture at path
// is uploaded in its 本地上传 tab, where it is then picked; without one,
// the body's first picture is picked in 正文图片. 确定 takes it as it is.
func shCover(ctx context.Context, page *rod.Page, path string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		const open = document.querySelector(".cover-button .upload-file");
		if (!open?.getClientRects().length) document.querySelector(".cover-button .change-cover")?.click();
		else open.click();
	}`); err != nil {
		return err
	}
	tab := "正文图片"
	if path != "" {
		tab = "本地上传"
	}
	if err := clickVisible(ctx, page, `.select-dialog .dialog-title h3`, "^"+tab+"$", 10*time.Second); err != nil {
		return errors.New("封面对话框没出来")
	}
	if path != "" {
		in, err := find(page, `.select-dialog input#new-file`, 10*time.Second)
		if err != nil {
			return errors.New("未找到封面上传框")
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
	} else if err := clickVisible(ctx, page, `.select-dialog .content-images .img-wrapper`, "", 10*time.Second); err != nil {
		// A video's dialog has no 正文图片; shVideo gives it a picture.
		return errors.New("封面对话框里没有正文图片")
	}
	// 确定 is enabled once a picture is picked, an uploaded one by itself.
	if err := clickVisible(ctx, page, `.select-dialog .bottom-buttons p.positive-button`, "^确定$", 30*time.Second); err != nil {
		return errors.New("封面图片没有选上")
	}
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => /res\.mp\.sohu\.com/.test(document.querySelector(".cover-button .pic-cover")?.style.backgroundImage ?? "")`)
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

// shTopics joins the topics named like the tags: typing in the topic box
// lists matching ones.
func shTopics(ctx context.Context, page *rod.Page, tags []string) error {
	in, err := find(page, `.select-topic input.el-select__input`, 5*time.Second)
	if err != nil {
		return errors.New("未找到话题输入框")
	}
	n := 0
	for _, t := range tags {
		if n == shTopicMax {
			break
		}
		if err := focus(in); err != nil {
			return err
		}
		if err := page.InsertText(t); err != nil {
			return err
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		res, err := page.Timeout(evalTimeout).Eval(`(tag) => {
			const item = [...document.querySelectorAll(".el-select-dropdown__item")]
				.find((i) => i.getClientRects().length && !i.classList.contains("selected") && i.textContent.trim().toLowerCase().includes(tag.toLowerCase()));
			item?.click();
			return !!item;
		}`, t)
		if err != nil {
			return err
		}
		if res.Value.Bool() {
			n++
		}
		if err := fill(page, in, ""); err != nil {
			return err
		}
	}
	// The list of topics stays open over the form.
	return page.Keyboard.Type(input.Escape)
}

// shStatement picks the statement (必选声明), a row of radio buttons.
func shStatement(page *rod.Page, statement string) error {
	if statement == "" {
		statement = "内容无需标注"
	}
	if s, ok := shStatements[statement]; ok {
		statement = s
	}
	res, err := page.Timeout(evalTimeout).Eval(`(want) => {
		const label = [...document.querySelectorAll("#info-source-signature .el-radio")]
			.find((l) => l.querySelector(".el-radio__label")?.textContent.trim() === want);
		if (label && !label.classList.contains("is-checked")) label.click();
		return !!label;
	}`, statement)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return fmt.Errorf("创作声明里没有「%s」", statement)
	}
	return nil
}

// shSubmit clicks 发布, answers the 确认发布文章么？ Sohu may ask (for an
// article under 200 characters, suggesting a moment instead), and waits
// for Sohu to take the post, which goes to the home page.
func shSubmit(ctx context.Context, page *rod.Page, j Job, note string) error {
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, `.button-list li.publish-report-btn:not(.disable-button)`, "^发布$", 30*time.Second); err != nil {
		return err
	}
	clickVisible(ctx, page, `.alert-dialog .sure-btn`, "^确定$", 5*time.Second)
	if err := waitPublished(ctx, page, []string{"contentManagement/first"}, "发布成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}
