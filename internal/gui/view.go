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
	{"/nodes", "节点", icServer},
	{"/logs", "日志", icTerminal},
	{"/settings", "设置", icSliders},
}

// Shared layout sizes keep every page aligned: every page's content is padPage from the window's edges,
// its blocks gapBlock apart; every control of a toolbar row is controlHeight high; the content of every
// pane starts paneInset in from its edge. The sidebar's boxes are sideInset from its edges, their content
// sideInset more, and its bottom lines up with the pages'.
const (
	padPage       = 24
	gapBlock      = 16
	controlHeight = 34
	paneInset     = 16
	// MainWidth and MainHeight are the main window's first size.
	MainWidth, MainHeight = 1180, 760
	sideWidth             = 248
	sideInset             = 12
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
	logLS       ui.ListState
	cf          confirmation // deletion being asked about
	ruleTab     int
	settingsTab int
	widths      map[string]float32 // measured text widths, by size, weight, features and text (textWidth)
	// popupArea is the box of the dialog being built, as last laid out, which its drop-downs keep their
	// options in (choice); zero outside a dialog, for the window.
	popupArea ui.Rect
}

func New(a *app.App) *View {
	return &View{
		App:    a,
		router: ui.NewRouter("/nodes"),
		logo:   ui.NewBitmap(brand.Image(68, brand.Active)), // shown at 34 DIP, drawn at 2x
		selRow: -1,
		names:  newNameOrder(),
	}
}

// SoftwareRendering reports whether the last frame used software rendering.
// The window can retain its view in the tray to avoid rebuilding text and images.
// Like the view, this is read only on the UI thread.
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
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c)
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				a.topBar(c)
				a.router.View(c, func(r *ui.Route) {
					switch {
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
	})
	a.nodeDialog(c)
	a.groupDialog(c)
	a.portDialog(c)
	a.adminDialog(c)
	a.confirmDialog(c)
	a.toasts(c)
}

func (a *View) sidebar(c *ui.Context) {
	p := a.pal
	bar := c.TitleBar()
	ui.Column(c).Width(sideWidth).Shrink(0).Background(p.fill).BorderWidth(0, 1, 0, 0).BorderColor(p.border).
		Padding(max(bar.Height, 16), sideInset, padPage).Gap(18).DragWindow().Children(func() {
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

// navigation lists the pages. It is built here rather than with ui.Sidebar, which paints its icons and
// focused choice in the accent: the page shown is a capsule of colorless glass (chosen).
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

// topBar shows global notices in the title bar area: core progress, core errors and pending reconnects.
func (a *View) topBar(c *ui.Context) {
	p := a.pal
	bar := c.TitleBar()
	ui.Row(c).Height(max(bar.Height, 44)).Padding(0, bar.Right+16, 0, padPage).Gap(10).AlignItems(ui.Center).
		Justify(ui.End).DragWindow().Children(func() {
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
				ui.Text(c, "设置已修改，尚未应用到当前连接").FontSize(12).TextColor(p.warning)
				if button(c, "应用").Height(bannerButton).Padding(0, 10).FontSize(12).Clicked() {
					a.Reconnect()
				}
			})
		}
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

// connect connects to node id from the window, telling about a failure the connect reports at once
// (except a missing elevation, which opens its own dialog).
func (a *View) connect(c *ui.Context, id string) {
	a.Connect(id, c.Toast)
	if a.ConnErr != "" && !a.AdminPrompt {
		c.Toast(a.ConnErr)
	}
}

// adminDialog explains why TUN needs admin rights and offers an elevated restart.
func (a *View) adminDialog(c *ui.Context) {
	p := a.pal
	a.dialog(c, &a.AdminPrompt, dialog{width: 460, title: "TUN 模式需要管理员权限", icon: icShield, tone: p.warning, err: &a.adminErr,
		body: func() {
			ui.Text(c, "TUN 模式需要创建虚拟网卡 "+xray.TUNName+" 并修改系统路由，Windows 只允许管理员进行这些操作。").FontSize(13).TextColor(p.muted).LineHeight(1.5)
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
