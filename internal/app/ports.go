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

// SyncCores reconciles node, chain, port and network changes with the running pair.
// In-flight starts restart only when their inputs changed; identical targets do nothing.
func (a *App) SyncCores() {
	a.portBasis = portSettings{a.State.Settings.AllowLAN, a.State.Settings.Port}
	if a.closing {
		return
	}
	switch {
	case a.Conn == StateOff:
		a.syncIdle(change{})
	case a.WaitCore:
	case a.run != nil && a.run.b.Connected():
		a.connect(a.ActiveID, a.run.opts, nil, change{})
	case a.run == nil && a.planned != nil && a.startStale(a.planned):
		a.Connect(a.ActiveID, nil)
	}
}

// startStale reports whether start t no longer starts the connection's chain and the dedicated ports.
func (a *App) startStale(t *coreTarget) bool {
	n := a.NodeByID(a.ActiveID)
	if n == nil {
		return true
	}
	chain, err := a.Chain(n)
	if err != nil {
		return true
	}
	b, err := a.backendFor(chain, t.run.opts)
	return err != nil || !sameJSON(b.Chain, t.run.b.Chain) || !sameJSON(b.Ports, t.run.b.Ports)
}

func (a *App) idleOptions() xray.Options {
	o := a.xrayOptions()
	o.TUN.Enabled = false
	return o
}

// syncIdle serves dedicated ports without a connection. New cores wait for file installation;
// existing dedicated routes keep running while the files are replaced. UI thread only.
func (a *App) syncIdle(ch change) {
	routes := a.portRoutes()
	if len(routes) == 0 || a.closing {
		if a.planned != nil {
			a.stopPair()
		}
		return
	}
	if a.coreHold || !a.CoreReady() {
		if t := a.planned; t != nil && t.run.b.Connected() {
			a.stopPair()
		}
		return
	}
	if a.run != nil && a.frontRun != nil && a.apply(nil, nil, a.idleOptions(), nil, ch) {
		return
	}
	if t := a.planned; t != nil && a.run == nil && !t.run.b.Connected() && sameJSON(t.run.b.Ports, routes) {
		return // starting them already
	}
	b, err := a.backendFor(nil, a.idleOptions())
	if err != nil {
		a.markPorts(err.Error())
		return
	}
	run, err := newBackend(a.idleOptions(), b)
	if err != nil {
		a.markPorts(err.Error())
		return
	}
	front, err := a.newFront(run)
	if err != nil {
		a.markPorts(err.Error())
		return
	}
	t := &coreTarget{run: run, front: front, served: a.describePorts(routes), drain: true}
	a.startPair(t, func(err error) {
		if err != nil {
			a.QueueLog("[RayDock] 专用端口启动失败：" + err.Error())
			a.markPorts("启动失败，详见日志")
		}
	})
}

// holdPorts reserves planned front-core ports so portTaken recognizes them as ours.
func (a *App) holdPorts(t *coreTarget) {
	if t == nil {
		return
	}
	if a.frontPorts == nil {
		a.frontPorts = map[int]bool{}
	}
	for _, r := range t.front.f.Backend.Ports {
		a.frontPorts[r.Port] = true
	}
}

// heldPorts combines live and planned port ownership.
func (a *App) heldPorts(live *coreTarget) {
	clear(a.frontPorts)
	a.holdPorts(live)
	a.holdPorts(a.planned)
}

// portTaken excludes foreign listeners so one occupied port cannot break the whole pair.
func (a *App) portTaken(port int) bool {
	if a.frontPorts[port] {
		return false
	}
	host := "127.0.0.1"
	if a.State.Settings.AllowLAN {
		host = "0.0.0.0"
	}
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return true
	}
	l.Close()
	return false
}

func (a *App) notePorts(t *coreTarget) {
	if t.served == a.servedPorts {
		return
	}
	a.servedPorts = t.served
	if t.served != "" {
		a.QueueLog("[RayDock] 专用端口：" + t.served)
	}
}

// portRoutes returns the dedicated ports that can run and records in PortErrs why the others cannot.
func (a *App) portRoutes() []xray.PortRoute {
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
		if port == a.State.Settings.Port || (a.Conn != StateOff && port == a.runningPort) {
			a.PortErrs[id] = "与本地代理端口冲突"
			continue
		}
		if a.portTaken(port) {
			a.PortErrs[id] = "端口已被其他程序占用"
			continue
		}
		chain, err := a.Chain(a.NodeByID(id))
		if err == nil {
			// Check the route alone, so one broken node does not take the other ports or the connection down.
			_, err = xray.BuildBackend(xray.Options{}, xray.Backend{Mixed: 1, Ports: []xray.PortRoute{{Port: port, Chain: chain}}})
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
