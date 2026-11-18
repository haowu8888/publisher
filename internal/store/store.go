// Package store persists materials (videos and articles) and publish tasks
// as a JSON file in the app's user data directory.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Kind is what a material is, and so which platforms can take it.
type Kind string

const (
	KindVideo   Kind = "video"
	KindArticle Kind = "article"
)

// Kinds are all kinds of material.
var Kinds = []Kind{KindVideo, KindArticle}

// Material is a finished video or article waiting to be published.
type Material struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Title string `json:"title"`
	// Description is the video's description (简介); unused for articles.
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	// VideoPath is the local video file of a video.
	VideoPath string `json:"videoPath,omitempty"`
	// CoverPath is an optional local cover image.
	CoverPath string `json:"coverPath,omitempty"`
	// ArticlePath is the file an article was imported from.
	ArticlePath string `json:"articlePath,omitempty"`
	// Images are an article's pictures, in order: the folder beside it
	// with its name. Xiaohongshu posts them as an image note, and the
	// official account as a picture post (贴图) when WeixinType says so.
	Images []string `json:"images,omitempty"`
	// WeixinType is how the official account posts an article: empty for
	// an article (图文) with its cover, "images" for a picture post.
	WeixinType string `json:"weixinType,omitempty"`
	// Collections are the collections (合集) the material joins, by
	// platform id, in place of Settings.Collections; NoCollection joins
	// none.
	Collections map[string]string `json:"collections,omitempty"`
	// Statement is the creation statement platforms ask for, in Bilibili's
	// words: "内容无需标注", "含AI生成内容" and so on; empty leaves it to the
	// user.
	Statement string `json:"statement,omitempty"`
	// Body is an article's content, in Markdown.
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// ScheduledAt is when the schedule queued the material, which it does
	// once only; a failed publish is for the user to retry.
	ScheduledAt time.Time `json:"scheduledAt,omitzero"`
	// Published is when the material first went out to each platform, by
	// platform id. It outlives the tasks, which the user may clear.
	Published map[string]time.Time `json:"published,omitempty"`
}

// Status is where a task is in its life.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	// StatusReview means the form is filled in the browser and waits for
	// the user to check it and submit it there.
	StatusReview   Status = "review"
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// Mode is how far a task goes on its own.
type Mode string

const (
	// ModeReview fills the form and leaves the submit to the user.
	ModeReview Mode = "review"
	// ModeAuto submits the form.
	ModeAuto Mode = "auto"
)

// Task publishes one material to one platform.
type Task struct {
	ID         string `json:"id"`
	MaterialID string `json:"materialId"`
	// Title and Kind are the material's, so the task still shows what it
	// published once the material is deleted.
	Title    string `json:"title,omitempty"`
	Kind     Kind   `json:"kind,omitempty"`
	Platform string `json:"platform"`
	Mode     Mode   `json:"mode"`
	Status   Status `json:"status"`
	Step     string `json:"step"`
	Error    string `json:"error,omitempty"`
	// Shot is set when the task failed and a screenshot of its page was
	// saved, as <id>.jpg in the queue's shots folder.
	Shot      bool      `json:"shot,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Account is the last known login state of a platform.
type Account struct {
	LoggedIn  bool      `json:"loggedIn"`
	CheckedAt time.Time `json:"checkedAt"`
}

// Schedule publishes materials not yet published at set times of day,
// each kind of material at its own times.
type Schedule struct {
	Enabled bool `json:"enabled"`
	// Times are the local times of day, as "15:04", to publish each kind
	// of material at.
	Times map[Kind][]string `json:"kindTimes"`
	// PerRun is how many materials of a kind each of its runs publishes.
	PerRun map[Kind]int `json:"kindPerRun"`
	// Platforms are the platform ids to publish each kind of material to.
	Platforms map[Kind][]string `json:"platforms"`
	// Statement is the creation statement for materials without one.
	Statement string `json:"statement,omitempty"`
	// Ran is when each kind last ran, or its times were last saved.
	Ran map[Kind]time.Time `json:"ran"`
	// LastRun is when the schedule last ran, and LastResult what it did.
	LastRun    time.Time `json:"lastRun"`
	LastResult string    `json:"lastResult,omitempty"`
	// OldTimes and OldPerRun are the one setting all kinds shared before
	// each had its own.
	OldTimes  []string `json:"times,omitempty"`
	OldPerRun int      `json:"perRun,omitempty"`
}

// Settings are the app's preferences. Their zero values are the defaults.
type Settings struct {
	// Theme is "light", "dark", or empty to follow the system.
	Theme string `json:"theme,omitempty"`
	// QuitOnClose ends the app when its window closes, instead of hiding
	// it to the tray.
	QuitOnClose bool `json:"quitOnClose,omitempty"`
	// KeepBrowser leaves the browser open after tasks that submit
	// themselves, which the queue otherwise closes.
	KeepBrowser bool `json:"keepBrowser,omitempty"`
	// Collections are the collections (合集) materials join, by platform
	// id, unless they name their own.
	Collections map[string]string `json:"collections,omitempty"`
	// Original declares Xiaohongshu notes original (原创声明).
	Original bool `json:"original,omitempty"`
	// AutoSync has the schedule add the new files of the sync folders
	// before each of its runs.
	AutoSync bool `json:"autoSync,omitempty"`
	// NoUpdateCheck stops the app looking for a newer build by itself.
	NoUpdateCheck bool `json:"noUpdateCheck,omitempty"`
	// UI is the look: colors, style, background.
	UI UI `json:"ui"`
}

// UI is the look of the app beyond light and dark; zero values are the
// defaults.
type UI struct {
	// Accent is the main color as #rrggbb, or empty for the pink.
	Accent string `json:"accent,omitempty"`
	// Style is "" for the standard one, "glass" for see-through panels
	// or "soft" for shadows in place of borders.
	Style string `json:"style,omitempty"`
	// Corner is "", "small" or "large".
	Corner string `json:"corner,omitempty"`
	// Zoom scales the page, in percent; 0 is 100.
	Zoom int `json:"zoom,omitempty"`
	// Background is the background image, a file in the backgrounds
	// folder beside data.json.
	Background string `json:"background,omitempty"`
	// Veil is how much the page color covers the background image: its
	// transparency, 0–95 %.
	Veil int `json:"veil,omitempty"`
	// Panel is the panels' transparency over the background, 0–90 %.
	Panel int `json:"panel,omitempty"`
	// Blur blurs the background, in pixels.
	Blur int `json:"blur,omitempty"`
}

// NoCollection, as a material's collection, keeps it out of the default
// one.
const NoCollection = "无"

// Collection is the collection m joins on platform, or empty for none.
func (st Settings) Collection(m Material, platform string) string {
	c, ok := m.Collections[platform]
	if !ok || c == "" {
		c = st.Collections[platform]
	}
	if c == NoCollection {
		return ""
	}
	return c
}

type data struct {
	Materials []Material         `json:"materials"`
	Tasks     []Task             `json:"tasks"`
	Accounts  map[string]Account `json:"accounts"`
	Schedule  Schedule           `json:"schedule"`
	Settings  Settings           `json:"settings"`
	// Folders are the folders the library syncs new files from.
	Folders []string `json:"folders,omitempty"`
	// Folder is the one folder kept before there could be several.
	Folder string `json:"folder,omitempty"`
	// Imported are the files ever added as materials, by fileKey, so a
	// material deleted after publishing does not come back on syncing.
	Imported []string `json:"imported,omitempty"`
}

// ErrNotFound is returned for an unknown id.
var ErrNotFound = errors.New("记录不存在，可能已经被删除")

// ErrBusy is returned for deleting a task that is queued or running.
var ErrBusy = errors.New("任务还在排队或进行中，请先取消")

// ErrMaterialBusy is returned for deleting a material with such a task,
// which would go on publishing it with nowhere to record how it went.
var ErrMaterialBusy = errors.New("素材还有排队或进行中的发布任务，请先在任务页取消")

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	d    data
	// restored is when the backup that Open fell back on was made.
	restored time.Time
}

// Open loads the store at path, or starts an empty one. Each good load is
// kept beside it as a backup, which takes the place of a file that can no
// longer be read; that one is kept too, as ".corrupt". The backup may miss
// what was published since, so the schedule is turned off with it: see
// Restored.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	bak := path + ".bak"
	if bad := json.Unmarshal(b, &s.d); bad != nil {
		info, err := os.Stat(bak)
		old, _ := os.ReadFile(bak)
		s.d = data{}
		if err != nil || json.Unmarshal(old, &s.d) != nil {
			return nil, fmt.Errorf("%s 已损坏，也没有可用的备份: %w", path, bad)
		}
		if err := os.Rename(path, path+".corrupt"); err != nil {
			return nil, err
		}
		s.restored = info.ModTime()
		s.d.Schedule.Enabled = false
		if err := s.save(); err != nil {
			return nil, err
		}
	} else {
		os.WriteFile(bak, b, 0o644)
	}
	// A task cut short by quitting the app will not resume by itself.
	for i := range s.d.Tasks {
		if t := &s.d.Tasks[i]; t.Status == StatusRunning || t.Status == StatusPending {
			t.Status, t.Error = StatusFailed, "应用退出时任务被中断"
		}
	}
	// Data from before Published was kept has it only in tasks.
	for _, t := range slices.Backward(s.d.Tasks) {
		if t.Status == StatusSuccess {
			s.markPublished(t)
		}
	}
	if s.d.Folder != "" {
		s.d.Folders = append(s.d.Folders, s.d.Folder)
		s.d.Folder = ""
	}
	if sc := &s.d.Schedule; sc.Times == nil && (sc.OldTimes != nil || sc.OldPerRun != 0) {
		sc.Times, sc.PerRun, sc.Ran = map[Kind][]string{}, map[Kind]int{}, map[Kind]time.Time{}
		for _, k := range Kinds {
			sc.Times[k], sc.PerRun[k], sc.Ran[k] = slices.Clone(sc.OldTimes), sc.OldPerRun, sc.LastRun
		}
		sc.OldTimes, sc.OldPerRun = nil, 0
	}
	// Tasks from before they kept their material's title and kind.
	for i := range s.d.Tasks {
		t := &s.d.Tasks[i]
		if j := slices.IndexFunc(s.d.Materials, func(m Material) bool { return m.ID == t.MaterialID }); j >= 0 && t.Kind == "" {
			t.Title, t.Kind = s.d.Materials[j].Title, s.d.Materials[j].Kind
		}
	}
	// Data from before Imported was kept has it only in materials.
	for _, m := range s.d.Materials {
		s.markImported(m)
	}
	return s, nil
}

// Restored is when the backup was made that Open took in place of a data
// file it could not read, or the zero time for one it could.
func (s *Store) Restored() time.Time { return s.restored }

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// NewID returns a random id.
func NewID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Materials returns all materials, newest first.
func (s *Store) Materials() []Material {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.d.Materials)
}

// Material returns the material with id.
func (s *Store) Material(id string) (Material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.Materials, func(m Material) bool { return m.ID == id })
	if i < 0 {
		return Material{}, ErrNotFound
	}
	return s.d.Materials[i], nil
}

// PutMaterial adds m, or replaces the material with its id. Published is
// the store's to keep and stays as it was.
func (s *Store) PutMaterial(m Material) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(s.d.Materials, func(x Material) bool { return x.ID == m.ID }); i >= 0 {
		m.Published = s.d.Materials[i].Published
		s.d.Materials[i] = m
	} else {
		s.d.Materials = slices.Insert(s.d.Materials, 0, m)
		s.markImported(m)
	}
	return s.save()
}

// AddMaterials adds new materials in order, so the last is the newest.
func (s *Store) AddMaterials(ms ...Material) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range ms {
		s.d.Materials = slices.Insert(s.d.Materials, 0, m)
		s.markImported(m)
	}
	return s.save()
}

// HasFile reports whether a material in the library is of the file at
// path.
func (s *Store) HasFile(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fileKey(path)
	return slices.ContainsFunc(s.d.Materials, func(m Material) bool {
		return (m.VideoPath != "" && fileKey(m.VideoPath) == k) || (m.ArticlePath != "" && fileKey(m.ArticlePath) == k)
	})
}

// Imported reports whether the file at path was ever added as a material.
func (s *Store) Imported(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.d.Imported, fileKey(path))
}

func (s *Store) markImported(m Material) {
	for _, p := range []string{m.VideoPath, m.ArticlePath} {
		if k := fileKey(p); p != "" && !slices.Contains(s.d.Imported, k) {
			s.d.Imported = append(s.d.Imported, k)
		}
	}
}

// fileKey identifies a file by path; Windows paths ignore case.
func fileKey(p string) string { return strings.ToLower(filepath.Clean(p)) }

// Folders returns the folders the library syncs from, in the order added.
func (s *Store) Folders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.d.Folders)
}

// AddFolder adds a folder to sync from; one already there stays once.
func (s *Store) AddFolder(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.d.Folders, func(d string) bool { return fileKey(d) == fileKey(dir) }) {
		return nil
	}
	s.d.Folders = append(s.d.Folders, dir)
	return s.save()
}

// RemoveFolder stops syncing from a folder. Its materials stay.
func (s *Store) RemoveFolder(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Folders = slices.DeleteFunc(s.d.Folders, func(d string) bool { return d == dir })
	return s.save()
}

// DeleteMaterials removes materials, or none of them when one has a task
// queued or running. Their tasks stay, for "清除已结束" to clear. Their
// files may be synced again unless they were published.
func (s *Store) DeleteMaterials(ids ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.d.Tasks, func(t Task) bool {
		return slices.Contains(ids, t.MaterialID) && (t.Status == StatusPending || t.Status == StatusRunning)
	}) {
		return ErrMaterialBusy
	}
	s.d.Materials = slices.DeleteFunc(s.d.Materials, func(m Material) bool {
		if !slices.Contains(ids, m.ID) {
			return false
		}
		// Syncing brings back a material deleted before it went out
		// anywhere, but not one published.
		if len(m.Published) == 0 {
			s.d.Imported = slices.DeleteFunc(s.d.Imported, func(k string) bool {
				return k == fileKey(m.VideoPath) || k == fileKey(m.ArticlePath)
			})
		}
		return true
	})
	return s.save()
}

// Tasks returns all tasks, newest first.
func (s *Store) Tasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.d.Tasks)
}

// AddTask stores a new task.
func (s *Store) AddTask(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Tasks = slices.Insert(s.d.Tasks, 0, t)
	return s.save()
}

// UpdateTask applies fn to the task with id and returns the result. A
// change of its step alone is not written to the file until the next
// change of anything: an upload reports one every other second, and a
// step is of no use once the app has quit.
func (s *Store) UpdateTask(id string, fn func(*Task)) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.Tasks, func(t Task) bool { return t.ID == id })
	if i < 0 {
		return Task{}, ErrNotFound
	}
	t := &s.d.Tasks[i]
	old := *t
	fn(t)
	t.UpdatedAt = time.Now()
	if t.Status == StatusSuccess {
		s.markPublished(*t)
	}
	if old.Step, old.UpdatedAt = t.Step, t.UpdatedAt; old == *t {
		return *t, nil
	}
	return *t, s.save()
}

// markPublished records that t's material went out to t's platform, unless
// it already had.
func (s *Store) markPublished(t Task) {
	i := slices.IndexFunc(s.d.Materials, func(m Material) bool { return m.ID == t.MaterialID })
	if i < 0 {
		return
	}
	m := &s.d.Materials[i]
	if _, ok := m.Published[t.Platform]; ok {
		return
	}
	// Callers may hold the old map, from Materials.
	m.Published = maps.Clone(m.Published)
	if m.Published == nil {
		m.Published = map[string]time.Time{}
	}
	m.Published[t.Platform] = t.UpdatedAt
}

// DeleteTask removes a task that is not queued or running. One already
// gone, as with its material, is no error: a page may still list it.
func (s *Store) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.Tasks, func(t Task) bool { return t.ID == id })
	if i < 0 {
		return nil
	}
	if st := s.d.Tasks[i].Status; st == StatusPending || st == StatusRunning {
		return ErrBusy
	}
	s.d.Tasks = slices.Delete(s.d.Tasks, i, i+1)
	return s.save()
}

// ClearFinishedTasks removes tasks that are over. One waiting for the user
// to submit it stays: without it the material would count as never sent,
// and the schedule would publish it once more.
func (s *Store) ClearFinishedTasks() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Tasks = slices.DeleteFunc(s.d.Tasks, func(t Task) bool {
		return t.Status != StatusPending && t.Status != StatusRunning && t.Status != StatusReview
	})
	return s.save()
}

// Accounts returns the last known login states, by platform id.
func (s *Store) Accounts() map[string]Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.d.Accounts)
}

// SetAccount records the login state of a platform.
func (s *Store) SetAccount(platform string, a Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Accounts == nil {
		s.d.Accounts = map[string]Account{}
	}
	s.d.Accounts[platform] = a
	return s.save()
}

// Schedule returns the schedule settings.
func (s *Store) Schedule() Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.d.Schedule
	sc.Times = maps.Clone(sc.Times)
	sc.PerRun = maps.Clone(sc.PerRun)
	sc.Platforms = maps.Clone(sc.Platforms)
	sc.Ran = maps.Clone(sc.Ran)
	return sc
}

// UpdateSchedule applies fn to the schedule settings and returns the result.
func (s *Store) UpdateSchedule(fn func(*Schedule)) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.d.Schedule)
	return s.d.Schedule, s.save()
}

// Settings returns the app's preferences.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Settings
}

// SetSettings replaces the app's preferences.
func (s *Store) SetSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Settings = st
	return s.save()
}
