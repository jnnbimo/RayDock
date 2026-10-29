package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"raydock/internal/localnet"
	"raydock/internal/node"
	"raydock/internal/system"
	"raydock/internal/xray"
)

// The front core owns local, TUN and dedicated inbounds; the backend resolves and routes.
// Only TUN changes restart the front. Routing uses the API; other changes replace the backend.
// Retired backends drain existing connections, except on manual switching or disconnect.

type frontCore struct {
	cfg []byte
	f   xray.Front
}

type runningCore struct {
	core     *xray.Core
	cfg      []byte
	opts     xray.Options // what cfg and the front core's were built from, the backend's API port included
	b        xray.Backend // the chain, the dedicated ports, its relay ports and interface
	switches int
}

type change struct {
	renew bool // a new backend core even with the same config: new rule data, a new core
}

// Draining backends stop when idle or after drainLimit, including idle keep-alive connections.
var (
	drainPoll  = 2 * time.Second
	drainLimit = 30 * time.Second
)

// coreSet owns active and draining backends.
type coreSet struct {
	mu    sync.Mutex
	cores []*xray.Core
	apis  map[*xray.Core]int // their API ports, which serve their traffic counts (xray.ReadTraffic)
	// Sampling and retirement share mu: no poll can recount a retired core.
	counts  map[*xray.Core]xray.Traffic
	samples []trafficSample
	client  *http.Client
	readAt  time.Time
	moving  bool
}

func (s *coreSet) add(c *xray.Core, api int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.cores, c) {
		s.cores = append(s.cores, c)
	}
	if s.apis == nil {
		s.apis = map[*xray.Core]int{}
	}
	s.apis[c] = api
}

func (s *coreSet) remove(c *xray.Core) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordTraffic(s.readTraffic(context.Background(), c), false)
	s.cores = slices.DeleteFunc(s.cores, func(x *xray.Core) bool { return x == c })
	delete(s.apis, c)
	delete(s.counts, c)
}

// take removes and returns the cores but those in keep.
func (s *coreSet) take(keep ...*xray.Core) []*xray.Core {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*xray.Core
	s.cores = slices.DeleteFunc(s.cores, func(c *xray.Core) bool {
		if slices.Contains(keep, c) {
			return false
		}
		out = append(out, c)
		return true
	})
	// One slow API must not add a timeout for every core on exit.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var moved xray.Traffic
	for _, c := range out {
		moved = moved.Add(s.readTraffic(ctx, c))
		delete(s.apis, c)
		delete(s.counts, c)
	}
	s.recordTraffic(moved, false)
	return out
}

func (a *App) newCore() *xray.Core { return &xray.Core{OnLog: a.QueueLog} }

// startBackend reports exits of the actual committed backend, not the newest plan. Core queue only.
func (a *App) startBackend(r *runningCore, seq int) error {
	a.procs.add(r.core, r.opts.APIPort)
	err := r.core.StartWithExit(a.coreExe(), filepath.Join(a.DataDir, "config.json"), r.cfg, func(err error) {
		a.Post(func() {
			if live := a.live.Load(); live != nil && live.run.core == r.core {
				a.pairExited(seq, err)
			}
		})
	})
	if err != nil {
		a.procs.remove(r.core) // it does not run
	}
	return err
}

// tunLocal binds backend traffic and node DNS to the physical interface under TUN.
func (a *App) tunLocal(o xray.Options) (*xray.Local, error) {
	if !o.TUN.Enabled {
		return nil, nil
	}
	iface, err := localnet.Find()
	if err != nil || iface == nil {
		return nil, err
	}
	l := localXray(iface, o.DNSStrategy)
	return &l, nil
}

// backendFor snapshots chains and dedicated routes for a planned backend.
func (a *App) backendFor(chain []*node.Node, opts xray.Options) (xray.Backend, error) {
	b := xray.Backend{Chain: copyChain(chain), Ports: a.portRoutes()}
	for i := range b.Ports {
		b.Ports[i].Chain = copyChain(b.Ports[i].Chain)
	}
	if chain == nil {
		return b, nil
	}
	var err error
	b.Local, err = a.tunLocal(opts)
	return b, err
}

// copyChain copies the nodes of chain, which the planned cores keep: the nodes in the state change.
func copyChain(chain []*node.Node) []*node.Node {
	if chain == nil {
		return nil
	}
	out := make([]*node.Node, len(chain))
	for i, n := range chain {
		c := *n
		out[i] = &c
	}
	return out
}

func newBackend(opts xray.Options, b xray.Backend) (*runningCore, error) {
	var err error
	if b.Mixed, err = freePort(); err != nil {
		return nil, err
	}
	b.TUN = 0
	if b.Connected() && opts.TUN.Enabled {
		if b.TUN, err = freePort(); err != nil {
			return nil, err
		}
	}
	if opts.APIPort, err = freePort(); err != nil {
		return nil, err
	}
	cfg, err := xray.BuildBackend(opts, b)
	if err != nil {
		return nil, err
	}
	return &runningCore{cfg: cfg, opts: opts, b: b}, nil
}

func (a *App) newFront(run *runningCore) (*frontCore, error) {
	f := &frontCore{f: xray.Front{Backend: run.b, PortsLAN: a.State.Settings.AllowLAN, Gen: 1}}
	var err error
	if f.f.APIPort, err = freePort(); err != nil {
		return nil, err
	}
	f.cfg, err = xray.BuildFront(run.opts, f.f)
	return f, err
}

// coreTarget is immutable: the UI plans it and the core queue reaches it.
// Superseded plans are applied from live state, not earlier unexecuted plans.
type coreTarget struct {
	seq     int    // the cores' pairSeq
	connSeq int    // the connection's connSeq
	id      string // the node connected, "" for the dedicated ports alone
	run     *runningCore
	front   *frontCore
	served  string // the dedicated ports, for the log
	// drain lets the backend core replaced on the way finish its connections: unless the user picked
	// another node or disconnected, which ends them.
	drain bool
	// switched moves the connection to another node: it is connected since then (App.Since). reroute
	// changes where its requests go: the node, its chain, the settings, the network or the core files,
	// which the home page's check was made through (App.routeGen); a dedicated port alone does not.
	switched, reroute bool
	ctx               context.Context // the cores': ends API calls with them
	drains            context.Context // ends the cores finishing their connections with the cores
}

func mergeTarget(old, t *coreTarget) {
	if old.seq == t.seq {
		t.drain = t.drain && old.drain
		// t replaces old, which never ran: t makes its changes too.
		t.switched, t.reroute = t.switched || old.switched, t.reroute || old.reroute
	}
}

// newPair cancels work planned for the previous pair.
func (a *App) newPair() {
	a.pairSeq++
	a.pairCancel()
	a.pairCtx, a.pairCancel = context.WithCancel(a.workCtx)
	a.resetDrains()
}

// startPair queues a fresh backend/front pair and retires the previous pair.
// done runs on the UI thread only while this pair is still current.
func (a *App) startPair(t *coreTarget, done func(error)) {
	a.newPair()
	t.seq, t.ctx, t.drains = a.pairSeq, a.pairCtx, a.drainCtx
	t.run.core = a.newCore()
	a.run, a.frontRun, a.planned = nil, nil, t
	a.holdPorts(t)
	prevTUN := a.tunOn
	a.tunOn = t.run.b.Connected() && t.run.opts.TUN.Enabled
	seq := t.seq
	a.coreDo(func() {
		if t.ctx.Err() != nil {
			return
		}
		err := a.startBackend(t.run, seq)
		if err == nil {
			// Start stops the old front core first.
			err = a.front.StartWithExit(a.coreExe(), filepath.Join(a.DataDir, "front.json"), t.front.cfg, func(err error) {
				a.Post(func() { a.pairExited(seq, err) })
			})
		}
		if err != nil {
			a.stopCores()
		} else {
			for _, c := range a.procs.take(t.run.core) {
				c.Stop() // the cores before
			}
			a.live.Store(t)
			if !t.run.core.Running() || !a.front.Running() {
				err = fmt.Errorf("核心在提交连接前已退出")
				a.stopCores()
			}
		}
		if prevTUN && a.FlushDNS != nil {
			a.FlushDNS()
		}
		a.Post(func() {
			if seq != a.pairSeq {
				return
			}
			if err != nil {
				a.run, a.frontRun, a.planned, a.tunOn = nil, nil, nil, false
				a.heldPorts(nil)
			} else {
				a.run, a.frontRun = t.run, t.front
				a.notePorts(t)
				a.heldPorts(t)
			}
			done(err)
		})
	})
}

// stopPair stops the cores after the operations queued before, then flushes the DNS cache a TUN leaves.
func (a *App) stopPair() {
	a.newPair()
	a.run, a.frontRun, a.planned = nil, nil, nil
	tun := a.tunOn
	a.tunOn = false
	a.servedPorts = ""
	seq := a.pairSeq
	a.coreDo(func() {
		a.stopCores()
		if tun && a.FlushDNS != nil {
			a.FlushDNS()
		}
		a.Post(func() {
			if seq == a.pairSeq {
				a.heldPorts(nil)
			}
		})
	})
}

// pairExited ends the current pair; dedicated ports restart only after a connection ends.
func (a *App) pairExited(seq int, err error) {
	if seq != a.pairSeq {
		return
	}
	a.stopPair()
	if a.Conn != StateOff {
		a.endConnection(err.Error())
		return
	}
	a.QueueLog("[RayDock] 专用端口的核心意外退出：" + err.Error())
	a.markPorts("已停止：" + err.Error())
}

// apply queues a target beside the running cores; false means the TUN requires a restart.
// Plans are coalesced by reconcile and remain separate from live state.
func (a *App) apply(n *node.Node, chain []*node.Node, opts xray.Options, toast func(string), ch change) bool {
	r, f := a.run, a.frontRun
	fail := func(err error) bool {
		if n != nil {
			a.ConnErr = err.Error()
		} else {
			a.QueueLog("[RayDock] 专用端口：" + err.Error())
		}
		return true
	}
	if n == nil {
		opts.TUN.Enabled = false
	}
	if opts.TUN.Enabled != r.opts.TUN.Enabled {
		return false // the TUN comes and goes with the front core, and the backend core relays it on a port of its own
	}
	opts.APIPort = r.opts.APIPort

	// The backend core: the running one as it is, its routing rules switched through its API, or a new one.
	b, err := a.backendFor(chain, opts)
	if err != nil {
		return fail(err)
	}
	b.Mixed, b.TUN = r.b.Mixed, r.b.TUN
	cfg, err := xray.BuildBackend(opts, b)
	if err != nil {
		return fail(err)
	}
	backend, apiSwitch := r, false
	if ch.renew || !bytes.Equal(cfg, r.cfg) {
		if !ch.renew && xray.CanSwitch(r.cfg, cfg) {
			backend, apiSwitch = &runningCore{core: r.core, cfg: cfg, opts: opts, b: b, switches: r.switches + 1}, true
		} else if backend, err = newBackend(opts, b); err != nil {
			return fail(err)
		} else {
			backend.core = a.newCore()
		}
	}

	// The front core: switched through its API to the new backend core or settings, or restarted.
	front := f
	fr := f.f
	fr.Backend, fr.PortsLAN = backend.b, a.State.Settings.AllowLAN
	if cur, err := xray.BuildFront(opts, fr); err != nil {
		return fail(err)
	} else if !bytes.Equal(cur, f.cfg) {
		fr.Gen++
		next, err := xray.BuildFront(opts, fr)
		if err != nil {
			return fail(err)
		}
		if !xray.CanSwitchFront(f.cfg, next) {
			return false
		}
		front = &frontCore{cfg: next, f: fr}
	}
	if backend == r { // its config stays; the options it was built from may have changed where it has none
		backend = &runningCore{core: r.core, cfg: r.cfg, opts: opts, b: b, switches: r.switches}
	}
	replaced := backend.core != r.core
	// Another node with the same connection settings runs on the same cores, but is still the one
	// connected now.
	switched := n != nil && n.ID != a.ActiveID
	if !replaced && !apiSwitch && front == f && !switched {
		a.run = backend
		if n != nil {
			a.ConnErr, a.SettingsDirty = "", a.pending() // all running already
		}
		return true
	}
	connecting := n != nil && !r.b.Connected()
	cut := r.b.Connected() && (n == nil || switched)
	a.run, a.frontRun = backend, front
	a.holdPorts(&coreTarget{front: front})
	id := ""
	if n != nil {
		id = n.ID
		a.warnChain(chain, toast)
		if connecting {
			a.cancelConnect()
			a.connCtx, a.connCancel = context.WithCancel(a.workCtx)
			a.Conn, a.WaitCore = StateConnecting, false
		}
		a.ActiveID, a.ConnErr, a.SettingsDirty = n.ID, "", a.pending()
		a.RealTest = Latency{}
		a.State.SelectedID, a.State.LastID = n.ID, n.ID
		a.Save()
		switch {
		case connecting:
			a.logConnect(n, chain, opts)
		case switched && !replaced:
			a.QueueLog(fmt.Sprintf("[RayDock] 切换到 %s（%s），与原节点的连接配置相同，连接不受影响", n.Label(), n.Address()))
		case switched:
			a.QueueLog(fmt.Sprintf("[RayDock] 切换到 %s（%s），经原节点的连接已断开，程序重连后经新节点", n.Label(), n.Address()))
			if len(chain) > 1 {
				a.QueueLog("[RayDock] 代理链：" + chainLabel(chain))
			}
		case !sameJSON(r.b.Chain, b.Chain):
			a.QueueLog(fmt.Sprintf("[RayDock] %s（%s）的配置已更新，新连接改用新配置，已建立的连接不中断", n.Label(), n.Address()))
		case sameJSON(r.b.Ports, b.Ports): // a change of the dedicated ports reports itself (notePorts)
			a.QueueLog("[RayDock] 已在运行中应用设置，已建立的连接不中断")
		}
	}
	a.changed()
	reroute := n != nil && (switched || ch.renew || opts != r.opts || !sameJSON(r.b.Chain, b.Chain) || !sameJSON(r.b.Local, b.Local))
	t := &coreTarget{seq: a.pairSeq, connSeq: a.connSeq, id: id, run: backend, front: front, served: a.describePorts(b.Ports),
		drain: !cut, switched: switched, reroute: reroute, ctx: a.pairCtx, drains: a.drainCtx}
	a.planned = t
	a.coreWant.set(t, mergeTarget, func(op func()) <-chan struct{} { return queueOperation(a.coreOps, op) }, a.reconcile)
	return true
}

func (a *App) logConnect(n *node.Node, chain []*node.Node, opts xray.Options) {
	a.QueueLog(fmt.Sprintf("[RayDock] 连接 %s（%s）", n.Label(), n.Address()))
	if len(chain) > 1 {
		a.QueueLog("[RayDock] 代理链：" + chainLabel(chain))
	}
	if opts.TUN.Enabled {
		a.QueueLog("[RayDock] TUN 模式：创建虚拟网卡 " + xray.TUNName + " 并接管系统路由")
	}
}

// reconcile reaches a target from live state. Startup failure preserves the old pair;
// API failure makes the state uncertain and requires a restart. Core queue only.
func (a *App) reconcile(t *coreTarget) {
	live := a.live.Load()
	if live == nil || live.seq != t.seq {
		return // the cores stopped or started again since
	}
	replaced := t.run.core != live.run.core
	apiSwitch := !replaced && !bytes.Equal(t.run.cfg, live.run.cfg)
	frontSwitch := !bytes.Equal(t.front.cfg, live.front.cfg)
	clean := true // nothing running was changed: a failure leaves what runs as it was
	err := func() error {
		var routing []byte
		var fs xray.FrontSwitch
		var err error
		if apiSwitch {
			if routing, err = xray.SwitchParts(t.run.cfg); err != nil {
				return err
			}
		}
		if frontSwitch {
			if fs, err = xray.FrontParts(live.front.cfg, t.front.cfg); err != nil {
				return err
			}
		}
		if replaced {
			if err := a.startBackend(t.run, t.seq); err != nil {
				return err
			}
		}
		clean = false
		exe := a.coreExe()
		if apiSwitch {
			api := xray.API{Exe: exe, Addr: fmt.Sprintf("127.0.0.1:%d", live.run.opts.APIPort), Ctx: t.ctx}
			if err := api.Switch(routing); err != nil {
				return err
			}
		}
		if frontSwitch {
			api := xray.API{Exe: exe, Addr: fmt.Sprintf("127.0.0.1:%d", live.front.f.APIPort), Ctx: t.ctx}
			return api.SwitchFront(fs)
		}
		return nil
	}()
	if err != nil {
		if clean {
			// The backend core started in vain, if any; nothing else changed.
			if replaced && t.run.core.Running() {
				a.procs.remove(t.run.core)
				t.run.core.Stop()
			}
		}
		clean = clean && live.run.core.Running() && a.front.Running()
		if !clean {
			a.live.Store(nil) // partial changes or a dead original pair cannot be rolled back
		}
		a.Post(func() { a.applyFailed(t, live, err, clean) })
		return
	}
	if !t.drain {
		// Another node or a disconnect ends every connection made before: on the backend core replaced, and
		// on those still finishing theirs from earlier changes.
		for _, c := range a.procs.take(t.run.core) {
			c.Stop()
		}
	} else if replaced {
		a.drainCore(t.drains, live.run.core, live.run.b.Mixed, live.run.b.TUN)
	}
	a.live.Store(t)
	if !t.run.core.Running() || !a.front.Running() {
		a.Post(func() { a.pairExited(t.seq, fmt.Errorf("核心在提交变更时已退出")) })
		return
	}
	a.Post(func() { a.applied(t) })
}

func (a *App) applied(t *coreTarget) {
	if t.seq != a.pairSeq || a.live.Load() != t {
		return
	}
	a.notePorts(t)
	a.heldPorts(t)
	if t.id != "" && t.connSeq == a.connSeq {
		switch a.Conn {
		case StateConnecting: // connected beside the dedicated ports
			a.Conn, a.Since, a.runningPort = StateOn, time.Now(), t.run.opts.Port
			a.SettingsDirty = a.pending()
			a.SyncProxy()
		case StateOn:
			if a.ActiveID == t.id {
				if t.switched {
					a.Since = time.Now()
				}
				if t.reroute {
					a.routeGen++
				}
			}
			if port := t.run.opts.Port; a.runningPort != port {
				a.runningPort = port // the new local port opened: the system proxy moves there
				a.SyncProxy()
			}
		}
	}
	a.changed()
}

// applyFailed restores the live plan after a clean failure unless a newer plan exists.
// A partially applied change requires a full restart.
func (a *App) applyFailed(t, live *coreTarget, err error, clean bool) {
	if t.seq != a.pairSeq {
		return
	}
	if !clean || !live.run.core.Running() || !a.front.Running() {
		a.QueueLog("[RayDock] 无法在运行中应用，重新启动核心：" + err.Error())
		a.restartPair()
		return
	}
	a.QueueLog("[RayDock] 无法在运行中应用：" + err.Error())
	if t != a.planned {
		return // a later change is carried out from what runs, and reports for itself
	}
	a.run, a.frontRun, a.planned = live.run, live.front, live
	a.heldPorts(live)
	for id, port := range a.State.Ports {
		if servesPort(t.run.b, port) && !servesPort(live.run.b, port) {
			a.PortErrs[id] = "启动失败，详见日志"
		}
	}
	defer a.changed()
	switch {
	case t.id == "" && live.id != "":
		// Disconnecting failed: the connection's cores must not go on. The dedicated ports start afresh.
		a.stopPair()
		a.syncIdle(change{})
	case t.connSeq != a.connSeq || t.id == "":
	case live.id == "": // connecting beside the dedicated ports failed; they go on
		a.endConnection("连接失败：" + err.Error())
	default:
		switched := a.ActiveID != live.id
		a.ActiveID, a.State.LastID = live.id, live.id
		if switched {
			a.ConnErr = "未能切换节点，仍使用原节点：" + err.Error()
		} else if !sameJSON(t.run.b.Chain, live.run.b.Chain) || sameJSON(t.run.b.Ports, live.run.b.Ports) {
			a.ConnErr = "设置未能应用：" + err.Error()
		}
		a.SettingsDirty = a.pending()
		a.Save()
	}
}

func (a *App) restartPair() {
	if a.Conn != StateOff {
		a.Restart()
		return
	}
	a.stopPair()
	a.syncIdle(change{})
}

// drainCore stops a retired backend when idle, canceled, or past drainLimit.
func (a *App) drainCore(ctx context.Context, c *xray.Core, ports ...int) {
	ports = slices.DeleteFunc(ports, func(p int) bool { return p == 0 })
	a.drains.Add(1)
	go func() {
		defer a.drains.Done()
		defer func() {
			a.procs.remove(c)
			c.Stop()
		}()
		t := time.NewTicker(drainPoll)
		defer t.Stop()
		for limit := time.Now().Add(drainLimit); time.Now().Before(limit); {
			if system.Connections(ports...) == 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// resetDrains stops the cores finishing their connections. UI only.
func (a *App) resetDrains() {
	a.drainCancel()
	a.drainCtx, a.drainCancel = context.WithCancel(a.workCtx)
}

// stopCores closes the front listener before retiring every backend. Core queue only.
func (a *App) stopCores() {
	a.live.Store(nil)
	a.front.Stop()
	for _, c := range a.procs.take() {
		c.Stop()
	}
}

// endTUN runs after a TUN core exited: adapter, routes and WFP rules go with the process; only the DNS cache remains.
func (a *App) endTUN() {
	if a.tunOn {
		a.tunOn = false
		if a.FlushDNS != nil {
			go a.FlushDNS()
		}
	}
}

func sameJSON(x, y any) bool {
	bx, errX := json.Marshal(x)
	by, errY := json.Marshal(y)
	return errX == nil && errY == nil && bytes.Equal(bx, by)
}

func servesPort(b xray.Backend, port int) bool {
	return slices.ContainsFunc(b.Ports, func(r xray.PortRoute) bool { return r.Port == port })
}
