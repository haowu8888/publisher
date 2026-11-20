// Package browser drives a visible Chrome (or Edge) with its own profile, so
// that logins to the platforms persist between runs and the user can scan
// QR codes and watch the uploads.
package browser

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Browser launches Chrome on first use and again after the user closes it.
type Browser struct {
	profileDir string

	mu sync.Mutex
	b  *rod.Browser
	l  *launcher.Launcher
	// last is the tab Page opened last, which for a task is its form.
	last *rod.Page
}

// New returns a Browser that keeps its profile in profileDir.
func New(profileDir string) *Browser {
	return &Browser{profileDir: profileDir}
}

// Get returns the running browser, launching it if needed.
func (x *Browser) Get() (*rod.Browser, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.b != nil {
		if alive(x.b) {
			return x.b, nil
		}
		x.closeLocked()
	}
	// A Chrome left running by an app that crashed still holds the profile,
	// so a second one could not start with it: take that one over.
	if b := x.reconnect(); b != nil {
		x.b = b
		return b, nil
	}
	bin, ok := launcher.LookPath()
	if !ok {
		return nil, errors.New("未找到 Chrome 或 Edge，请先安装 Chrome")
	}
	l := launcher.New().
		Bin(bin).
		UserDataDir(x.profileDir).
		Headless(false).
		Leakless(false).
		Delete("enable-automation").
		Delete("no-startup-window").
		Set("disable-blink-features", "AutomationControlled").
		Set("start-maximized")
	u, err := l.Launch()
	if err != nil {
		return nil, err
	}
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, err
	}
	x.b, x.l = b, l
	return b, nil
}

// reconnect connects to a Chrome already running with the profile, which
// writes its debugging address to DevToolsActivePort.
func (x *Browser) reconnect() *rod.Browser {
	data, err := os.ReadFile(filepath.Join(x.profileDir, "DevToolsActivePort"))
	if err != nil {
		return nil
	}
	port, path, ok := strings.Cut(strings.TrimSpace(string(data)), "\n")
	if !ok {
		return nil
	}
	// The file outlives the Chrome that wrote it.
	addr := "127.0.0.1:" + strings.TrimSpace(port)
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return nil
	}
	conn.Close()
	b := rod.New().ControlURL("ws://" + addr + strings.TrimSpace(path)).NoDefaultDevice()
	if b.Connect() != nil {
		return nil
	}
	return b
}

// Page opens url in a new tab.
func (x *Browser) Page(url string) (*rod.Page, error) {
	b, err := x.Get()
	if err != nil {
		return nil, err
	}
	page, err := b.Page(proto.TargetCreateTarget{URL: url})
	if err == nil {
		x.mu.Lock()
		x.last = page
		x.mu.Unlock()
	}
	return page, err
}

// Show brings page's tab to the front, and its window back if minimized.
func Show(page *rod.Page) {
	page.Timeout(5 * time.Second).Activate()
	Restore(page)
}

// Restore brings page's window back if minimized, maximized as the browser
// starts. A minimized window does not paint, so its pages run no animation
// frames: rod's waits for an element to keep still, as in Click, never end,
// and some pages, such as Zhihu's upload page, do nothing meanwhile.
// Covered by other windows, pages still run.
func Restore(page *rod.Page) {
	page = page.Timeout(5 * time.Second)
	if w, err := page.GetWindow(); err == nil && w.WindowState == proto.BrowserWindowStateMinimized {
		// Chrome takes a minimized window only back to normal first.
		page.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowStateNormal})
		page.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowStateMaximized})
	}
}

// Last returns the tab Page opened last since ForgetLast, or nil.
func (x *Browser) Last() *rod.Page {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.last
}

// ForgetLast clears what Last returns.
func (x *Browser) ForgetLast() {
	x.mu.Lock()
	x.last = nil
	x.mu.Unlock()
}

// Cookies returns the cookies of all sites, or nil when the browser is not
// running: checking logins should not open a window.
func (x *Browser) Cookies() ([]*proto.NetworkCookie, error) {
	x.mu.Lock()
	b := x.b
	x.mu.Unlock()
	if b == nil {
		return nil, nil
	}
	return b.Timeout(10 * time.Second).GetCookies()
}

// SetCookies sets cookies, unless the browser is not running.
func (x *Browser) SetCookies(cookies []*proto.NetworkCookieParam) error {
	x.mu.Lock()
	b := x.b
	x.mu.Unlock()
	if b == nil || len(cookies) == 0 {
		return nil
	}
	return b.Timeout(10 * time.Second).SetCookies(cookies)
}

// Running reports whether the browser was launched and is still open.
func (x *Browser) Running() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.b != nil && alive(x.b)
}

// alive reports whether b answers. A Chrome that hangs, rather than quits,
// must not hold the lock, and with it the queue and the login checks,
// forever.
func alive(b *rod.Browser) bool {
	_, err := b.Timeout(5 * time.Second).Version()
	return err == nil
}

// Close quits the browser.
func (x *Browser) Close() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.closeLocked()
}

func (x *Browser) closeLocked() {
	if x.b != nil {
		x.b.Timeout(5 * time.Second).Close()
		x.b = nil
	}
	if x.l != nil {
		x.l.Kill()
		x.l = nil
	}
}
