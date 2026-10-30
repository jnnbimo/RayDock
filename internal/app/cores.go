package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"raydock/internal/localnet"
	"raydock/internal/node"
	"raydock/internal/xray"
)

// RayDock runs one core (see package xray): it serves the connection, if any, and the dedicated ports. It
// reads its config at start only, so it restarts for any change of what it serves: another node, a node's
// settings, the settings applied, the dedicated ports, the network under TUN, new rule data or core files.
// A connection keeps the node it was made through to its end, so a restart ends the connections the core
// carried; programs make them again.
//
// The new core starts beside the old one, which stops once the new one runs (xray.Overlap): under TUN the
// system's routes lead to one of the two cores' adapters throughout, and no traffic leaves around them.

// coreSpec is what the core serves.
type coreSpec struct {
	opts xray.Options
	run  xray.Run
}

// build returns the config serving s, its authenticated API on port api, under TUN on adapter.
func (s coreSpec) build(api, adapter int, token string, deferred bool) ([]byte, error) {
	o, r := s.opts, s.run
	o.APIPort, o.APIToken, r.Adapter, r.Deferred = api, token, adapter, deferred
	return xray.Build(o, r)
}

// runningCore is a core planned: its config is fixed, the process one start of it.
type runningCore struct {
	coreSpec
	core  *xray.Core
	cfg   []byte // its config on the first adapter, what serves compares
	api   int    // its authenticated API port
	token string // random per process; never part of persistent settings
	// adapter and since are written by the core queue before the core is published (App.live): the
	// adapter it runs the TUN on, and when it started.
	adapter int
	since   time.Time
	// inactive is set by the core queue while the process runs without its public listeners: it started
	// beside the core before it, and they open once that core released their ports.
	inactive bool
	// exited is set on the UI thread when it ended by itself: no plan keeps it.
	exited bool
}

// tun reports whether r runs the TUN; false without r.
func (r *runningCore) tun() bool { return r != nil && r.run.Chain != nil && r.opts.TUN.Enabled }

// serves reports whether r serves s already.
func (r *runningCore) serves(s coreSpec) bool {
	if r.exited {
		return false
	}
	cfg, err := s.build(r.api, 0, r.token, false)
	return err == nil && bytes.Equal(cfg, r.cfg)
}

// planCore returns the core serving s: cur when it does already, unless renew; else a new one.
func planCore(cur *runningCore, s coreSpec, renew bool) (*runningCore, error) {
	if cur != nil && !renew && cur.serves(s) {
		return cur, nil
	}
	api, err := freePort()
	if err != nil {
		return nil, err
	}
	token := rand.Text()
	cfg, err := s.build(api, 0, token, false)
	if err != nil {
		return nil, err
	}
	return &runningCore{coreSpec: s, cfg: cfg, api: api, token: token}, nil
}

// change tells why the core restarts even serving the same.
type change struct {
	files      bool // new core files
	geo        bool // new rule data, which only a connection reads
	quarantine bool // recover a healthy committed plan after rejecting an unrelated port
}

func (ch change) renews(s coreSpec) bool { return ch.files || ch.geo && s.run.Chain != nil }

// coreTarget is immutable: the UI plans it and the core queue carries it out.
type coreTarget struct {
	seq     int    // the plan's coreSeq
	connSeq int    // the connection's connSeq
	id      string // the node connected, "" without a connection
	// opts are the settings the connection is made with. Its core may run from a plan before, built from
	// other settings that make the same config.
	opts   xray.Options
	proc   *runningCore // nil when nothing is served
	served string       // the dedicated ports, for the log
	checks []portCheck  // unchecked dedicated chains; immutable snapshots for the core queue
	// fresh starts the connection; switched moves it to another node: it is connected since then
	// (App.Since). Other starts restart its core, and it goes on.
	fresh, switched bool
	ctx             context.Context // ends when another plan is made
}

// core returns the core of t; nil without one, or without t.
func (t *coreTarget) core() *runningCore {
	if t == nil {
		return nil
	}
	return t.proc
}

// planTarget plans the core serving s and the dedicated ports; ch renews it. The core of the plan before
// goes on if it serves the same.
func (a *App) planTarget(s coreSpec, ch change) (*coreTarget, error) {
	local := a.State.Settings.Port // a connection's local port, else the one it will take
	if s.run.Chain != nil {
		local = s.opts.Port
	}
	s.run.Ports = a.portRoutes(local, s.opts.AllowLAN)
	for i := range s.run.Ports {
		s.run.Ports[i].Chain = copyChain(s.run.Ports[i].Chain)
	}
	t := &coreTarget{served: a.describePorts(s.run.Ports)}
	for _, p := range s.run.Ports {
		check, err := a.portCheckFor(p, s.opts.AllowLAN)
		if err != nil {
			return nil, err
		}
		if cached, ok := a.portChecks[p.Port]; !ok || cached.key != check.key {
			t.checks = append(t.checks, check)
		}
	}
	if s.run.Chain == nil && len(s.run.Ports) == 0 {
		return t, nil
	}
	var err error
	t.proc, err = planCore(a.planned.core(), s, ch.renews(s))
	if err == nil && ch.quarantine && !ch.renews(s) {
		if committed := a.running.core(); committed != nil && a.live.Load() == committed && committed.core != nil && committed.core.Running() && committed.serves(s) {
			t.proc = committed
		}
	}
	return t, err
}

// coreSet owns the core processes: while the core restarts, the new one runs beside the old one.
type coreSet struct {
	mu    sync.Mutex
	cores []*xray.Core
	apis  map[*xray.Core]coreAPI
	// Sampling and stopping share mu: no poll can recount a stopped core.
	counts  map[*xray.Core]xray.Traffic
	samples []trafficSample
	readAt  time.Time
	moving  bool
}

// coreAPI is where a core serves its traffic counts (xray.ReadTraffic): its API port, and the client
// authenticated for that process.
type coreAPI struct {
	port   int
	client *http.Client
}

func (s *coreSet) add(c *xray.Core, port int, client *http.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.cores, c) {
		s.cores = append(s.cores, c)
	}
	if s.apis == nil {
		s.apis = map[*xray.Core]coreAPI{}
	}
	if old := s.apis[c].client; old != nil {
		old.CloseIdleConnections()
	}
	s.apis[c] = coreAPI{port, client}
}

func (s *coreSet) remove(c *xray.Core) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordTraffic(s.readTraffic(context.Background(), c), false)
	s.cores = slices.DeleteFunc(s.cores, func(x *xray.Core) bool { return x == c })
	s.forget(c)
}

// forget drops what s knows of c, closing its API connections. Requires mu.
func (s *coreSet) forget(c *xray.Core) {
	if client := s.apis[c].client; client != nil {
		client.CloseIdleConnections()
	}
	delete(s.apis, c)
	delete(s.counts, c)
}

// take removes and returns the cores, after a last reading of their counts.
func (s *coreSet) take() []*xray.Core {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.cores
	s.cores = nil
	// One slow API must not add a timeout for every core on exit.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var moved xray.Traffic
	for _, c := range out {
		moved = moved.Add(s.readTraffic(ctx, c))
		s.forget(c)
	}
	s.recordTraffic(moved, false)
	return out
}

func (a *App) newCore() *xray.Core { return &xray.Core{OnLog: a.QueueLog} }

// newStart cancels work planned for the core before.
func (a *App) newStart() {
	a.coreSeq++
	a.coreCancel()
	a.coreCtx, a.coreCancel = context.WithCancel(a.workCtx)
}

// startCore carries out plan t: its core replaces the one running, unless it is that one. A plan replaced
// before its turn starts nothing. done runs on the UI thread while t is still the newest, with the error of
// starting the core.
func (a *App) startCore(t *coreTarget, done func(error)) {
	a.newStart()
	t.seq, t.ctx = a.coreSeq, a.coreCtx
	if r := t.proc; r != nil && r.core == nil {
		r.core = a.newCore()
	}
	a.planned = t
	a.holdPorts(t)
	seq := t.seq
	a.coreDo(func() {
		if t.ctx.Err() != nil {
			return
		}
		checks, bad, err := a.validatePorts(t.ctx, t.checks)
		if bad {
			a.Post(func() {
				if seq == a.coreSeq {
					a.recordPortChecks(checks)
					if a.Conn == StateOff {
						a.syncIdle(change{quarantine: true})
					} else {
						a.connect(a.ActiveID, a.connOpts(), nil, change{quarantine: true})
					}
				}
			})
			return
		}
		if err == nil {
			err = a.replaceProcess(t.ctx, t.proc)
		}
		a.Post(func() {
			if seq != a.coreSeq {
				return
			}
			a.recordPortChecks(checks)
			a.running = t
			a.heldPorts(t)
			if t.id == "" || err == nil {
				a.portsStarted(t, err) // a connection that failed ends, and the ports start without it
			}
			done(err)
		})
	})
}

// replaceProcess replaces the core running with r, nil for none. With xray.Overlap r starts beside it, on
// the other adapter and without the public listeners, which it opens once the old one stopped: under TUN
// the system's traffic goes through either core throughout. Core queue only.
func (a *App) replaceProcess(ctx context.Context, r *runningCore) error {
	old := a.live.Load()
	if r == nil {
		if old != nil {
			a.stopProcess(old)
			if old.tun() && a.FlushDNS != nil {
				a.FlushDNS()
			}
		}
		return nil
	}
	if old != r {
		overlap := old != nil && xray.Overlap
		if old != nil && !overlap {
			a.stopProcess(old)
		}
		r.adapter = 0
		if old != nil && old.adapter == 0 {
			r.adapter = 1
		}
		if err := a.startProcess(ctx, r, overlap); err != nil {
			return err
		}
		// Ownership is committed before the old TUN retires. From here on a cancellation leaves r running
		// for the next queued plan, which replaces or keeps it, rather than removing the last protected route.
		a.live.Store(r)
		if overlap {
			a.stopProcess(old)
		}
		if old != nil && old.tun() && !r.tun() && a.FlushDNS != nil {
			a.FlushDNS()
		}
	}
	// A plan kept from a cancelled activation still opens its listeners here.
	return a.activate(ctx, r)
}

// activate opens the public listeners of r, started without them (startProcess). A failure leaves no
// half-served core: one without TUN stops, and a TUN core is left to the end of its connection, so the
// system's routes do not fall back to the physical network meanwhile. Core queue only.
func (a *App) activate(ctx context.Context, r *runningCore) error {
	if !r.inactive {
		return nil
	}
	if !r.core.Running() {
		return errors.New("核心在启用端口前退出")
	}
	client := xray.StatsClient(r.token)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := xray.ActivateListeners(ctx, client, r.api, r.cfg); err != nil {
		if !errors.Is(err, context.Canceled) && !r.tun() {
			a.stopProcess(r)
		}
		return err
	}
	r.inactive = false
	return nil
}

// startProcess starts an unpublished candidate, without its public listeners when inactive. A cancelled
// startup is stopped before returning.
func (a *App) startProcess(ctx context.Context, r *runningCore, inactive bool) error {
	if r.tun() {
		if err := xray.WaitTUNReleased(ctx, xray.TUNNames[r.adapter]); err != nil {
			return err
		}
	}
	cfg := r.cfg
	if inactive || r.adapter != 0 {
		var err error
		if cfg, err = r.build(r.api, r.adapter, r.token, inactive); err != nil {
			return err
		}
	}
	a.procs.add(r.core, r.api, xray.StatsClient(r.token))
	err := r.core.Start(ctx, a.coreExe(), a.configPath(), cfg, func(err error) { a.Post(func() { a.coreExited(r, err) }) })
	if err == nil && !r.core.Running() {
		err = errors.New("核心在启动后立即退出")
	}
	if err != nil {
		a.procs.remove(r.core)
		r.core.Stop()
		return err
	}
	r.since, r.inactive = time.Now(), inactive
	return nil
}

// stopProcess stops r, after a last reading of its traffic. Core queue only.
func (a *App) stopProcess(r *runningCore) {
	a.live.CompareAndSwap(r, nil)
	a.procs.remove(r.core)
	r.core.Stop()
}

// stopCores stops every core, after a last reading of its traffic, and reports whether one ran the TUN.
// Core queue only.
func (a *App) stopCores() (tun bool) {
	if r := a.live.Swap(nil); r != nil {
		tun = r.tun()
	}
	for _, c := range a.procs.take() {
		c.Stop()
	}
	return tun
}

// portsRestartAfter is how long the core serving the dedicated ports alone must have run for it to restart
// at once when it exits by itself: one exiting sooner would only exit again.
const portsRestartAfter = time.Minute

// coreExited handles the unexpected exit of r, unless another core replaced it since: it ends the
// connection; the core serving the dedicated ports alone restarts, unless it ran only briefly, then after
// the next change.
func (a *App) coreExited(r *runningCore, err error) {
	if a.live.Load() != r {
		return
	}
	r.exited = true
	a.heldPorts(a.running)
	if a.planned.core() != r {
		return // its replacement is starting; only that start may end the new connection
	}
	if a.Conn != StateOff {
		a.endConnection(err.Error())
		return
	}
	a.QueueLog("[RayDock] 专用端口的核心意外退出：" + err.Error())
	if time.Since(r.since) < portsRestartAfter {
		a.QueueLog("[RayDock] 专用端口已停止，修改端口或设置后会重新启动")
		a.markPorts("已停止：" + err.Error())
		return
	}
	a.SyncCores()
}

// portsStarted ends the start of t's dedicated ports: logs the ports served, or why none are.
func (a *App) portsStarted(t *coreTarget, err error) {
	if err != nil {
		t.proc.exited = true // the next plan starts it again
		a.heldPorts(a.running)
		a.QueueLog("[RayDock] 专用端口启动失败：" + err.Error())
		a.markPorts("启动失败，详见日志")
		return
	}
	if t.served != a.servedPorts {
		a.servedPorts = t.served
		if t.served != "" {
			a.QueueLog("[RayDock] 专用端口：" + t.served)
		}
	}
}

// tunLocal returns, under TUN, the physical network the core's own connections leave by.
func (a *App) tunLocal(o xray.Options) (*xray.Local, error) {
	if !o.TUN.Enabled {
		return nil, nil
	}
	iface, err := localnet.Find()
	if err != nil {
		return nil, err
	}
	l := localXray(iface, o.DNSStrategy)
	return &l, nil
}

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

func (a *App) logConnect(n *node.Node, chain []*node.Node, opts xray.Options) {
	a.QueueLog("[RayDock] 连接 " + n.Label() + "（" + n.Address() + "）")
	if len(chain) > 1 {
		a.QueueLog("[RayDock] 代理链：" + chainLabel(chain))
	}
	if opts.TUN.Enabled {
		a.QueueLog("[RayDock] TUN 模式：创建虚拟网卡并接管系统路由")
	}
}

func sameJSON(x, y any) bool {
	bx, errX := json.Marshal(x)
	by, errY := json.Marshal(y)
	return errX == nil && errY == nil && bytes.Equal(bx, by)
}
