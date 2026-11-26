package schedule

import (
	"slices"
	"testing"
	"time"

	"publisher/internal/store"
)

func TestDue(t *testing.T) {
	day := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.Local) }
	v, a := store.KindVideo, store.KindArticle
	sc := store.Schedule{
		Times: map[store.Kind][]string{v: {"09:00", "20:00"}, a: {"12:00"}},
		Ran:   map[store.Kind]time.Time{v: day(8, 0), a: day(8, 0)},
	}
	for _, c := range []struct {
		kind store.Kind
		now  time.Time
		want bool
	}{
		{v, day(8, 59), false},
		{v, day(9, 0), true},
		{a, day(9, 0), false},  // articles have their own times
		{v, day(9, 9), true},   // woke up late
		{v, day(9, 11), false}, // too late
		{v, day(20, 0), true},
		{a, day(12, 0), true},
	} {
		if got := due(sc, c.kind, c.now); got != c.want {
			t.Errorf("due %s at %s = %v, want %v", c.kind, c.now.Format("15:04"), got, c.want)
		}
	}
	sc.Ran[v] = day(9, 0)
	if due(sc, v, day(9, 1)) {
		t.Error("ran twice for 09:00")
	}
}

func TestNext(t *testing.T) {
	sc := store.Schedule{Times: map[store.Kind][]string{store.KindVideo: {"09:00", "20:00"}, store.KindArticle: {"22:00"}}}
	now := time.Date(2026, 11, 10, 21, 0, 0, 0, time.Local)
	if got, want := Next(sc, store.KindVideo, now), time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("Next video = %s, want %s", got, want)
	}
	if got, want := Next(sc, store.KindArticle, now), time.Date(2026, 11, 10, 22, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("Next article = %s, want %s", got, want)
	}
}

func TestRuns(t *testing.T) {
	day := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.Local) }
	sc := store.Schedule{Times: map[store.Kind][]string{store.KindVideo: {"20:00", "09:00"}}}
	got := Runs(sc, store.KindVideo, day(10, 12), 4)
	want := []time.Time{day(10, 20), day(11, 9), day(11, 20), day(12, 9)}
	if !slices.Equal(got, want) {
		t.Errorf("Runs = %v, want %v", got, want)
	}
	if got := Runs(sc, store.KindArticle, day(10, 12), 4); len(got) != 0 {
		t.Errorf("Runs without times = %v", got)
	}
}

func TestPending(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatal(err)
	}
	// PutMaterial puts new materials first, as importing does.
	for _, m := range []store.Material{
		{ID: "old", Kind: store.KindVideo},
		{ID: "published", Kind: store.KindVideo},
		{ID: "cleared", Kind: store.KindVideo},
		{ID: "failed", Kind: store.KindVideo},
		{ID: "queued", Kind: store.KindVideo, ScheduledAt: time.Now()},
		{ID: "article", Kind: store.KindArticle},
		{ID: "new", Kind: store.KindVideo},
	} {
		s.PutMaterial(m)
	}
	s.AddTask(store.Task{ID: "1", MaterialID: "published", Platform: "kuaishou", Status: store.StatusSuccess})
	s.AddTask(store.Task{ID: "2", MaterialID: "failed", Platform: "kuaishou", Status: store.StatusFailed})
	// A material stays published after its tasks are cleared.
	s.AddTask(store.Task{ID: "3", MaterialID: "cleared", Platform: "kuaishou", Status: store.StatusRunning})
	s.UpdateTask("3", func(t *store.Task) { t.Status = store.StatusSuccess })
	if err := s.DeleteTask("3"); err != nil {
		t.Fatal(err)
	}
	sc := store.Schedule{Platforms: map[store.Kind][]string{store.KindVideo: {"bilibili", "kuaishou"}}}
	var got []string
	for _, m := range Pending(s, sc) {
		got = append(got, m.ID)
	}
	if want := []string{"old", "failed", "new"}; !equal(got, want) {
		t.Errorf("Pending = %v, want %v", got, want)
	}
	// Without a statement to give, Bilibili is left out.
	if ids := Targets(sc, store.Material{Kind: store.KindVideo}); !equal(ids, []string{"kuaishou"}) {
		t.Errorf("Targets = %v", ids)
	}
	sc.Statement = "含AI生成内容"
	if ids := Targets(sc, store.Material{Kind: store.KindVideo}); !equal(ids, []string{"bilibili", "kuaishou"}) {
		t.Errorf("Targets with statement = %v", ids)
	}
	// Douyin posts an article as pictures, so it needs some.
	sc.Platforms[store.KindArticle] = []string{"douyin", "weixin-mp"}
	if ids := Targets(sc, store.Material{Kind: store.KindArticle}); !equal(ids, []string{"weixin-mp"}) {
		t.Errorf("Targets for an article without pictures = %v", ids)
	}
	if ids := Targets(sc, store.Material{Kind: store.KindArticle, CoverPath: "c.jpg"}); !equal(ids, []string{"douyin", "weixin-mp"}) {
		t.Errorf("Targets for an article with a cover = %v", ids)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
