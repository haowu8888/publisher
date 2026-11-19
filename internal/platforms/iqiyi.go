package platforms

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
)

// publishIqiyi uploads a video through iQiyi's creator platform (爱奇艺号).
// The form, an Element UI one, shows once the file is chosen and is filled
// while the video uploads. It has a title and a description but no tags,
// and wants a tall (3:4) cover, from which it makes the wide ones.
// Selectors as of November 2026.
func publishIqiyi(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://creator.iqiyi.com/publish/video/wemedia", j)
	if err != nil {
		return err
	}
	const fileSel = `input[type="file"][accept*=".mp4"]`
	if _, err := waitElement(ctx, page, fileSel, "creator.iqiyi.com"); err != nil {
		return err
	}
	// The page says how many more videos it takes today, 10 a day.
	if res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`); err == nil && iqiyiNoneLeft.MatchString(res.Value.Str()) {
		return errors.New("爱奇艺号今天的上传次数已用完（每天 10 个），明天再试")
	}
	const titleSel = `input[placeholder^="标题字数"]`
	if err := startUpload(ctx, page, fileSel, "creator.iqiyi.com", m.VideoPath, titleSel); err != nil {
		return err
	}
	dismissGuides(page)

	j.Report("填写标题")
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(iqiyiText(m.Title), iqiyiLimits.Title)); err != nil {
		return err
	}
	if m.Description != "" {
		j.Report("填写简介")
		desc, err := find(page, `textarea[placeholder^="添加作品简介"]`, 10*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(iqiyiText(m.Description), iqiyiLimits.Text)); err != nil {
			return err
		}
	}
	if err := iqiyiStatement(page, m.Statement); err != nil {
		return err
	}

	if err := iqiyiUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := iqiyiCovers(ctx, page, j)
	if err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(iqiyiReviewNote + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, "button.el-button--success", "^发布作品$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"/content", "manage"}, "发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// iqiyiText keeps of s only what iQiyi's form lets through: Chinese,
// kana, ASCII and the punctuation in iqiyiPunct. Others, such as emoji,
// get 简介文字中含有1个特殊字符 or 简介不允许输入emoji表情哦~.
func iqiyiText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r < utf8.RuneSelf && (unicode.IsPrint(r) || unicode.IsSpace(r)),
			r >= 0x4E00 && r <= 0x9FA5, r >= 0xF900 && r <= 0xFA2D, r >= 0x3040 && r <= 0x30FF,
			strings.ContainsRune(iqiyiPunct, r):
			return r
		}
		return -1
	}, s))
}

// iqiyiPunct is the full-width punctuation iQiyi's form takes.
const iqiyiPunct = "。—？＜＞…：，；！￥（）【】《》、”“’‘·＠＃＆＾＊＿＋｜～¨％"

const iqiyiReviewNote = "已填写完成，请在浏览器中检查后点「发布作品」"

// iqiyiNoneLeft is what the upload page says once the day's uploads are
// used up.
var iqiyiNoneLeft = regexp.MustCompile(`还可上传\s*0\s*个`)

// iqiyiStatement picks the material's statement (创作声明), which iQiyi
// words as Bilibili does; a material without one gets 内容无需标注, which
// the form asks for.
func iqiyiStatement(page *rod.Page, statement string) error {
	if statement == "" {
		statement = "内容无需标注"
	}
	res, err := page.Timeout(evalTimeout).Eval(`async (s) => {
		const radio = [...document.querySelectorAll(".form-declare-group label.el-radio")].find((r) => r.textContent.trim() === s);
		if (!radio) return "创作声明里没有「" + s + "」";
		if (!radio.classList.contains("is-checked")) radio.click();
		await new Promise((r) => setTimeout(r, 300));
		return radio.classList.contains("is-checked") ? "" : "创作声明「" + s + "」没选上";
	}`, statement)
	if err != nil {
		return err
	}
	if msg := res.Value.Str(); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// iqiyiUploaded waits until the video is uploaded: the progress card by
// the form, with its percentage, then gives way to the video's preview.
func iqiyiUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => ({
			text: document.body.innerText,
			uploading: !!document.querySelector(".uploadProgress-wrap"),
			done: !!document.querySelector("aside .vp-video"),
			pct: document.querySelector(".up-progress-percent")?.textContent.trim() ?? "",
		})`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		if err := watch.check(page, res.Value.Get("text").Str()); err != nil {
			if m := iqiyiFailed.FindString(res.Value.Get("text").Str()); m != "" {
				return errors.New(m)
			}
			return err
		}
		if !res.Value.Get("uploading").Bool() && res.Value.Get("done").Bool() {
			return nil
		}
		j.Report(fmt.Sprintf("上传视频中 %s（已用 %s）", res.Value.Get("pct").Str(), time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// iqiyiFailed is the page's own word on a failed upload, as 上传失败：视频
// 时长过短，无法上传.
var iqiyiFailed = regexp.MustCompile(`上传失败[：:][^\n]*`)

// iqiyiCovers sets the cover, which iQiyi requires: the material's, or
// without one, or when it does not take, the frame of the video that the
// cover editor starts on. The note returned says when the cover did not
// take.
func iqiyiCovers(ctx context.Context, page *rod.Page, j Job) (string, error) {
	if cover := j.Material.CoverPath; cover != "" {
		j.Report("设置封面")
		err := iqiyiCover(ctx, page, cover)
		if err == nil {
			return "", nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		iqiyiCloseEditor(page)
		note := "（封面没设上，用的是视频画面：" + err.Error() + "）"
		j.Report("设置视频画面封面")
		if err := iqiyiCover(ctx, page, ""); err != nil {
			return "", fmt.Errorf("设置封面失败: %w", err)
		}
		return note, nil
	}
	j.Report("设置视频画面封面")
	if err := iqiyiCover(ctx, page, ""); err != nil {
		return "", fmt.Errorf("设置封面失败: %w", err)
	}
	return "", nil
}

// iqiyiCover opens the cover editor (选择封面, or 编辑封面 once there is a
// cover), on its tall cover's tab, puts the picture at path in it, or
// without one keeps the frame it starts on, and takes it with 完成. An
// uploaded picture shows from pan.iqiyi.com.
func iqiyiCover(ctx context.Context, page *rod.Page, path string) error {
	const dialog = `.image-crop-dialog`
	if _, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".image-crop-wrap .main-edit-bar, .image-crop-wrap .edit-bar").click()`); err != nil {
		return errors.New("未找到「选择封面」")
	}
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(d) => {
			const dlg = document.querySelector(d);
			const img = dlg?.querySelector(".canvas-img");
			return !!dlg?.getClientRects().length && !!img?.src;
		}`, dialog)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("封面编辑框没出来")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if path != "" {
		// The first input is the tall cover's tab, the one it opens on.
		in, err := find(page, dialog+` .crop-area input[type=file]`, 10*time.Second)
		if err != nil {
			return errors.New("未找到封面上传框")
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
		for deadline := time.Now().Add(time.Minute); ; {
			res, err := page.Timeout(evalTimeout).Eval(`(d) => document.querySelector(d + " .canvas-img")?.src.startsWith("https://pan.iqiyi.com/")`, dialog)
			if err == nil && res.Value.Bool() {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("封面图片没有上传成功")
			}
			if err := sleep(ctx, time.Second); err != nil {
				return err
			}
		}
	}
	if err := clickUntilGone(ctx, page, dialog+` button`, `^完成$`, 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面编辑框%w", err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => !!document.querySelector(".image-crop-wrap .img-card img")`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("表单上没有显示封面")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// iqiyiCloseEditor closes the cover editor a failed try may have left
// open, with its 取消.
func iqiyiCloseEditor(page *rod.Page) {
	page.Timeout(evalTimeout).Eval(`() => {
		const dlg = document.querySelector(".image-crop-dialog");
		if (dlg?.getClientRects().length) [...dlg.querySelectorAll("button")].find((b) => b.textContent.trim() === "取消")?.click();
	}`)
}
