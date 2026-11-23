// Command inspect runs JavaScript in a tab of the app's automation Chrome,
// to study a platform's publish page while writing its automation. It runs
// on the Windows host, beside the running app:
//
//	go run ./.dev/inspect <url or tab URL substring> <base64 js> [screenshot.png]
//
// A script "upload:<selector>|<file>" instead sets a file input's files
// ("<host> >>> <selector>" for one in a shadow root),
// "shadow:<selector>" prints the element's shadow root, closed or not, and
// "cookies:<name,...>" prints the profile's cookies of those names,
// "cookiedomains:<domain,...>" the names of those on the domains, and
// "close" closes the tab and "shadowclick:<host>|<selector>" clicks an
// element in a host's shadow root, closed or not. "chooser:<selector>|<text>|<file>"
// clicks an element and answers the file chooser it opens, and
// "jschooser:<selector>|<file>" does so with a script click. "window" prints
// the state of the tab's window (minimized, maximized...) and whether the
// page is hidden; "window:<state>" sets the state first.
//
// A first argument starting with "http" opens a new tab; otherwise it picks
// the open tab whose URL contains it.
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func main() {
	// A page blocked by a native dialog never answers; give up instead of hanging.
	time.AfterFunc(30*time.Second, func() { fmt.Println("timeout: page not responding (native dialog?)"); os.Exit(2) })
	data, err := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), "Publisher", "chrome-profile", "DevToolsActivePort"))
	if err != nil {
		panic(err)
	}
	port, path, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	// No device emulation: rod's default one would give the tab a Mac
	// Chrome 114 user agent, which sites such as Google refuse.
	b := rod.New().ControlURL("ws://127.0.0.1:" + strings.TrimSpace(port) + strings.TrimSpace(path)).NoDefaultDevice().MustConnect()

	var page *rod.Page
	if strings.HasPrefix(os.Args[1], "http") {
		page = b.MustPage(os.Args[1])
		page.MustWaitLoad()
		time.Sleep(4 * time.Second)
	} else {
		for _, p := range b.MustPages() {
			if info, err := p.Info(); err == nil && info.Type == proto.TargetTargetInfoTypePage && strings.Contains(info.URL, os.Args[1]) {
				page = p
			}
		}
		if page == nil {
			panic("no tab matches " + os.Args[1])
		}
		// Chrome freezes a tab left in the background for a while, and a
		// frozen one runs no script.
		page.MustActivate()
	}
	fmt.Println("url:", page.MustInfo().URL)
	if len(os.Args) > 2 && os.Args[2] != "" {
		js, err := base64.StdEncoding.DecodeString(os.Args[2])
		if err != nil {
			panic(err)
		}
		if state, ok := strings.CutPrefix(string(js), "window"); ok && (state == "" || state[0] == ':') {
			if state != "" {
				if err := page.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowState(state[1:])}); err != nil {
					panic(err)
				}
			}
			w := page.MustGetWindow()
			fmt.Printf("state=%s bounds=%v,%v %vx%v visibility=%s\n", w.WindowState, *w.Left, *w.Top, *w.Width, *w.Height, page.MustEval(`() => document.visibilityState`).Str())
			return
		}
		if string(js) == "close" {
			// Leaving an editor with content asks 离开此网站？, which would
			// block the browser.
			go page.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
				proto.PageHandleJavaScriptDialog{Accept: true}.Call(page)
			})()
			page.MustClose()
			fmt.Println("closed")
			return
		}
		if arg, ok := strings.CutPrefix(string(js), "shadowclick:"); ok {
			host, sel, _ := strings.Cut(arg, "|")
			page.MustElement(host).MustShadowRoot().MustElement(sel).MustEval(`() => this.click()`)
			fmt.Println("clicked")
			return
		}
		if arg, ok := strings.CutPrefix(string(js), "chooser:"); ok {
			// "chooser:<selector>|<text regexp>|<files>" clicks the element
			// as a user would and answers the file chooser it opens, for a
			// page that ignores files set on its input by script.
			sel, rest, _ := strings.Cut(arg, "|")
			text, files, _ := strings.Cut(rest, "|")
			set := page.MustHandleFileDialog()
			page.MustElementR(sel, text).MustClick()
			set(strings.Split(files, ";")...)
			fmt.Println("chosen", files)
			return
		}
		if arg, ok := strings.CutPrefix(string(js), "jschooser:"); ok {
			// "jschooser:<selector>|<files>" clicks the element by script,
			// which needs no visible tab, and answers its file chooser.
			sel, files, _ := strings.Cut(arg, "|")
			set := page.MustHandleFileDialog()
			page.MustElement(sel).MustEval(`() => this.click()`)
			set(strings.Split(files, ";")...)
			fmt.Println("chosen", files)
			return
		}
		if arg, ok := strings.CutPrefix(string(js), "frame:"); ok {
			// "frame:<iframe selector>|<js>" runs <js> in the iframe, which
			// may be on another site, as NetEase's login box.
			sel, code, _ := strings.Cut(arg, "|")
			f := page.MustElement(sel).MustFrame()
			fmt.Println(f.MustEval(`async () => { const r = await (` + code + `); return r === undefined ? null : r }`).String())
		} else if names, ok := strings.CutPrefix(string(js), "cookies:"); ok {
			for _, c := range b.MustGetCookies() {
				if strings.Contains(","+names+",", ","+c.Name+",") {
					fmt.Printf("%s domain=%s path=%s expires=%s len=%d httpOnly=%v\n", c.Name, c.Domain, c.Path,
						time.Unix(int64(c.Expires), 0).Format(time.DateTime), len(c.Value), c.HTTPOnly)
				}
			}
		} else if domains, ok := strings.CutPrefix(string(js), "cookiedomains:"); ok {
			// The names of the cookies on each of those domains or their
			// subdomains, to find a new platform's login cookie.
			for _, c := range b.MustGetCookies() {
				for _, d := range strings.Split(domains, ",") {
					if h := strings.TrimPrefix(c.Domain, "."); h == d || strings.HasSuffix(h, "."+d) {
						fmt.Printf("%s %s expires=%s session=%v len=%d\n", c.Domain, c.Name,
							time.Unix(int64(c.Expires), 0).Format(time.DateOnly), c.Session, len(c.Value))
					}
				}
			}
		} else if sel, ok := strings.CutPrefix(string(js), "shadow:"); ok {
			fmt.Println(page.MustElement(sel).MustShadowRoot().MustHTML())
		} else if sel, file, ok := strings.Cut(strings.TrimPrefix(string(js), "upload:"), "|"); ok && strings.HasPrefix(string(js), "upload:") {
			// Several files go as "a;b", and "a;b|<js>" then prints <js>
			// every half second for 15 seconds, to watch the upload.
			files, watch, _ := strings.Cut(file, "|")
			// "<host> >>> <selector>" finds the input in the host's shadow
			// root, as WeChat Channels' form in wujie-app.
			var el *rod.Element
			if host, inner, ok := strings.Cut(sel, " >>> "); ok {
				el = page.MustElement(host).MustShadowRoot().MustElement(inner)
			} else {
				el = page.MustElement(sel)
			}
			el.MustSetFiles(strings.Split(files, ";")...)
			fmt.Println("uploaded", files)
			for i := 0; watch != "" && i < 30; i++ {
				fmt.Println(page.MustEval(`() => (` + watch + `)`).String())
				time.Sleep(500 * time.Millisecond)
			}
		} else {
			fmt.Println(page.MustEval(`async () => { const r = await (` + string(js) + `); return r === undefined ? null : r }`).String())
		}
	}
	if len(os.Args) > 3 {
		page.MustScreenshot(os.Args[3])
	}
}
