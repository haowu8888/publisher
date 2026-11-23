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

// acfunUpload is AcFun's video upload page (投稿), whose form shows before
// a file is chosen.
const acfunUpload = "https://member.acfun.cn/upload-video"

// acfunPost is the article page (文章投稿).
const acfunPost = "https://member.acfun.cn/post-article"

// acfunChannel and acfunArticleChannel are the channels (分区) videos and
// articles go in, by their levels, which materials do not have: the
// user's videos explain AI.
var (
	acfunChannel        = []string{"科技", "人文科普"}
	acfunArticleChannel = []string{"资讯", "自媒体专栏"}
)

// acfunTagMax is how many tags a post takes.
const acfunTagMax = 6

// publishAcfun uploads a video to AcFun, or writes an article.
func publishAcfun(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return acfunArticle(ctx, b, j)
	}
	return acfunVideo(ctx, b, j)
}

// acfunVideo uploads a video. Its form, filled while the video uploads,
// needs a cover: the material's, or else the first frame AcFun takes from
// the video.
func acfunVideo(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, acfunUpload, j)
	if err != nil {
		return err
	}
	const fileSel = `input.el-upload__input[multiple]`
	if _, err := waitElement(ctx, page, fileSel, "member.acfun.cn"); err != nil {
		return err
	}
	if err := startUpload(ctx, page, fileSel, "member.acfun.cn", m.VideoPath, `.video-upload-list .video-upload-item`); err != nil {
		return err
	}

	j.Report("填写标题和简介")
	title, err := find(page, `.video-select-title textarea`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, acfunLimits.Title)); err != nil {
		return err
	}
	if m.Description != "" {
		desc, err := find(page, `textarea[maxlength="1000"]`, 10*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(m.Description, acfunLimits.Text)); err != nil {
			return err
		}
	}
	notes, err := acfunSettings(ctx, page, j, acfunChannel)
	if err != nil {
		return err
	}

	if err := acfunUploaded(ctx, page, j); err != nil {
		return err
	}
	j.Report("设置封面")
	if m.CoverPath != "" {
		if err := acfunCover(ctx, page, m.CoverPath, `.video-cover-tool input.el-upload__input`, `.video-cover-tool img[src^="http"]`); err != nil {
			if j.Mode == store.ModeAuto {
				return fmt.Errorf("设置封面失败: %w", err)
			}
			notes = append(notes, "封面没设上，请手动上传："+err.Error())
		}
	} else if err := acfunFrameCover(ctx, page); err != nil {
		if j.Mode == store.ModeAuto {
			return fmt.Errorf("设置封面失败: %w", err)
		}
		notes = append(notes, "封面请手动选择："+err.Error())
	}
	note := ""
	if len(notes) > 0 {
		note = "（" + strings.Join(notes, "；") + "）"
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后点「发布内容」" + note)
		return nil
	}
	return acfunSubmit(ctx, page, j, `.video-submit-container button`, note)
}

// acfunSettings picks 原创, the channel, and the tags, which the video and
// article forms share. It returns what the user is to finish in a review.
func acfunSettings(ctx context.Context, page *rod.Page, j Job, channel []string) ([]string, error) {
	if err := clickVisible(ctx, page, `.ivu-radio-group label.ivu-radio-wrapper`, "^原创$", 5*time.Second); err != nil {
		return nil, fmt.Errorf("类型%w", err)
	}
	var notes []string
	j.Report("选择分区")
	if err := acfunPickChannel(ctx, page, channel); err != nil {
		if j.Mode == store.ModeAuto {
			return nil, err
		}
		notes = append(notes, "分区请手动选择："+err.Error())
	}
	if len(j.Material.Tags) > 0 {
		j.Report("填写标签")
		if err := acfunTags(ctx, page, j.Material.Tags); err != nil {
			return nil, err
		}
	}
	return notes, nil
}

// acfunSubmit clicks 发布内容 and waits for the page saying 提交成功, at an
// acid= URL. AcFun may first ask for a code texted to the account's
// phone, which only the user can give.
func acfunSubmit(ctx context.Context, page *rod.Page, j Job, sel, note string) error {
	j.Report("提交发布")
	startNotes(page)
	// A refused post the page reports only as 网络错误; the server says why.
	if _, err := page.Timeout(evalTimeout).Eval(acfunWatchRefusal); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, sel, "^发布内容$", 10*time.Second); err != nil {
		return err
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		res, err := page.Timeout(evalTimeout).Eval(`() => ({
			verify: [...document.querySelectorAll("iframe[src*='verify-sms']")].some((f) => f.getClientRects().length),
			refused: window.publisherRefusal ?? "",
		})`)
		if err == nil && res.Value.Get("verify").Bool() {
			return errAcfunVerify
		}
		if err == nil && res.Value.Get("refused").Str() != "" {
			return fmt.Errorf("AcFun 拒绝了投稿：%s", res.Value.Get("refused").Str())
		}
		if info, err := page.Timeout(evalTimeout).Info(); err == nil && strings.Contains(info.URL, "acid=") {
			break
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	if err := waitPublished(ctx, page, []string{"acid="}, "提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// acfunWatchRefusal is a script keeping, in window.publisherRefusal, the
// error_msg of the first answer of AcFun's API refusing a post, as
// postArticle answers {"result":109020,"error_msg":"…该账号暂时无法投稿"}.
const acfunWatchRefusal = `() => {
	if (window.publisherRefusal !== undefined) return;
	window.publisherRefusal = "";
	const open = XMLHttpRequest.prototype.open;
	XMLHttpRequest.prototype.open = function (method, url, ...rest) {
		this.publisherURL = String(url);
		return open.call(this, method, url, ...rest);
	};
	const send = XMLHttpRequest.prototype.send;
	XMLHttpRequest.prototype.send = function (...args) {
		this.addEventListener("loadend", () => {
			if (!/post|contribut|submit|publish/i.test(this.publisherURL ?? "")) return;
			try {
				const body = JSON.parse(this.responseText);
				if (body.result && body.result !== 0 && body.error_msg && !window.publisherRefusal) window.publisherRefusal = body.error_msg;
			} catch {}
		});
		return send.apply(this, args);
	};
}`

var errAcfunVerify = &HandOver{"AcFun 要求手机验证码：请在浏览器里点「获取验证码」，填好后点「确定」，发出后点「我已提交」"}

// acfunPickChannel picks the channel in its cascader: pointing at a
// level's item opens the next, whose items are named with a description
// after the name, and clicking the last picks it.
func acfunPickChannel(ctx context.Context, page *rod.Page, levels []string) error {
	res, err := page.Timeout(evalTimeout).Eval(`async (levels) => {
		const wait = (ms) => new Promise((r) => setTimeout(r, ms));
		const box = document.querySelector(".el-cascader input");
		if (!box) return "未找到分区选择框";
		box.click();
		for (const [i, name] of levels.entries()) {
			let item;
			for (let t = 0; t < 10 && !item; t++) {
				await wait(300);
				item = [...(document.querySelectorAll(".el-cascader-menu")[i]?.querySelectorAll("li") ?? [])]
					.find((li) => li.textContent.trim().split(/\s/)[0] === name);
			}
			if (!item) return "分区里没有「" + name + "」";
			item.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
			item.querySelector(".el-cascader-node__label").click();
		}
		await wait(500);
		return box.value === levels.join("-") ? "" : "分区「" + levels.join("/") + "」没选上";
	}`, levels)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
		return errors.New(msg)
	}
	return nil
}

// acfunTags types the tags into the tag box, each ended with Enter, which
// makes it a tag.
func acfunTags(ctx context.Context, page *rod.Page, tags []string) error {
	in, err := find(page, `input.video-input-box-val`, 5*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	for _, t := range tags[:min(len(tags), acfunTagMax)] {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if err := focus(in); err != nil {
			return err
		}
		if err := page.InsertText(t); err != nil {
			return err
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return err
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// acfunUploaded waits until the video is uploaded, as its row in the
// upload list says: 上传完成.
func acfunUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const item = document.querySelector(".video-upload-list .video-upload-item");
			return {
				status: item?.querySelector(".video-upload-item-status")?.textContent.trim() ?? "",
				progress: item?.querySelector(".ivu-progress-bg")?.style.width ?? "",
			};
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		status := res.Value.Get("status").Str()
		switch {
		case strings.Contains(status, "完成"):
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

// acfunCover uploads the cover to the file input sel, which opens a
// cropper (上传封面图片), cut to the cover's shape, whose 确定 takes it; the
// cover then shows as imgSel.
func acfunCover(ctx context.Context, page *rod.Page, path, sel, imgSel string) error {
	in, err := find(page, sel, 5*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if _, err := find(page, `.ivu-modal-wrap .vue-cropper`, 10*time.Second); err != nil {
		return errors.New("封面裁剪框没出来")
	}
	if err := clickUntilGone(ctx, page, `.ivu-modal-wrap .ivu-modal-footer .ivu-btn-primary`, "^确定$", 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面裁剪框%w", err)
	}
	if _, err := find(page, imgSel, 30*time.Second); err != nil {
		return errors.New("封面没有上传成功")
	}
	return nil
}

// acfunFrameCover picks the first of the frames AcFun takes from the video
// as its cover, once they are taken.
func acfunFrameCover(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const img = document.querySelector(".video-cover-list img:not(.video-cover-water)");
			if (!img) return false;
			img.click();
			return true;
		}`)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("视频封面 1 分钟还没抽取出来")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	if _, err := find(page, `.video-cover-tool img[src^="http"]`, 30*time.Second); err != nil {
		return errors.New("封面没有选上")
	}
	return nil
}

// acfunArticle writes an article. The body is a Quill editor that takes no
// pasting by script, so the HTML goes in through Quill itself, each local
// picture a marker; each marker is then selected and the picture given to
// the toolbar's picture input, whose upload puts it in place of the
// selection.
func acfunArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, acfunPost, j)
	if err != nil {
		return err
	}
	title, err := waitElement(ctx, page, `input.ivu-input[maxlength="50"]`, "member.acfun.cn")
	if err != nil {
		return err
	}
	if _, err := find(page, `.ql-container .ql-editor`, 30*time.Second); err != nil {
		return errors.New("未找到正文编辑器")
	}
	j.Report("填写标题")
	if err := fill(page, title, fitTitle(m.Title, acfunArticleLimits.Title)); err != nil {
		return err
	}
	if text := strings.TrimSpace(m.Description); text != "" {
		desc, err := find(page, `textarea.ivu-input[maxlength="50"]`, 5*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(text, acfunArticleLimits.Text)); err != nil {
			return err
		}
	}

	j.Report("填写正文")
	marked, paths, err := markImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m))
	if err != nil {
		return err
	}
	if _, err := page.Timeout(evalTimeout).Eval(`(html) => {
		const q = document.querySelector(".ql-container").__quill;
		q.clipboard.dangerouslyPasteHTML(html);
		// The toolbar makes its picture input on the first click of its
		// button, and clicks it; this makes it without the file dialog.
		const tb = q.getModule("toolbar");
		if (!tb.container.querySelector("input.ql-image[type=file]")) {
			const click = HTMLInputElement.prototype.click;
			HTMLInputElement.prototype.click = function () {};
			try { tb.handlers.image.call(tb); } finally { HTMLInputElement.prototype.click = click; }
		}
		if (!q.getText().trim()) throw new Error("正文没填上");
	}`, marked); err != nil {
		return err
	}
	for i, p := range paths {
		i++
		j.Report(fmt.Sprintf("上传正文图片 %d/%d", i, len(paths)))
		if err := acfunInsertPicture(ctx, page, fmt.Sprintf("〔图%d〕", i), p); err != nil {
			return fmt.Errorf("正文第 %d 张图片%w", i, err)
		}
	}

	notes, err := acfunSettings(ctx, page, j, acfunArticleChannel)
	if err != nil {
		return err
	}
	if m.CoverPath != "" {
		j.Report("设置封面")
		if err := acfunCover(ctx, page, m.CoverPath, `.ivu-upload-select input.ivu-upload-input`, `.ivu-upload img.articleCoverImage[src^="http"]`); err != nil {
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
		j.Report("已填写完成，请在浏览器中检查后点「发布内容」" + note)
		return nil
	}
	return acfunSubmit(ctx, page, j, `button.article-post-confirm`, note)
}

// acfunInsertPicture selects the marker in the editor and gives the
// picture to the toolbar's picture input, waiting until the upload has put
// it in the marker's place.
func acfunInsertPicture(ctx context.Context, page *rod.Page, mark, path string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(mark) => {
		const q = document.querySelector(".ql-container").__quill;
		const at = q.getText().indexOf(mark);
		if (at < 0) return false;
		q.setSelection(at, mark.length, "user");
		return true;
	}`, mark)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return fmt.Errorf("的占位「%s」不在正文里", mark)
	}
	in, err := find(page, `.ql-toolbar input.ql-image[type=file]`, 5*time.Second)
	if err != nil {
		return errors.New("未找到图片上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	for deadline := time.Now().Add(time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(mark) => !document.querySelector(".ql-container").__quill.getText().includes(mark)`, mark)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("1 分钟还没上传完")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}
