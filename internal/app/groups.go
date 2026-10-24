package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"raydock/internal/node"
	"raydock/internal/store"
)

// GroupInput is what the user enters for a group. A subscription group has a URL.
type GroupInput struct {
	Name         string
	URL          string
	Subscription bool
	Front, Back  string // node IDs, empty for none
}

func (a *App) GroupByID(id string) *store.Group {
	for i := range a.State.Groups {
		if a.State.Groups[i].ID == id {
			return &a.State.Groups[i]
		}
	}
	return nil
}

// AddGroup adds a group; a subscription is fetched at once.
func (a *App) AddGroup(in GroupInput) (string, error) {
	g := store.Group{ID: node.NewID()}
	if err := a.applyGroup(&g, in); err != nil {
		return "", err
	}
	a.State.Groups = append(a.State.Groups, g)
	a.Save()
	if g.Subscription() {
		a.UpdateSubscription(g.ID)
	}
	return g.ID, nil
}

// EditGroup changes a group's name, subscription URL and proxies; its kind stays.
func (a *App) EditGroup(id string, in GroupInput) error {
	g := a.GroupByID(id)
	if g == nil {
		return errors.New("分组不存在")
	}
	in.Subscription = g.Subscription()
	edited := *g
	if err := a.applyGroup(&edited, in); err != nil {
		return err
	}
	refetch := edited.URL != g.URL
	if edited.Front != g.Front || edited.Back != g.Back {
		a.chainChanged(id)
	}
	*g = edited
	a.Save()
	a.SyncCores()
	if refetch {
		a.cancelSubscription(id)
		a.UpdateSubscription(id)
	}
	return nil
}

// applyGroup validates in and copies it to g.
func (a *App) applyGroup(g *store.Group, in GroupInput) error {
	name := strings.TrimSpace(in.Name)
	g.URL = ""
	if in.Subscription {
		raw := strings.TrimSpace(in.URL)
		u, err := url.Parse(raw)
		if raw == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("请输入 http:// 或 https:// 开头的订阅地址")
		}
		g.URL = u.String()
		name = cmp.Or(name, u.Host)
	}
	if name == "" {
		return errors.New("请输入分组名称")
	}
	for _, p := range []struct{ id, what string }{{in.Front, "前置代理"}, {in.Back, "后置代理"}} {
		if p.id == "" {
			continue
		}
		n := a.NodeByID(p.id)
		if n == nil {
			return fmt.Errorf("%s节点不存在", p.what)
		}
		if n.GroupID == g.ID {
			return fmt.Errorf("%s不能是本分组的节点", p.what)
		}
	}
	g.Name, g.Front, g.Back = name, in.Front, in.Back
	return nil
}

// DeleteGroup removes a group with its nodes.
func (a *App) DeleteGroup(id string) {
	a.cancelSubscription(id)
	if n := a.NodeByID(a.ActiveID); n != nil && n.GroupID == id {
		a.Disconnect()
	}
	var gone []string
	for _, n := range a.State.Nodes {
		if n.GroupID == id {
			gone = append(gone, n.ID)
		}
	}
	a.State.Groups = slices.DeleteFunc(a.State.Groups, func(g store.Group) bool { return g.ID == id })
	a.State.Nodes = slices.DeleteFunc(a.State.Nodes, func(n node.Node) bool { return n.GroupID == id })
	a.forgetNodes(gone...)
	a.Save()
	a.SyncCores()
}

// forgetNodes drops results and ports, but preserves invalid proxy references.
// A required hop disappearing must fail closed, never silently shorten the chain.
func (a *App) forgetNodes(ids ...string) {
	for _, id := range ids {
		delete(a.Latency, id)
		delete(a.Speed, id)
		delete(a.State.Ports, id)
		for i := range a.State.Groups {
			g := &a.State.Groups[i]
			if g.Front == id || g.Back == id {
				a.chainChanged(g.ID)
				if n := a.NodeByID(a.ActiveID); n != nil && n.GroupID == g.ID {
					a.Disconnect()
					a.ConnErr = "代理链节点已不存在，请重新选择"
				}
			}
		}
	}
}

// chainChanged drops the results measured through the old proxies of group id. A connection through it
// takes the new ones once the change is made (refreshNode).
func (a *App) chainChanged(id string) {
	for _, n := range a.State.Nodes {
		if n.GroupID == id {
			delete(a.Latency, n.ID)
			delete(a.Speed, n.ID)
		}
	}
}

// Chain returns the nodes traffic to n passes: its group's front proxy, n, then the group's back proxy.
func (a *App) Chain(n *node.Node) ([]*node.Node, error) {
	if n == nil {
		return nil, errors.New("节点不存在")
	}
	chain := []*node.Node{n}
	g := a.GroupByID(n.GroupID)
	if g == nil {
		return chain, nil
	}
	if g.Front != "" {
		f := a.NodeByID(g.Front)
		if f == nil {
			return nil, fmt.Errorf("分组「%s」的前置代理节点已不存在，请重新选择", g.Name)
		}
		chain = append([]*node.Node{f}, chain...)
	}
	if g.Back != "" {
		b := a.NodeByID(g.Back)
		if b == nil {
			return nil, fmt.Errorf("分组「%s」的后置代理节点已不存在，请重新选择", g.Name)
		}
		chain = append(chain, b)
	}
	return chain, nil
}

type subscriptionJob struct{ cancel context.CancelFunc }

func (a *App) cancelSubscription(id string) {
	if job := a.subJobs[id]; job != nil {
		job.cancel()
	}
	delete(a.subJobs, id)
	delete(a.SubBusy, id)
}

func (a *App) UpdateSubscription(id string) {
	g := a.GroupByID(id)
	if a.closing || g == nil || !g.Subscription() || a.SubBusy[id] {
		return
	}
	a.SubBusy[id] = true
	ctx, cancel := context.WithCancel(a.workCtx)
	job := &subscriptionJob{cancel: cancel}
	a.subJobs[id] = job
	target, client := g.URL, a.httpClient(30*time.Second)
	go func() {
		defer cancel()
		defer client.CloseIdleConnections()
		nodes, errs, err := fetchSubscription(ctx, client, target)
		a.Post(func() {
			g := a.GroupByID(id)
			if a.subJobs[id] != job || g == nil || g.URL != target {
				return
			}
			delete(a.SubBusy, id)
			delete(a.subJobs, id)
			for _, e := range errs {
				a.QueueLog(fmt.Sprintf("[RayDock] 订阅「%s」跳过：%v", g.Name, e))
			}
			if err != nil {
				g.LastError = err.Error()
				a.Save()
				return
			}
			// Entries that cannot be used are skipped; the update succeeds with the rest.
			a.replaceGroupNodes(id, nodes)
			a.PinCertificates()
			g.Count, g.Skipped, g.UpdatedAt, g.LastError = len(nodes), len(errs), time.Now(), ""
			a.Save()
			a.SyncCores()
		})
	}()
}

// replaceGroupNodes swaps a subscription's nodes, keeping the IDs of unchanged servers so selection,
// connection and proxies set to them survive.
func (a *App) replaceGroupNodes(groupID string, fresh []node.Node) {
	var old []node.Node
	kept := a.State.Nodes[:0:0]
	for _, n := range a.State.Nodes {
		if n.GroupID == groupID {
			old = append(old, n)
		} else {
			kept = append(kept, n)
		}
	}
	// Unchanged servers keep their place in the order the user arranged; new ones follow. Each old node
	// passes its ID on once, so a server listed twice does not give two nodes one ID.
	pos := make([]int, len(fresh))
	taken, matched := make([]bool, len(old)), make([]bool, len(fresh))
	for i := range fresh {
		fresh[i].GroupID = groupID
		pos[i] = len(old) + i
	}
	// Match exact configs first across the whole list: variants of one endpoint
	// must not exchange their IDs/ports just because the subscription reordered them.
	for pass := range 2 {
		for i := range fresh {
			if matched[i] {
				continue
			}
			for j := range old {
				if taken[j] {
					continue
				}
				sameConfig := node.SameConfig(&old[j], &fresh[i])
				if (pass == 0 && sameConfig) || (pass == 1 && node.SameEndpoint(&old[j], &fresh[i])) {
					taken[j], matched[i] = true, true
					fresh[i].ID, pos[i] = old[j].ID, j
					if sameConfig {
						fresh[i].AutoPin = old[j].AutoPin // the server is the same
					} else {
						a.nodeChanged(old[j].ID)
					}
					break
				}
			}
		}
	}
	order := make([]int, len(fresh))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(x, y int) int { return pos[x] - pos[y] })
	for _, i := range order {
		kept = append(kept, fresh[i])
	}
	var gone []string
	for j, n := range old {
		if !taken[j] {
			if a.ActiveID == n.ID {
				a.Disconnect()
			}
			gone = append(gone, n.ID)
		}
	}
	a.State.Nodes = kept
	a.forgetNodes(gone...)
}

// nodeChanged drops the results measured with a server's old settings, including those of the groups it
// is a front or back proxy of. A connection through it takes the new ones once the change is made
// (SyncCores).
func (a *App) nodeChanged(id string) {
	delete(a.Latency, id)
	delete(a.Speed, id)
	for _, g := range a.State.Groups {
		if g.Front == id || g.Back == id {
			a.chainChanged(g.ID)
		}
	}
}

// MoveGroup moves group id before group beforeID, or to the end when beforeID is "".
func (a *App) MoveGroup(id, beforeID string) {
	i := slices.IndexFunc(a.State.Groups, func(g store.Group) bool { return g.ID == id })
	if i < 0 || id == beforeID {
		return
	}
	g := a.State.Groups[i]
	a.State.Groups = slices.Delete(a.State.Groups, i, i+1)
	to := slices.IndexFunc(a.State.Groups, func(g store.Group) bool { return g.ID == beforeID })
	if to < 0 {
		to = len(a.State.Groups)
	}
	a.State.Groups = slices.Insert(a.State.Groups, to, g)
	a.Save()
}

// MoveNodes moves nodes ids, all of one group, in the order given, before node beforeID of that group,
// or to the group's end when beforeID is "". Nodes of other groups keep their places.
func (a *App) MoveNodes(ids []string, beforeID string) {
	if len(ids) == 0 || slices.Contains(ids, beforeID) {
		return
	}
	first := a.NodeByID(ids[0])
	if first == nil {
		return
	}
	group := first.GroupID
	var slots []int
	var moved, rest []node.Node
	for i, n := range a.State.Nodes {
		if n.GroupID != group {
			continue
		}
		slots = append(slots, i)
		if !slices.Contains(ids, n.ID) {
			rest = append(rest, n)
		}
	}
	for _, id := range ids {
		if n := a.NodeByID(id); n != nil && n.GroupID == group {
			moved = append(moved, *n)
		}
	}
	to := slices.IndexFunc(rest, func(n node.Node) bool { return n.ID == beforeID })
	if to < 0 {
		to = len(rest)
	}
	order := slices.Concat(rest[:to], moved, rest[to:])
	for k, i := range slots {
		a.State.Nodes[i] = order[k]
	}
	a.Save()
}

// httpClient returns the client for subscriptions and core downloads: through RayDock when connected,
// otherwise through the system proxy, else direct. A zero timeout leaves it to the context.
func (a *App) httpClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	if u := a.upstreamProxy(); u != nil {
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func (a *App) upstreamProxy() *url.URL {
	if a.Conn == StateOn {
		return &url.URL{Scheme: "http", Host: a.ProxyAddr()}
	}
	if a.proxyActive() {
		return nil // the system proxy is RayDock's own but the core is not running
	}
	cur, err := a.Proxy.Current()
	// Only host:port is supported; per-protocol settings (http=…;https=…) are skipped.
	if err != nil || !cur.Enabled || cur.Server == "" || strings.ContainsAny(cur.Server, "=; ") {
		return nil
	}
	return &url.URL{Scheme: "http", Host: cur.Server}
}

// maxSubscription is the size of the largest subscription read.
const maxSubscription = 16 << 20

func fetchSubscription(ctx context.Context, client *http.Client, target string) ([]node.Node, []error, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	// Panels pick the format by User-Agent; v2rayN gets a Base64 link list.
	req.Header.Set("User-Agent", "v2rayN/7.0 RayDock")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, errors.New(shortErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// One byte past the limit tells a subscription cut there from one that ends there: a part of the nodes
	// would replace the group's.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscription+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxSubscription {
		return nil, nil, fmt.Errorf("订阅内容超过 %d MB", maxSubscription>>20)
	}
	text, err := node.DecodeSubscription(body)
	if err != nil {
		return nil, nil, err
	}
	nodes, errs := node.ParseMany(text)
	if len(nodes) == 0 {
		return nil, errs, errors.New("订阅中没有可用的节点")
	}
	return nodes, errs, nil
}
