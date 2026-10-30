package app

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	"raydock/internal/node"
	"raydock/internal/system"
	"raydock/internal/xray"
)

// SetNodePort assigns a persistent proxy route, even when disconnected. Zero removes it.
func (a *App) SetNodePort(id string, port int) error {
	if a.NodeByID(id) == nil {
		return errors.New("节点不存在")
	}
	if port != 0 {
		if port < 1024 || port > 65535 {
			return errors.New("端口应在 1024–65535 之间")
		}
		if port == a.State.Settings.Port {
			return fmt.Errorf("端口 %d 已用作本地代理端口", port)
		}
		for other, p := range a.State.Ports {
			if p == port && other != id {
				name := other
				if n := a.NodeByID(other); n != nil {
					name = n.Label()
				}
				return fmt.Errorf("端口 %d 已分配给 %s", port, name)
			}
		}
	}
	if port == 0 {
		delete(a.State.Ports, id)
	} else {
		if a.State.Ports == nil {
			a.State.Ports = map[string]int{}
		}
		a.State.Ports[id] = port
	}
	a.Save()
	a.SyncCores()
	return nil
}

// SyncCores restarts the core if its nodes, chains, dedicated ports or network changed, with the settings
// it runs; a core serving them already, running or starting, goes on. Without a connection the dedicated
// ports follow the settings at once.
func (a *App) SyncCores() {
	s := a.State.Settings
	a.portBasis = portSettings{s.AllowLAN, s.Port, s.LogLevel}
	if a.closing {
		return
	}
	switch {
	case a.Conn == StateOff:
		a.syncIdle(change{})
	case !a.WaitCore && a.connPlan() != nil:
		a.connect(a.ActiveID, a.connOpts(), nil, change{})
	}
}

// syncIdle serves the dedicated ports without a connection: the core restarts serving them alone, or
// stops. While the core files are checked or replaced nothing new starts: a core serving the ports alone
// goes on. UI thread only.
func (a *App) syncIdle(ch change) {
	if a.closing {
		return
	}
	cur := a.planned.core()
	var t *coreTarget
	if a.coreHold || !a.CoreReady() {
		t = &coreTarget{}
		if cur != nil && !cur.exited && cur.run.Chain == nil {
			t = a.planned
		}
	} else {
		var err error
		if t, err = a.planTarget(coreSpec{opts: a.xrayOptions()}, ch); err != nil {
			a.markPorts(err.Error())
			t.proc = nil
		}
	}
	if t == a.planned || t.proc == cur && (a.planned == nil || a.planned.id == "") {
		return
	}
	a.startCore(t, func(error) {})
}

// holdPorts reserves the dedicated ports of t's core so portTaken recognizes them as ours.
func (a *App) holdPorts(t *coreTarget) {
	r := t.core()
	if r == nil || r.exited {
		return
	}
	if a.ownPorts == nil {
		a.ownPorts = map[int]bool{}
	}
	for _, p := range r.run.Ports {
		a.ownPorts[p.Port] = true
	}
}

// heldPorts combines live and planned port ownership.
func (a *App) heldPorts(live *coreTarget) {
	clear(a.ownPorts)
	a.holdPorts(live)
	a.holdPorts(a.planned)
}

// portTaken excludes foreign listeners so one occupied port cannot fail the whole core.
func (a *App) portTaken(port int, lan ...bool) bool {
	if a.ownPorts[port] {
		if r := a.live.Load(); r != nil && !r.exited && r.core != nil && r.core.Running() {
			owned, err := system.ListenerOwned(r.core.PID(), port)
			if err != nil {
				a.QueueLog("[RayDock] 无法确认端口所有者：" + err.Error())
				return true
			}
			if owned {
				return false
			}
		}
	}
	host := "127.0.0.1"
	allow := a.State.Settings.AllowLAN
	if len(lan) > 0 {
		allow = lan[0]
	}
	if allow {
		host = "0.0.0.0"
	}
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return true
	}
	l.Close()
	return false
}

// portRoutes returns the dedicated ports that can run beside the local port local, and records in PortErrs
// why the others cannot.
func (a *App) portRoutes(local int, lan ...bool) []xray.PortRoute {
	allow := a.State.Settings.AllowLAN
	if len(lan) > 0 {
		allow = lan[0]
	}
	// A validation/reservation cannot outlive its configured port.
	for port := range a.portChecks {
		used := false
		for _, p := range a.State.Ports {
			used = used || p == port
		}
		if !used {
			delete(a.portChecks, port)
		}
	}
	// Ports of nodes gone with a subscription update are released.
	released := false
	for id := range a.State.Ports {
		if a.NodeByID(id) == nil {
			delete(a.State.Ports, id)
			released = true
		}
	}
	if released {
		a.Save()
	}
	clear(a.PortErrs)
	var routes []xray.PortRoute
	ids := slices.SortedFunc(maps.Keys(a.State.Ports), func(x, y string) int { return a.State.Ports[x] - a.State.Ports[y] })
	for _, id := range ids {
		port := a.State.Ports[id]
		if port == local {
			a.PortErrs[id] = "与本地代理端口冲突"
			continue
		}
		if a.portTaken(port, allow) {
			a.PortErrs[id] = "端口已被其他程序占用"
			continue
		}
		chain, err := a.Chain(a.NodeByID(id))
		if err == nil {
			check, buildErr := a.portCheckFor(xray.PortRoute{Port: port, Chain: chain}, allow)
			err = buildErr
			if cached, ok := a.portChecks[port]; err == nil && ok && cached.key == check.key && cached.err != "" {
				err = errors.New(cached.err)
			}
		}
		if err != nil {
			a.PortErrs[id] = err.Error()
			continue
		}
		routes = append(routes, xray.PortRoute{Port: port, Chain: chain})
	}
	return routes
}

// describePorts names the owning node, not the final node in its proxy chain.
func (a *App) describePorts(routes []xray.PortRoute) string {
	served := make([]string, 0, len(routes))
	for _, r := range routes {
		name := "?"
		for id, p := range a.State.Ports {
			if n := a.NodeByID(id); p == r.Port && n != nil {
				name = n.Label()
			}
		}
		served = append(served, fmt.Sprintf("%d → %s", r.Port, name))
	}
	return strings.Join(served, "，")
}

func (a *App) markPorts(reason string) {
	for id := range a.State.Ports {
		if a.PortErrs[id] == "" {
			a.PortErrs[id] = reason
		}
	}
}

// PortNodes lists the nodes that have a dedicated port, by port.
func (a *App) PortNodes() []*node.Node {
	var out []*node.Node
	for id := range a.State.Ports {
		if n := a.NodeByID(id); n != nil {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(x, y *node.Node) int { return a.State.Ports[x.ID] - a.State.Ports[y.ID] })
	return out
}
