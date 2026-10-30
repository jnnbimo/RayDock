// Package gui renders RayDock with MyGo's native UI: the main window, its pages and the tray icon.
package gui

import (
	"cmp"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/brand"
	"raydock/internal/xray"
)

type page struct {
	path, title string
	icon        *ui.SVG
}

var pages = []page{
	{"/home", "首页", icHome},
	{"/nodes", "节点", icServer},
	{"/logs", "日志", icTerminal},
	{"/settings", "设置", icSliders},
}

// Shared layout dimensions in DIPs.
const (
	padPage       = 24
	gapBlock      = 16
	controlHeight = 34
	paneInset     = 16
	// MainWidth and MainHeight are the size of the main window's page, which the window keeps: at this
	// height the home page's traffic card, under its cards of topCardsHeight, starts and ends with the
	// sidebar's connection panel.
	MainWidth, MainHeight = 980, 640
	// titleBarHeight is the height of the window's buttons (windowButtons), as Windows 11's title bars.
	titleBarHeight = 32
	sideWidth      = 248
	sideInset      = 12
)

// View is the main window: the app state plus what only the interface needs.
type View struct {
	*app.App
	router      *ui.Router
	pal         palette
	logo        *ui.Bitmap
	glows       map[ui.Color]*ui.Bitmap // the backdrop's glows, by color (glow)
	ambient     backdropCache           // current background composited for software rendering
	software    bool                    // renderer used by the last painted frame
	theme       ui.Theme
	group       string // group shown on the nodes page
	sortBy      sortColumn
	sortDesc    bool
	names       *nameOrder
	nodeLS      ui.ListState
	selRow      int
	nf          nodeForm
	menu        ctxMenu
	editSel     pendingSel // a selection a text input takes after its menu edited it
	typeahead   typeahead  // what was typed into the open options of a choice
	menuX       float32    // where the keyboard opens the selected row's menu, in the window
	menuY       float32
	portOpen    bool
	portNode    string
	portValue   float64
	portErr     string // the error of the last save in the port dialog
	adminErr    string // the error of the last elevated restart
	gf          groupForm
	share       nodeShare
	logLS       ui.ListState
	cf          confirmation // deletion being asked about
	ruleTab     int
	settingsTab int
	widths      map[string]float32 // measured text widths, by size, weight, features and text (textWidth)
	// popupArea is the box of the dialog being built, as last laid out, which its drop-downs keep their
	// options in (choice); zero outside a dialog, for the window.
	popupArea ui.Rect
	win       windowActions // nil until SetWindow
}

// windowActions is what the title bar's buttons do to the window: a *mygo.Window.
type windowActions interface {
	Minimize()
	Close()
	IsFocused() bool
}

// SetWindow gives the view the window it shows in, which its title bar's buttons minimize and close.
func (a *View) SetWindow(w windowActions) { a.win = w }

func New(a *app.App) *View {
	return &View{
		App:    a,
		router: ui.NewRouter("/home"),
		logo:   ui.NewBitmap(brand.Image(68, brand.Active)), // shown at 34 DIP, drawn at 2x
		selRow: -1,
		names:  newNameOrder(),
	}
}

// SoftwareRendering reports the last frame's renderer; UI thread only.
// Software-rendered windows retain their view while hidden in the tray.
func (a *View) SoftwareRendering() bool { return a.software }

// Build builds the window content.
func (a *View) Build(c *ui.Context) {
	a.pal = newPalette(c.Theme().Dark)
	a.theme = a.pal.theme(*c.Theme())
	c.SetTheme(&a.theme)
	if a.Conn == app.StateOn {
		c.After(time.Second) // refresh the connected time
	}

	a.backdrop(c, func() {
		// The page is laid out at its size, MainHeight, which the window has, and scrolls in a window a
		// short screen leaves shorter (main.go).
		if _, h := c.Size(); h < MainHeight {
			ui.Scroll(c.Key("window")).Fill().Children(func() { a.page(c).Height(MainHeight) })
		} else {
			a.page(c).Fill()
		}
	})
	a.nodeDialog(c)
	a.groupDialog(c)
	a.portDialog(c)
	a.shareDialog(c)
	a.adminDialog(c)
	a.confirmDialog(c)
	a.toasts(c)
}

// page is the window's content: the sidebar beside the title bar and the page shown.
func (a *View) page(c *ui.Context) ui.Element {
	return ui.Row(c).AlignItems(ui.Stretch).Children(func() {
		a.sidebar(c)
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			a.topBar(c)
			a.router.View(c, func(r *ui.Route) {
				switch {
				case r.Match("/home"):
					r.Title("首页")
					a.homePage(c)
				case r.Match("/nodes"):
					r.Title("节点")
					a.nodesPage(c)
				case r.Match("/logs"):
					r.Title("日志")
					a.logsPage(c)
				case r.Match("/settings"):
					r.Title("设置")
					a.settingsPage(c)
				}
			})
		})
	})
}

func (a *View) sidebar(c *ui.Context) {
	p := a.pal
	ui.Column(c).Width(sideWidth).Shrink(0).Background(p.fill).BorderWidth(0, 1, 0, 0).BorderColor(p.border).
		Padding(titleBarHeight, sideInset, padPage).Gap(18).DragWindow().Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(4, sideInset+1, 0).Children(func() {
			ui.Image(c, a.logo).Size(34, 34)
			ui.Column(c).Gap(1).Children(func() {
				ui.Text(c, "RayDock").FontSize(15).Bold()
				ui.Text(c, "Xray 客户端").FontSize(11).TextColor(p.muted)
			})
		})
		a.navigation(c)
		a.controlPanel(c)
	})
}

// Build from base widgets to keep the selected page colorless.
func (a *View) navigation(c *ui.Context) {
	p, t := a.pal, a.theme
	ui.Column(c).Grow(1).Gap(t.Space(0.5)).Padding(t.Space(2.5), 0).Label("页面").Children(func() {
		for _, pg := range pages {
			shown := a.router.Path() == pg.path
			item := choosable(ui.ButtonBase(c.Key("nav"+pg.path))).Label(pg.title).Cursor(ui.CursorPointer).Justify(ui.Start).
				Gap(t.Space(2)).Padding(t.Space(1.5), sideInset).Radius(t.Radius)
			col := p.muted
			switch {
			case shown:
				p.chosen(item)
				col = p.text
			case item.Hovered():
				item.Background(t.SurfaceHover)
			}
			item.Children(func() {
				ui.Icon(c, pg.icon).FontSize(16).TextColor(col)
				ui.Text(c, pg.title).Grow(1).SingleLine()
				if n := len(a.State.Nodes); pg.path == "/nodes" && n > 0 {
					ui.Badge(c, fmt.Sprint(n))
				}
			})
			if item.Clicked() && !shown {
				a.router.Push(pg.path)
			}
		}
	})
}

func (a *View) topBar(c *ui.Context) {
	ui.Row(c).Height(44).Padding(0, 0, 0, padPage).DragWindow().Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Height(44).Padding(0, 16, 0, 0).Gap(10).AlignItems(ui.Center).Justify(ui.End).
			DragWindow().Children(func() { a.banners(c) })
		a.windowButtons(c)
	})
}

// banners tell what the core and the connection are waiting for, in the title bar.
func (a *View) banners(c *ui.Context) {
	p := a.pal
	switch {
	case a.CoreBusy:
		a.banner(c, p.accent, func() {
			ui.Spinner(c).Size(12, 12)
			msg := cmp.Or(a.CoreMsg, "正在准备 Xray 核心…")
			if a.CoreProgress > 0 {
				msg = fmt.Sprintf("%s %d%%", msg, int(a.CoreProgress*100))
			}
			ui.Text(c, msg).FontSize(12).TextColor(p.text).FontFeatures("tnum")
		})
	case a.CoreErr != "":
		a.banner(c, p.danger, func() {
			ui.Text(c, "Xray 核心下载失败").FontSize(12).TextColor(p.danger).Tooltip(a.CoreErr)
			if button(c, "重试").Height(bannerButton).Padding(0, 10).FontSize(12).Clicked() {
				a.InstallCore()
			}
		})
	}
	if a.SettingsDirty && a.Conn == app.StateOn {
		a.banner(c, p.warning, func() {
			ui.Text(c, "设置已修改，应用时会重新连接，已建立的连接将断开").FontSize(12).TextColor(p.warning)
			if button(c, "应用").Height(bannerButton).Padding(0, 10).FontSize(12).Clicked() {
				a.Reconnect()
			}
		})
	}
}

// windowButtons minimize and close the window, as Windows' own title bar buttons do and look: the
// window has a fixed size, and the system's buttons would show a maximize button too, disabled. The glyphs
// are Segoe Fluent Icons' (Windows 11), or Segoe MDL2 Assets' at the same code points (Windows 10).
func (a *View) windowButtons(c *ui.Context) {
	// At the window's top, as Windows' are, not centered in the taller title bar.
	ui.Row(c).Shrink(0).AlignSelf(ui.Start).Children(func() {
		if a.windowButton(c, "win-minimize", "\ue921", "最小化", false).Clicked() && a.win != nil {
			a.win.Minimize()
		}
		if a.windowButton(c, "win-close", "\ue8bb", "关闭", true).Clicked() && a.win != nil {
			a.win.Close()
		}
	})
}

// The close button's red, Windows 11's.
var closeRed = ui.Hex("#c42b1c")

func (a *View) windowButton(c *ui.Context, key, glyph, label string, close bool) ui.Element {
	// Windows' colors: the glyph in the appearance's text color, fainter while the window is inactive; on
	// hover and press a backplate of that color at 10% and 20%, or red behind a white glyph for close.
	white := ui.Hex("#ffffff")
	base, fore := ui.Hex("#000000"), ui.Hex("#000000").Alpha(0xe4/255.0)
	if a.pal.dark {
		base, fore = white, white
	}
	// A box, not a button: as Windows' own, they take the pointer and not the keyboard focus, which Tab
	// would give them unseen (Alt+F4 and the window menu are the keyboard's).
	b := ui.Box(c.Key(key)).Label(label).Size(46, titleBarHeight).Center()
	switch {
	case close && b.Hovered() && b.Pressed():
		b.Background(closeRed.Alpha(0.8))
		fore = white
	case close && b.Hovered():
		b.Background(closeRed)
		fore = white
	case b.Hovered() && b.Pressed():
		b.Background(base.Alpha(0.2))
	case b.Hovered():
		b.Background(base.Alpha(0.1))
	case a.win != nil && !a.win.IsFocused():
		fore = base.Alpha(0x72 / 255.0)
	}
	return b.Children(func() {
		ui.Text(c, glyph).Font("Segoe Fluent Icons, Segoe MDL2 Assets").FontSize(10).TextColor(fore)
	})
}

// The title bar's banners, and the buttons in them, in DIPs.
const (
	bannerHeight = 28
	bannerButton = 22
)

func (a *View) banner(c *ui.Context, col ui.Color, body func()) {
	ui.Row(c).Height(bannerHeight).Gap(8).Padding(0, 4, 0, 12).Radius(999).Background(col.Alpha(0.12)).AlignItems(ui.Center).Children(body)
}

func (a *View) togglePower(c *ui.Context) {
	if a.Conn != app.StateOff {
		a.Disconnect()
		return
	}
	id := a.ConnectTarget()
	if id == "" {
		if len(a.State.Nodes) == 0 {
			c.Toast("还没有节点，请先导入")
			return
		}
		id = a.State.Nodes[0].ID
	}
	a.connect(c, id)
}

// connect also surfaces immediate errors and elevation prompts.
func (a *View) connect(c *ui.Context, id string) {
	a.Connect(id, c.Toast)
	if a.ConnErr != "" && !a.AdminPrompt {
		c.Toast(a.ConnErr)
	}
}

func (a *View) adminDialog(c *ui.Context) {
	p := a.pal
	a.dialog(c, &a.AdminPrompt, dialog{width: 460, title: "TUN 模式需要管理员权限", icon: icShield, tone: p.warning, err: &a.adminErr,
		body: func() {
			ui.Text(c, "TUN 模式需要创建虚拟网卡 "+xray.TUNNames[0]+" 并修改系统路由，Windows 只允许管理员进行这些操作。").FontSize(13).TextColor(p.muted).LineHeight(1.5)
			ui.Text(c, "重新启动后会自动连接所选节点。").FontSize(13).TextColor(p.muted)
		},
		start: func() {
			if button(c, "关闭 TUN 模式").Clicked() {
				a.AdminPrompt = false
				a.State.Settings.TUN.Enabled = false
				a.ConnErr = ""
				a.Save()
			}
		},
		actions: func() {
			if primaryIconButton(c, icShield, "以管理员身份重启").Clicked() {
				if err := a.RestartAsAdmin(); err != nil {
					a.adminErr = err.Error()
				} else {
					a.AdminPrompt = false
				}
			}
		},
	})
}
