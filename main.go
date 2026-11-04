// RayDock is an Xray client with a native MyGo UI.
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
func (o osAutostart) AdminRisk() error             { return system.AdminAutostartRisk(o.exe) }

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

const (
	prepareUninstallArg = "--prepare-uninstall"
	uninstallErrorFile  = "uninstall-error.txt" // in the data directory
)

// prepareUninstall acquires the application lock only after the running instance completed Shutdown.
// Errors propagate to NSIS before it deletes any installation files or registration metadata.
// It moves no data: the proxy backup is restored from the data directory, and from an earlier
// version's directory that was never moved.
func prepareUninstall(dataDir string) error {
	deadline := time.Now().Add(35 * time.Second)
	for !mygo.App.RequestSingleInstanceLock() {
		if time.Now().After(deadline) {
			return errors.New("RayDock 未能正常退出，卸载已停止；请退出后重试")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, dir := range []string{dataDir, store.LegacyDataDir()} {
		if dir == "" {
			continue
		}
		if err := app.RestoreProxyBackup(dir, winProxy{}); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return system.CleanupAutostart(exe)
}

func main() {
	dataDir, err := store.DataDir()
	if err != nil {
		fatal(err)
	}
	// MyGo keeps its own files (window-state.json) there too.
	mygo.App.SetPath(mygo.PathUserData, dataDir)
	if slices.Contains(os.Args[1:], prepareUninstallArg) {
		// Why it failed is kept for the uninstaller's message to point to (installer.nsh), until it succeeds.
		report := filepath.Join(dataDir, uninstallErrorFile)
		if err := prepareUninstall(dataDir); err != nil {
			log.Print(err)
			if os.MkdirAll(dataDir, 0o700) == nil {
				_ = os.WriteFile(report, []byte(err.Error()+"\n"), 0o600)
			}
			os.Exit(1)
		}
		_ = os.Remove(report)
		return
	}
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
	moveWarning, err := store.PrepareDataDir(dataDir)
	if err != nil {
		fatal(err)
	}
	selfExe, _ := os.Executable()
	// The installer ships the core in resources\core (resources/windows-amd64/core in the repo).
	bundledCore := ""
	if res, err := mygo.App.Path(mygo.PathResources); err == nil {
		bundledCore = filepath.Join(res, "core")
	}
	st, err := loadState(filepath.Join(dataDir, "state.json"))
	if err != nil {
		fatal(err) // never create a writable app after a failed read or backup
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
	if moveWarning != nil {
		a.QueueLog("[RayDock] " + moveWarning.Error())
	}
	applyAppearance(st.Settings.Appearance)

	mygo.App.WhenReady(func() {
		a.CleanStaleProxy()
		var tray *mygo.Tray
		quitting := false
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { quitting = true; a.Shutdown() })
		// Each window has a view of its own: widget state such as a list's belongs to the window that
		// built it, while the app's state lives in a.
		open := func(hidden bool) *mygo.Window {
			view := gui.New(a)
			// The window has the size of its page, which fits the home page's cards: resizing redraws their
			// glass, and on slower renderers it stutters. The view draws minimize and close, and no maximize.
			w := mygo.NewWindow(mygo.WindowOptions{
				Title:             "RayDock",
				Width:             gui.MainWidth,
				Height:            gui.MainHeight,
				UseContentSize:    true,
				DisableResize:     true,
				DisableMaximize:   true,
				DisableFullScreen: true,
				StateKey:          "main",
				TitleBarStyle:     mygo.TitleBarHidden,
				HideWindowButtons: true,
				Hidden:            true,
				Content:           ui.View(view.Build),
			})
			// The saved state keeps the window where it was; a size or maximized state saved while it could
			// be resized goes.
			w.Unmaximize()
			w.SetContentSize(gui.MainWidth, gui.MainHeight)
			fitWindow(w, true)
			w.OnMove(func() { fitWindow(w, false) }) // to another display
			view.SetWindow(w)
			// The window's buttons are fainter while it is inactive.
			redraw := func() { w.Update(func() {}) }
			w.OnFocus(redraw)
			w.OnBlur(redraw)
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
		mygo.Screen.OnDisplaysChanged(func() {
			if w := current(); !w.IsDestroyed() {
				fitWindow(w, true)
			}
		})
		show := func() {
			w := current()
			if w.IsDestroyed() {
				w = open(false)
			}
			fitWindow(w, true)
			w.Show()
			if w.IsMinimized() {
				w.Restore()
			}
			w.Focus()
		}
		mygo.App.OnSecondInstance(func(argv []string, _ string) {
			if slices.Contains(argv, prepareUninstallArg) {
				mygo.App.Quit()
				return
			}
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
		a.StartLogs()
		a.StartTraffic()

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

// fatal reports an error that keeps RayDock from starting, before it has a window, and exits.
func fatal(err error) {
	system.ShowError("RayDock 无法启动", err.Error())
	log.Fatal(err)
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

// fitWindow gives the window the page's size, or what the work area of its display leaves when that is
// smaller (the page then scrolls), and with place moves it into the work area. Its size is the user's
// to change no more than the page's: a display too short for it would hide the bottom of the page.
func fitWindow(w *mygo.Window, place bool) {
	b, cb := w.Bounds(), w.ContentBounds()
	wa := mygo.Screen.DisplayMatching(b).WorkArea
	if wa.Width <= 0 || wa.Height <= 0 {
		return
	}
	frameW, frameH := b.Width-cb.Width, b.Height-cb.Height
	width, height := min(gui.MainWidth, wa.Width-frameW), min(gui.MainHeight, wa.Height-frameH)
	if width != cb.Width || height != cb.Height {
		w.SetContentSize(width, height)
		b = w.Bounds()
	}
	if !place {
		return
	}
	x := max(wa.X, min(b.X, wa.X+wa.Width-b.Width))
	y := max(wa.Y, min(b.Y, wa.Y+wa.Height-b.Height))
	if x != b.X || y != b.Y {
		w.SetPosition(x, y)
	}
}

func applyAppearance(v int) {
	sources := []mygo.ThemeSource{mygo.ThemeSystem, mygo.ThemeLight, mygo.ThemeDark}
	if v < 0 || v >= len(sources) {
		v = 0
	}
	mygo.Theme.SetSource(sources[v])
}
