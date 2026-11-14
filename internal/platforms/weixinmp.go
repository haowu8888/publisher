package platforms

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"publisher/internal/browser"
	"publisher/internal/store"

	"github.com/go-rod/rod"
)

// publishWeixinMP writes an article, or a picture post when the material
// says so, into a new draft in the official account's editor. ModeAuto
// saves the draft; mass sending stays with the user, since it needs the
// admin's QR scan anyway.
//
// Selectors as of November 2026, when the editor was ProseMirror based.
func publishWeixinMP(ctx context.Context, b *browser.Browser, j Job) error {
	m := j.Material
	page, err := openForm(ctx, b, "https://mp.weixin.qq.com/", j)
	if err != nil {
		return err
	}
	// The home page redirects to a URL carrying the session's token.
	var token string
	for deadline := time.Now().Add(15 * time.Second); token == ""; {
		if info, err := page.Info(); err == nil {
			if u, err := url.Parse(info.URL); err == nil {
				token = u.Query().Get("token")
			}
		}
		if token != "" {
			break
		}
		if time.Now().After(deadline) {
			page.Close()
			return ErrNotLoggedIn
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}

	if m.WeixinType == "images" {
		err = mpImages(ctx, page, token, j)
	} else {
		err = mpArticle(ctx, page, token, j)
	}
	if err != nil || j.Mode == store.ModeReview {
		return err
	}
	j.Report("保存草稿")
	// The editor now and then ignores 保存为草稿 right after the form is
	// filled, while it still takes in what was pasted; a later click saves.
	// Saving twice is the same draft saved again.
	for try := 1; ; try++ {
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		if err := clickText(page, "button, span, a", "^保存为草稿$", 10*time.Second); err != nil {
			return err
		}
		err := waitPublishedFor(ctx, page, []string{"appmsgid="}, "已保存|保存成功", 20*time.Second)
		if err == nil {
			break
		}
		if try == 4 || ctx.Err() != nil {
			return fmt.Errorf("保存草稿未确认: %w", err)
		}
	}
	return finish(page, j, "已保存到草稿箱")
}

// mpEdit opens a new draft: an article, or with extra "&createType=8" a
// picture post.
func mpEdit(page *rod.Page, token, extra string) error {
	edit := "https://mp.weixin.qq.com/cgi-bin/appmsg?t=media/appmsg_edit_v2&action=edit&isNew=1&type=77&lang=zh_CN&token=" + url.QueryEscape(token) + extra
	if err := page.Navigate(edit); err != nil {
		return err
	}
	return page.WaitLoad()
}

// mpArticle writes an article (图文): its title, its body with its
// pictures, which the editor stores on its own servers as they are
// pasted, its digest and its cover.
func mpArticle(ctx context.Context, page *rod.Page, token string, j Job) error {
	m := j.Material
	j.Report("新建图文草稿")
	if err := mpEdit(page, token, ""); err != nil {
		return err
	}

	j.Report("填写标题和正文")
	title, err := waitElement(ctx, page, `.title-editor__input .ProseMirror`, "appmsg")
	if err != nil {
		return err
	}
	dismissGuides(page)
	if err := fill(page, title, truncate(m.Title, mpLimits.Title)); err != nil {
		return err
	}
	body, err := mpEditor(page)
	if err != nil {
		return err
	}
	content, err := inlineImages(spreadImages(markdownHTML(articleBody(m)), bodyImages(m)), articleDirs(m)...)
	if err != nil {
		return err
	}
	if err := pasteHTML(body, content, m.Body); err != nil {
		return err
	}
	if strings.Contains(content, `src="data:`) {
		j.Report("上传正文图片")
		if err := mpImagesStored(ctx, body); err != nil {
			return err
		}
	}
	digest := m.Description
	if digest == "" {
		digest = plainText(articleBody(m))
	}
	if el, err := find(page, `textarea[name="digest"]`, 3*time.Second); err == nil {
		if err := fill(page, el, truncate(digest, 120)); err != nil {
			return err
		}
	}
	if m.CoverPath != "" {
		j.Report("设置封面")
		if err := mpCover(ctx, page, m.CoverPath); err != nil {
			return fmt.Errorf("设置封面失败: %w", err)
		}
	}
	if j.Mode == store.ModeReview {
		if m.CoverPath == "" {
			j.Report("已填写完成；请设置封面，检查后点「保存为草稿」或「发表」")
		} else {
			j.Report("已填写完成；检查后点「保存为草稿」或「发表」")
		}
	}
	return nil
}

// mpImagesStored waits until the editor has stored every pasted picture,
// none left as a data URL.
func mpImagesStored(ctx context.Context, body *rod.Element) error {
	for deadline := time.Now().Add(3 * time.Minute); ; {
		res, err := body.Timeout(evalTimeout).Eval(`() => this.querySelectorAll('img[src^="data:"]').length`)
		if err == nil && res.Value.Int() == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("正文图片上传超时")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// mpCover uploads the cover through the picture library dialog, which
// selects what it just uploaded, and takes the default crops.
func mpCover(ctx context.Context, page *rod.Page, path string) error {
	if _, err := page.Timeout(evalTimeout).Eval(`() => {
		const b = document.querySelector(".js_cover_btn_area");
		if (b) ["mouseover", "mouseenter"].forEach((t) => b.dispatchEvent(new MouseEvent(t, { bubbles: true })));
		document.querySelector(".js_imagedialog").click();
	}`); err != nil {
		return fmt.Errorf("未找到「从图片库选择」: %w", err)
	}
	in, err := find(page, `.weui-desktop-dialog input[type=file]`, 15*time.Second)
	if err != nil {
		return fmt.Errorf("图片库没有打开: %w", err)
	}
	if err := in.SetFiles([]string{path}); err != nil {
		return err
	}
	// The list renders again now and then; the upload comes first in it.
	selected := `.weui-desktop-img-picker__item.selected`
	if err := waitCount(ctx, page, selected, 1, 30*time.Second); err != nil {
		if err := waitCount(ctx, page, `.weui-desktop-img-picker__item`, 1, 15*time.Second); err != nil {
			return errors.New("图片库里没有出现上传的封面")
		}
		if _, err := page.Timeout(evalTimeout).Eval(`() => document.querySelector(".weui-desktop-img-picker__item i").click()`); err != nil {
			return err
		}
	}
	if err := clickVisible(ctx, page, ".weui-desktop-dialog button", "^下一步$", 10*time.Second); err != nil {
		return err
	}
	if err := clickVisible(ctx, page, ".weui-desktop-dialog button", "^确认$", 20*time.Second); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		res, err := page.Timeout(evalTimeout).Eval(`() => !!document.querySelector('.setting-group__cover_area [style*="mmbiz"]')`)
		if err == nil && res.Value.Bool() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("封面没有出现在页面上")
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// mpImages writes a picture post (贴图): its pictures, cover first, its
// title and its description.
func mpImages(ctx context.Context, page *rod.Page, token string, j Job) error {
	m := j.Material
	images := noteImages(m)
	if len(images) == 0 {
		return errors.New("贴图至少需要一张图片：请在素材里选择图片或封面")
	}
	if len(images) > 20 {
		images = images[:20]
	}
	j.Report("新建贴图草稿")
	if err := mpEdit(page, token, "&createType=8"); err != nil {
		return err
	}
	// The picker's own input, not the one of the toolbar's picture menu.
	sel := `input[type=file][accept^="image/bmp"]`
	if _, err := waitElement(ctx, page, sel, "appmsg"); err != nil {
		return err
	}
	dismissGuides(page)
	for i, p := range images {
		j.Report(fmt.Sprintf("上传图片 %d/%d", i+1, len(images)))
		if err := mpAddImage(ctx, page, sel, p, i+1); err != nil {
			return fmt.Errorf("第 %d 张图片没有上传成功: %w", i+1, err)
		}
	}

	j.Report("填写标题和描述")
	title, err := find(page, `.title-editor__input .ProseMirror`, 10*time.Second)
	if err != nil {
		return fmt.Errorf("未找到标题框: %w", err)
	}
	if err := fill(page, title, fitTitle(m.Title, mpImageLimits.Title)); err != nil {
		return err
	}
	desc := m.Description
	if desc == "" {
		desc = paragraphs(articleBody(m))
	}
	if desc != "" {
		el, err := find(page, `.share-text__input .ProseMirror`, 10*time.Second)
		if err != nil {
			return fmt.Errorf("未找到描述框: %w", err)
		}
		desc = truncate(desc, mpImageLimits.Text)
		if err := pasteHTML(el, paragraphsHTML(desc), desc); err != nil {
			return err
		}
	}
	if j.Mode == store.ModeReview {
		j.Report("已填写完成；检查后点「保存为草稿」或「发表」")
	}
	return nil
}

// mpAddImage adds a picture to a picture post, which then has n stored
// on the official account's servers: an item shows its picture from there
// once uploaded, and a draft saved before that is not saved. The picker
// ignores a file given while it still takes in the one before, so it is
// given again.
func mpAddImage(ctx context.Context, page *rod.Page, sel, path string, n int) error {
	for try := 0; ; try++ {
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		in, err := find(page, sel, 10*time.Second)
		if err != nil {
			return err
		}
		if err := in.SetFiles([]string{path}); err != nil {
			return err
		}
		err = waitCount(ctx, page, `.image-selector__bottom-list-item[style*="mmbiz"]`, n, time.Minute)
		if err == nil || try == 2 || ctx.Err() != nil {
			return err
		}
	}
}

// mpEditor finds the body editor: the ProseMirror one of the current
// editor, or the body of the older UEditor's frame.
func mpEditor(page *rod.Page) (*rod.Element, error) {
	if el, err := find(page, `.mock-iframe-body .ProseMirror`, 10*time.Second); err == nil {
		return el, nil
	}
	if frame, err := find(page, `#ueditor_0`, 3*time.Second); err == nil {
		if fp, err := frame.Frame(); err == nil {
			return fp.Element("body")
		}
	}
	return nil, errors.New("未找到正文编辑器，公众号编辑器可能已改版")
}
