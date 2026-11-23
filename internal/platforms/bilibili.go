package platforms

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
)

const bilibiliUploadURL = "https://member.bilibili.com/platform/upload/video/frame"

// publishBilibili uploads a video through the member center's upload page,
// or writes an article (专栏). The selectors follow the pages as of 2026;
// when Bilibili changes them, these are the functions to update.
func publishBilibili(ctx context.Context, b *browser.Browser, j Job) error {
	if j.Material.Kind == store.KindArticle {
		return biliArticle(ctx, b, j)
	}
	m := j.Material
	page, err := openForm(ctx, b, bilibiliUploadURL, j)
	if err != nil {
		return err
	}
	if err := startUpload(ctx, page, `.bcc-upload-wrapper input[type="file"]`, "member.bilibili.com/platform", m.VideoPath, `input[maxlength="80"]`); err != nil {
		return err
	}

	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => document.body.innerText`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		if done, _, _ := page.Timeout(evalTimeout).HasR("span", "上传完成"); done {
			break
		}
		if err := watch.check(page, res.Value.Str()); err != nil {
			return err
		}
		j.Report(fmt.Sprintf("上传视频中（已用 %s）", time.Since(start).Round(time.Second)))
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}

	j.Report("填写标题")
	title, err := find(page, `input[maxlength="80"]`, 30*time.Second)
	if err != nil {
		return fmt.Errorf("未找到标题输入框: %w", err)
	}
	if err := fill(page, title, fitTitle(m.Title, biliLimits.Title)); err != nil {
		return err
	}

	if m.Description != "" {
		j.Report("填写简介")
		if editor, err := find(page, `div.ql-editor`, 10*time.Second); err == nil {
			if err := fill(page, editor, truncate(m.Description, biliLimits.Text)); err != nil {
				return err
			}
		}
	}

	if len(m.Tags) > 0 {
		j.Report("填写标签")
		if err := bilibiliTags(ctx, page, m.Tags); err != nil {
			return err
		}
	}

	if m.Statement != "" {
		j.Report("选择创作声明")
		if err := bilibiliStatement(page, m.Statement); err != nil {
			return err
		}
	}

	if m.CoverPath != "" {
		j.Report("上传封面")
		if err := bilibiliCover(ctx, page, m.CoverPath); err != nil {
			return fmt.Errorf("上传封面失败: %w", err)
		}
	}

	if j.Mode == store.ModeReview {
		j.Report("已填写完成，请在浏览器中检查后点击「立即投稿」")
		return nil
	}

	j.Report("提交投稿")
	submit, err := findR(page, "span, button", "^立即投稿$", 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到投稿按钮: %w", err)
	}
	browser.Show(page)
	if err := submit.Click("left", 1); err != nil {
		return err
	}
	if _, err := findR(page, "*", "稿件投递成功|投稿成功", 60*time.Second); err != nil {
		return fmt.Errorf("提交后未看到成功提示，请在浏览器中查看: %w", err)
	}
	return finish(page, j, "投稿成功")
}

// bilibiliTags replaces the tags Bilibili suggests with tags.
func bilibiliTags(ctx context.Context, page *rod.Page, tags []string) error {
	// Each suggested tag has an unnamed svg that removes it.
	for range 15 {
		res, err := page.Eval(`() => {
			const x = document.querySelector(".tag-pre-wrp .label-item-v2-container svg");
			if (x) x.dispatchEvent(new MouseEvent("click", { bubbles: true }));
			return !!x;
		}`)
		if err != nil || !res.Value.Bool() {
			break
		}
		if err := sleep(ctx, 300*time.Millisecond); err != nil {
			return err
		}
	}
	in, err := find(page, `.tag-input-wrp input[placeholder*="回车"]`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到标签输入框: %w", err)
	}
	for i, tag := range tags {
		if i == 10 { // Bilibili takes at most 10 tags.
			break
		}
		// fill, not typing on: the input may still hold the last tag.
		if err := fill(page, in, truncate(tag, 20)); err != nil {
			return err
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return err
		}
		if err := sleep(ctx, 800*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// bilibiliStatement picks the creation statement whose option reads
// statement.
func bilibiliStatement(page *rod.Page, statement string) error {
	box, err := find(page, `.creation-statement-container .bcc-select-input-wrap`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到创作声明: %w", err)
	}
	if err := jsClick(box); err != nil {
		return err
	}
	return clickText(page, `.creation-statement-container li.bcc-option`, "^"+regexp.QuoteMeta(statement)+"$", 5*time.Second)
}

// bilibiliCover uploads a custom cover through the cover editor. Its
// "完成" asks first whether the 16:9 cover should follow the 4:3 one.
func bilibiliCover(ctx context.Context, page *rod.Page, path string) error {
	entry, err := find(page, `.cover-main .cover-empty, .cover-main-img > .img`, 10*time.Second)
	if err != nil {
		if entry, err = findR(page, `.cover-main span`, "添加封面|更换封面|封面设置", 5*time.Second); err != nil {
			return err
		}
	}
	if err := jsClick(entry); err != nil {
		return err
	}
	in, err := find(page, `.bcc-dialog input[type="file"][accept*="image"]`, 10*time.Second)
	if err != nil {
		return err
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if err := sleep(ctx, 3*time.Second); err != nil {
		return err
	}
	const done = `.bcc-dialog div.button.submit`
	if err := clickText(page, done, "^完成$", 10*time.Second); err != nil {
		return err
	}
	if sync, err := findR(page, "button, div.button, span", "^确认同步$", 3*time.Second); err == nil {
		if err := jsClick(sync); err != nil {
			return err
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		if err := clickText(page, done, "^完成$", 10*time.Second); err != nil {
			return err
		}
	}
	// The editor closes once the cover is taken.
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if ok, _, _ := page.HasR(".cover-main span", "封面设置|更换封面"); ok {
			return nil
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("封面编辑框未关闭")
}

// checkBilibili refuses a ModeAuto publish of a video without a creation
// statement, which Bilibili requires and only the user can choose.
func checkBilibili(m store.Material, mode store.Mode) error {
	if mode == store.ModeAuto && m.Kind == store.KindVideo && m.Statement == "" {
		return errors.New("B站全自动发布需要先在素材里选择「创作声明」，或改用确认模式在页面上选")
	}
	return nil
}
