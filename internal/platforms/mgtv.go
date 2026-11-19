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

// publishMgtv uploads a video through Mango TV's creator platform (芒果内容开放平台).
// The form, an Element UI one, shows once the file is chosen and is filled
// while the video uploads. It wants a wide (860x484) and a tall (342x478)
// cover, a category and the agreement checked. Selectors as of November
// 2026.
func publishMgtv(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://7.mgtv.com/video/edit", j)
	if err != nil {
		return err
	}
	const titleSel = `input[placeholder^="视频标题"]`
	if err := startUpload(ctx, page, `input.el-upload__input[accept^="video"]`, "7.mgtv.com", m.VideoPath, titleSel); err != nil {
		return err
	}
	dismissGuides(page)

	j.Report("填写标题")
	title, err := find(page, titleSel, 10*time.Second)
	if err != nil {
		return errors.New("未找到标题输入框")
	}
	if err := fill(page, title, fitTitle(m.Title, mgtvLimits.Title)); err != nil {
		return err
	}
	if len(m.Tags) > 0 {
		j.Report("填写标签")
		if err := mgtvTags(ctx, page, m.Tags); err != nil {
			return err
		}
	}
	if m.Description != "" {
		j.Report("填写简介")
		desc, err := find(page, `textarea[placeholder*="简介"]`, 10*time.Second)
		if err != nil {
			return errors.New("未找到简介输入框")
		}
		if err := fill(page, desc, truncate(m.Description, mgtvLimits.Text)); err != nil {
			return err
		}
	}
	if err := mgtvStatement(page, m.Statement); err != nil {
		return err
	}
	j.Report("选择分类")
	if err := mgtvCategoryPick(ctx, page, mgtvCategory); err != nil {
		return err
	}

	if err := mgtvUploaded(ctx, page, j); err != nil {
		return err
	}
	note, err := mgtvCovers(ctx, page, j)
	if err != nil {
		return err
	}
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		const box = document.querySelector(".agree-protocol .el-checkbox");
		if (box && !box.classList.contains("is-checked")) box.click();
	}`); err != nil {
		return err
	}
	if j.Mode == store.ModeReview {
		j.Report(reviewNote + note)
		return nil
	}
	j.Report("提交发布")
	startNotes(page)
	if err := clickVisible(ctx, page, "button.el-button--primary", "^发布$", 10*time.Second); err != nil {
		return err
	}
	if err := waitPublished(ctx, page, []string{"video/list"}, "发布成功|提交成功"); err != nil {
		return err
	}
	return finish(page, j, "发布成功"+note)
}

// mgtvTitleMin is the shortest title Mango TV takes.
const mgtvTitleMin = 5

// mgtvCategory is the category (分类) videos go in, by its levels, which
// Mango TV requires and materials do not have: the user's videos are on AI.
var mgtvCategory = []string{"涨知识", "科学科普"}

// checkMgtv refuses, before the form is filled, a material to submit by
// itself whose title Mango TV would turn down as too short. A review
// leaves that to the user.
func checkMgtv(m store.Material, mode store.Mode) error {
	if mode != store.ModeAuto {
		return nil
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(m.Title)); n < mgtvTitleMin {
		return fmt.Errorf("芒果TV标题至少 %d 个字，「%s」只有 %d 个字", mgtvTitleMin, m.Title, n)
	}
	return nil
}

// mgtvTags types the tags into the tag box, each ended with Enter. Mango
// TV takes 5 at most, each 2 to 8 long as it counts (mgtvTagLen): others
// are left out.
func mgtvTags(ctx context.Context, page *rod.Page, tags []string) error {
	in, err := find(page, `.video-tag input.el-select__input`, 10*time.Second)
	if err != nil {
		return errors.New("未找到标签输入框")
	}
	n := 0
	for _, t := range tags {
		if n == 5 {
			break
		}
		if l := mgtvTagLen(t); l < 2 || l > 8 {
			continue
		}
		n++
		if err := focus(in); err != nil {
			return err
		}
		if err := page.InsertText(t); err != nil {
			return err
		}
		if err := sleep(ctx, 800*time.Millisecond); err != nil {
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

// mgtvTagLen is a tag's length as Mango TV counts it: half a character
// for each ASCII one, so "AI" is too short and "LangChain" fits.
func mgtvTagLen(t string) float64 {
	n := 0.0
	for _, r := range t {
		if r < utf8.RuneSelf {
			n += 0.5
		} else {
			n++
		}
	}
	return n
}

// mgtvStatement picks the material's statement (创作声明), which Mango TV
// words as Bilibili does; a material without one gets 内容无需标注, which
// the form asks for.
func mgtvStatement(page *rod.Page, statement string) error {
	if statement == "" {
		statement = "内容无需标注"
	}
	res, err := page.Timeout(evalTimeout).Eval(`async (s) => {
		const radio = [...document.querySelectorAll("label.el-radio")].find((r) => r.textContent.trim() === s);
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

// mgtvCategoryPick opens the category cascader and clicks each level in
// turn; picking the last closes it.
func mgtvCategoryPick(ctx context.Context, page *rod.Page, levels []string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".class-id input").click()`); err != nil {
		return err
	}
	for i, name := range levels {
		var names []string
		for deadline := time.Now().Add(10 * time.Second); ; {
			res, err := page.Timeout(evalTimeout).Eval(`(i, name) => {
				const menu = document.querySelectorAll(".m-cascader-pop .el-cascader-menu")[i];
				if (!menu) return [];
				const nodes = [...menu.querySelectorAll(".el-cascader-node")];
				const node = nodes.find((n) => n.textContent.trim() === name);
				if (node) node.click();
				return node ? null : nodes.map((n) => n.textContent.trim());
			}`, i, name)
			if err == nil && res.Value.Nil() {
				break
			}
			if err == nil {
				names = nil
				for _, v := range res.Value.Arr() {
					names = append(names, v.Str())
				}
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("分类里没有「%s」（现有：%s）", name, strings.Join(names, "、"))
			}
			if err := sleep(ctx, 500*time.Millisecond); err != nil {
				return err
			}
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	res, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".class-id input").value`)
	if err != nil {
		return err
	}
	if res.Value.Str() == "" {
		return errors.New("分类没选上")
	}
	return nil
}

// mgtvUploaded waits until the video is uploaded: the upload box then says
// 上传成功.
func mgtvUploaded(ctx context.Context, page *rod.Page, j Job) error {
	start := time.Now()
	var watch uploadWatch
	for {
		res, err := page.Timeout(evalTimeout).Eval(`() => {
			const text = document.body.innerText;
			const pct = [...document.querySelectorAll("span, div, p")]
				.filter((e) => e.children.length === 0)
				.map((e) => e.textContent.trim())
				.find((t) => /^\d{1,2}(\.\d+)?%$/.test(t));
			return { done: text.includes("上传成功"), pct: pct || "", text };
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

// mgtvCovers sets both covers, which Mango TV requires: the material's
// cover in the one of its shape, a frame of the video, as the form offers
// them, in the other or in both when there is no cover or it does not
// take. The note returned says when the cover did not.
func mgtvCovers(ctx context.Context, page *rod.Page, j Job) (string, error) {
	cover := j.Material.CoverPath
	ownSlot := ""
	if cover != "" {
		ownSlot = "vertical"
		if wide(cover) {
			ownSlot = "cross"
		}
	}
	note := ""
	for _, slot := range []string{"cross", "vertical"} {
		if slot == ownSlot {
			j.Report("设置封面")
			err := mgtvCover(ctx, page, slot, cover)
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			page.Timeout(evalTimeout).Keyboard.Type(input.Escape)
			note = "（封面没设上，用的是视频画面：" + err.Error() + "）"
		}
		j.Report("设置视频画面封面")
		if err := mgtvCover(ctx, page, slot, ""); err != nil {
			return "", fmt.Errorf("设置封面失败: %w", err)
		}
	}
	return note, nil
}

// mgtvCover sets the cover of a slot ("cross", the wide one, or
// "vertical"): the picture at path, or without one the last of the frames
// of the video the slot offers, the first being often still dark. Either opens a cropper; once its 确定裁剪 is taken
// the picture is uploaded and its address goes in the slot's hidden input.
func mgtvCover(ctx context.Context, page *rod.Page, slot, path string) error {
	wrap := `.upload-img-wrap.` + slot
	if _, err := page.Timeout(evalTimeout).Eval(`(wrap) => {
		window.publisherOldCover = document.querySelector(wrap).closest(".upload-img-item").querySelector("input[type=hidden]").value;
	}`, wrap); err != nil {
		return err
	}
	if path != "" {
		in, err := find(page, wrap+` input[type=file]`, 10*time.Second)
		if err != nil {
			return errors.New("未找到封面上传框")
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
	} else {
		for deadline := time.Now().Add(time.Minute); ; {
			res, err := page.Timeout(evalTimeout).Eval(`(wrap) => {
				const img = [...document.querySelectorAll(wrap + ' .cover-list-select-wrap li img[src^="data:"]')].pop();
				if (img) img.click();
				return !!img;
			}`, wrap)
			if err == nil && res.Value.Bool() {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("页面没有给出视频画面")
			}
			if err := sleep(ctx, time.Second); err != nil {
				return err
			}
		}
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => [...document.querySelectorAll(".crop-dialog")].some((d) => d.getClientRects().length)`)
		if err == nil && res.Value.Bool() {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("裁剪框没出来")
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if err := clickUntilGone(ctx, page, ".crop-dialog button", "^确定裁剪$", 2*time.Second, 30*time.Second); err != nil {
		return fmt.Errorf("裁剪框%w", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`(wrap) => {
			const v = document.querySelector(wrap).closest(".upload-img-item").querySelector("input[type=hidden]").value;
			return v.startsWith("http") && v !== window.publisherOldCover;
		}`, wrap)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面图片没有上传成功")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}
