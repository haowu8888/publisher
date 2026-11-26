package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"publisher/internal/store"
)

// TestNewFiles takes a folder's videos in the order of their numbers and
// its articles in the order they were made, whatever their names.
func TestNewFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"甲.md", "ep10.mp4", "乙.md", "ep2.mp4", "notes.txt", "ep2.来源.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		// Creation times differ by more than the file system's resolution.
		time.Sleep(20 * time.Millisecond)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	l := &Library{store: s}
	// ep2.来源.md goes with its video, and is no article.
	// Written just now, they are not for the schedule's own sync yet.
	if files, err := l.newFiles(dir, time.Minute); err != nil || len(files) != 0 {
		t.Errorf("fresh files: %q, %v", files, err)
	}
	files, err := l.newFiles(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range files {
		files[i] = filepath.Base(files[i])
	}
	if want := []string{"ep2.mp4", "ep10.mp4", "甲.md", "乙.md"}; !slices.Equal(files, want) {
		t.Errorf("got %q, want %q", files, want)
	}

	// The settings turn the orders round.
	st := s.Settings()
	st.Import.VideoOrder, st.Import.ArticleOrder = "created", "name"
	if err := s.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	files, _ = l.newFiles(dir, 0)
	for i := range files {
		files[i] = filepath.Base(files[i])
	}
	if want := []string{"ep10.mp4", "ep2.mp4", "乙.md", "甲.md"}; !slices.Equal(files, want) {
		t.Errorf("by settings: got %q, want %q", files, want)
	}
}

// TestImportPathsSkips takes a video once only, and leaves its caption,
// dropped along with it, out of the articles.
func TestImportPathsSkips(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ep1.mp4", "ep1_caption.txt", "ep2.mp4", "ep2.txt", "随笔.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("正文。\n#标签\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	l := &Library{store: s}
	path := func(name string) string { return filepath.Join(dir, name) }
	added, err := l.ImportPaths([]string{path("ep1.mp4"), path("ep1_caption.txt"), path("ep2.txt"), path("随笔.txt"), path("ep1.mp4")})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range added {
		got = append(got, filepath.Base(m.VideoPath+m.ArticlePath))
	}
	if want := []string{"ep1.mp4", "随笔.txt"}; !slices.Equal(got, want) {
		t.Errorf("imported %q, want %q", got, want)
	}
	if added, err := l.ImportPaths([]string{path("ep1.mp4"), path("随笔.txt")}); err == nil || len(added) != 0 || len(s.Materials()) != 2 {
		t.Errorf("importing the same files again: %v, %d added", err, len(added))
	}
	// Some new, some not: the new ones come in without complaint.
	if added, err := l.ImportPaths([]string{path("ep1.mp4"), path("ep2.mp4")}); err != nil || len(added) != 1 {
		t.Errorf("importing one more: %v, %d added", err, len(added))
	}
}
