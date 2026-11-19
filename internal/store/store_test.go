package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestOpenKeepsOldFolder moves the single sync folder of older data into
// Folders, and remembers the files materials came from.
func TestOpenKeepsOldFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	old := `{"folder": "D:/videos", "materials": [{"id": "a", "kind": "video", "videoPath": "D:/videos/ep1.mp4"}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Folders(); !slices.Equal(got, []string{"D:/videos"}) {
		t.Errorf("Folders = %q", got)
	}
	if !s.Imported("D:/videos/ep1.mp4") || s.Imported("D:/videos/ep2.mp4") {
		t.Error("Imported is wrong")
	}
	if err := s.AddFolder("D:/videos"); err != nil || len(s.Folders()) != 1 {
		t.Errorf("adding the folder again: %v, %q", err, s.Folders())
	}
	if err := s.AddFolder("D:/more"); err != nil || len(s.Folders()) != 2 {
		t.Errorf("adding another folder: %v, %q", err, s.Folders())
	}
	if err := s.RemoveFolder("D:/videos"); err != nil || !slices.Equal(s.Folders(), []string{"D:/more"}) {
		t.Errorf("removing: %v, %q", err, s.Folders())
	}
}

// TestCollection prefers a material's own collection to the default, and
// joins none for NoCollection.
func TestCollection(t *testing.T) {
	st := Settings{Collections: map[string]string{"douyin": "AI视频", "xiaohongshu": "AI集合"}}
	m := Material{Collections: map[string]string{"xiaohongshu": "教程", "douyin": NoCollection}}
	for platform, want := range map[string]string{"douyin": "", "xiaohongshu": "教程", "bilibili": ""} {
		if got := st.Collection(m, platform); got != want {
			t.Errorf("%s: got %q, want %q", platform, got, want)
		}
	}
	if got := st.Collection(Material{}, "douyin"); got != "AI视频" {
		t.Errorf("default: got %q", got)
	}
}

// TestDeleteForgetsUnpublished lets syncing bring back a deleted material
// that never went out, but not a published one.
func TestDeleteForgetsUnpublished(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	draft := Material{ID: "a", Kind: KindArticle, ArticlePath: "D:/a/草稿.md"}
	sent := Material{ID: "b", Kind: KindVideo, VideoPath: "D:/a/ep1.mp4", Published: map[string]time.Time{"douyin": time.Now()}}
	if _, err := s.AddMaterials(draft, sent); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMaterials("a", "b"); err != nil {
		t.Fatal(err)
	}
	if s.Imported(draft.ArticlePath) {
		t.Error("an unpublished material stays imported")
	}
	if !s.Imported(sent.VideoPath) {
		t.Error("a published material was forgotten")
	}
}

// TestOpenSplitsOldSchedule gives each kind the times and count all kinds
// shared in older data.
func TestOpenSplitsOldSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	old := `{"schedule": {"enabled": true, "times": ["09:00"], "perRun": 2, "lastRun": "2026-11-10T09:00:00Z"}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sc := s.Schedule()
	for _, k := range Kinds {
		if !slices.Equal(sc.Times[k], []string{"09:00"}) || sc.PerRun[k] != 2 || !sc.Ran[k].Equal(sc.LastRun) {
			t.Errorf("%s: times %v, perRun %d, ran %s", k, sc.Times[k], sc.PerRun[k], sc.Ran[k])
		}
	}
	if sc.OldTimes != nil || sc.OldPerRun != 0 {
		t.Error("old fields kept")
	}
}

// TestAddMaterialsOnce adds a file's material once, as when two syncs
// overlap, and UpdateMaterial changes a material without undoing what
// else changed it.
func TestAddMaterialsOnce(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	if added, err := s.AddMaterials(Material{ID: "a", VideoPath: "D:/a/ep1.mp4"}); err != nil || len(added) != 1 {
		t.Fatalf("first add: %v %v", added, err)
	}
	added, err := s.AddMaterials(Material{ID: "b", VideoPath: `d:\a\EP1.mp4`}, Material{ID: "c", ArticlePath: "D:/a/x.md"})
	if err != nil || len(added) != 1 || added[0].ID != "c" || len(s.Materials()) != 2 {
		t.Fatalf("second add: %v %v, %d materials", added, err, len(s.Materials()))
	}
	s.AddTask(Task{ID: "t", MaterialID: "a", Platform: "douyin"})
	s.UpdateTask("t", func(t *Task) { t.Status = StatusSuccess })
	m, err := s.UpdateMaterial("a", func(m *Material) error {
		m.Title, m.Published = "新标题", nil
		return nil
	})
	if err != nil || m.Title != "新标题" || m.Published["douyin"].IsZero() {
		t.Errorf("UpdateMaterial: %+v %v", m, err)
	}
	if _, err := s.UpdateMaterial("a", func(m *Material) error {
		m.Title = "不要"
		return ErrBusy
	}); err != ErrBusy {
		t.Errorf("fn's error: %v", err)
	}
	if m, _ := s.Material("a"); m.Title != "新标题" {
		t.Errorf("a failed update was kept: %q", m.Title)
	}
	if _, err := s.UpdateMaterial("x", func(*Material) error { return nil }); err != ErrNotFound {
		t.Errorf("unknown id: %v", err)
	}
}

// TestBusyAndWaitingTasksStay keeps a material with a task queued, and
// keeps the task waiting for the user's submit when the others are cleared.
func TestBusyAndWaitingTasksStay(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMaterials(Material{ID: "a", VideoPath: "D:/a/ep1.mp4"}, Material{ID: "b", VideoPath: "D:/a/ep2.mp4"}); err != nil {
		t.Fatal(err)
	}
	if !s.HasFile(`d:\a\EP1.mp4`) || s.HasFile("D:/a/ep3.mp4") || s.HasFile("") {
		t.Error("HasFile is wrong")
	}
	for id, st := range map[string]Status{"t1": StatusPending, "t2": StatusReview, "t3": StatusFailed, "t4": StatusSuccess} {
		if err := s.AddTask(Task{ID: id, MaterialID: "a", Platform: id, Status: st}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteMaterials("b", "a"); err != ErrMaterialBusy || len(s.Materials()) != 2 {
		t.Errorf("deleting a material with a queued task: %v, %d materials left", err, len(s.Materials()))
	}
	if err := s.ClearFinishedTasks(); err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, task := range s.Tasks() {
		left = append(left, task.ID)
	}
	slices.Sort(left)
	if !slices.Equal(left, []string{"t1", "t2"}) {
		t.Errorf("tasks left after clearing: %q", left)
	}
	if _, err := s.UpdateTask("t1", func(t *Task) { t.Status = StatusCanceled }); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMaterials("a"); err != nil || len(s.Materials()) != 1 {
		t.Errorf("deleting once the task is canceled: %v", err)
	}
	// Its tasks stay until cleared.
	if len(s.Tasks()) != 2 {
		t.Errorf("tasks left after deleting their material: %d", len(s.Tasks()))
	}
}

// TestOpenRestoresBackup falls back on the last good data when the file
// cannot be read, with the schedule off: the backup may not know of what
// was published since.
func TestOpenRestoresBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	good := `{"materials": [{"id": "a", "kind": "video"}], "schedule": {"enabled": true}}`
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err != nil || !s.Restored().IsZero() {
		t.Fatalf("opening good data: %v", err)
	}
	if err := os.WriteFile(path, []byte(good[:20]), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Restored().IsZero() || len(s.Materials()) != 1 || s.Schedule().Enabled {
		t.Errorf("restored %v, %d materials, schedule on: %v", s.Restored(), len(s.Materials()), s.Schedule().Enabled)
	}
	if b, _ := os.ReadFile(path + ".corrupt"); string(b) != good[:20] {
		t.Errorf("the unreadable file was not kept: %q", b)
	}
	// The restored data is the file's again.
	if s, err := Open(path); err != nil || !s.Restored().IsZero() || len(s.Materials()) != 1 {
		t.Errorf("opening again: %v", err)
	}
	// Without a backup there is nothing to open.
	alone := filepath.Join(t.TempDir(), "data.json")
	os.WriteFile(alone, []byte("{"), 0o644)
	if _, err := Open(alone); err == nil {
		t.Error("unreadable data without a backup opened")
	}
}

// TestUpdateTaskWritesNoSteps keeps a task's steps out of the file until
// something else about it changes.
func TestUpdateTaskWritesNoSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTask(Task{ID: "t", Status: StatusRunning, Step: "准备中"}); err != nil {
		t.Fatal(err)
	}
	saved := func() string {
		b, _ := os.ReadFile(path)
		return string(b)
	}
	before := saved()
	got, err := s.UpdateTask("t", func(t *Task) { t.Step = "上传视频中 12%" })
	if err != nil || got.Step != "上传视频中 12%" || s.Tasks()[0].Step != got.Step {
		t.Fatalf("step not kept: %v, %q", err, got.Step)
	}
	if saved() != before {
		t.Error("a step alone was written to the file")
	}
	if _, err := s.UpdateTask("t", func(t *Task) { t.Status = StatusSuccess }); err != nil || saved() == before {
		t.Errorf("a change of status was not written: %v", err)
	}
}

// TestOpenKeepsQueuedTasks queues again the tasks that had not started when
// the app quit, but not one cut short, which may have half filled a form.
func TestOpenKeepsQueuedTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	old := `{"tasks": [{"id": "t1", "status": "pending"}, {"id": "t2", "status": "running"}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Status{}
	for _, task := range s.Tasks() {
		got[task.ID] = task.Status
	}
	if got["t1"] != StatusPending || got["t2"] != StatusFailed {
		t.Errorf("statuses = %v", got)
	}
}

// TestUnpublish takes a platform out of Published and fails its successful
// tasks, so that reopening the store does not mark it again.
func TestUnpublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutMaterial(Material{ID: "m", Kind: KindVideo}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"toutiao", "douyin"} {
		if err := s.AddTask(Task{ID: p, MaterialID: "m", Platform: p, Status: StatusPending}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateTask(p, func(t *Task) { t.Status = StatusSuccess }); err != nil {
			t.Fatal(err)
		}
	}
	m, tasks, err := s.Unpublish("m", "toutiao")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Published["toutiao"]; ok || len(tasks) != 1 || tasks[0].Status != StatusFailed {
		t.Errorf("Unpublish = %v, %v", m.Published, tasks)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = s.Material("m")
	if _, ok := m.Published["toutiao"]; ok {
		t.Error("reopening marked it published again")
	}
	if _, ok := m.Published["douyin"]; !ok {
		t.Error("douyin is no longer published")
	}
}
