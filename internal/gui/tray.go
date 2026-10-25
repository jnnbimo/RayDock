package gui

import (
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/brand"
)

// Reuse and pre-render the flyout to avoid an empty frame when opening it.
type trayMenu struct {
	*View
	tray       *mygo.Tray
	post       func(func()) // runs a change on the UI thread and redraws the main window, if there is one
	show, quit func()
	win        *mygo.Window // the flyout, nil before the app is ready or once it quits
	quitting   bool         // the app quits: the flyout closes rather than hides
	height     float32      // the flyout's height as last sized
	// seen is the state the menu last showed: changed by its controls, the main window shows it again.
	// The controls report changes after the frame they are built in, so it is the next that sees.
	seen    trayState
	seenSet bool
	prompt  bool // the admin prompt was up when the menu last drew
}

type trayState struct {
	proxy, tun bool
	mode       string
	conn       app.ConnState
	active     string
}

// Fixed flyout dimensions in DIPs.
const (
	trayWidth   = 224
	trayPad     = 6 // around the menu, and again around its connection controls
	trayGap     = 10
	trayItem    = 34
	trayDivider = 9
)

func (m *trayMenu) trayHeight() float32 {
	return 4*trayPad + m.connectionHeight(trayGap) + trayDivider + 2*trayItem
}

// SetupTray creates the tray icon: click shows the window, right-click opens the menu. post runs a change
// on the UI thread, with or without a window. Returns nil on failure.
func SetupTray(a *app.App, post func(func()), show, quit func()) *mygo.Tray {
	tray, err := mygo.NewTray(mygo.TrayOptions{Icon: trayIcon(false), ToolTip: "RayDock · 未连接"})
	if err != nil {
		log.Println("托盘：", err)
		return nil
	}
	m := &trayMenu{tray: tray, post: post, show: show, quit: quit}
	m.View = &View{App: a}
	m.height = m.trayHeight()
	m.make()
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { m.quitting = true })
	tray.OnClick(func() {
		m.close()
		show()
	})
	tray.OnRightClick(m.toggle)
	a.OnChange = func() {
		on := a.Conn == app.StateOn
		_ = tray.SetIcon(trayIcon(on), false)
		tip := "RayDock · 未连接"
		switch a.Conn {
		case app.StateConnecting:
			tip = "RayDock · 连接中"
		case app.StateOn:
			tip = "RayDock · 已连接"
			if n := a.NodeByID(a.ActiveID); n != nil {
				tip += " · " + n.Label()
			}
		}
		tray.SetToolTip(tip)
		if m.win != nil {
			m.win.Update(func() {}) // the menu shows the new state, drawn before it shows again
		}
	}
	return tray
}

func (m *trayMenu) make() {
	w := mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:      m.tray,
		Placement: mygo.PlacementTop,
		Gap:       6,
		Width:     trayWidth,
		Height:    int(m.height),
		Focusable: true, // closes as the user presses elsewhere, and on Escape
		Shadow:    true, // Windows 11 rounds and outlines it as its menus
		Hidden:    true,
		Content:   ui.View(m.build),
	})
	m.win = w
	w.OnClose(func(e *mygo.CloseEvent) {
		if !m.quitting {
			e.PreventDefault()
			w.Hide()
		}
	})
	w.OnClosed(func() { m.win = nil })
	// A hidden window draws nothing until it shows: shown the first time, it would show its empty
	// frame before its picture. It draws its first frame now, unseen, and hides.
	w.SetOpacity(0)
	w.OnReadyToShow(func() {
		w.Hide()
		w.SetOpacity(1)
	})
	w.ShowInactive()
}

func (m *trayMenu) toggle() {
	w := m.win
	if w == nil {
		return
	}
	if w.IsVisible() {
		w.Hide()
		return
	}
	m.seenSet = false
	m.height = m.trayHeight()
	w.SetSize(trayWidth, int(m.height)) // places it by the icon again, which may have moved
	w.Update(func() {})
	w.Show()
	w.Focus()
}

func (m *trayMenu) close() {
	if m.win != nil {
		m.win.Hide()
	}
}

func (m *trayMenu) then(fn func()) {
	m.close()
	m.post(fn)
}

func (m *trayMenu) build(c *ui.Context) {
	a := m.View
	a.pal = newPalette(c.Theme().Dark)
	a.theme = a.pal.theme(*c.Theme())
	c.SetTheme(&a.theme)
	p := a.pal
	m.follow()
	if a.Conn == app.StateOn {
		c.After(time.Second) // refresh the connected time, as the main window does
	}
	// The menu looks as the sidebar's connection panel it holds does: glass over what the main window,
	// at its first size, paints under the panel, the sidebar's frosting over the window's backdrop.
	a.backdropIn(c, func(r ui.Rect) ui.Rect {
		return ui.Rect{X: r.X - sideInset, Y: r.Y + r.H + padPage - MainHeight, W: MainWidth, H: MainHeight}
	}, func() {
		// The system draws the flyout's rounded corners and outline: the glass fills it.
		ui.Column(c).Fill().Background(p.fill).Children(func() {
			ui.Column(c).Fill().Padding(trayPad).Material(glass.Glass{}).Children(func() {
				// The controls of the sidebar's connection panel, which change where they are and leave the
				// menu open, as a menu's check marks do; the window's panel then shows them changed too.
				ui.Column(c).Gap(trayGap).Padding(trayPad).Children(func() { a.connectionControls(c, true) })
				m.divider(c)
				// The actions, together at the bottom, nearest the icon.
				if m.item(c, "显示主窗口", p.text, false) {
					m.close()
					m.show()
				}
				if m.item(c, "退出 RayDock", p.text, false) {
					m.then(m.quit)
				}
			})
		})
	})
}

// follow synchronizes controls, flyout size and elevation prompts with the main window.
func (m *trayMenu) follow() {
	a := m.View
	s := &a.State.Settings
	now := trayState{s.SystemProxy, s.TUN.Enabled, s.Mode, a.Conn, a.ActiveID}
	if m.seenSet && now != m.seen {
		m.post(func() {})
	}
	m.seen, m.seenSet = now, true
	if h := m.trayHeight(); h != m.height {
		m.height = h
		if w := m.win; w != nil {
			go w.SetSize(trayWidth, int(h)) // after this frame: SetSize waits for the UI thread
		}
	}
	if a.AdminPrompt && !m.prompt && m.win != nil && m.win.IsVisible() {
		m.close()
		m.show()
	}
	m.prompt = a.AdminPrompt
}

func (m *trayMenu) divider(c *ui.Context) {
	ui.Box(c).Height(1).Margin((trayDivider-1)/2, 6).Background(m.pal.border)
}

func (m *trayMenu) item(c *ui.Context, label string, col ui.Color, disabled bool) bool {
	p, t := m.pal, m.theme
	b := choosable(ui.ButtonBase(c.Key("tray-"+label))).Label(label).Role(ui.RoleMenuItem).Cursor(ui.CursorPointer).
		Height(trayItem).Justify(ui.Start).Padding(0, trayPad-1).Radius(t.Radius).Disabled(disabled)
	switch {
	case disabled:
		col = p.muted.Alpha(0.6)
	case b.Hovered() || b.FocusVisible():
		p.chosen(b)
	}
	b.Children(func() { ui.Text(c, label).FontSize(13).TextColor(col).SingleLine() })
	return b.Clicked()
}

func trayIcon(on bool) []byte {
	if on {
		return trayOn
	}
	return trayOff
}

// 16 pt tray icons as 32 px images for high DPI.
var (
	trayOn  = brand.PNG(32, brand.Active)
	trayOff = brand.PNG(32, brand.Idle)
)
