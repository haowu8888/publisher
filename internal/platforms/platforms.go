// Package platforms knows each publishing platform: where to log in, how to
// tell a login apart, and how to fill and submit its publish form.
package platforms

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"publisher/internal/browser"
	"publisher/internal/cookiesync"
	"publisher/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// ErrNotLoggedIn is returned when a publish finds the login page instead.
var ErrNotLoggedIn = errors.New("未登录，请先在「账号」页登录该平台")

// Job is what a publish needs.
type Job struct {
	Material store.Material
	Mode     store.Mode
	// Collection is the collection (合集) to add the material to, if any.
	Collection string
	// Original declares the material original (原创声明) where the
	// platform has that.
	Original bool
	// Report shows the current step to the user.
	Report func(step string)
}

// PublishFunc fills, and in ModeAuto submits, the platform's publish form.
// In ModeReview it returns once the form is filled, leaving its tab open.
type PublishFunc func(ctx context.Context, b *browser.Browser, j Job) error

// Platform describes one publishing platform.
type Platform struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kinds are the materials the platform takes. Publish tells them
	// apart by the job's material.
	Kinds []store.Kind `json:"kinds"`
	// HomeURL is the platform's creator center, which shows its login
	// page to a visitor who is not logged in.
	HomeURL string `json:"homeUrl"`
	// Cookie, set on CookieDomain or a subdomain, exists only once the
	// user is logged in. Without one, the login is told from where HomeURL
	// lands: on a URL containing LoggedInMark, or else on one without
	// LoginMark.
	Cookie       string `json:"-"`
	CookieDomain string `json:"-"`
	LoginMark    string `json:"-"`
	LoggedInMark string `json:"-"`
	// CookieDomains hold the platform's login cookies, to copy from the
	// user's own Chrome (see package cookiesync), or to clear before a
	// login.
	CookieDomains []string `json:"-"`
	// LoginText, when set, is shown by HomeURL to a visitor not logged in.
	// CheckLogin then goes by the page rather than by Cookie, which can
	// outlive a session the server ended.
	LoginText string `json:"-"`
	// LoginNote, when set, says why the platform's login cannot be copied
	// from the user's Chrome; the user scans a QR code in the app instead.
	LoginNote string `json:"loginNote,omitempty"`
	// Check refuses a material the platform cannot publish in mode before
	// any of it is uploaded.
	Check func(m store.Material, mode store.Mode) error `json:"-"`
	// Publish is nil for a platform whose publishing is not written yet.
	Publish PublishFunc `json:"-"`
	// Collections is whether Publish adds materials to a collection (合集).
	Collections bool `json:"collections,omitempty"`
	// Limits are how long the platform lets titles and texts be, by
	// material kind, and "images" for the official account's picture
	// posts.
	Limits map[string]Limits `json:"limits,omitempty"`
	// AutoNote says what ModeAuto does when it stops short of publishing.
	AutoNote string `json:"autoNote,omitempty"`
}

// Ready reports whether publishing to p is implemented.
func (p *Platform) Ready() bool { return p.Publish != nil }

// Takes reports whether p publishes materials of kind k.
func (p *Platform) Takes(k store.Kind) bool { return slices.Contains(p.Kinds, k) }

// Limits are the most characters a platform takes in a title and in a
// text, a video's description or an article's text; publishing cuts what
// is longer. The first Tags of the material's tags go into the text as
// "#tag " and count against it. Zero is no limit, or no title at all for
// Kuaishou, which puts the title into the text.
type Limits struct {
	Title int `json:"title"`
	Text  int `json:"text"`
	Tags  int `json:"tags,omitempty"`
}

// fitText shortens text to leave room for the tags that follow it.
func (l Limits) fitText(text string, tags []string) string {
	n := l.Text - 1
	for _, t := range tags[:min(len(tags), l.Tags)] {
		n -= utf8.RuneCountInString(t) + 2
	}
	return truncate(text, max(n, 0))
}

var (
	biliLimits     = Limits{Title: 80, Text: 2000}
	douyinLimits   = Limits{Title: 30, Text: 1000, Tags: 5}
	kuaishouLimits = Limits{Text: 500, Tags: 4}
	xhsLimits      = Limits{Title: 20, Text: 1000, Tags: 10}
	channelsLimits = Limits{Text: 1000, Tags: 10}
	mpLimits       = Limits{Title: 64}
	// Baijiahao's video takes no description, only a short one that
	// shows as its title.
	bjhVideoLimits   = Limits{Title: 50}
	bjhArticleLimits = Limits{Title: 64}
	mpImageLimits    = Limits{Title: 20, Text: 1000}
)

var (
	video     = []store.Kind{store.KindVideo}
	article   = []store.Kind{store.KindArticle}
	videoText = []store.Kind{store.KindVideo, store.KindArticle}
)

// All lists the platforms in display order.
var All = []*Platform{
	{
		ID: "bilibili", Name: "哔哩哔哩", Kinds: video,
		HomeURL: "https://member.bilibili.com/platform/home",
		Cookie:  "SESSDATA", CookieDomain: "bilibili.com",
		CookieDomains: []string{"bilibili.com"},
		Publish:       publishBilibili,
		Limits:        map[string]Limits{"video": biliLimits},
		Check:         checkBilibili,
	},
	{
		ID: "douyin", Name: "抖音", Kinds: video,
		HomeURL: "https://creator.douyin.com/creator-micro/home",
		Cookie:  "sessionid", CookieDomain: "douyin.com",
		CookieDomains: []string{"douyin.com"},
		Publish:       publishDouyin,
		Limits:        map[string]Limits{"video": douyinLimits},
		Collections:   true,
		// Douyin signs its write requests with keys kept in the page's
		// storage beside the login, so copied cookies show as logged in
		// but publishing fails with 登录失效.
		LoginNote: "需在本应用里扫码登录：从 Chrome 同步的登录能看不能发",
		LoginText: "扫码登录",
	},
	{
		ID: "kuaishou", Name: "快手", Kinds: video,
		HomeURL:       "https://cp.kuaishou.com/profile",
		LoginMark:     "passport",
		CookieDomains: []string{"kuaishou.com"},
		Publish:       publishKuaishou,
		Limits:        map[string]Limits{"video": kuaishouLimits},
	},
	{
		ID: "xiaohongshu", Name: "小红书", Kinds: videoText,
		HomeURL:       "https://creator.xiaohongshu.com/new/home",
		LoginMark:     "login",
		CookieDomains: []string{"xiaohongshu.com"},
		Publish:       publishXiaohongshu,
		Limits:        map[string]Limits{"video": xhsLimits, "article": xhsLimits},
		Collections:   true,
	},
	{
		ID: "weixin-channels", Name: "视频号", Kinds: video,
		HomeURL:       "https://channels.weixin.qq.com/platform",
		LoginMark:     "login",
		CookieDomains: []string{"channels.weixin.qq.com"},
		Publish:       publishWeixinChannels,
		Limits:        map[string]Limits{"video": channelsLimits},
		Collections:   true,
	},
	{
		ID: "weixin-mp", Name: "公众号", Kinds: article,
		HomeURL:      "https://mp.weixin.qq.com/",
		LoggedInMark: "token=",
		// Its cookies are all on mp.weixin.qq.com; weixin.qq.com would
		// take in Channels', and a login here would drop that one.
		CookieDomains: []string{"mp.weixin.qq.com"},
		Publish:       publishWeixinMP,
		Limits:        map[string]Limits{"article": mpLimits, "images": mpImageLimits},
		// Mass sending needs the admin's QR scan on the phone anyway.
		AutoNote: "全自动只保存到草稿箱，群发需在公众号后台扫码确认",
	},
	{
		ID: "baijiahao", Name: "百家号", Kinds: videoText,
		HomeURL: "https://baijiahao.baidu.com/builder/rc/home",
		Cookie:  "BDUSS", CookieDomain: "baidu.com",
		CookieDomains: []string{"baidu.com"},
		Publish:       publishBaijiahao,
		Limits:        map[string]Limits{"video": bjhVideoLimits, "article": bjhArticleLimits},
	},
}

// Get returns the platform with id, or nil.
func Get(id string) *Platform {
	i := slices.IndexFunc(All, func(p *Platform) bool { return p.ID == id })
	if i < 0 {
		return nil
	}
	return All[i]
}

// Owns reports whether a cookie of domain belongs to p.
func (p *Platform) Owns(domain string) bool {
	domain = strings.TrimPrefix(domain, ".")
	for _, d := range p.CookieDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

// SyncPlatforms lists the platforms for the cookie sync extension; those
// whose logins cannot be copied come without domains, with the reason.
func SyncPlatforms() []cookiesync.Platform {
	out := []cookiesync.Platform{}
	for _, p := range All {
		sp := cookiesync.Platform{ID: p.ID, Name: p.Name, Domains: p.CookieDomains, Note: p.LoginNote}
		if p.LoginNote != "" {
			sp.Domains = nil
		}
		out = append(out, sp)
	}
	return out
}

// hasCookie reports whether cookies hold p's login cookie.
func (p *Platform) hasCookie(cookies []*proto.NetworkCookie) bool {
	now := float64(time.Now().Unix())
	for _, c := range cookies {
		d := strings.TrimPrefix(c.Domain, ".")
		if c.Name == p.Cookie && c.Value != "" &&
			(d == p.CookieDomain || strings.HasSuffix(d, "."+p.CookieDomain)) &&
			(c.Session || float64(c.Expires) > now) {
			return true
		}
	}
	return false
}

// landedLoggedIn reports whether a page that opened HomeURL now shows the
// creator center rather than a login page.
func (p *Platform) landedLoggedIn(pageURL string) bool {
	u, err := url.Parse(pageURL)
	home, _ := url.Parse(p.HomeURL)
	if err != nil || u.Host != home.Host {
		return false
	}
	if p.LoggedInMark != "" {
		return strings.Contains(pageURL, p.LoggedInMark)
	}
	return p.LoginMark == "" || !strings.Contains(pageURL, p.LoginMark)
}

// CheckLogin reports whether the browser profile is logged in to p.
func (p *Platform) CheckLogin(ctx context.Context, b *browser.Browser) (bool, error) {
	if p.Cookie != "" && p.LoginText == "" {
		rb, err := b.Get()
		if err != nil {
			return false, err
		}
		cookies, err := rb.GetCookies()
		return p.hasCookie(cookies), err
	}
	page, err := b.Page(p.HomeURL)
	if err != nil {
		return false, err
	}
	defer page.Close()
	page = page.Context(ctx)
	if err := page.WaitLoad(); err != nil {
		return false, err
	}
	// Login redirects often happen in the page's script after load.
	if err := sleep(ctx, 3*time.Second); err != nil {
		return false, err
	}
	info, err := page.Info()
	if err != nil {
		return false, err
	}
	if p.LoginText != "" {
		res, err := page.Eval(`(text) => document.body.innerText.includes(text)`, p.LoginText)
		if err != nil {
			return false, err
		}
		return p.landedLoggedIn(info.URL) && !res.Value.Bool(), nil
	}
	return p.landedLoggedIn(info.URL), nil
}

// Login opens p's creator center for the user to log in, and waits until
// they have, or ctx ends. The tab closes once the login is seen. It drops
// the old login first, which may be one that only looks valid.
func (p *Platform) Login(ctx context.Context, b *browser.Browser) error {
	if err := p.logout(b); err != nil {
		return err
	}
	page, err := b.Page(p.HomeURL)
	if err != nil {
		return err
	}
	page.Activate()
	// Waiting long for a QR code scan, so not with page.Context(ctx).
	for {
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		var ok bool
		if p.Cookie != "" {
			cookies, err := page.Browser().GetCookies()
			if err != nil {
				return err
			}
			ok = p.hasCookie(cookies)
		} else {
			info, err := page.Info()
			if err != nil {
				return errors.New("登录页已关闭")
			}
			ok = p.landedLoggedIn(info.URL)
		}
		if ok {
			page.Close()
			return nil
		}
	}
}

// logout deletes p's cookies from the browser profile.
func (p *Platform) logout(b *browser.Browser) error {
	rb, err := b.Get()
	if err != nil {
		return err
	}
	cookies, err := rb.GetCookies()
	if err != nil {
		return err
	}
	var gone []*proto.NetworkCookieParam
	for _, c := range cookies {
		if p.Owns(c.Domain) {
			// An expiry in the past deletes the cookie.
			gone = append(gone, &proto.NetworkCookieParam{
				Name: c.Name, Value: "", Domain: c.Domain, Path: c.Path,
				Secure: c.Secure, HTTPOnly: c.HTTPOnly, Expires: 1,
			})
		}
	}
	if len(gone) == 0 {
		return nil
	}
	return rb.SetCookies(gone)
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// fill replaces the text of an input, a textarea or a contenteditable
// editor, as typing would.
func fill(page *rod.Page, el *rod.Element, text string) error {
	if err := el.Focus(); err != nil {
		return err
	}
	if _, err := el.Eval(`() => {
		if (this.select) return this.select();
		const r = document.createRange();
		r.selectNodeContents(this);
		const s = getSelection();
		s.removeAllRanges();
		s.addRange(r);
	}`); err != nil {
		return err
	}
	if text == "" {
		return page.Keyboard.Type(input.Backspace)
	}
	return page.InsertText(text)
}

// truncate shortens s to n characters.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// fitTitle shortens a title to n characters, ending it at a clause or word
// break when that keeps more than half of it.
func fitTitle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	for i := n; i > n/2; i-- {
		if strings.ContainsRune("，；：、—。,;: （(", r[i]) || strings.ContainsRune("？！?!", r[i-1]) {
			return strings.TrimSpace(string(r[:i]))
		}
	}
	return string(r[:n])
}
