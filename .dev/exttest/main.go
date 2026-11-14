// Command exttest loads resources/extension in a Chromium holding fake
// platform cookies, clicks its sync button, and prints what the app's sync
// server received. It runs in the Linux container:
//
//	go run ./.dev/exttest <chromium binary>
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"publisher/internal/cookiesync"
	"publisher/internal/platforms"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// extensionID is Chromium's id of an unpacked extension at dir.
func extensionID(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	var b strings.Builder
	for _, c := range hex.EncodeToString(sum[:16]) {
		b.WriteByte("abcdefghijklmnop"[strings.IndexRune("0123456789abcdef", c)])
	}
	return b.String()
}

func main() {
	got := make(chan []cookiesync.Cookie, 1)
	h := &cookiesync.Handler{
		Domains: platforms.CookieDomains,
		Import: func(c []cookiesync.Cookie) (any, error) {
			got <- c
			return map[string]any{"imported": len(c), "failed": 0, "platforms": []map[string]string{
				{"id": "bilibili", "name": "哔哩哔哩", "status": "loggedIn"},
			}}, nil
		},
	}
	// Logs each request, to see what Chromium sends.
	go http.ListenAndServe("127.0.0.1:17891", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("%s %s origin=%q type=%q\n", r.Method, r.URL.Path, r.Header.Get("Origin"), r.Header.Get("Content-Type"))
		h.ServeHTTP(w, r)
	}))

	dir, _ := filepath.Abs("resources/extension")
	u := launcher.New().Bin(os.Args[1]).Set("headless", "new").
		Set("load-extension", dir).Set("disable-extensions-except", dir).MustLaunch()
	b := rod.New().ControlURL(u).MustConnect()
	defer b.MustClose()

	exp := proto.TimeSinceEpoch(time.Now().Add(24 * time.Hour).Unix())
	must(b.SetCookies([]*proto.NetworkCookieParam{
		&proto.NetworkCookieParam{Name: "SESSDATA", Value: "s1", Domain: ".bilibili.com", Path: "/", Secure: true, HTTPOnly: true, Expires: exp},
		&proto.NetworkCookieParam{Name: "slave_sid", Value: "w1", URL: "https://mp.weixin.qq.com/"},
		&proto.NetworkCookieParam{Name: "other", Value: "x", Domain: ".example.com", Path: "/", Expires: exp},
	}))

	// The extension loads after the browser starts.
	page := b.MustPage("")
	for i := 0; ; i++ {
		page.MustNavigate("chrome-extension://" + extensionID(dir) + "/popup.html").MustWaitLoad()
		if ok, _, _ := page.Has("#sync"); ok || i == 20 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	page.MustElement("#sync").MustClick()
	page.Timeout(15*time.Second).MustElementR("#status", "已导入|失败|连不上|没有")
	fmt.Println("popup:", page.MustElement("#status").MustText())
	select {
	case c := <-got:
		for _, x := range c {
			fmt.Printf("received %s=%s domain=%s hostOnly=%v httpOnly=%v → %+v\n", x.Name, x.Value, x.Domain, x.HostOnly, x.HTTPOnly, *x.Param())
		}
	default:
		fmt.Println("server received nothing")
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
