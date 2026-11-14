// Command dialog answers a native JavaScript dialog (alert, beforeunload)
// blocking a tab of the app's automation Chrome, which otherwise stops
// answering DevTools calls:
//
//	go run ./.dev/dialog <tab URL substring> accept|dismiss
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/cdp"
)

func main() {
	data, err := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), "Publisher", "chrome-profile", "DevToolsActivePort"))
	if err != nil {
		panic(err)
	}
	port, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	res, err := http.Get("http://127.0.0.1:" + strings.TrimSpace(port) + "/json/list")
	if err != nil {
		panic(err)
	}
	var tabs []struct{ Type, URL, WebSocketDebuggerURL string }
	json.NewDecoder(res.Body).Decode(&tabs)
	ws := ""
	for _, t := range tabs {
		if t.Type == "page" && strings.Contains(t.URL, os.Args[1]) {
			ws = t.WebSocketDebuggerURL
		}
	}
	if ws == "" {
		panic("no tab matches " + os.Args[1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := cdp.New().Start(cdp.MustConnectWS(ws))
	if len(os.Args) > 2 {
		_, err := c.Call(ctx, "", "Page.handleJavaScriptDialog", map[string]any{"accept": os.Args[2] == "accept"})
		fmt.Println("handled:", err)
		time.Sleep(3 * time.Second)
	}
	r, err := c.Call(ctx, "", "Runtime.evaluate", map[string]any{"expression": "location.href", "returnByValue": true})
	fmt.Println("eval:", string(r), err)
}
