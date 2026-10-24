package gui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// confirmation is an action asked about before it runs, as a deletion, which cannot be undone.
type confirmation struct {
	open   bool
	title  string
	body   []string // paragraphs
	action string   // label of the button that runs it
	run    func(c *ui.Context)
}

// confirm asks before running run; the dialog opens on the next frame.
func (a *View) confirm(title, action string, run func(c *ui.Context), body ...string) {
	a.cf = confirmation{open: true, title: title, body: body, action: action, run: run}
}

func (a *View) confirmDialog(c *ui.Context) {
	p, f := a.pal, &a.cf
	a.dialog(c, &f.open, dialog{width: 420, title: f.title, icon: icTrash, tone: p.danger, focusCancel: true,
		body: func() {
			for _, s := range f.body {
				ui.Text(c, s).FontSize(13).TextColor(p.muted).LineHeight(1.5)
			}
		},
		actions: func() {
			// Run after the build: the lists on the page are built from what it deletes.
			primaryButton(c, f.action).TextColor(p.on(p.danger)).OnClick(func() {
				run := f.run
				*f = confirmation{}
				if run != nil {
					run(c)
				}
			})
		},
	})
}

// confirmDeleteNode asks before deleting node id, telling what goes with it.
func (a *View) confirmDeleteNode(id string) {
	n := a.NodeByID(id)
	if n == nil {
		return
	}
	name := n.Label()
	body := []string{fmt.Sprintf("确定删除节点“%s”吗？删除后无法恢复。", name)}
	if id == a.ActiveID {
		body = append(body, "这是正在使用的节点，删除会先断开连接。")
	}
	if port, ok := a.State.Ports[id]; ok {
		body = append(body, fmt.Sprintf("它的专用端口 %d 也会一并移除。", port))
	}
	if uses := a.chainUsers(id); len(uses) > 0 {
		body = append(body, "分组“"+strings.Join(uses, "”、“")+"”把它用作前置或后置代理，删除后这些分组的节点将无法连接，需要重新选择。")
	}
	if g := a.GroupByID(n.GroupID); g != nil && g.Subscription() {
		body = append(body, "它来自订阅，下次更新订阅时会重新出现。")
	}
	a.confirm("删除节点", "删除", func(c *ui.Context) {
		a.DeleteNode(id)
		c.Toast("已删除 " + name)
	}, body...)
}

// confirmDeleteGroup asks before deleting group id and its nodes.
func (a *View) confirmDeleteGroup(id string) {
	g := a.GroupByID(id)
	if g == nil {
		return
	}
	name, count, active, ports := g.Name, 0, false, 0
	var hops []string // the group's nodes other groups use as proxies
	for _, n := range a.State.Nodes {
		if n.GroupID != id {
			continue
		}
		count++
		active = active || n.ID == a.ActiveID
		if _, ok := a.State.Ports[n.ID]; ok {
			ports++
		}
		for _, u := range a.chainUsers(n.ID) {
			if u != name && !slices.Contains(hops, u) {
				hops = append(hops, u)
			}
		}
	}
	body := []string{fmt.Sprintf("确定删除分组“%s”及其中的 %d 个节点吗？删除后无法恢复。", name, count)}
	if g.Subscription() {
		body[0] = fmt.Sprintf("确定删除订阅分组“%s”及其中的 %d 个节点吗？订阅地址也会一并删除，删除后无法恢复。", name, count)
	}
	if active {
		body = append(body, "正在使用的节点在这个分组里，删除会先断开连接。")
	}
	if ports > 0 {
		body = append(body, fmt.Sprintf("其中 %d 个节点的专用端口也会一并移除。", ports))
	}
	if len(hops) > 0 {
		body = append(body, "分组“"+strings.Join(hops, "”、“")+"”把其中的节点用作前置或后置代理，删除后这些分组的节点将无法连接，需要重新选择。")
	}
	a.confirm("删除分组", "删除", func(c *ui.Context) {
		a.DeleteGroup(id)
		a.group = ""
		c.Toast("已删除分组 " + name)
	}, body...)
}

// chainUsers names the groups using node id as their front or back proxy.
func (a *View) chainUsers(id string) []string {
	var names []string
	for _, g := range a.State.Groups {
		if g.Front == id || g.Back == id {
			names = append(names, g.Name)
		}
	}
	return names
}
