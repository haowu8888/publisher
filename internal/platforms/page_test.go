package platforms

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"publisher/internal/browser"

	"github.com/go-rod/rod/lib/launcher"
)

// TestOpenFormAnswersDialogs reloads a page that asks before leaving, as
// Douyin does after a failed submit. Unanswered, the dialog would stop the
// page and every later call to it.
func TestOpenFormAnswersDialogs(t *testing.T) {
	if _, ok := launcher.LookPath(); !ok {
		t.Skip("no Chrome")
	}
	dir := t.TempDir()
	page := filepath.Join(dir, "form.html")
	html := `<input id="f"><script>
		addEventListener("beforeunload", (e) => { e.preventDefault(); e.returnValue = ""; });
		document.title = "loaded " + Date.now();
	</script>`
	if err := os.WriteFile(page, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	b := browser.New(filepath.Join(dir, "profile"))
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var steps []string
	p, err := openForm(ctx, b, "file:///"+filepath.ToSlash(page), Job{Report: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatal(err)
	}
	// Chrome asks before leaving only a page the user has interacted with.
	el, err := find(p, "#f", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := el.Input("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(`() => setTimeout(() => location.reload(), 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(`() => alert("提示")`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	res, err := p.Timeout(evalTimeout).Eval(`() => document.querySelector("#f").value`)
	if err != nil {
		t.Fatalf("page stopped answering: %v", err)
	}
	if res.Value.Str() != "" {
		t.Errorf("page did not reload, input still %q", res.Value.Str())
	}
	if steps[len(steps)-1] != "页面提示：提示" {
		t.Errorf("alert not reported, steps %q", steps)
	}
}

// TestUploadFailureAndFinish ends the wait for an upload when the page
// starts saying it failed, though not for words it showed all along, and
// closes the tab of a publish that went through.
func TestUploadFailureAndFinish(t *testing.T) {
	if _, ok := launcher.LookPath(); !ok {
		t.Skip("no Chrome")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "upload.html")
	html := `<p>上传失败时可以重试</p><p id="s">取消上传</p><span id="pct">12%</span><div id="msg" class="toast"></div>`
	if err := os.WriteFile(file, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	b := browser.New(filepath.Join(dir, "profile"))
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	j := Job{Report: func(string) {}}
	url := "file:///" + filepath.ToSlash(file)

	// The page's standing words about failing do not end the wait.
	p, err := openForm(ctx, b, url, j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(`() => setTimeout(() => { s.textContent = "重新上传"; pct.remove(); }, 3000)`); err != nil {
		t.Fatal(err)
	}
	if err := waitUploaded(ctx, p, j); err != nil {
		t.Errorf("an upload that finished: %v", err)
	}
	rb, _ := b.Get()
	before, _ := rb.Pages()
	if err := finish(p, j, "发布成功"); err != nil {
		t.Fatal(err)
	}
	if after, _ := rb.Pages(); len(after) != len(before)-1 {
		t.Errorf("finish left %d tabs of %d", len(after), len(before))
	}

	// Its saying so later does.
	p, err = openForm(ctx, b, url, j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(`() => setTimeout(() => { msg.textContent = "视频上传出错，请重新上传"; }, 3000)`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = waitUploaded(ctx, p, j)
	if err == nil || time.Since(start) > 30*time.Second {
		t.Fatalf("a failed upload: %v after %s", err, time.Since(start).Round(time.Second))
	}
	if want := "视频上传失败，页面显示：视频上传出错，请重新上传"; err.Error() != want {
		t.Errorf("got %q, want %q", err, want)
	}
}
