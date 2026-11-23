package platforms

import (
	"context"
	"errors"
	"fmt"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
)

// biliArticleURL is the article editor (专栏), which the member center
// shows in a frame; on its own it works the same, and after publishing
// goes to the article list.
const biliArticleURL = "https://member.bilibili.com/york/read-editor"

// biliArticle writes an article: the title, the body pasted as HTML (a
// tiptap editor), the cover, and the AI statement (AI辅助创作声明) for a
// material that has AI-made content. The editor uploads each pasted
// picture, a data URL one or one on the web, to Bilibili by itself, and
// goes on showing it from a data URL.
func biliArticle(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, biliArticleURL, j)
	if err != nil {
		return err
	}
	title, err := waitElement(ctx, page, `textarea.title-input__inner`, "member.bilibili.com/york")
	if err != nil {
		return err
	}
	editor, err := find(page, `.tiptap.ProseMirror`, 30*time.Second)
	if err != nil {
		return errors.New("未找到正文编辑器")
	}
	j.Report("填写标题")
	if err := fill(page, title, fitTitle(m.Title, biliArticleLimits.Title)); err != nil {
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
	if err := biliImagesUploaded(ctx, editor, j); err != nil {
		return err
	}

	note, err := setCover(ctx, page, j, biliArticleCover)
	if err != nil {
		return err
	}
	if m.Statement == "含AI生成内容" {
		if _, err := page.Timeout(evalTimeout).Eval(`() => {
			const box = [...document.querySelectorAll(".declaration-item label.vui_checkbox")]
				.find((l) => l.querySelector(".declaration-title")?.textContent.trim() === "AI辅助创作声明");
			if (!box) throw new Error("未找到「AI辅助创作声明」");
			if (!box.querySelector("input").checked) box.click();
		}`); err != nil {
			return err
		}
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, `button.vui_button--blue`, "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"upload-manager/opus"}, "已提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// biliImagesUploaded waits until the editor has uploaded the body's
// pictures: their nodes' status is 0 while one uploads and 1 once it is
// stored, at data-url; another status is a failure, its message saying
// why.
func biliImagesUploaded(ctx context.Context, editor *rod.Element, j Job) error {
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := editor.Timeout(evalTimeout).Eval(`() => {
			const out = { total: 0, done: 0, failed: "" };
			this.editor.state.doc.descendants((n) => {
				if (n.type.name !== "enhancedImage") return;
				out.total++;
				if (n.attrs.status === 1 && n.attrs["data-url"]) out.done++;
				else if (n.attrs.status !== 0) out.failed ||= n.attrs.message || "状态 " + n.attrs.status;
			});
			return out;
		}`)
		if err != nil {
			return fmt.Errorf("页面无响应: %w", err)
		}
		v := res.Value
		if f := v.Get("failed").Str(); f != "" {
			return fmt.Errorf("正文图片上传失败：%s", f)
		}
		total, done := v.Get("total").Int(), v.Get("done").Int()
		if done == total {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("正文图片 3 分钟还没传完（%d/%d）", done, total)
		}
		j.Report(fmt.Sprintf("上传正文图片 %d/%d", done, total))
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// biliArticleCover sets the article's cover: the 自定义封面 switch shows
// 添加封面, which opens a menu whose 本地上传 takes the picture into a
// cropper, cut to 16:9, which 确定 takes.
func biliArticleCover(ctx context.Context, page *rod.Page, path string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		const item = [...document.querySelectorAll(".form-item")].find((f) => f.querySelector(".form-item-label")?.textContent.trim() === "自定义封面");
		const sw = item?.querySelector(".vui_switch--switch");
		if (!sw) throw new Error("未找到「自定义封面」");
		if (sw.getAttribute("aria-checked") !== "true") sw.click();
	}`); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, `.select-cover .upload-button`, "添加封面", 10*time.Second); err != nil {
		return err
	}
	in, err := find(page, `.select-method input[type=file]`, 10*time.Second)
	if err != nil {
		return errors.New("未找到封面上传框")
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	if _, err := find(page, `.vui_dialog--root .vui_dialog--btn-confirm`, 10*time.Second); err != nil {
		return errors.New("封面裁剪框没出来")
	}
	// The cropper takes no click before its picture shows.
	if err := clickUntilGone(ctx, page, `.vui_dialog--root .vui_dialog--btn-confirm`, "^确定$", 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("封面裁剪框%w", err)
	}
	if _, err := find(page, `.selected-cover img[src*="hdslb.com"]`, 30*time.Second); err != nil {
		return errors.New("封面没有上传成功")
	}
	return nil
}
