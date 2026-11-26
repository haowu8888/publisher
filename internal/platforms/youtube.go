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
)

// ytStudio is YouTube Studio, whose upload dialog takes the video.
const ytStudio = "https://studio.youtube.com/"

// ytTagsMax is how many characters YouTube takes in all the tags together,
// counting a comma between them.
const ytTagsMax = 500

// publishYoutube uploads a video in YouTube Studio. The upload dialog
// opens on the video's details (title, description, thumbnail, audience,
// tags, altered content), then two steps without anything to fill, then
// visibility, which is public.
func publishYoutube(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, ytStudio, j)
	if err != nil {
		return err
	}
	if _, err := waitElement(ctx, page, `ytcp-icon-button#upload-icon`, "studio.youtube.com"); err != nil {
		return err
	}
	j.Report("打开上传对话框")
	if err := ytOpenUpload(ctx, page); err != nil {
		return err
	}
	if err := startUpload(ctx, page, `ytcp-uploads-file-picker input[type=file]`, "studio.youtube.com", m.VideoPath, `ytcp-uploads-dialog #title-textarea #textbox`); err != nil {
		return err
	}

	// An upload the channel may not make today (已达到每日上传数上限) shows
	// the reason in place of the dialog's buttons.
	if err := sleep(ctx, 2*time.Second); err != nil {
		return err
	}
	if e := ytDialogError(page); e != "" {
		return fmt.Errorf("YouTube 提示：%s（账号做一次电话验证可以多传，或者等 24 小时）", e)
	}
	j.Report("填写标题和说明")
	title, err := find(page, `ytcp-uploads-dialog #title-textarea #textbox`, 30*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, youtubeLimits.Title)); err != nil {
		return err
	}
	if m.Description != "" {
		desc, err := find(page, `ytcp-uploads-dialog #description-textarea #textbox`, 10*time.Second)
		if err != nil {
			return errors.New("未找到说明输入框")
		}
		if err := fill(page, desc, ytText(truncate(m.Description, youtubeLimits.Text))); err != nil {
			return err
		}
	}
	// Not setCover: the Escape it presses after a failure closes the
	// whole upload dialog, leaving the video a draft.
	note := ""
	if m.CoverPath != "" {
		j.Report("设置缩略图")
		if err := ytThumbnail(ctx, page, m.CoverPath); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			note = "（缩略图没设上，用的是 YouTube 自动生成的：" + err.Error() + "）"
		}
	}
	j.Report("选择观众和其他设置")
	if err := ytRadio(page, "VIDEO_MADE_FOR_KIDS_NOT_MFK"); err != nil {
		if e := ytDialogError(page); e != "" {
			return fmt.Errorf("YouTube 提示：%s", e)
		}
		if ytClosed(page) {
			return errClosedDialog
		}
		return fmt.Errorf("「不是面向儿童的内容」%w", err)
	}
	if err := clickVisible(ctx, page, `ytcp-uploads-dialog #toggle-button`, "", 5*time.Second); err != nil {
		if ytClosed(page) {
			return errClosedDialog
		}
		return errors.New("未找到「展开」按钮")
	}
	if err := sleep(ctx, time.Second); err != nil {
		return err
	}
	if len(m.Tags) > 0 {
		if err := ytTags(ctx, page, m.Tags); err != nil {
			if ytClosed(page) {
				return errClosedDialog
			}
			return err
		}
	}
	altered := "VIDEO_HAS_ALTERED_CONTENT_NO"
	if m.Statement == "含AI生成内容" || m.Statement == "含虚构演绎内容" {
		altered = "VIDEO_HAS_ALTERED_CONTENT_YES"
	}
	if err := ytRadio(page, altered); err != nil {
		return fmt.Errorf("「加工的内容」%w", err)
	}

	// 下一步 to 视频元素, 检查, and 公开范围.
	for i := 0; i < 3; i++ {
		if err := clickVisible(ctx, page, `ytcp-uploads-dialog #next-button`, "", 10*time.Second); err != nil {
			if e := ytDialogError(page); e != "" {
				return fmt.Errorf("YouTube 提示：%s", e)
			}
			if ytClosed(page) {
				return errClosedDialog
			}
			return errors.New("未找到「下一步」按钮")
		}
		if err := sleep(ctx, 1500*time.Millisecond); err != nil {
			return err
		}
	}
	if err := ytRadio(page, "PUBLIC"); err != nil {
		return fmt.Errorf("「公开」%w", err)
	}
	if err := ytUploaded(ctx, page, j); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后点「发布」" + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if _, err := page.Timeout(evalTimeout).Eval(ytWatchSave); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, `ytcp-uploads-dialog #done-button`, "", 10*time.Second); err != nil {
		return errors.New("未找到「发布」按钮")
	}
	// The save (metadata_update) answers field by field. A field that
	// failed, as the thumbnail of an account not verified by phone, has
	// the page say 抱歉，我们未能保存你的视频。 and keep the dialog open,
	// though the video is published once its privacy saved.
	for deadline := time.Now().Add(2 * time.Minute); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => window.publisherYtSave ?? null`)
		if err == nil && !res.Value.Nil() {
			v := res.Value
			if !v.Get("privacy").Bool() {
				return fmt.Errorf("YouTube 没有保存成公开（保存失败的项：%s）", v.Get("failed").Str())
			}
			if failed := v.Get("failed").Str(); failed == "videoStill" {
				note += "（自定义缩略图没设上：YouTube 账号要先完成电话验证才能用，用的是自动截取的画面）"
			} else if failed != "" {
				note += "（这些项没保存上：" + failed + "）"
			}
			break
		}
		if time.Now().After(deadline) {
			return errors.New("点了「发布」2 分钟还没看到 YouTube 保存的结果")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return finish(page, j, "发布成功"+note)
}

// ytWatchSave is a script keeping, in window.publisherYtSave, how the
// upload dialog's save (metadata_update) went: whether the privacy saved,
// and the fields that failed.
const ytWatchSave = `() => {
	if (window.publisherYtSaveWatched) return;
	window.publisherYtSaveWatched = true;
	const open = XMLHttpRequest.prototype.open;
	XMLHttpRequest.prototype.open = function (method, url, ...rest) {
		this.publisherURL = String(url);
		return open.call(this, method, url, ...rest);
	};
	const send = XMLHttpRequest.prototype.send;
	XMLHttpRequest.prototype.send = function (...args) {
		this.addEventListener("loadend", () => {
			if (!/metadata_update/.test(this.publisherURL ?? "")) return;
			try {
				const body = JSON.parse(this.responseText);
				if (!body.privacy) return;
				const failed = Object.entries(body).filter(([, v]) => v && v.success === false).map(([k]) => k);
				window.publisherYtSave = { privacy: !!body.privacy.success, failed: failed.join(",") };
			} catch {}
		});
		return send.apply(this, args);
	};
}`

// ytOpenUpload opens the upload dialog with the header's 上传视频 button.
func ytOpenUpload(ctx context.Context, page *rod.Page) error {
	for deadline := time.Now().Add(20 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			if (document.querySelector("ytcp-uploads-file-picker input[type=file]")?.isConnected &&
				document.querySelector("ytcp-uploads-dialog tp-yt-paper-dialog")?.getClientRects().length) return true;
			document.querySelector("ytcp-icon-button#upload-icon")?.click();
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("上传对话框没打开，YouTube Studio 可能已改版")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// ytDialogError is the reason the upload dialog shows in place of its
// buttons, as 已达到每日上传数上限, or "" without one. The dialog keeps such
// texts hidden too (糟糕，出了点问题。); only one shown counts.
func ytDialogError(page *rod.Page) string {
	res, err := page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll("ytcp-uploads-dialog .error-short")]
		.find((e) => e.getClientRects().length && e.textContent.trim())?.textContent.trim() ?? ""`)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// errClosedDialog is the error once the upload dialog has closed by
// itself, seen after the channel had uploaded many videos that day. A
// video uploaded by then stays in Studio as a draft.
var errClosedDialog = errors.New("YouTube 的上传对话框自己关掉了，可能是今天上传次数到了上限（没做电话验证的账号上限更低）；已传上的视频会留在 Studio 里成为草稿，请在 Studio 里看看")

// ytClosed tells whether the upload dialog is no longer shown.
func ytClosed(page *rod.Page) bool {
	res, err := page.Timeout(evalTimeout).Eval(`() => !document.querySelector("ytcp-uploads-dialog tp-yt-paper-dialog")?.getClientRects().length`)
	return err == nil && res.Value.Bool()
}

// ytText leaves out the angle brackets YouTube refuses in titles and
// descriptions.
func ytText(s string) string {
	return strings.NewReplacer("<", "", ">", "").Replace(s)
}

// ytRadio picks the dialog's radio button named name.
func ytRadio(page *rod.Page, name string) error {
	res, err := page.Timeout(evalTimeout).Eval(`(name) => {
		const r = document.querySelector("ytcp-uploads-dialog tp-yt-paper-radio-button[name=" + JSON.stringify(name) + "]");
		if (!r) return false;
		if (r.getAttribute("aria-checked") !== "true") { r.scrollIntoView({ block: "center" }); r.click(); }
		return true;
	}`, name)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return errors.New("的选项没找到，YouTube Studio 可能已改版")
	}
	return nil
}

// ytTags types the tags into the tag box, comma separated, as many as fit
// in ytTagsMax.
func ytTags(ctx context.Context, page *rod.Page, tags []string) error {
	in, err := find(page, `ytcp-uploads-dialog ytcp-form-input-container#tags-container input#text-input, ytcp-uploads-dialog #tags-container input`, 5*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	var kept []string
	n := 0
	for _, t := range tags {
		t = strings.TrimSpace(strings.ReplaceAll(ytText(t), ",", ""))
		if t == "" {
			continue
		}
		if l := utf8.RuneCountInString(t) + 1; n+l <= ytTagsMax {
			kept = append(kept, t)
			n += l
		}
	}
	if err := focus(in); err != nil {
		return err
	}
	if err := page.InsertText(strings.Join(kept, ",") + ","); err != nil {
		return err
	}
	return page.Keyboard.Type(input.Enter)
}

// ytThumbnail uploads the thumbnail, which YouTube takes as it is; an
// account not yet verified by phone may not set one.
func ytThumbnail(ctx context.Context, page *rod.Page, path string) error {
	in, err := find(page, `ytcp-uploads-dialog input#file-loader`, 5*time.Second)
	if err != nil {
		return errors.New("未找到缩略图上传框（账号可能还没完成电话验证）")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// It shows the picture from a data URL once it has taken it.
	if _, err := find(page, `ytcp-uploads-dialog ytcp-thumbnail-uploader img#img-with-fallback[src^="data:image"]`, 30*time.Second); err != nil {
		return errors.New("缩略图没有上传成功")
	}
	return nil
}

// ytUploaded waits until the video is uploaded: the dialog's progress
// label then no longer counts up. Processing need not finish to publish.
func ytUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const p = document.querySelector("ytcp-uploads-dialog ytcp-video-upload-progress");
			return {
				label: p?.querySelector(".progress-label")?.textContent.trim() ?? "",
				uploading: p?.getAttribute("uploading") !== null && p?.hasAttribute("uploading"),
				done: !!document.querySelector("ytcp-uploads-dialog #done-button:not([disabled])"),
			};
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		label := res.Value.Get("label").Str()
		if strings.Contains(label, "错误") || strings.Contains(strings.ToLower(label), "error") {
			return fmt.Errorf("视频上传失败：%s", label)
		}
		if !res.Value.Get("uploading").Bool() && res.Value.Get("done").Bool() {
			return nil
		}
		j.Report(fmt.Sprintf("上传视频中 %s（已用 %s）", label, time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}
