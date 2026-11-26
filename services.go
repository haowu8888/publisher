package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"publisher/internal/browser"
	"publisher/internal/cookiesync"
	"publisher/internal/platforms"
	"publisher/internal/queue"
	"publisher/internal/schedule"
	"publisher/internal/store"

	"github.com/egoist/mygo"
	"github.com/go-rod/rod/lib/proto"
)

// TaskUpdated is sent whenever a publish task changes.
var TaskUpdated = mygo.NewEvent[store.Task]("taskUpdated")

// AccountUpdated is sent whenever a platform's login state is learned.
var AccountUpdated = mygo.NewEvent[AccountInfo]("accountUpdated")

// CookiesSynced is sent when the extension has brought over logins.
var CookiesSynced = mygo.NewEvent[SyncResult]("cookiesSynced")

// ScheduleRan is sent after each run of the schedule.
var ScheduleRan = mygo.NewEvent[store.Schedule]("scheduleRan")

// Library manages the videos and articles to publish.
type Library struct {
	store *store.Store
}

// List returns all materials, newest first.
func (l *Library) List() []store.Material { return l.store.Materials() }

var (
	videoExts   = []string{"mp4", "mov", "mkv", "flv", "webm", "avi", "m4v"}
	articleExts = []string{"md", "markdown", "txt"}
)

// ImportVideos asks for video files and adds each as a material.
func (l *Library) ImportVideos(ctx context.Context) ([]store.Material, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:   mygo.CallerWindow(ctx),
		Title:    "选择视频",
		Multiple: true,
		Filters:  []mygo.FileFilter{{Name: "视频", Extensions: videoExts}},
	})
	if err != nil {
		return nil, err
	}
	return l.ImportPaths(paths)
}

// ImportArticles asks for Markdown or text files and adds each as a
// material.
func (l *Library) ImportArticles(ctx context.Context) ([]store.Material, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:   mygo.CallerWindow(ctx),
		Title:    "选择文章",
		Multiple: true,
		Filters:  []mygo.FileFilter{{Name: "文章", Extensions: articleExts}},
	})
	if err != nil {
		return nil, err
	}
	return l.ImportPaths(paths)
}

// ImportPaths adds videos and articles, such as files dropped on the
// window, telling them apart by extension and skipping other files, and
// those the library already has: a second material of a file would be
// published a second time.
func (l *Library) ImportPaths(paths []string) ([]store.Material, error) {
	added := []store.Material{}
	var had []string
	for _, p := range paths {
		if l.store.HasFile(p) {
			had = append(had, filepath.Base(p))
			continue
		}
		m, ok, err := newMaterial(p, l.store.Settings().Import)
		if err != nil {
			return added, err
		}
		if !ok {
			continue
		}
		ms, err := l.store.AddMaterials(m)
		if err != nil {
			return added, err
		}
		if len(ms) == 0 {
			had = append(had, filepath.Base(p))
		}
		added = append(added, ms...)
	}
	if len(added) == 0 && len(had) > 0 {
		return added, fmt.Errorf("已经在素材库里，没有重复导入：%s", strings.Join(had, "、"))
	}
	return added, nil
}

// FolderInfo is a folder the library syncs from and the files in it that
// are new.
type FolderInfo struct {
	Dir string `json:"dir"`
	New int    `json:"new"`
	// Files are the names of the new files, in the order they would sync.
	Files []string `json:"files,omitempty"`
	// Error is why the folder could not be read.
	Error string `json:"error,omitempty"`
}

// Folders returns the folders the library syncs from.
func (l *Library) Folders() []FolderInfo {
	out := []FolderInfo{}
	for _, dir := range l.store.Folders() {
		info := FolderInfo{Dir: dir}
		files, err := l.newFiles(dir, 0)
		info.New = len(files)
		for _, f := range files {
			info.Files = append(info.Files, filepath.Base(f))
		}
		if err != nil {
			info.Error = err.Error()
		}
		out = append(out, info)
	}
	return out
}

// AddFolder asks for a folder to sync from as well.
func (l *Library) AddFolder(ctx context.Context) ([]FolderInfo, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:    mygo.CallerWindow(ctx),
		Title:     "添加要同步的文件夹",
		Directory: true,
	})
	if err == nil && len(paths) > 0 {
		err = l.store.AddFolder(paths[0])
	}
	return l.Folders(), err
}

// RemoveFolder stops syncing from a folder; its materials stay.
func (l *Library) RemoveFolder(dir string) ([]FolderInfo, error) {
	err := l.store.RemoveFolder(dir)
	return l.Folders(), err
}

// SyncFolders adds the videos and Markdown articles in the folders that
// were never added before: folder by folder, each in name order with
// episode numbers by value, so the schedule takes them first to last. A
// file is added once only: deleting its material does not bring it back.
// A folder that cannot be read is skipped; Folders tells why.
func (l *Library) SyncFolders() ([]store.Material, error) {
	if len(l.store.Folders()) == 0 {
		return nil, errors.New("还没有添加同步文件夹")
	}
	return l.sync(false)
}

// sync is SyncFolders, leaving for later, when settle, the files changed
// within the settings' settle time, as the schedule's own sync does. A
// file that cannot be read is left for later too, and is the error only
// when nothing else came in.
func (l *Library) sync(settle bool) ([]store.Material, error) {
	im := l.store.Settings().Import
	var quiet time.Duration
	if settle {
		quiet = importRules(im).settle()
	}
	added := []store.Material{}
	var failed []string
	for _, dir := range l.store.Folders() {
		files, _ := l.newFiles(dir, quiet)
		for _, p := range files {
			m, ok, err := newMaterial(p, im)
			if err != nil {
				failed = append(failed, err.Error())
				continue
			}
			if !ok {
				continue
			}
			added = append(added, m)
		}
	}
	if len(added) == 0 {
		if len(failed) > 0 {
			return added, fmt.Errorf("有文件读不了，没有导入：%s", strings.Join(failed, "；"))
		}
		return added, nil
	}
	return l.store.AddMaterials(added...)
}

// fresh reports whether the file at p, or one that goes with it, changed
// within quiet: a video's caption and cover, an article's folder of
// pictures.
func fresh(p string, quiet time.Duration, r rules) bool {
	stem := strings.TrimSuffix(p, filepath.Ext(p))
	for _, f := range append(r.captions(p), p, r.siblingCover(p), stem) {
		if info, err := os.Stat(f); f != "" && err == nil && time.Since(info.ModTime()) < quiet {
			return true
		}
	}
	return false
}

// newFiles lists the files in dir, not its subfolders, to add on syncing,
// but for those changed within quiet, in the order of the settings' rules.
// Text files are left out: beside videos they are captions. So are
// Markdown files that go with a video, as its "x.来源.md".
func (l *Library) newFiles(dir string, quiet time.Duration) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取同步文件夹失败：%w", err)
	}
	r := importRules(l.store.Settings().Import)
	var videos, articles []string
	created := map[string]time.Time{}
	for _, e := range entries {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
		p := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() || l.store.Imported(p) || (quiet > 0 && fresh(p, quiet, r)) {
			continue
		}
		isVideo := slices.Contains(videoExts, ext)
		if !isVideo && (ext != "md" && ext != "markdown" || r.isCaption(p)) {
			continue
		}
		if info, err := e.Info(); err == nil {
			created[p] = createdAt(info)
		}
		if isVideo {
			videos = append(videos, p)
		} else {
			articles = append(articles, p)
		}
	}
	order := func(files []string, by string) {
		if by == "created" {
			slices.SortStableFunc(files, func(a, b string) int { return created[a].Compare(created[b]) })
			return
		}
		slices.SortFunc(files, func(a, b string) int {
			if naturalLess(filepath.Base(a), filepath.Base(b)) {
				return -1
			}
			return 1
		})
	}
	order(videos, r.VideoOrder)
	order(articles, r.ArticleOrder)
	return append(videos, articles...), nil
}

// Update saves edits to a material's title, description, tags or body.
func (l *Library) Update(m store.Material) (store.Material, error) {
	if m.WeixinType != "" && m.WeixinType != "images" {
		return store.Material{}, fmt.Errorf("未知的公众号发布形式 %q", m.WeixinType)
	}
	return l.store.UpdateMaterial(m.ID, func(old *store.Material) error {
		old.Title = strings.TrimSpace(m.Title)
		old.Description = m.Description
		old.Body = m.Body
		old.Tags = cleanTags(m.Tags)
		old.Statement = m.Statement
		old.WeixinType = m.WeixinType
		old.Collections = cleanCollections(m.Collections)
		return nil
	})
}

// cleanCollections trims collection names and drops the empty ones, which
// leave a platform to the default.
func cleanCollections(c map[string]string) map[string]string {
	out := map[string]string{}
	for id, name := range c {
		if name = strings.TrimSpace(name); name != "" {
			out[id] = name
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PickImages asks for an article's pictures, which replace its current
// ones in natural order of their names. An empty result keeps them.
func (l *Library) PickImages(ctx context.Context, id string) (store.Material, error) {
	m, err := l.store.Material(id)
	if err != nil {
		return m, err
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:   mygo.CallerWindow(ctx),
		Title:    "选择图片（可多选）",
		Multiple: true,
		Filters:  []mygo.FileFilter{{Name: "图片", Extensions: imageExts}},
	})
	if err != nil || len(paths) == 0 {
		return m, err
	}
	slices.SortFunc(paths, func(a, b string) int {
		if naturalLess(filepath.Base(a), filepath.Base(b)) {
			return -1
		}
		return 1
	})
	return l.store.UpdateMaterial(id, func(m *store.Material) error {
		// A cover taken from the old pictures, or gone, follows the new
		// ones.
		if _, err := os.Stat(m.CoverPath); m.CoverPath == "" || err != nil || slices.Contains(m.Images, m.CoverPath) {
			m.CoverPath = paths[0]
			if i := slices.IndexFunc(paths, func(p string) bool { return strings.EqualFold(baseName(p), "cover") }); i >= 0 {
				m.CoverPath = paths[i]
				paths = slices.Delete(paths, i, i+1)
			}
		}
		m.Images = paths
		return nil
	})
}

// ClearImages removes an article's pictures.
func (l *Library) ClearImages(id string) (store.Material, error) {
	return l.store.UpdateMaterial(id, func(m *store.Material) error {
		m.Images = nil
		return nil
	})
}

// PickCover asks for a cover image for a material. An empty result keeps
// the current cover.
func (l *Library) PickCover(ctx context.Context, id string) (store.Material, error) {
	m, err := l.store.Material(id)
	if err != nil {
		return m, err
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  mygo.CallerWindow(ctx),
		Title:   "选择封面",
		Filters: []mygo.FileFilter{{Name: "图片", Extensions: []string{"jpg", "jpeg", "png", "webp"}}},
	})
	if err != nil || len(paths) == 0 {
		return m, err
	}
	return l.store.UpdateMaterial(id, func(m *store.Material) error {
		m.CoverPath = paths[0]
		return nil
	})
}

// ClearCover removes a material's cover.
func (l *Library) ClearCover(id string) (store.Material, error) {
	return l.store.UpdateMaterial(id, func(m *store.Material) error {
		m.CoverPath = ""
		return nil
	})
}

// Reveal shows a material's file in the file manager.
func (l *Library) Reveal(id string) error {
	m, err := l.store.Material(id)
	if err != nil {
		return err
	}
	p := m.VideoPath
	if p == "" {
		p = m.ArticlePath
	}
	mygo.Shell.ShowItemInFolder(p)
	return nil
}

// Delete removes a material; its tasks and files stay. A material with a
// task queued or running stays too.
func (l *Library) Delete(id string) error { return l.store.DeleteMaterials(id) }

// Move puts the material id just before target in import order, or just
// after it if after is set. The schedule publishes in this order.
func (l *Library) Move(id, target string, after bool) error {
	return l.store.MoveMaterial(id, target, after)
}

// Unpublish takes back that a material went out to a platform, which a
// task took for success though nothing was published there; its tasks
// there turn failed, to retry, and the page reloads them.
func (l *Library) Unpublish(id, platform string) (store.Material, error) {
	m, _, err := l.store.Unpublish(id, platform)
	return m, err
}

// DeletePublished removes the materials published to every platform they
// were sent to and returns their ids; their tasks and files stay.
// A material with a publish still under way, or a platform it has not gone
// out to yet, stays.
func (l *Library) DeletePublished() ([]string, error) {
	tasks := l.store.Tasks()
	var ids []string
	for _, m := range l.store.Materials() {
		done := len(m.Published) > 0
		for _, t := range tasks {
			if t.MaterialID != m.ID {
				continue
			}
			if _, ok := m.Published[t.Platform]; !ok || t.Status == store.StatusPending || t.Status == store.StatusRunning {
				done = false
			}
		}
		if done {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, l.store.DeleteMaterials(ids...)
}

// AccountInfo is a platform with its login state.
type AccountInfo struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Kinds     []store.Kind `json:"kinds"`
	HomeURL   string       `json:"homeUrl"`
	Ready     bool         `json:"ready"`
	AutoNote  string       `json:"autoNote,omitempty"`
	LoginNote string       `json:"loginNote,omitempty"`
	// Collections is whether the platform adds materials to a collection.
	Collections bool `json:"collections,omitempty"`
	// Limits are the platform's title and text lengths.
	Limits    map[string]platforms.Limits `json:"limits,omitempty"`
	LoggedIn  bool                        `json:"loggedIn"`
	CheckedAt time.Time                   `json:"checkedAt"`
}

// Accounts manages the logins to the platforms.
type Accounts struct {
	store   *store.Store
	browser *browser.Browser
	// syncErr tells why the server for the extension did not start.
	syncErr error

	syncMu sync.Mutex
}

func (a *Accounts) info(p *platforms.Platform) AccountInfo {
	s := a.store.Accounts()[p.ID]
	return AccountInfo{
		ID: p.ID, Name: p.Name, Kinds: p.Kinds, HomeURL: p.HomeURL, Ready: p.Ready(), AutoNote: p.AutoNote,
		Collections: p.Collections,
		Limits:      p.Limits,
		LoginNote:   p.LoginNote,
		LoggedIn:    s.LoggedIn, CheckedAt: s.CheckedAt,
	}
}

func (a *Accounts) record(p *platforms.Platform, loggedIn bool) AccountInfo {
	a.store.SetAccount(p.ID, store.Account{LoggedIn: loggedIn, CheckedAt: time.Now()})
	info := a.info(p)
	AccountUpdated.Broadcast(info)
	return info
}

// List returns every platform with its last known login state.
func (a *Accounts) List() []AccountInfo {
	var out []AccountInfo
	for _, p := range platforms.All {
		out = append(out, a.info(p))
	}
	return out
}

// Check finds out whether the browser is logged in to a platform. It opens
// the browser if needed.
func (a *Accounts) Check(ctx context.Context, id string) (AccountInfo, error) {
	p := platforms.Get(id)
	if p == nil {
		return AccountInfo{}, errors.New("未知平台")
	}
	ok, err := p.CheckLogin(ctx, a.browser)
	if err != nil {
		return a.info(p), err
	}
	return a.record(p, ok), nil
}

// Login opens a platform's login page in the browser and returns once the
// user has logged in, or after ten minutes.
func (a *Accounts) Login(ctx context.Context, id string) (AccountInfo, error) {
	p := platforms.Get(id)
	if p == nil {
		return AccountInfo{}, errors.New("未知平台")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := p.Login(ctx, a.browser); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("等待登录超时")
		}
		return a.info(p), err
	}
	return a.record(p, true), nil
}

// SyncInfo tells how the login sync extension reaches the app.
type SyncInfo struct {
	Port         int    `json:"port"`
	ExtensionDir string `json:"extensionDir"`
	// Error is why the app cannot receive logins, if it cannot.
	Error string `json:"error,omitempty"`
}

// SyncPlatform is a platform whose cookies a sync brought over. Status is
// "loggedIn", "loggedOut", or "checking" while a page load decides it.
type SyncPlatform struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// SyncResult is the outcome of a sync, for the app and the extension.
type SyncResult struct {
	Imported  int            `json:"imported"`
	Failed    int            `json:"failed"`
	Platforms []SyncPlatform `json:"platforms"`
}

func extensionDir() string {
	dir, _ := mygo.App.Path(mygo.PathResources)
	return filepath.Join(dir, "extension")
}

// SyncInfo returns how to set up the login sync extension.
func (a *Accounts) SyncInfo() SyncInfo {
	info := SyncInfo{Port: cookiesync.Port, ExtensionDir: extensionDir()}
	if a.syncErr != nil {
		info.Error = "同步服务未启动（端口 " + strconv.Itoa(cookiesync.Port) + " 被占用？）：" + a.syncErr.Error()
	}
	return info
}

// OpenExtensionDir shows the extension's folder, to load it in Chrome.
func (a *Accounts) OpenExtensionDir() error { return mygo.Shell.OpenPath(extensionDir()) }

// importCookies puts the cookies the extension sent into the automation
// browser, then checks the logins of the platforms they belong to.
func (a *Accounts) importCookies(cookies []cookiesync.Cookie) (any, error) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	res := SyncResult{Platforms: []SyncPlatform{}}
	if len(cookies) == 0 {
		CookiesSynced.Broadcast(res)
		return res, nil
	}
	b, err := a.browser.Get()
	if err != nil {
		return nil, err
	}
	// One at a time, so that a cookie Chrome refuses spoils no others.
	for _, c := range cookies {
		if b.SetCookies([]*proto.NetworkCookieParam{c.Param()}) != nil {
			res.Failed++
		} else {
			res.Imported++
		}
	}
	var slow []*platforms.Platform
	for _, p := range platforms.All {
		if !slices.ContainsFunc(cookies, func(c cookiesync.Cookie) bool { return p.Owns(c.Domain) }) {
			continue
		}
		sp := SyncPlatform{ID: p.ID, Name: p.Name, Status: "checking"}
		if p.Cookie != "" {
			ok, _ := p.CheckLogin(context.Background(), a.browser)
			a.record(p, ok)
			sp.Status = map[bool]string{true: "loggedIn", false: "loggedOut"}[ok]
		} else {
			slow = append(slow, p)
		}
		res.Platforms = append(res.Platforms, sp)
	}
	CookiesSynced.Broadcast(res)
	go func() {
		for _, p := range slow {
			if ok, err := p.CheckLogin(context.Background(), a.browser); err == nil {
				a.record(p, ok)
			}
		}
	}()
	return res, nil
}

// CloseBrowser quits the automation browser.
func (a *Accounts) CloseBrowser() {
	platforms.KeepSessions(a.browser)
	a.browser.Close()
}

// Publisher queues and tracks publish tasks.
type Publisher struct {
	store *store.Store
	queue *queue.Queue
}

// Tasks returns all tasks, newest first.
func (p *Publisher) Tasks() []store.Task { return p.store.Tasks() }

// Publish queues a material for each platform. Mode "review" fills the
// forms and leaves submitting to the user; "auto" submits them.
func (p *Publisher) Publish(materialID string, platformIDs []string, mode store.Mode) ([]store.Task, error) {
	if mode != store.ModeAuto {
		mode = store.ModeReview
	}
	return p.queue.Add(materialID, platformIDs, mode)
}

// Retry queues a finished task again.
func (p *Publisher) Retry(id string) error { return p.queue.Retry(id) }

// Cancel stops a running or pending task.
func (p *Publisher) Cancel(id string) error { return p.queue.Cancel(id) }

// MarkDone records that the user submitted a reviewed task themselves,
// or that a failed one went out after all, as when the platform's success
// message was missed.
func (p *Publisher) MarkDone(id string) error {
	failed := false
	t, err := p.store.UpdateTask(id, func(t *store.Task) {
		switch t.Status {
		case store.StatusReview:
			t.Status, t.Step = store.StatusSuccess, "已手动提交"
		case store.StatusFailed:
			failed = true
			t.Status, t.Step, t.Error, t.Shot = store.StatusSuccess, "已手动标为成功", "", false
		}
	})
	if err == nil {
		if failed {
			os.Remove(p.queue.ShotPath(id))
		}
		TaskUpdated.Broadcast(t)
	}
	return err
}

// Delete removes a task that is not queued or running. A material it
// published stays marked as published.
func (p *Publisher) Delete(id string) error {
	err := p.store.DeleteTask(id)
	p.queue.PruneShots()
	return err
}

// ClearFinished removes tasks that are over, but for those waiting for
// the user to submit them; materials stay marked as published.
func (p *Publisher) ClearFinished() error {
	err := p.store.ClearFinishedTasks()
	p.queue.PruneShots()
	return err
}

// OpenShot opens the screenshot of a failed task's page in the system's
// image viewer.
func (p *Publisher) OpenShot(id string) error {
	path := p.queue.ShotPath(id)
	if _, err := os.Stat(path); err != nil {
		return errors.New("截图已不存在")
	}
	return mygo.Shell.OpenPath(path)
}

func baseName(p string) string {
	return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
}

// splitTitle takes a leading "# " heading off a Markdown document.
func splitTitle(doc string) (title, body string) {
	doc = strings.TrimPrefix(doc, "\ufeff")
	trimmed := strings.TrimLeft(doc, "\r\n\t ")
	if !strings.HasPrefix(trimmed, "# ") {
		return "", doc
	}
	line, rest, _ := strings.Cut(trimmed, "\n")
	return strings.TrimSpace(line[2:]), strings.TrimLeft(rest, "\r\n")
}

func cleanTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Scheduler sets up publishing the library's unpublished materials at set
// times of day.
type Scheduler struct {
	store *store.Store
	sched *schedule.Scheduler
}

// ScheduleInfo is the schedule with what it will do next.
type ScheduleInfo struct {
	Schedule store.Schedule `json:"schedule"`
	// Next is the next run of each kind, if the schedule is on.
	Next map[store.Kind]time.Time `json:"next"`
	// Pending are the materials it will publish, the next first.
	Pending []PendingMaterial `json:"pending"`
}

// PendingMaterial is a material waiting for the schedule, and the
// platforms it will go to.
type PendingMaterial struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Kind      store.Kind `json:"kind"`
	Platforms []string   `json:"platforms"`
	// At is the run that will publish it, if the schedule is on.
	At time.Time `json:"at,omitzero"`
	// Cover is where the page loads its cover from, if it has one.
	Cover string `json:"cover,omitempty"`
	// Summary is the start of its description or text.
	Summary string   `json:"summary,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	// File is the name of its video or article file.
	File   string `json:"file,omitempty"`
	Images int    `json:"images,omitempty"`
}

// Get returns the schedule.
func (x *Scheduler) Get() ScheduleInfo { return x.info(x.store.Schedule()) }

func (x *Scheduler) info(sc store.Schedule) ScheduleInfo {
	info := ScheduleInfo{Schedule: sc, Next: map[store.Kind]time.Time{}, Pending: []PendingMaterial{}}
	for _, k := range store.Kinds {
		if sc.Enabled && len(sc.Platforms[k]) > 0 {
			info.Next[k] = schedule.Next(sc, k, time.Now())
		}
	}
	pending := schedule.Pending(x.store, sc)
	runs, seen := map[store.Kind][]time.Time{}, map[store.Kind]int{}
	for _, k := range store.Kinds {
		if !info.Next[k].IsZero() {
			runs[k] = schedule.Runs(sc, k, time.Now(), len(pending)/max(sc.PerRun[k], 1)+1)
		}
	}
	for _, m := range pending {
		p := PendingMaterial{ID: m.ID, Title: m.Title, Kind: m.Kind, Platforms: schedule.Targets(sc, m),
			Cover: coverURL(m), Summary: summary(m), Tags: m.Tags, File: filepath.Base(m.VideoPath + m.ArticlePath), Images: len(m.Images)}
		if r := runs[m.Kind]; len(r) > 0 {
			p.At = r[min(seen[m.Kind]/max(sc.PerRun[m.Kind], 1), len(r)-1)]
		}
		seen[m.Kind]++
		info.Pending = append(info.Pending, p)
	}
	return info
}

// coverURL is where the page loads m's cover from, the cover scheme
// (main), which WebView2 serves as http://cover.localhost, or "" if it has
// none. The file's name keeps a changed cover from showing the old one.
func coverURL(m store.Material) string {
	if coverFile(m) == "" {
		return ""
	}
	base := "cover://localhost/"
	if runtime.GOOS == "windows" {
		base = "http://cover.localhost/"
	}
	return base + url.PathEscape(m.ID) + "?" + url.QueryEscape(filepath.Base(coverFile(m)))
}

// coverFile is m's cover, or for an article without one its first picture.
func coverFile(m store.Material) string {
	if m.CoverPath == "" && len(m.Images) > 0 {
		return m.Images[0]
	}
	return m.CoverPath
}

// serveCover serves the cover of the material whose id is the path, and
// no other file.
func serveCover(s *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := s.Material(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil || coverFile(m) == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, coverFile(m))
	})
}

// summaryLen is how much of a material's text the pending list shows.
const summaryLen = 80

// summary is the start of m's description, or of an article's text
// without its Markdown pictures, headings and code.
func summary(m store.Material) string {
	text := m.Description
	if m.Kind == store.KindArticle {
		var lines []string
		code := false
		for _, l := range strings.Split(m.Body, "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "```") {
				code = !code
				continue
			}
			if code || l == "" || strings.HasPrefix(l, "!") || strings.HasPrefix(l, "<") {
				continue
			}
			lines = append(lines, strings.TrimLeft(l, "#>-*0123456789. "))
		}
		text = strings.Join(lines, " ")
	}
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > summaryLen {
		text = string(r[:summaryLen]) + "…"
	}
	return text
}

// Save replaces the schedule's settings. Times already past today do not
// run on saving.
func (x *Scheduler) Save(sc store.Schedule) (ScheduleInfo, error) {
	times, perRun := map[store.Kind][]string{}, map[store.Kind]int{}
	for _, k := range store.Kinds {
		name := schedule.KindName(k)
		ts, err := cleanTimes(sc.Times[k])
		if err != nil {
			return ScheduleInfo{}, fmt.Errorf("%s%w", name, err)
		}
		if sc.Enabled && len(sc.Platforms[k]) > 0 && len(ts) == 0 {
			return ScheduleInfo{}, fmt.Errorf("%s勾选了平台，请设置发布时间，或者取消勾选", name)
		}
		if n := sc.PerRun[k]; n < 1 || n > 20 {
			return ScheduleInfo{}, fmt.Errorf("%s每次发布的条数要在 1 到 20 之间", name)
		}
		times[k], perRun[k] = ts, sc.PerRun[k]
	}
	if sc.Enabled && !slices.ContainsFunc(store.Kinds, func(k store.Kind) bool { return len(sc.Platforms[k]) > 0 }) {
		return ScheduleInfo{}, errors.New("请至少给视频或文章勾选一个平台")
	}
	if sc.Statement != "" && !slices.Contains(statements, sc.Statement) {
		return ScheduleInfo{}, errors.New("未知的创作声明")
	}
	saved, err := x.store.UpdateSchedule(func(s *store.Schedule) {
		s.Enabled, s.Times, s.PerRun, s.Platforms, s.Statement = sc.Enabled, times, perRun, sc.Platforms, sc.Statement
		s.Ran = map[store.Kind]time.Time{}
		for _, k := range store.Kinds {
			s.Ran[k] = time.Now()
		}
	})
	if err != nil {
		return ScheduleInfo{}, err
	}
	return x.info(saved), nil
}

// cleanTimes reads times of day as "15:04", sorted and without repeats;
// "20：00" and "20" are 20:00.
func cleanTimes(in []string) ([]string, error) {
	times := []string{}
	for _, t := range in {
		t = strings.TrimSpace(strings.ReplaceAll(t, "：", ":"))
		if t == "" {
			continue
		}
		at, err := time.Parse("15:04", t)
		if err != nil {
			if at, err = time.Parse("15", t); err != nil {
				return nil, fmt.Errorf("的时间「%s」格式不对，请写成 20:00 这样", t)
			}
		}
		if t = at.Format("15:04"); !slices.Contains(times, t) {
			times = append(times, t)
		}
	}
	slices.Sort(times)
	return times, nil
}

// RunNow publishes the next materials right away.
func (x *Scheduler) RunNow() ScheduleInfo { return x.info(x.sched.Run()) }


// statements are the creation statements Bilibili offers, in its words.
var statements = []string{"内容无需标注", "含AI生成内容", "含虚构演绎内容"}

// Settings are the app's preferences, with the ones the system keeps.
type Settings struct {
	store *store.Store
	// dataDir holds data.json and the browser profile.
	dataDir string
}

// SettingsInfo is the settings with what the system says.
type SettingsInfo struct {
	Settings store.Settings `json:"settings"`
	// OpenAtLogin is whether the app starts when the user logs in.
	OpenAtLogin bool   `json:"openAtLogin"`
	DataDir     string `json:"dataDir"`
	Version     string `json:"version"`
	// BackgroundURL is where the page loads the background image from.
	BackgroundURL string `json:"backgroundURL,omitempty"`
}

// Get returns the settings.
func (x *Settings) Get() SettingsInfo {
	st := x.store.Settings()
	info := SettingsInfo{
		Settings:    st,
		OpenAtLogin: mygo.App.OpenAtLogin(),
		DataDir:     x.dataDir,
		Version:     mygo.App.Version(),
	}
	if st.UI.Background != "" {
		// The bg scheme (main), which WebView2 serves as
		// http://bg.localhost.
		base := "bg://localhost/"
		if runtime.GOOS == "windows" {
			base = "http://bg.localhost/"
		}
		info.BackgroundURL = base + url.PathEscape(st.UI.Background)
	}
	return info
}

// Save replaces the settings and applies them.
func (x *Settings) Save(st store.Settings, openAtLogin bool) (SettingsInfo, error) {
	if !slices.Contains([]string{"", "light", "dark"}, st.Theme) {
		return SettingsInfo{}, fmt.Errorf("未知的外观 %q", st.Theme)
	}
	if openAtLogin != mygo.App.OpenAtLogin() {
		if err := mygo.App.SetOpenAtLogin(openAtLogin); err != nil {
			return SettingsInfo{}, fmt.Errorf("设置开机自启失败：%w", err)
		}
	}
	st.Collections = cleanCollections(st.Collections)
	st.Quote = strings.TrimSpace(st.Quote)
	ui, err := cleanUI(st.UI)
	if err != nil {
		return SettingsInfo{}, err
	}
	if st.Import, err = cleanImport(st.Import); err != nil {
		return SettingsInfo{}, err
	}
	// The background changes only through PickBackground, which copies
	// the file into the app's folder.
	ui.Background = x.store.Settings().UI.Background
	st.UI = ui
	if err := x.store.SetSettings(st); err != nil {
		return SettingsInfo{}, err
	}
	applyTheme(st)
	applyZoom(st)
	return x.Get(), nil
}

// accentColor is a color the page can take as its accent.
var accentColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// cleanUI checks the look's choices and keeps the numbers in range.
func cleanUI(ui store.UI) (store.UI, error) {
	if ui.Accent != "" && !accentColor.MatchString(ui.Accent) {
		return ui, fmt.Errorf("主题色 %q 不是 #rrggbb", ui.Accent)
	}
	if !slices.Contains([]string{"", "glass", "soft"}, ui.Style) {
		return ui, fmt.Errorf("未知的风格 %q", ui.Style)
	}
	if !slices.Contains([]string{"", "small", "large"}, ui.Corner) {
		return ui, fmt.Errorf("未知的圆角 %q", ui.Corner)
	}
	if ui.Zoom != 0 {
		ui.Zoom = min(max(ui.Zoom, 80), 130)
	}
	ui.Veil = min(max(ui.Veil, 0), 95)
	ui.Panel = min(max(ui.Panel, 0), 90)
	ui.Blur = min(max(ui.Blur, 0), 30)
	return ui, nil
}

// cleanImport checks the import rules and keeps the numbers in range.
func cleanImport(im store.Import) (store.Import, error) {
	for _, f := range []*string{&im.CoverSuffix, &im.CaptionSuffix, &im.ImageCover} {
		*f = strings.TrimSpace(*f)
		if strings.ContainsAny(*f, `/\:*?"<>|`) {
			return im, fmt.Errorf("文件名里不能有 %q", *f)
		}
	}
	if im.Statement != "" && !slices.Contains(statements, im.Statement) {
		return im, fmt.Errorf("未知的创作声明 %q", im.Statement)
	}
	if !slices.Contains([]string{"", "article"}, im.Weixin) {
		return im, fmt.Errorf("未知的公众号发布形式 %q", im.Weixin)
	}
	for _, o := range []string{im.VideoOrder, im.ArticleOrder} {
		if !slices.Contains([]string{"", "name", "created"}, o) {
			return im, fmt.Errorf("未知的排序 %q", o)
		}
	}
	if im.TitleMin != 0 {
		im.TitleMin = min(max(im.TitleMin, 2), 30)
	}
	if im.TitleMax != 0 {
		im.TitleMax = min(max(im.TitleMax, 10), 100)
	}
	if r := importRules(im); r.TitleMin > r.TitleMax {
		return im, fmt.Errorf("标题最短 %d 字超过了最长 %d 字", r.TitleMin, r.TitleMax)
	}
	if im.Settle != 0 {
		im.Settle = min(max(im.Settle, 1), 120)
	}
	return im, nil
}

// backgroundsDir holds the background images, served to the page by the
// bg scheme (main).
func (x *Settings) backgroundsDir() string { return filepath.Join(x.dataDir, "backgrounds") }

// PickBackground asks for an image and makes it the background. It keeps
// a copy, so the image may move or go away afterwards; each copy has a new
// name, so the page loads it afresh.
func (x *Settings) PickBackground(ctx context.Context) (SettingsInfo, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  mygo.CallerWindow(ctx),
		Title:   "选择背景图片",
		Filters: []mygo.FileFilter{{Name: "图片", Extensions: append(slices.Clone(imageExts), "gif", "bmp")}},
	})
	if err != nil || len(paths) == 0 {
		return x.Get(), err
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		return x.Get(), err
	}
	dir := x.backgroundsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return x.Get(), err
	}
	name := strconv.FormatInt(time.Now().UnixMilli(), 36) + strings.ToLower(filepath.Ext(paths[0]))
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		return x.Get(), err
	}
	return x.setBackground(name)
}

// ClearBackground goes back to the plain page color.
func (x *Settings) ClearBackground() (SettingsInfo, error) { return x.setBackground("") }

func (x *Settings) setBackground(name string) (SettingsInfo, error) {
	st := x.store.Settings()
	if st.UI.Background == "" && name != "" && st.UI.Veil == 0 {
		// Text stays readable on a busy picture.
		st.UI.Veil = 40
	}
	old := st.UI.Background
	st.UI.Background = name
	if err := x.store.SetSettings(st); err != nil {
		return x.Get(), err
	}
	if old != "" && old != name {
		os.Remove(filepath.Join(x.backgroundsDir(), old))
	}
	return x.Get(), nil
}

// OpenDataDir opens the data folder in the file manager.
func (x *Settings) OpenDataDir() error { return mygo.Shell.OpenPath(x.dataDir) }

// applyZoom scales the windows' pages as st asks. The web view zooms
// them, so the layout fits the window at any scale.
func applyZoom(st store.Settings) {
	z := 1.0
	if st.UI.Zoom != 0 {
		z = float64(st.UI.Zoom) / 100
	}
	for _, w := range mygo.Windows() {
		w.Page().SetZoomFactor(z)
	}
}

// applyTheme gives the app the appearance st asks for.
func applyTheme(st store.Settings) {
	src := mygo.ThemeSystem
	if st.Theme != "" {
		src = mygo.ThemeSource(st.Theme)
	}
	mygo.Theme.SetSource(src)
}
