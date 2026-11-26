// Package schedule publishes the library's materials that are not yet
// published, a few at a time at set times of day, while the app runs.
package schedule

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"publisher/internal/platforms"
	"publisher/internal/queue"
	"publisher/internal/store"
)

// lateness is how long after a set time a run still happens, for a
// computer that was asleep at the time.
const lateness = 10 * time.Minute

// Scheduler runs the store's schedule.
type Scheduler struct {
	store *store.Store
	queue *queue.Queue
	// before is called ahead of every run at a set time, to bring in
	// what there is to publish.
	before func()
	// onRun is told of every run, with what it did.
	onRun func(store.Schedule)
	mu    sync.Mutex
}

// New starts the schedule.
func New(s *store.Store, q *queue.Queue, before func(), onRun func(store.Schedule)) *Scheduler {
	x := &Scheduler{store: s, queue: q, before: before, onRun: onRun}
	go x.loop()
	return x
}

func (x *Scheduler) loop() {
	for now := range time.Tick(15 * time.Second) {
		sc := x.store.Schedule()
		var kinds []store.Kind
		for _, k := range store.Kinds {
			if due(sc, k, now) {
				kinds = append(kinds, k)
			}
		}
		if sc.Enabled && len(kinds) > 0 {
			x.before()
			x.Run(kinds...)
		}
	}
}

// due reports whether one of the times of kind k passed since its last
// run, not longer than lateness ago.
func due(sc store.Schedule, k store.Kind, now time.Time) bool {
	for _, t := range sc.Times[k] {
		at, err := today(t, now)
		if err == nil && at.After(sc.Ran[k]) && !at.After(now) && now.Sub(at) < lateness {
			return true
		}
	}
	return false
}

// today returns the time of day hm ("15:04") on now's day.
func today(hm string, now time.Time) (time.Time, error) {
	t, err := time.ParseInLocation("15:04", hm, now.Location())
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location()), nil
}

// Next returns the next run of kind k after now, or the zero time if it
// has none.
func Next(sc store.Schedule, k store.Kind, now time.Time) time.Time {
	var next time.Time
	for _, t := range sc.Times[k] {
		at, err := today(t, now)
		if err != nil {
			continue
		}
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next
}

// Runs returns the first n runs of kind k after now, in order, or none if
// it has no times set.
func Runs(sc store.Schedule, k store.Kind, now time.Time, n int) []time.Time {
	var day []time.Time
	for _, t := range sc.Times[k] {
		if at, err := today(t, now); err == nil {
			day = append(day, at)
		}
	}
	slices.SortFunc(day, func(a, b time.Time) int { return a.Compare(b) })
	var out []time.Time
	for d := 0; len(day) > 0 && len(out) < n; d++ {
		for _, at := range day {
			if at = at.AddDate(0, 0, d); at.After(now) && len(out) < n {
				out = append(out, at)
			}
		}
	}
	return out
}

// Pending returns the materials the schedule would publish, the first
// imported first: those it never queued, never published anywhere, with no
// publish under way, and with a platform set for their kind.
func Pending(s *store.Store, sc store.Schedule) []store.Material {
	busy := map[string]bool{}
	for _, t := range s.Tasks() {
		if t.Status != store.StatusFailed && t.Status != store.StatusCanceled {
			busy[t.MaterialID] = true
		}
	}
	var out []store.Material
	ms := s.Materials()
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if m.ScheduledAt.IsZero() && len(m.Published) == 0 && !busy[m.ID] && len(Targets(sc, m)) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// Targets returns the platforms sc publishes m to, leaving out those that
// would refuse it: Bilibili, which requires a creation statement, for a
// material without one when sc has none to give it either, and Douyin for
// an article without pictures.
func Targets(sc store.Schedule, m store.Material) []string {
	if m.Statement == "" {
		m.Statement = sc.Statement
	}
	var out []string
	for _, id := range sc.Platforms[m.Kind] {
		p := platforms.Get(id)
		if p == nil || !p.Ready() || !p.Takes(m.Kind) {
			continue
		}
		if p.Check != nil && p.Check(m, store.ModeAuto) != nil {
			continue
		}
		out = append(out, id)
	}
	return out
}

// Run queues the next materials of the given kinds, or of all kinds, now,
// in auto mode, and records what it did.
func (x *Scheduler) Run(kinds ...store.Kind) store.Schedule {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(kinds) == 0 {
		kinds = store.Kinds
	}
	now := time.Now()
	sc := x.store.Schedule()
	var done, failed []string
	count := map[store.Kind]int{}
	for _, m := range Pending(x.store, sc) {
		if !slices.Contains(kinds, m.Kind) || count[m.Kind] >= max(sc.PerRun[m.Kind], 1) {
			continue
		}
		if err := x.publish(m, sc, now); err != nil {
			failed = append(failed, fmt.Sprintf("「%s」%v", m.Title, err))
			continue
		}
		count[m.Kind]++
		done = append(done, "「"+m.Title+"」")
	}
	result := "没有待发布的" + kindNames(kinds)
	if len(done) > 0 {
		result = "已加入发布：" + strings.Join(done, "、")
	}
	if len(failed) > 0 {
		result += "；未能加入：" + strings.Join(failed, "、")
	}
	sc, _ = x.store.UpdateSchedule(func(s *store.Schedule) {
		if s.Ran == nil {
			s.Ran = map[store.Kind]time.Time{}
		}
		for _, k := range kinds {
			s.Ran[k] = now
		}
		s.LastRun, s.LastResult = now, result
	})
	x.onRun(sc)
	return sc
}

// kindNames names kinds of material, or all of them as 素材.
func kindNames(kinds []store.Kind) string {
	if len(kinds) != 1 {
		return "素材"
	}
	return KindName(kinds[0])
}

// KindName names a kind of material.
func KindName(k store.Kind) string {
	if k == store.KindArticle {
		return "文章"
	}
	return "视频"
}

// publish queues m to its platforms and marks it as queued.
func (x *Scheduler) publish(m store.Material, sc store.Schedule, now time.Time) error {
	ids := Targets(sc, m)
	// Baijiahao takes the statement too, though without one it publishes.
	if m.Statement == "" && (slices.Contains(ids, "bilibili") || slices.Contains(ids, "baijiahao")) {
		if _, err := x.store.UpdateMaterial(m.ID, func(m *store.Material) error {
			if m.Statement == "" {
				m.Statement = sc.Statement
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if _, err := x.queue.Add(m.ID, ids, store.ModeAuto); err != nil {
		return err
	}
	_, err := x.store.UpdateMaterial(m.ID, func(m *store.Material) error {
		m.ScheduledAt = now
		return nil
	})
	return err
}
