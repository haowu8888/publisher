package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestRunUpdaterPortable runs the script as an update of the zip does,
// with system tools standing in for the app: it waits for the process,
// copies the new files and folders over the old and starts the exe.
func TestRunUpdaterPortable(t *testing.T) {
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	dir := t.TempDir()
	app, src := filepath.Join(dir, "app"), filepath.Join(dir, "new")
	for _, d := range []string{filepath.Join(app, "extension"), filepath.Join(src, "extension")} {
		os.MkdirAll(d, 0o755)
	}
	copyFile := func(from, to string) {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(to, b, 0o755)
	}
	exe := filepath.Join(app, "app.exe")
	copyFile(filepath.Join(sys, "where.exe"), exe)
	copyFile(filepath.Join(sys, "hostname.exe"), filepath.Join(src, "app.exe"))
	os.WriteFile(filepath.Join(app, "extension", "popup.js"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(src, "extension", "popup.js"), []byte("new"), 0o644)
	want, _ := os.ReadFile(filepath.Join(src, "app.exe"))
	zip := filepath.Join(dir, "x.zip")
	os.WriteFile(zip, []byte("zip"), 0o644)

	// The script waits for this process, which stands for the app.
	app0 := exec.Command(filepath.Join(sys, "ping.exe"), "-n", "3", "127.0.0.1")
	if err := app0.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app0.Process.Kill() })
	if err := runUpdaterFor(app0.Process.Pid, dir, zip, src, exe); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := os.ReadFile(exe)
		if _, err := os.Stat(zip); bytes.Equal(got, want) && os.IsNotExist(err) {
			log, _ := os.ReadFile(filepath.Join(dir, "update.log"))
			t.Logf("update.log: %s", log)
			if js, _ := os.ReadFile(filepath.Join(app, "extension", "popup.js")); string(js) != "new" {
				t.Fatalf("extension/popup.js = %q, want the new one", js)
			}
			if _, err := os.Stat(filepath.Join(app, "extension", "extension")); err == nil {
				t.Fatal("the folder was copied into itself")
			}
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "update.log"))
	t.Fatalf("the exe was not replaced; update.log: %s", log)
}
