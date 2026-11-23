// Command try runs one platform's publish against the app's automation
// Chrome, taken over through its DevToolsActivePort as the app does, so a
// platform's code can be tried without building and restarting the app.
// It runs on the Windows host, beside the app, which should be idle:
//
//	go run ./.dev/try <platform> <material title substring | .md file> [review|auto]
//
// The material comes from the app's data.json, or is the article file
// given, its title its "# " line and its cover the cover.* in the folder
// beside it with its name, as the app imports one (but tags).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"publisher/internal/browser"
	"publisher/internal/platforms"
	"publisher/internal/store"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: try <platform> <material title substring> [review|auto]")
		os.Exit(2)
	}
	dir := filepath.Join(os.Getenv("APPDATA"), "Publisher")
	m := material(dir, os.Args[2])
	p := platforms.Get(os.Args[1])
	if p == nil || p.Publish == nil {
		panic("no platform " + os.Args[1])
	}
	mode := store.ModeReview
	if len(os.Args) > 3 {
		mode = store.Mode(os.Args[3])
	}
	if p.Check != nil {
		if err := p.Check(*m, mode); err != nil {
			fmt.Println("check:", err)
			os.Exit(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	b := browser.New(filepath.Join(dir, "chrome-profile"))
	start := time.Now()
	err := p.Publish(ctx, b, platforms.Job{
		Material: *m, Mode: mode,
		Report: func(step string) { fmt.Printf("%5.1fs %s\n", time.Since(start).Seconds(), step) },
	})
	fmt.Println("result:", err)
}

func material(dir, arg string) *store.Material {
	if strings.HasSuffix(arg, ".md") {
		return article(arg)
	}
	data, err := os.ReadFile(filepath.Join(dir, "data.json"))
	if err != nil {
		panic(err)
	}
	var db struct{ Materials []store.Material }
	if err := json.Unmarshal(data, &db); err != nil {
		panic(err)
	}
	for i := range db.Materials {
		if strings.Contains(db.Materials[i].Title, arg) {
			return &db.Materials[i]
		}
	}
	panic("no material titled like " + arg)
}

func article(path string) *store.Material {
	path, _ = filepath.Abs(path)
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	m := &store.Material{Kind: store.KindArticle, ArticlePath: path, Statement: "内容无需标注"}
	body := strings.ReplaceAll(string(data), "\r\n", "\n")
	if first, rest, _ := strings.Cut(body, "\n"); strings.HasPrefix(first, "# ") {
		m.Title, body = strings.TrimSpace(first[2:]), rest
	}
	m.Body = strings.TrimSpace(body)
	folder := strings.TrimSuffix(path, filepath.Ext(path))
	entries, _ := os.ReadDir(folder)
	for _, e := range entries {
		p := filepath.Join(folder, e.Name())
		m.Images = append(m.Images, p)
		if strings.HasPrefix(e.Name(), "cover.") {
			m.CoverPath = p
		}
	}
	return m
}
