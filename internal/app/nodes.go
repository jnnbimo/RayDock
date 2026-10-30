package app

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"raydock/internal/node"
	"raydock/internal/probe"
	"raydock/internal/store"
	"raydock/internal/xray"
)

// ImportResult is what ImportLinks did.
type ImportResult struct {
	Added     int
	Duplicate int     // already in the group
	Invalid   []error // why entries were not recognized
	Err       error   // why nothing could go into the group
}

// String sums the result up.
func (r ImportResult) String() string {
	if r.Err != nil {
		return r.Err.Error()
	}
	msg := fmt.Sprintf("导入 %d 个节点", r.Added)
	if r.Duplicate > 0 {
		msg += fmt.Sprintf("，%d 个已存在", r.Duplicate)
	}
	if len(r.Invalid) > 0 {
		msg += fmt.Sprintf("，%d 条无法识别", len(r.Invalid))
	}
	return msg
}

// Problem says why nothing was imported, for correcting the input; "" when something was.
func (r ImportResult) Problem() string {
	switch {
	case r.Err != nil:
		return r.Err.Error()
	case r.Added > 0:
		return ""
	case len(r.Invalid) > 0:
		msg := "没有可导入的节点：" + r.Invalid[0].Error()
		if len(r.Invalid) > 1 {
			msg += fmt.Sprintf("（共 %d 条无法识别，详见日志）", len(r.Invalid))
		}
		return msg
	case r.Duplicate > 0:
		return "这些节点在该分组中都已存在"
	}
	return "没有找到节点链接"
}

// ImportLinks imports share links or Xray JSON into a normal group. With no group ID they go to the
// first normal group, created as store.DefaultGroup when there is none.
func (a *App) ImportLinks(text, groupID string) ImportResult {
	nodes, errs := node.ParseMany(text)
	r := ImportResult{Invalid: errs}
	for _, e := range errs {
		a.QueueLog("[RayDock] 导入失败：" + e.Error())
	}
	if len(nodes) > 0 {
		if groupID == "" {
			groupID = a.firstNormalGroup()
		}
		if g := a.GroupByID(groupID); g == nil {
			r.Err = errors.New("分组不存在")
			return r
		} else if g.Subscription() {
			r.Err = errors.New("订阅分组的节点随订阅更新，不能手动添加")
			return r
		}
	}
	have := map[node.Node]bool{}
	for i := range a.State.Nodes {
		if m := &a.State.Nodes[i]; m.GroupID == groupID {
			have[node.ConfigKey(m)] = true
		}
	}
	var added []string
	for _, n := range nodes {
		key := node.ConfigKey(&n)
		if have[key] {
			continue
		}
		have[key] = true
		n.GroupID = groupID
		a.State.Nodes = append(a.State.Nodes, n)
		added = append(added, n.ID)
	}
	if len(added) > 0 {
		a.Save()
		a.PinCertificates(added...)
	}
	r.Added, r.Duplicate = len(added), len(nodes)-len(added)
	return r
}

// AddNode adds a node filled in by hand to a normal group, as importing its share link would: with no
// group ID to the first normal group, created when there is none. It returns the node's ID.
func (a *App) AddNode(groupID string, n node.Node) (string, error) {
	n, err := checkNode(n)
	if err != nil {
		return "", err
	}
	if groupID == "" {
		groupID = a.firstNormalGroup()
	}
	if g := a.GroupByID(groupID); g == nil {
		return "", errors.New("分组不存在")
	} else if g.Subscription() {
		return "", errors.New("订阅分组的节点随订阅更新，不能手动添加")
	}
	if slices.ContainsFunc(a.State.Nodes, func(m node.Node) bool { return m.GroupID == groupID && node.SameConfig(&m, &n) }) {
		return "", errors.New("该分组中已有相同配置的节点")
	}
	n.ID, n.GroupID = node.NewID(), groupID
	a.State.Nodes = append(a.State.Nodes, n)
	a.Save()
	a.PinCertificates(n.ID)
	return n.ID, nil
}

// EditNode replaces the settings and name of node n.ID with n's, checked as AddNode does. Results
// measured with the old settings are dropped, and a connection or dedicated port using the node picks the
// new ones up at once: the core using it restarts with them (SyncCores).
func (a *App) EditNode(n node.Node) error {
	old := a.NodeByID(n.ID)
	if old == nil {
		return errors.New("节点不存在")
	}
	n.GroupID = old.GroupID
	n, err := checkNode(n)
	if err != nil {
		return err
	}
	if node.SameConfig(old, &n) && old.Name == n.Name {
		return nil
	}
	if !node.SameConfig(old, &n) {
		a.nodeChanged(n.ID)
	}
	if node.SameConfig(old, &n) {
		n.AutoPin = old.AutoPin
	}
	*old = n
	a.Save()
	a.SyncCores()
	a.PinCertificates(n.ID)
	return nil
}

// checkNode normalizes n as an import would and checks that Xray can use it.
func checkNode(n node.Node) (node.Node, error) {
	n, err := node.Normalize(n)
	if err != nil {
		return node.Node{}, err
	}
	return n, xray.Check(&n)
}

func (a *App) DeleteNode(id string) {
	if id == a.ActiveID {
		a.Disconnect()
	}
	a.State.Nodes = slices.DeleteFunc(a.State.Nodes, func(n node.Node) bool { return n.ID == id })
	a.forgetNodes(id)
	a.Save()
	a.SyncCores()
}

// TestReal requests a page through the local proxy to check the whole path. A result measured through
// cores that no longer serve the connection is dropped, and the test ends without one.
func (a *App) TestReal() {
	target := a.connCore()
	if target == nil {
		return
	}
	a.realTests++
	a.RealTest = Latency{Testing: true}
	test, port, ctx := a.realTests, target.opts.Port, a.connCtx
	go func() {
		d, err := probe.ViaProxy(ctx, port, probe.TestURL, 10*time.Second)
		a.Post(func() {
			if test != a.realTests {
				return // a later test owns the result shown
			}
			if a.connCore() != target {
				a.RealTest = Latency{}
				return
			}
			a.RealTest = Latency{Duration: d}
			if err != nil {
				a.RealTest.Err = shortErr(err)
			}
		})
	}()
}

func (a *App) firstNormalGroup() string {
	for _, g := range a.State.Groups {
		if !g.Subscription() {
			return g.ID
		}
	}
	g := store.Group{ID: node.NewID(), Name: store.DefaultGroup}
	a.State.Groups = append(a.State.Groups, g)
	return g.ID
}
