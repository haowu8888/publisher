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

// acfunChannel is the channel (分区) videos go in, by its levels, which
// materials do not have: the user's videos explain AI.
var acfunChannel = []string{"科技", "人文科普"}

// acfunTagMax is how many tags a video takes.
const acfunTagMax = 6

// publishAcfun uploads a video to AcFun. Its form, filled while the video
// uploads, needs a cover: the material's, or else the first frame AcFun
// takes from the video.
func publishAcfun(ctx context.Context, b *browser.Browser, j Job) error {
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
	if err := clickVisible(ctx, page, `.ivu-radio-group label.ivu-radio-wrapper`, "^原创$", 5*time.Second); err != nil {
		return fmt.Errorf("类型%w", err)
	}
	var notes []string
	j.Report("选择分区")
	if err := acfunPickChannel(ctx, page, acfunChannel); err != nil {
		if j.Mode == store.ModeAuto {
			return err
		}
		notes = append(notes, "分区请手动选择："+err.Error())
	}
	if len(m.Tags) > 0 {
		j.Report("填写标签")
		if err := acfunTags(ctx, page, m.Tags); err != nil {
			return err
		}
	}

	if err := acfunUploaded(ctx, page, j); err != nil {
		return err
	}
	j.Report("设置封面")
	if m.CoverPath != "" {
		if err := acfunCover(ctx, page, m.CoverPath); err != nil {
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
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, `.video-submit-container button`, "^发布内容$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"success", "/manage"}, "投稿成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// acfunPickChannel picks the channel in its cascader: clicking a level's
// item opens the next, whose items are named with a description after the
// name.
func acfunPickChannel(ctx context.Context, page *rod.Page, levels []string) error {
	res, err := page.Timeout(evalTimeout).Eval(`async (levels) => {
		const wait = (ms) => new Promise((r) => setTimeout(r, ms));
		const box = document.querySelector(".video-channel-area.el-cascader input");
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

// acfunCover uploads the cover, which opens a cropper (上传封面图片), cut to
// the cover's shape, whose 确定 takes it.
func acfunCover(ctx context.Context, page *rod.Page, path string) error {
	in, err := find(page, `.video-cover-tool input.el-upload__input`, 5*time.Second)
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
	if _, err := find(page, `.video-cover-tool img[src^="http"]`, 30*time.Second); err != nil {
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
