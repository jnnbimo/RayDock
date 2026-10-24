// RayDock is an Xray-core client with a native MyGo UI.
//
// Flags:
//
//	--wait-pid=N  wait for process N to exit first (elevated relaunch)
//	--connect     connect to the node last connected to on start
//	--autostart   started at logon: stay in the tray
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/brand"
	"raydock/internal/gui"
	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/system"
)

type osAutostart struct{ exe string }

func (o osAutostart) Get() system.StartMode        { return system.Autostart(o.exe) }
func (o osAutostart) Set(m system.StartMode) error { return system.SetAutostart(m, o.exe) }

type winProxy struct{}

func (winProxy) Current() (sysproxy.Settings, error) { return sysproxy.Current() }
func (winProxy) Apply(s sysproxy.Settings) error     { return sysproxy.Apply(s) }

type launchArgs struct {
	waitPID   int
	connect   bool
	autostart bool
}

func parseArgs(args []string) launchArgs {
	var l launchArgs
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--wait-pid="); ok {
			l.waitPID, _ = strconv.Atoi(v)
		} else if a == "--connect" {
			l.connect = true
		} else if a == system.AutostartArg {
			l.autostart = true
		}
	}
	return l
}

func main() {
	// Relaunched through UAC only to change the logon task: no window, no single-instance lock.
	if code, ok := system.TaskHelper(os.Args[1:]); ok {
		os.Exit(code)
	}
	args := parseArgs(os.Args[1:])
	if args.waitPID > 0 {
		system.WaitProcessExit(args.waitPID, 15*time.Second)
	}
	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance gets OnSecondInstance
	}
	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	selfExe, _ := os.Executable()
	// The installer ships the core in resources\core (resources/windows-amd64/core in the repo).
	bundledCore := ""
	if res, err := mygo.App.Path(mygo.PathResources); err == nil {
		bundledCore = filepath.Join(res, "core")
	}
	st, err := loadState(filepath.Join(dataDir, "state.json"))
	if err != nil {
		log.Fatal(err) // never create a writable app after a failed read or backup
	}

	// win is the latest main window. Software-rendered windows stay hidden in
	// the tray; hardware-rendered ones are destroyed to release the renderer.
	var (
		win   *mygo.Window
		winMu sync.Mutex
	)
	current := func() *mygo.Window {
		winMu.Lock()
		defer winMu.Unlock()
		return win
	}
	// post runs fn on the UI thread. A destroyed window still runs it there and only skips the redraw, so
	// the app keeps its state current while it sits in the tray without a window.
	post := func(fn func()) {
		if w := current(); w != nil {
			w.Update(fn)
		}
	}
	a := app.New(app.Deps{
		Post:          post,
		Clipboard:     mygo.Clipboard,
		Proxy:         winProxy{},
		SetAppearance: applyAppearance,
		OpenPath:      func(p string) { _ = mygo.Shell.OpenPath(p) },
		DataDir:       dataDir,
		Elevated:      system.IsElevated(),
		Autostart:     osAutostart{selfExe},
		RelaunchAdmin: func(connect bool) error {
			args := []string{"--wait-pid=" + strconv.Itoa(os.Getpid())}
			if connect {
				args = append(args, "--connect")
			}
			if err := system.RelaunchElevated(args); err != nil {
				return err
			}
			// The new process waits for this one, which stops the core and restores the proxy on exit.
			mygo.App.Quit()
			return nil
		},
		FlushDNS:    system.FlushDNS,
		CoreSource:  app.OfficialCore{},
		BundledCore: bundledCore,
		Version:     mygo.App.Version(),
	}, st)
	applyAppearance(st.Settings.Appearance)

	mygo.App.WhenReady(func() {
		a.CleanStaleProxy()
		var tray *mygo.Tray
		quitting := false
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { quitting = true })
		// Each window has a view of its own: widget state such as a list's belongs to the window that
		// built it, while the app's state lives in a.
		open := func(hidden bool) *mygo.Window {
			view := gui.New(a)
			w := mygo.NewWindow(mygo.WindowOptions{
				Title:         "RayDock",
				Width:         gui.MainWidth,
				Height:        gui.MainHeight,
				MinWidth:      980,
				MinHeight:     620,
				StateKey:      "main",
				TitleBarStyle: mygo.TitleBarHidden,
				Hidden:        true,
				Content:       ui.View(view.Build),
			})
			w.OnClose(func(e *mygo.CloseEvent) {
				// Retain the text engine and ambient image on devices without a GPU.
				// Hidden windows draw nothing; MyGo releases idle frame buffers.
				if tray != nil && !quitting && view.SoftwareRendering() {
					e.PreventDefault()
					w.Hide()
				}
			})
			_ = w.SetIcon(brand.PNG(256, brand.Active)) // dev builds have no icon resource
			winMu.Lock()
			win = w
			winMu.Unlock()
			if !hidden {
				// Shown at once, a new window shows its empty frame, then a white one, then its picture: it
				// shows transparent until it has drawn.
				w.SetOpacity(0)
				w.OnReadyToShow(func() { w.SetOpacity(1) })
				w.Show()
			}
			return w
		}
		open(args.autostart)
		show := func() {
			w := current()
			if w.IsDestroyed() {
				w = open(false)
			}
			w.Show()
			if w.IsMinimized() {
				w.Restore()
			}
			w.Focus()
		}
		mygo.App.OnSecondInstance(func(argv []string, _ string) {
			if !slices.Contains(argv, system.AutostartArg) {
				show()
			}
		})

		tray = gui.SetupTray(a, post, show, mygo.App.Quit)
		if tray == nil && args.autostart {
			show() // without a tray icon a hidden window could never be reached
		}
		if tray != nil {
			// Closing leaves RayDock in the tray. Hardware-rendered windows are
			// destroyed to release Direct3D; software views are retained above.
			mygo.App.OnWindowAllClosed(func() {})
		}
		go flushLogsLoop(a, post)

		// Prepare the core first; a connect started before it is ready waits for it.
		a.LoadAutostart()
		a.EnsureCore()
		a.SyncCores() // dedicated ports run whether or not RayDock connects
		a.PinCertificates()
		if id := a.ConnectTarget(); (args.connect || st.Settings.AutoConnect) && id != "" {
			a.Connect(id, nil)
			if a.AdminPrompt {
				show() // TUN needs elevation: the prompt must not stay in a hidden window
			}
		}
	})
	runErr := mygo.App.Run()
	a.Shutdown() // stop the core, restore the proxy
	if runErr != nil {
		log.Fatal(runErr)
	}
}

// loadState reads saved data, renaming a corrupt file aside so it is not overwritten.
func loadState(path string) (store.State, error) {
	st, err := store.Load(path)
	if err == nil {
		return st, nil
	}
	var syntax *json.SyntaxError
	var fieldType *json.UnmarshalTypeError
	if !errors.As(err, &syntax) && !errors.As(err, &fieldType) {
		return store.State{}, fmt.Errorf("读取 %s 失败，未修改原文件：%w", path, err)
	}
	bad := fmt.Sprintf("%s.bad-%s", path, time.Now().Format("20060102-150405.000000000"))
	if rerr := os.Rename(path, bad); rerr != nil {
		return store.State{}, fmt.Errorf("读取 %s 失败：%v；备份失败：%w", path, err, rerr)
	}
	log.Printf("读取 %s 失败，已备份为 %s：%v", path, bad, err)
	return st, nil
}

func applyAppearance(v int) {
	sources := []mygo.ThemeSource{mygo.ThemeSystem, mygo.ThemeLight, mygo.ThemeDark}
	if v < 0 || v >= len(sources) {
		v = 0
	}
	mygo.Theme.SetSource(sources[v])
}

// flushLogsLoop hands core output to the UI thread every 200 ms instead of redrawing per line, for as
// long as RayDock runs, with or without a window.
func flushLogsLoop(a *app.App, post func(func())) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		if a.LogsPending() {
			post(func() { a.FlushLogs() })
		}
	}
}
