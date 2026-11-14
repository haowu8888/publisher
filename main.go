package main

import (
	"log"
	"path/filepath"

	"publisher/internal/browser"
	"publisher/internal/cookiesync"
	"publisher/internal/platforms"
	"publisher/internal/queue"
	"publisher/internal/schedule"
	"publisher/internal/store"

	"github.com/egoist/mygo"
)

func main() {
	// The app lives on in the tray: launching it again shows that window
	// instead of a second app fighting over the browser profile.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "data.json"))
	if err != nil {
		log.Fatal(err)
	}
	b := browser.New(filepath.Join(dir, "chrome-profile"))
	accounts := &Accounts{store: s, browser: b}
	alert := &alerts{store: s}
	q := queue.New(s, b, func(t store.Task) {
		TaskUpdated.Broadcast(t)
		alert.taskChanged(t)
	}, func(id string) { accounts.record(platforms.Get(id), false) })

	accounts.syncErr = cookiesync.Listen(&cookiesync.Handler{
		Domains: platforms.CookieDomains,
		Import:  accounts.importCookies,
	})

	library := &Library{store: s}
	mygo.Bind(library)
	mygo.Bind(accounts)
	mygo.Bind(&Publisher{store: s, queue: q})
	mygo.Bind(&Settings{store: s, dataDir: dir})
	applyTheme(s.Settings())
	// The schedule's runs bring in the sync folders' new files first, when
	// the settings say so.
	autoSync := func() {
		if !s.Settings().AutoSync {
			return
		}
		if _, err := library.sync(settleTime); err != nil {
			log.Println("sync:", err)
		}
	}
	mygo.Bind(&Scheduler{store: s, sched: schedule.New(s, q, autoSync, func(sc store.Schedule) { ScheduleRan.Broadcast(sc) })})

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           appTitle,
			Width:           1180,
			Height:          760,
			MinWidth:        900,
			MinHeight:       560,
			BackgroundColor: "light-dark(#f5f6f8, #111318)", // the page's --bg in src/style.css
			StateKey:        "main",
			URL:             "/",
			// Started with the system, the app waits in the tray.
			Hidden: mygo.App.WasOpenedAtLogin(),
		})
		show := func() {
			win.Show()
			if win.IsMinimized() {
				win.Restore()
			}
			win.Focus()
		}
		// Closing hides the window to the tray, so the schedule keeps
		// running, unless the settings say to quit. The tray's Quit, which
		// closes every window too, ends the app.
		quitting := false
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { quitting = true })
		win.OnClose(func(e *mygo.CloseEvent) {
			if !quitting && !s.Settings().QuitOnClose {
				e.PreventDefault()
				win.Hide()
			}
		})
		mygo.App.OnSecondInstance(func([]string, string) { show() })
		if at := s.Restored(); !at.IsZero() {
			go mygo.Dialog.Error("数据文件损坏，已从备份恢复",
				"data.json 读不了，已换成 "+at.Format("2006-01-02 15:04")+" 的备份（损坏的文件留作 data.json.corrupt）。\n\n"+
					"备份之后发布过的素材会显示成未发布，所以定时发布已关闭：请先核对素材的发布状态，再到「定时发布」里重新启用。")
		}
		alert.win.Store(win)
		win.OnFocus(alert.seen)
		mygo.App.OnNotificationClick(func(string) { show() })
		tray, err := mygo.NewTray(mygo.TrayOptions{
			ID:      "publisher",
			Icon:    trayIcon,
			ToolTip: appTitle,
			Menu: mygo.NewMenu([]*mygo.MenuItem{
				{Label: "打开 Publisher", Click: func(*mygo.MenuItem, *mygo.Window) { show() }},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			}),
		})
		if err != nil {
			log.Println("tray:", err)
		} else {
			// Windows opens the menu on a right click only.
			tray.OnClick(show)
			alert.tray.Store(tray)
		}
	})
	err = mygo.App.Run()
	b.Close()
	if err != nil {
		log.Fatal(err)
	}
}
