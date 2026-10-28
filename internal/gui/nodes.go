package gui

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/node"
)

// colWidths reserves the non-name columns; the name takes the remaining width.
type colWidths struct{ protocol, port, latency, ip, speed float32 }

// Reserve worst-case numeric widths so measurement updates never shift columns.
var (
	widestLatency = []string{"9999 ms"}
	widestIP      = []string{"255.255.255.255"}
	widestRate    = []string{"↓ 999.9 MB/s", "↓ 1023 KB/s"}
	widestPort    = []string{"65535"}
)

// Sizes shared by the header, the cells and their measurement.
const (
	headerSize   = 11.5
	headerHeight = 34 // the column header, which the rows scroll under
	listInset    = 6  // between the list's pane and its rows, whose capsules show the row chosen
	cellSize     = 12
	rateSize     = 11.5
	colGap       = 16 // between columns: a right-aligned value and a left-aligned one next to it read apart
	sortIcon     = 4 + 11
)

func protocolParts(n *node.Node) (name, transport string) {
	return n.ProtocolName(), " · " + n.Transport()
}

// textWidth is the width of text in the list's font at size, weight and features, measured once.
func (a *View) textWidth(c *ui.Context, size float32, weight int, features, text string) float32 {
	key := fmt.Sprintf("%g|%d|%s|%s", size, weight, features, text)
	if w, ok := a.widths[key]; ok {
		return w
	}
	w, _ := c.MeasureText(0, ui.Span{Text: text, Size: size, Weight: weight, Features: features})
	w = float32(math.Ceil(float64(w)))
	if a.widths == nil {
		a.widths = map[string]float32{}
	}
	a.widths[key] = w
	return w
}

// nodeColumns reserves numeric widths and measures variable protocol/status text.
func (a *View) nodeColumns(c *ui.Context, rows []int) colWidths {
	header := func(label string) float32 { return a.textWidth(c, headerSize, 0, "", label) }
	widest := func(size float32, features string, texts []string) float32 {
		var w float32
		for _, t := range texts {
			w = max(w, a.textWidth(c, size, 0, features, t))
		}
		return w
	}
	w := colWidths{
		port:    max(header("专用端口"), widest(cellSize, "tnum", widestPort)),
		latency: max(header("延迟")+sortIcon, widest(cellSize, "tnum", widestLatency), 14),
		ip:      max(header("出口 IP"), widest(cellSize, "tnum", widestIP)),
		speed:   max(header("速度"), widest(rateSize, "tnum", widestRate)),
	}
	w.protocol = header("协议")
	for _, i := range rows {
		n := &a.State.Nodes[i]
		name, transport := protocolParts(n)
		w.protocol = max(w.protocol, a.textWidth(c, cellSize, 600, "", name)+a.textWidth(c, cellSize, 0, "", transport))
		if l := a.Latency[n.ID]; l.Err != "" {
			w.latency = max(w.latency, a.textWidth(c, cellSize, 0, "", l.Err))
		}
		if l := a.Latency[n.ID]; l.IPErr != "" {
			w.ip = max(w.ip, a.textWidth(c, cellSize, 0, "", l.IPErr))
		}
		if s, ok := a.Speed[n.ID]; ok && s.Testing() {
			phase := a.textWidth(c, rateSize, 0, "", s.Phase)
			if s.Phase != "排队" {
				phase += 12 + 6 // spinner and gap
			}
			w.speed = max(w.speed, phase)
		}
	}
	return w
}

func (a *View) nodesPage(c *ui.Context) {
	if a.GroupByID(a.group) == nil && len(a.State.Groups) > 0 {
		a.group = a.State.Groups[0].ID // first visit, or the group shown was deleted
	}
	rows := a.visibleNodes()
	ui.Column(c).Grow(1).MinHeight(0).MinWidth(0).Padding(4, padPage, padPage).Gap(gapBlock).Children(func() {
		sub := fmt.Sprintf("%d 个节点 · %d 个分组", len(a.State.Nodes), len(a.State.Groups))
		a.pageHeader(c, "节点", sub, func() {
			if iconButton(c, icFolderPlus, "添加分组").Clicked() {
				a.openGroupDialog("")
			}
			_, canAdd := a.addGroup()
			add := primaryIconButton(c, icPlus, "添加节点").Disabled(!canAdd)
			if !canAdd {
				add.Tooltip("订阅分组的节点随订阅更新，请切换到普通分组再添加")
			}
			if add.Clicked() {
				a.openAddNode()
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			a.groupTabs(c)
			ids := make([]string, len(rows))
			for i, r := range rows {
				ids[i] = a.State.Nodes[r].ID
			}
			if iconButton(c, icZap, "测延迟").Disabled(len(ids) == 0 || a.LatencyBusy).Tooltip("测量列表中节点的延迟与出口 IP").Clicked() {
				if err := a.TestLatency(ids); err != nil {
					c.Toast(err.Error())
				}
			}
		})
		if g := a.GroupByID(a.group); g != nil {
			a.groupBar(c, g)
		}
		a.nodeList(c, rows)
	})
}

func (a *View) nodeList(c *ui.Context, rows []int) {
	p := a.pal
	a.selRow = slices.IndexFunc(rows, func(r int) bool { return a.State.Nodes[r].ID == a.State.SelectedID })
	a.nodeLS.Selected = &a.selRow
	a.nodeLS.Key = func(i int) any { return a.State.Nodes[rows[i]].ID }
	a.nodeLS.Label = func(i int) string { return a.State.Nodes[rows[i]].Label() }
	// Rows drag to a new place in the group's own order; a sorted list shows another order, so it does not.
	a.nodeLS.Reorder = nil
	if a.sortBy == sortNone {
		a.nodeLS.Reorder = func(sel []int, to int) {
			ids := make([]string, len(sel))
			for k, i := range sel {
				ids[k] = a.State.Nodes[rows[i]].ID
			}
			before := ""
			if to < len(rows) {
				before = a.State.Nodes[rows[to]].ID
			}
			a.MoveNodes(ids, before)
		}
	}

	p.pane(ui.Column(c).Grow(1).MinHeight(0).Radius(16).Clip()).Children(func() {
		if len(a.State.Nodes) == 0 {
			a.emptyNodes(c)
			return
		}
		w := a.nodeColumns(c, rows)
		// A colorless wash under the chosen row's glass; it also draws where a dragged row would go.
		c.SetTheme(a.colorless(p.text.Alpha(0.14)))
		list := ui.List(c.Key("nodes"), &a.nodeLS, len(rows), func(i int) {
			a.nodeRow(c, &a.State.Nodes[rows[i]], i == a.selRow, w)
		}).Grow(1).Padding(headerHeight+listInset, listInset, listInset).ScrollbarInsets(headerHeight, 0, 0).Gap(2).Label("节点列表")
		c.SetTheme(&a.theme)
		list.Children(func() {
			if len(rows) == 0 {
				ui.Text(c, "此分组还没有节点").TextColor(p.muted).Padding(24).AlignSelf(ui.Center)
			}
		})
		// The column header floats over the list, frosting the rows that scroll under it; built after the
		// list so it paints, and takes the pointer, over them.
		ui.Row(c).Absolute().Top(0).Left(0).Right(0).Gap(colGap).Padding(0, paneInset).Height(headerHeight).AlignItems(ui.Center).
			Material(glass.Blur{Radius: 8}).BorderWidth(0, 0, 1, 0).BorderColor(p.border).
			FontSize(headerSize).TextColor(p.muted).Children(func() {
			a.sortHeader(c, "名称 / 地址", sortName, false).Grow(1).MinWidth(0)
			ui.Text(c, "协议").Width(w.protocol).Shrink(0)
			ui.Text(c, "专用端口").Width(w.port).Shrink(0).Tooltip("程序连接此端口时始终使用该节点，与当前连接无关")
			a.sortHeader(c, "延迟", sortLatency, true).Width(w.latency)
			ui.Text(c, "出口 IP").Width(w.ip).Shrink(0).Tooltip("经该节点访问网站时，网站看到的 IP")
			ui.Text(c, "速度").Width(w.speed).Shrink(0).TextAlign(ui.End)
		})
		if list.Changed() && a.selRow >= 0 && a.selRow < len(rows) {
			a.State.SelectedID = a.State.Nodes[rows[a.selRow]].ID
			a.Save()
		}
		if list.Submitted() && a.selRow >= 0 && a.selRow < len(rows) {
			a.connect(c, a.State.Nodes[rows[a.selRow]].ID)
		}
		// The menu key and Shift+F10 open the selected row's menu below its start.
		if (a.nodeLS.Shortcut(c, 0, ui.KeyContextMenu) || a.nodeLS.Shortcut(c, ui.Shift, ui.KeyF10)) && a.selRow >= 0 && a.selRow < len(rows) {
			a.openMenuAt(nodeMenuID(a.State.Nodes[rows[a.selRow]].ID), a.menuX, a.menuY, true)
		}
	})
	a.nodeMenu(c)
}

func nodeMenuID(id string) string { return "node:" + id }

// nodeMenu draws the open menu of a node row. It is built after the rows, so its actions may remove one.
func (a *View) nodeMenu(c *ui.Context) {
	nodeID, ok := strings.CutPrefix(a.menu.id, "node:")
	if !a.menu.open || !ok {
		return
	}
	n := a.NodeByID(nodeID)
	if n == nil {
		a.menu = ctxMenu{}
		return
	}
	id, link := n.ID, n.Link
	speed := menuItem{label: "测速", act: func() {
		if err := a.TestSpeed([]string{id}); err != nil {
			c.Toast(err.Error())
		}
	}}
	if a.SpeedBusy {
		speed = menuItem{label: "停止测速", act: a.StopSpeedTest}
	}
	a.drawMenu(c, []menuItem{
		{label: "连接", act: func() { a.connect(c, id) }},
		{label: "编辑", act: func() { a.openEditNode(id) }},
		speed,
		{label: "测延迟", disabled: a.LatencyBusy, act: func() {
			if err := a.TestLatency([]string{id}); err != nil {
				c.Toast(err.Error())
			}
		}},
		{label: "节点分享", disabled: link == "", act: func() { a.openShare(id) }},
		{label: "设置专用端口", act: func() { a.openPortDialog(id) }},
		{},
		{label: "删除", danger: true, act: func() { a.confirmDeleteNode(id) }},
	})
}

func (a *View) nodeRow(c *ui.Context, n *node.Node, selected bool, w colWidths) {
	p := a.pal
	active := n.ID == a.ActiveID
	id := n.ID
	// The row's text starts paneInset in from the list's pane, as its header: the list's inset, the row's
	// rim, then its padding.
	row := choosable(ui.Row(c).Gap(colGap).Padding(8, paneInset-listInset-1).MinHeight(48).Radius(8).AlignItems(ui.Center))
	a.takeRightClick(row, nodeMenuID(id))
	// The node in use is washed green, amber while connecting; chosen or hovered, the wash deepens.
	wash := p.success
	if a.Conn == app.StateConnecting {
		wash = p.warning
	}
	hovered := row.Hovered() || a.menuFor(nodeMenuID(id))
	switch {
	case selected && active:
		lift(p.dark, row.Material(glass.Glass{Tint: wash.Alpha(0.3)}).Border(1, raisedEdge(p.dark, p.border)))
	case selected:
		p.chosen(row)
	case active && hovered:
		row.Background(wash.Alpha(0.3))
	case active:
		row.Background(wash.Alpha(0.22))
	case hovered:
		row.Background(a.theme.SurfaceHover)
	}
	if selected {
		a.menuX, a.menuY = row.LocalToWindow(16, row.LayoutBounds().H)
	}
	sub := p.muted
	row.Children(func() {
		ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, n.Label()).FontSize(13.5).SingleLine()
			ui.Text(c, n.Address()).FontSize(11.5).TextColor(sub).SingleLine()
		})
		name, transport := protocolParts(n)
		ui.Row(c).Width(w.protocol).Shrink(0).AlignItems(ui.Center).FontSize(cellSize).Children(func() {
			ui.Text(c, name).FontWeight(600).TextColor(p.on(protocolColor(n.Protocol))).SingleLine()
			ui.Text(c, transport).TextColor(sub).SingleLine()
		})
		ui.Row(c).Width(w.port).Shrink(0).Children(func() {
			port, ok := a.State.Ports[id]
			switch {
			case !ok:
				ui.Text(c, "—").FontSize(12).TextColor(sub)
			case a.PortErrs[id] != "":
				ui.Textf(c, "%d", port).FontSize(12).FontFeatures("tnum").TextColor(p.danger).Tooltip(a.PortErrs[id])
			default:
				ui.Textf(c, "%d", port).FontSize(12).FontFeatures("tnum").TextColor(p.text).
					Tooltip(fmt.Sprintf("127.0.0.1:%d 始终经此节点上网（SOCKS5 / HTTP）", port))
			}
		})
		via := a.measuredVia(n)
		ui.Row(c).Width(w.latency).Shrink(0).Justify(ui.End).Children(func() {
			l, ok := a.Latency[n.ID]
			switch {
			case !ok:
				ui.Text(c, "—").FontSize(12).TextColor(sub)
			case l.Testing:
				ui.Spinner(c).Size(14, 14)
			case l.Err != "":
				ui.Text(c, l.Err).FontSize(12).TextColor(p.danger).Tooltip(via)
			default:
				ms := l.Duration.Milliseconds()
				ui.Textf(c, "%d ms", ms).FontSize(12).FontFeatures("tnum").TextColor(p.latencyColor(ms)).Tooltip(via)
			}
		})
		a.ipCell(c, n.ID, w.ip, sub)
		a.speedCell(c, n.ID, via, w.speed, sub)
	})
}

// measuredVia identifies measurements made through the full proxy chain.
func (a *View) measuredVia(n *node.Node) string {
	chain, err := a.Chain(n)
	if err != nil || len(chain) < 2 {
		return ""
	}
	names := make([]string, len(chain))
	for i, c := range chain {
		names[i] = c.Label()
	}
	return "经代理链测得：" + strings.Join(names, " → ")
}

func (a *View) ipCell(c *ui.Context, id string, width float32, sub ui.Color) {
	p := a.pal
	ui.Row(c).Width(width).Shrink(0).MinWidth(0).Children(func() {
		l := a.Latency[id]
		switch {
		case l.IP != "":
			ui.Text(c, l.IP).FontSize(cellSize).FontFeatures("tnum").TextColor(p.text).SingleLine().MinWidth(0).
				Tooltip("出口 IP：" + l.IP)
		case l.IPErr != "":
			ui.Text(c, l.IPErr).FontSize(12).TextColor(p.danger).
				Tooltip("节点服务器可以连接，但经它访问查询 IP 的网站失败：节点配置可能有误，或它无法访问外网")
		default:
			ui.Text(c, "—").FontSize(12).TextColor(sub)
		}
	})
}

func (a *View) speedCell(c *ui.Context, id, via string, width float32, sub ui.Color) {
	p := a.pal
	ui.Column(c).Width(width).Shrink(0).Gap(1).AlignItems(ui.End).FontSize(rateSize).FontFeatures("tnum").Children(func() {
		s, ok := a.Speed[id]
		switch {
		case !ok:
			ui.Text(c, "—").FontSize(12).TextColor(sub)
		case s.Testing():
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if s.Phase != "排队" {
					ui.Spinner(c).Size(12, 12)
				}
				ui.Text(c, s.Phase).TextColor(sub)
			})
		case s.Down == 0:
			ui.Text(c, "失败").FontSize(12).TextColor(p.danger).Tooltip(joinLines(s.Err, via))
		default:
			ui.Text(c, "↓ "+formatRate(s.Down)).TextColor(p.text).Tooltip(via)
			if s.Err != "" {
				ui.Text(c, "↑ 失败").TextColor(p.danger).Tooltip(joinLines(s.Err, via))
			} else {
				ui.Text(c, "↑ "+formatRate(s.Up)).TextColor(sub).Tooltip(via)
			}
		}
	})
}

func joinLines(lines ...string) string {
	return strings.Join(slices.DeleteFunc(lines, func(s string) bool { return s == "" }), "\n")
}

func formatRate(bps float64) string {
	if bps >= 1<<20 {
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	}
	return fmt.Sprintf("%.0f KB/s", bps/(1<<10))
}

func (a *View) emptyNodes(c *ui.Context) {
	p := a.pal
	ui.Column(c).Grow(1).Center().Gap(14).Padding(24).Children(func() {
		ui.Box(c).Size(56, 56).Radius(16).Center().Background(p.accent.Alpha(0.14)).Children(func() {
			ui.Icon(c, icServer).FontSize(26).TextColor(p.on(p.accent))
		})
		ui.Text(c, "还没有节点").FontSize(16).Bold()
		ui.Text(c, "粘贴分享链接或 Xray 出站 JSON、手动填写，或添加订阅分组批量导入").
			FontSize(12.5).TextColor(p.muted).TextAlign(ui.Center)
		ui.Row(c).Gap(10).Children(func() {
			if primaryIconButton(c, icPlus, "添加节点").Clicked() {
				a.openAddNode()
			}
			if iconButton(c, icFolderPlus, "添加分组").Clicked() {
				a.openGroupDialog("")
			}
		})
	})
}

func (a *View) groupName(id string) string {
	if g := a.GroupByID(id); g != nil {
		return g.Name
	}
	return "—"
}

func (a *View) visibleNodes() []int {
	var out []int
	for i := range a.State.Nodes {
		if a.State.Nodes[i].GroupID == a.group {
			out = append(out, i)
		}
	}
	a.sortRows(out)
	return out
}

// openPortDialog edits node id's dedicated port, suggesting the first free one from 20001.
func (a *View) openPortDialog(id string) {
	a.portOpen, a.portNode = true, id
	port := a.State.Ports[id]
	for p := 20001; port == 0; p++ {
		if p != a.State.Settings.Port && !slices.Contains(slices.Collect(maps.Values(a.State.Ports)), p) {
			port = p
		}
	}
	a.portValue = float64(port)
}

func (a *View) portDialog(c *ui.Context) {
	p := a.pal
	n := a.NodeByID(a.portNode)
	if n == nil {
		a.portOpen = false
	}
	save := func(port int) {
		if err := a.SetNodePort(a.portNode, port); err != nil {
			a.portErr = err.Error()
			return
		}
		a.portOpen = false
	}
	var submitted bool
	d := dialog{width: 480, title: "专用端口", err: &a.portErr}
	d.body = func() {
		ui.Text(c, "程序连接这个本地端口（SOCKS5 与 HTTP 共用）时，始终经「"+n.Label()+"」及其分组的代理链上网，与当前连接的节点无关。").
			FontSize(12.5).TextColor(p.muted).LineHeight(1.5)
		a.labeled(c, "端口", func() {
			port := ui.NumberInput(c.Key("node-port"), &a.portValue, 1024, 65535, 1).Label("专用端口").AutoFocus()
			submitted = a.numberMenu(c, port, "node-port", &a.portValue, 1024, 65535, nil).Submitted()
		})
		if a.State.Settings.AllowLAN {
			ui.Text(c, "已允许局域网连接：其它设备也可以通过本机地址使用这个端口。").FontSize(12).TextColor(p.warning)
		}
		if port, ok := a.State.Ports[n.ID]; ok {
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			a.labeled(c, "当前地址", func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					a.copyMenu(c, ui.Text(c, addr).Font("monospace").FontSize(13).Selectable().Grow(1), "port-address", addr)
					if iconButton(c, icCopy, "复制地址").Clicked() {
						a.Clipboard.WriteText(addr)
						c.Toast("已复制 " + addr)
					}
				})
			})
		}
	}
	d.start = func() {
		if _, ok := a.State.Ports[a.portNode]; ok && button(c, "移除").Clicked() {
			save(0)
		}
	}
	d.actions = func() {
		if primaryButton(c, "保存").Clicked() || submitted {
			save(int(a.portValue))
		}
	}
	a.dialog(c, &a.portOpen, d)
}
