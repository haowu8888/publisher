// Package queue runs publish tasks one at a time, since they share one
// browser and one user watching it.
package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"

	"publisher/internal/browser"
	"publisher/internal/platforms"
	"publisher/internal/store"
)

const taskTimeout = time.Hour

// Queue runs the store's pending tasks.
type Queue struct {
	store    *store.Store
	browser  *browser.Browser
	shotDir  string
	onUpdate func(store.Task)
	// onLoggedOut is told of a platform whose task found its login gone.
	onLoggedOut func(platformID string)

	wake chan struct{}

	mu      sync.Mutex
	running string
	cancel  context.CancelFunc
}

// New returns a queue that reports every task change to onUpdate, and a
// platform whose task found its login gone to onLoggedOut. A failed task's
// page is saved to shotDir as a screenshot.
func New(s *store.Store, b *browser.Browser, shotDir string, onUpdate func(store.Task), onLoggedOut func(platformID string)) *Queue {
	q := &Queue{store: s, browser: b, shotDir: shotDir, onUpdate: onUpdate, onLoggedOut: onLoggedOut, wake: make(chan struct{}, 1)}
	go q.loop()
	return q
}

// Add queues a task for each platform, or none if one of them refuses the
// material.
func (q *Queue) Add(materialID string, platformIDs []string, mode store.Mode) ([]store.Task, error) {
	m, err := q.store.Material(materialID)
	if err != nil {
		return nil, err
	}
	for _, id := range platformIDs {
		p := platforms.Get(id)
		if p == nil {
			return nil, fmt.Errorf("未知平台 %q", id)
		}
		if !p.Takes(m.Kind) {
			return nil, fmt.Errorf("%s 不接受这类素材", p.Name)
		}
		if p.Check != nil {
			if err := p.Check(m, mode); err != nil {
				return nil, err
			}
		}
	}
	var tasks []store.Task
	for _, id := range platformIDs {
		t := store.Task{
			ID: store.NewID(), MaterialID: m.ID, Title: m.Title, Kind: m.Kind, Platform: id, Mode: mode,
			Status: store.StatusPending, Step: "排队中",
		}
		t.CreatedAt = time.Now()
		t.UpdatedAt = t.CreatedAt
		if err := q.store.AddTask(t); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
		q.onUpdate(t)
	}
	q.poke()
	return tasks, nil
}

// Retry queues a finished task again.
func (q *Queue) Retry(id string) error {
	tasks := q.store.Tasks()
	if i := slices.IndexFunc(tasks, func(t store.Task) bool { return t.ID == id }); i >= 0 {
		if _, err := q.store.Material(tasks[i].MaterialID); err != nil {
			return errors.New("素材已删除，不能重试")
		}
	}
	t, err := q.store.UpdateTask(id, func(t *store.Task) {
		if t.Status != store.StatusRunning {
			t.Status, t.Step, t.Error, t.Shot = store.StatusPending, "排队中", "", false
		}
	})
	if err != nil {
		return err
	}
	os.Remove(q.ShotPath(id))
	q.onUpdate(t)
	q.poke()
	return nil
}

// Cancel stops a running task, or drops a pending one.
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	if q.running == id {
		q.cancel()
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()
	t, err := q.store.UpdateTask(id, func(t *store.Task) {
		if t.Status == store.StatusPending {
			t.Status, t.Step = store.StatusCanceled, "已取消"
		}
	})
	if err == nil {
		q.onUpdate(t)
	}
	return err
}

// Resume runs the tasks left queued when the app last quit.
func (q *Queue) Resume() { q.poke() }

func (q *Queue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) loop() {
	for range q.wake {
		// A browser opened only for tasks that submit themselves, as the
		// schedule's, is closed again once they are done: nobody is
		// watching it. One already open, with a review task left to
		// submit or with a failed task's page to look at, stays, as does
		// any when the settings say so.
		opened := !q.browser.Running()
		ran, keep := false, false
		for {
			t, ok := q.next()
			if !ok {
				break
			}
			ran = true
			if failed := q.run(t); failed || t.Mode == store.ModeReview {
				keep = true
			}
			// The task's pages may have renewed the session cookies.
			platforms.KeepSessions(q.browser)
		}
		if opened && ran && !keep && !q.store.Settings().KeepBrowser {
			q.browser.Close()
		}
	}
}

// next returns the oldest pending task.
func (q *Queue) next() (store.Task, bool) {
	tasks := q.store.Tasks()
	for i := len(tasks) - 1; i >= 0; i-- {
		if tasks[i].Status == store.StatusPending {
			return tasks[i], true
		}
	}
	return store.Task{}, false
}

func (q *Queue) update(id string, fn func(*store.Task)) {
	if t, err := q.store.UpdateTask(id, fn); err == nil {
		q.onUpdate(t)
	}
}

// run runs t and reports whether it failed.
func (q *Queue) run(t store.Task) (failed bool) {
	// A page that stops answering must not hold up the tasks behind it.
	ctx, cancel := context.WithTimeout(context.Background(), taskTimeout)
	defer cancel()
	q.mu.Lock()
	q.running, q.cancel = t.ID, cancel
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.running, q.cancel = "", nil
		q.mu.Unlock()
	}()

	q.update(t.ID, func(t *store.Task) { t.Status, t.Step = store.StatusRunning, "准备中" })
	q.browser.ForgetLast()
	err := q.publish(ctx, t)
	if errors.Is(err, platforms.ErrNotLoggedIn) {
		q.onLoggedOut(t.Platform)
	}
	shot := err != nil && !errors.Is(err, context.Canceled) && q.screenshot(t.ID)
	q.update(t.ID, func(t *store.Task) {
		switch {
		case errors.Is(err, context.Canceled):
			t.Status, t.Step = store.StatusCanceled, "已取消"
		case errors.Is(err, context.DeadlineExceeded):
			t.Status, t.Error = store.StatusFailed, "超过一小时未完成，已停止；请在浏览器中查看页面"
		case err != nil:
			t.Status, t.Error = store.StatusFailed, err.Error()
		case t.Mode == store.ModeReview:
			t.Status = store.StatusReview
		default:
			t.Status = store.StatusSuccess
		}
		failed = t.Status == store.StatusFailed
		t.Shot = failed && shot
	})
	return failed
}

// ShotPath is where the screenshot of task id's failed page goes.
func (q *Queue) ShotPath(id string) string { return filepath.Join(q.shotDir, id+".jpg") }

// screenshot saves the page the task opened last, which shows where it
// failed even once the browser is closed, and reports whether it did.
func (q *Queue) screenshot(id string) bool {
	page := q.browser.Last()
	if page == nil {
		return false
	}
	page = page.Timeout(10 * time.Second)
	// A background tab, or a minimized window, may not paint.
	browser.Show(page)
	quality := 70
	img, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatJpeg, Quality: &quality})
	if err != nil {
		return false
	}
	if err := os.MkdirAll(q.shotDir, 0o755); err != nil {
		return false
	}
	return os.WriteFile(q.ShotPath(id), img, 0o644) == nil
}

// PruneShots deletes the screenshots of tasks that are gone or no longer
// failed with one.
func (q *Queue) PruneShots() {
	entries, err := os.ReadDir(q.shotDir)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, t := range q.store.Tasks() {
		if t.Shot {
			keep[t.ID+".jpg"] = true
		}
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(q.shotDir, e.Name()))
		}
	}
}

func (q *Queue) publish(ctx context.Context, t store.Task) error {
	p := platforms.Get(t.Platform)
	if p == nil {
		return fmt.Errorf("未知平台 %q", t.Platform)
	}
	if !p.Ready() {
		return fmt.Errorf("%s 的自动发布还在开发中", p.Name)
	}
	m, err := q.store.Material(t.MaterialID)
	if err != nil {
		return err
	}
	// A file moved or renamed since it was imported fails here, by name,
	// rather than halfway through a form.
	for _, f := range append([]string{m.VideoPath, m.ArticlePath, m.CoverPath}, m.Images...) {
		if _, err := os.Stat(f); f != "" && err != nil {
			return fmt.Errorf("素材文件不存在：%s，请在素材里重新选择", f)
		}
	}
	if p.Check != nil {
		if err := p.Check(m, t.Mode); err != nil {
			return err
		}
	}
	if p.Cookie != "" {
		if ok, err := p.CheckLogin(ctx, q.browser); err != nil {
			return err
		} else if !ok {
			return platforms.ErrNotLoggedIn
		}
	}
	st := q.store.Settings()
	return p.Publish(ctx, q.browser, platforms.Job{
		Material:   m,
		Mode:       t.Mode,
		Collection: st.Collection(m, p.ID),
		Original:   st.Original,
		Report: func(step string) {
			q.update(t.ID, func(t *store.Task) { t.Step = step })
		},
	})
}
