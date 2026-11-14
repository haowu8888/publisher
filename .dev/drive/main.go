// Command drive runs JavaScript in the running Publisher window through
// WebView2's remote debugging port, for checking the app during development.
//
//	go run ./.dev/drive <base64 js expression> [screenshot.png]
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

func main() {
	u := launcher.MustResolveURL("127.0.0.1:9223")
	b := rod.New().ControlURL(u).MustConnect()
	var page *rod.Page
	for _, p := range b.MustPages() {
		if info := p.MustInfo(); strings.Contains(info.URL, "mygo") {
			page = p
		}
	}
	if page == nil {
		panic("Publisher page not found")
	}
	if len(os.Args) > 1 && os.Args[1] != "" {
		js, err := base64.StdEncoding.DecodeString(os.Args[1])
		if err != nil {
			panic(err)
		}
		fmt.Println(page.MustEval(`async () => { const r = await (` + string(js) + `); return r === undefined ? null : r }`).String())
	}
	if len(os.Args) > 2 {
		page.MustWaitStable()
		page.MustScreenshot(os.Args[2])
	}
}
