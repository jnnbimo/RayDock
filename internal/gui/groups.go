package gui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/store"
)

const noProxy = "不使用"

// groupDrag is the value a dragged group tab carries: its ID.
type groupDrag string

// groupForm is the state of the add/edit group dialog. Front and back hold the IDs of the proxies
// chosen, "" for none: labels only show them, as a subscription updated while the dialog is open may
// give a label to another node. A proxy whose node is gone stays chosen, and saving asks for another.
type groupForm struct {
	open        bool
	id          string // "" when adding
	kind        int    // 0 normal, 1 subscription
	name, url   string
	front, back string
	err         string // the error of the last save, or the proxies of the group whose nodes are gone
}

// groupTabs chooses the group shown, each with its node count.
func (a *View) groupTabs(c *ui.Context) {
	p := a.pal
	if len(a.State.Groups) == 0 {
		ui.Spacer(c)
		return
	}
	var ids []string
	for _, g := range a.State.Groups {
		ids = append(ids, g.ID)
	}
	idx := max(slices.Index(ids, a.group), 0)
	counts := map[string]int{}
	for _, n := range a.State.Nodes {
		counts[n.GroupID]++
	}
	// A group dropped on a tab takes its place; the move applies once the tabs are built.
	var dropped, before string
	ui.ScrollHorizontal(c.Key("group-scroll")).Grow(1).MinWidth(0).Children(func() {
		navTabs(c.Key("groups"), &idx, len(ids), func(i int) {
			g := a.GroupByID(ids[i])
			ic := icFolder
			if g.Subscription() {
				ic = icRSS
			}
			ui.Icon(c, ic).FontSize(13).TextColor(p.muted)
			ui.Text(c, g.Name).FontSize(12.5).SingleLine()
			ui.Textf(c, "%d", counts[ids[i]]).FontSize(11).FontFeatures("tnum").TextColor(p.muted)
		}, func(i int, tab ui.Element) {
			id := ids[i]
			tab.Drag(groupDrag(id)).Tooltip("拖动可调整分组顺序")
			if tab.Dragging() {
				tab.Opacity(0.4)
			}
			// Moving right lands after the tab, moving left before it, as the bar shows.
			after := func(from groupDrag) bool { return slices.Index(ids, string(from)) < i }
			if from, over := ui.DragOver[groupDrag](tab); over && string(from) != id {
				if after(from) {
					tab.BorderWidth(0, 2, 0, 0).BorderColor(p.accent)
				} else {
					tab.BorderWidth(0, 0, 0, 2).BorderColor(p.accent)
				}
			}
			if from, ok := ui.Drop[groupDrag](tab); ok && string(from) != id {
				dropped, before = string(from), id
				if after(from) {
					before = ""
					if i+1 < len(ids) {
						before = ids[i+1]
					}
				}
			}
		}).Label("分组").OnChange(func() { a.group = ids[idx] })
	})
	if dropped != "" {
		a.MoveGroup(dropped, before)
	}
}

// groupBar describes the group shown and holds its actions.
func (a *View) groupBar(c *ui.Context, g *store.Group) {
	p := a.pal
	id := g.ID
	p.pane(ui.Row(c).Gap(12).Padding(10, paneInset).Radius(16)).AlignItems(ui.Center).Children(func() {
		ic, kind := icFolder, "普通分组"
		if g.Subscription() {
			ic, kind = icRSS, "订阅分组"
		}
		ui.Box(c).Size(34, 34).Radius(10).Center().Background(p.accent.Alpha(0.14)).Shrink(0).Children(func() {
			ui.Icon(c, ic).FontSize(16).TextColor(p.on(p.accent))
		})
		ui.Column(c).Gap(3).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, g.Name).FontSize(13.5).Bold().SingleLine()
				a.chip(c, kind, p.muted)
			})
			status, col := a.groupStatus(g)
			ui.Text(c, status).FontSize(12).TextColor(col).SingleLine().Tooltip(status)
		})
		a.chainChips(c, g)
		if g.Subscription() {
			if a.SubBusy[id] {
				ui.Spinner(c).Size(16, 16)
			}
			if iconButton(c, icRefresh, "更新").Disabled(a.SubBusy[id]).Tooltip("重新获取订阅中的节点").Clicked() {
				a.UpdateSubscription(id)
			}
		}
		if toolButton(c, icPencil, "编辑分组", p.text).Clicked() {
			a.openGroupDialog(id)
		}
		if toolButton(c, icTrash, "删除分组及其节点", p.danger).Clicked() {
			a.confirmDeleteGroup(id)
		}
	})
}

func (a *View) groupStatus(g *store.Group) (string, ui.Color) {
	p := a.pal
	if !g.Subscription() {
		return "手动添加的节点", p.muted
	}
	// The URL is left out: long ones hid what follows. The group dialog shows it.
	status := "尚未更新"
	if !g.UpdatedAt.IsZero() {
		status = "更新于 " + ago(time.Since(g.UpdatedAt))
	}
	if g.LastError != "" {
		return "更新失败：" + g.LastError, p.danger
	}
	if g.Skipped > 0 {
		return fmt.Sprintf("%s · %d 个节点无法识别，已跳过（详见日志）", status, g.Skipped), p.warning
	}
	return status, p.muted
}

// chainChips shows the group's front and back proxies.
func (a *View) chainChips(c *ui.Context, g *store.Group) {
	p := a.pal
	for _, x := range []struct{ id, what string }{{g.Front, "前置"}, {g.Back, "后置"}} {
		if x.id == "" {
			continue
		}
		if n := a.NodeByID(x.id); n != nil {
			a.chip(c, x.what+" · "+n.Label(), p.accent).MaxWidth(180).Tooltip(x.what + "代理：" + n.Label() + "（" + n.Address() + "）")
		} else {
			a.chip(c, x.what+"节点已不存在", p.danger).Tooltip("请编辑分组重新选择")
		}
	}
}

// proxyOptions lists the nodes that can be a proxy of group id (those of other groups) as labels with
// their group, and the matching node IDs. The first option is none.
func (a *View) proxyOptions(id string) (labels, ids []string) {
	labels, ids = []string{noProxy}, []string{""}
	for _, n := range a.State.Nodes {
		if n.GroupID == id {
			continue
		}
		label := n.Label() + " · " + a.groupName(n.GroupID)
		for k := 2; slices.Contains(labels, label); k++ {
			label = fmt.Sprintf("%s · %s (%d)", n.Label(), a.groupName(n.GroupID), k)
		}
		labels, ids = append(labels, label), append(ids, n.ID)
	}
	return labels, ids
}

func (a *View) openGroupDialog(id string) {
	f := groupForm{open: true, id: id}
	if g := a.GroupByID(id); g != nil {
		f.name, f.url = g.Name, g.URL
		if g.Subscription() {
			f.kind = 1
		}
		f.front, f.back = g.Front, g.Back
		if missing := a.missingProxies(&f); len(missing) > 0 {
			f.err = "原" + strings.Join(missing, "、") + "代理节点已不存在，请重新选择"
		}
	}
	a.gf = f
}

// missingProxies names the proxies chosen in f whose nodes are gone (or may not be a proxy of the group).
func (a *View) missingProxies(f *groupForm) []string {
	_, ids := a.proxyOptions(f.id)
	var missing []string
	for _, x := range []struct{ id, what string }{{f.front, "前置"}, {f.back, "后置"}} {
		if !slices.Contains(ids, x.id) {
			missing = append(missing, x.what)
		}
	}
	return missing
}

// proxyChoice chooses a proxy of the dialog's group by node ID. A chosen node gone is listed as such.
func (a *View) proxyChoice(c *ui.Context, key, what string, value *string, labels, ids []string) {
	if !slices.Contains(ids, *value) {
		ids, labels = append(slices.Clone(ids), *value), append(slices.Clone(labels), what+"节点已不存在")
	}
	a.choice(c, key, what+"代理", value, ids, labels)
}

func (a *View) groupDialog(c *ui.Context) {
	p := a.pal
	f := &a.gf
	title, action := "编辑分组", "保存"
	if f.id == "" {
		title, action = "添加分组", "添加"
		if f.kind == 1 {
			action = "添加并更新"
		}
	}
	var submitted bool
	labels, ids := a.proxyOptions(f.id)
	body := func() {
		if f.id == "" {
			segmented(c.Key("group-kind"), &f.kind, "普通分组", "订阅分组").Label("分组类型")
			hint := "手动添加的节点放在普通分组里。"
			if f.kind == 1 {
				hint = "从订阅地址导入节点，每次更新替换本组全部节点。"
			}
			ui.Text(c, hint).FontSize(12.5).TextColor(p.muted)
		}
		a.labeled(c, "名称", func() {
			ph := "例如 自建"
			if f.kind == 1 {
				ph = "可留空，默认使用域名"
			}
			name := ui.TextInput(c.Key("group-name"), &f.name).Height(controlHeight).Placeholder(ph).Label("分组名称").AutoFocus()
			submitted = a.textMenu(c, name, "group-name", &f.name, false, nil).Submitted()
		})
		if f.kind == 1 {
			a.labeled(c, "订阅地址", func() {
				url := ui.TextInput(c.Key("group-url"), &f.url).Height(controlHeight).Placeholder("https://").Label("订阅地址")
				if a.textMenu(c, url, "group-url", &f.url, false, nil).Submitted() {
					submitted = true
				}
			})
		}
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "代理链").FontSize(13).Bold()
			ui.Text(c, "本组节点的流量依次经过：前置代理 → 本组节点 → 后置代理 → 目标网站。"+
				"前置代理用于中转，后置代理用于固定出口。").
				FontSize(12).TextColor(p.muted).LineHeight(1.5)
		})
		a.labeled(c, "前置代理", func() { a.proxyChoice(c, "group-front", "前置", &f.front, labels, ids) })
		a.labeled(c, "后置代理", func() { a.proxyChoice(c, "group-back", "后置", &f.back, labels, ids) })
	}
	save := func() {
		if missing := a.missingProxies(f); len(missing) > 0 {
			f.err = strings.Join(missing, "、") + "代理节点已不存在，请重新选择，或明确选择“不使用”"
			return
		}
		in := app.GroupInput{Name: f.name, URL: f.url, Subscription: f.kind == 1, Front: f.front, Back: f.back}
		if f.id == "" {
			id, err := a.AddGroup(in)
			if err != nil {
				f.err = err.Error()
				return
			}
			a.group = id
		} else {
			if err := a.EditGroup(f.id, in); err != nil {
				f.err = err.Error()
				return
			}
			a.savedToast(c, "分组 "+a.GroupByID(f.id).Name)
		}
		f.open = false
	}
	a.dialog(c, &f.open, dialog{width: 500, title: title, err: &f.err, body: body, actions: func() {
		if primaryButton(c, action).Clicked() || submitted {
			save()
		}
	}})
}

// labeled stacks a field under its label.
func (a *View) labeled(c *ui.Context, label string, field func()) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, label).FontSize(12.5).TextColor(a.pal.muted)
		field()
	})
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(d.Hours()/24))
}
