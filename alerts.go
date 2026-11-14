package main

import (
	_ "embed"
	"fmt"
	"log"
	"sync/atomic"

	"publisher/internal/platforms"
	"publisher/internal/store"

	"github.com/egoist/mygo"
)

const appTitle = "Publisher 一键分发"

//go:embed assets/tray.png
var trayIcon []byte

// trayAlertIcon is trayIcon with a red dot.
//
//go:embed assets/tray-alert.png
var trayAlertIcon []byte

// alerts tell of tasks that failed while the user was not looking at the
// window, which says so itself: the app mostly waits in the tray while the
// schedule publishes. A desktop notification names the task, and, as
// Windows may have those turned off, the tray icon wears a red dot until
// the window is looked at again.
type alerts struct {
	store *store.Store
	// win and tray are there once the app is ready.
	win    atomic.Pointer[mygo.Window]
	tray   atomic.Pointer[mygo.Tray]
	failed atomic.Int32
}

// taskChanged is told of every change to a task.
func (a *alerts) taskChanged(t store.Task) {
	win := a.win.Load()
	if t.Status != store.StatusFailed || win == nil || (win.IsVisible() && win.IsFocused()) {
		return
	}
	n := a.failed.Add(1)
	if tray := a.tray.Load(); tray != nil {
		tray.SetIcon(trayAlertIcon, false)
		tray.SetToolTip(fmt.Sprintf("%s：%d 个任务失败", appTitle, n))
	}
	title := "发布失败"
	if p := platforms.Get(t.Platform); p != nil {
		title = p.Name + " 发布失败"
	}
	body := t.Error
	if m, err := a.store.Material(t.MaterialID); err == nil {
		body = "「" + m.Title + "」" + body
	}
	if err := mygo.NewNotification(mygo.NotificationOptions{ID: "task-" + t.ID, Title: title, Body: body}).Show(); err != nil {
		log.Println("notification:", err)
	}
}

// seen takes the red dot off the tray icon: the user is at the window.
func (a *alerts) seen() {
	if a.failed.Swap(0) == 0 {
		return
	}
	if tray := a.tray.Load(); tray != nil {
		tray.SetIcon(trayIcon, false)
		tray.SetToolTip(appTitle)
	}
}
