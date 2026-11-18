package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"raydock/internal/localnet"
	"raydock/internal/node"
	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/xray"
)

func (a *App) xrayOptions() xray.Options {
	return a.State.Settings.Options
}

// Connect connects to node id, downloading the core first if needed and asking for elevation when TUN lacks
// rights. While connected, the core restarts with the change, ending the connections it carried (see
// cores.go). Switching to another node keeps the settings that run: those changed since stay
// pending (SettingsDirty) until reconnecting, which applies them.
func (a *App) Connect(id string, toast func(string)) {
	opts := a.xrayOptions()
	if a.connPlan() != nil && id != a.ActiveID {
		opts = a.connOpts()
	}
	a.connect(id, opts, toast, change{})
}

// connPlan returns the plan of the connection, running or starting; nil without one. While a change
// waits for the core files (WaitCore), it is the plan before, still running.
func (a *App) connPlan() *coreTarget {
	if t := a.planned; t != nil && t.id != "" && a.Conn != StateOff {
		return t
	}
	return nil
}

// connOpts returns the settings the connection is planned with: those of a change waiting for the core
// files, else those running or starting.
func (a *App) connOpts() xray.Options {
	if a.WaitCore {
		return a.waitOpts
	}
	if t := a.connPlan(); t != nil {
		return t.opts
	}
	return a.xrayOptions()
}

// connect is Connect with opts. Changes that apply one part of the settings alone (SetMode, SyncCores)
// pass the options that run with that part changed, leaving the rest for a reconnect.
func (a *App) connect(id string, opts xray.Options, toast func(string), ch change) {
	if a.closing {
		return
	}
	n := a.NodeByID(id)
	if n == nil {
		return
	}
	if opts.TUN.Enabled && !a.Elevated {
		a.ConnErr = "TUN 模式需要管理员权限"
		a.State.SelectedID, a.State.LastID = id, id // restarted elevated, it connects to this node
		a.Save()
		a.needAdmin(true)
		return
	}
	chain, err := a.Chain(n)
	if err != nil {
		a.ConnErr = err.Error()
		return
	}
	// A node still to be pinned connects now and again once pinned.
	for _, c := range chain {
		a.PinCertificates(c.ID)
	}
	local, err := a.tunLocal(opts)
	if err != nil {
		a.ConnErr = err.Error()
		return
	}
	prev := a.connPlan()
	t, err := a.planTarget(coreSpec{opts: opts, run: xray.Run{Chain: copyChain(chain), Local: local}}, ch)
	if err != nil {
		a.ConnErr = err.Error()
		return
	}
	t.id, t.opts = n.ID, opts
	if prev != nil && prev.id == n.ID && t.proc == prev.proc && !a.WaitCore {
		a.ConnErr, a.SettingsDirty = "", a.pending() // the connection goes on as planned
		return
	}
	// A change of the connection keeps it: connected since it started, unless it moves to another node.
	// Changes made while one is carried out add up.
	if a.Conn != StateConnecting {
		a.connFresh = a.Conn == StateOff
		a.connSwitched = a.connFresh
	}
	a.connSwitched = a.connSwitched || n.ID != a.ActiveID
	t.fresh, t.switched = a.connFresh, a.connSwitched
	a.warnChain(chain, toast)
	a.cancelConnect()
	ctx, cancel := context.WithCancel(a.workCtx)
	a.connCtx, a.connCancel = ctx, cancel
	t.connSeq = a.connSeq
	a.Conn, a.ActiveID, a.ConnErr, a.SettingsDirty = StateConnecting, n.ID, "", false
	a.RealTest = Latency{}
	a.State.SelectedID, a.State.LastID = n.ID, n.ID
	a.Save()
	a.changed()
	if a.coreHold || !a.coreUsable(opts.TUN.Enabled) {
		// The core files are being checked or replaced, missing, unverified or too old: the change is made
		// once that is done (resumeConnect), with these settings, or ends when no core can serve it. The core
		// running meanwhile goes on.
		a.WaitCore, a.waitOpts = true, opts
		if a.CoreBusy || a.coreHold {
			a.QueueLog("[RayDock] Xray 核心文件正在准备，完成后自动连接")
		} else {
			a.EnsureCore() // installs the bundled core where it is missing, broken or newer
		}
		return
	}
	a.WaitCore = false
	const cut = "，重新启动核心，已建立的连接会断开"
	switch cur := prev.core(); {
	case t.fresh || cur == nil:
		a.logConnect(n, chain, opts)
	case t.switched && n.ID != prev.id:
		a.QueueLog("[RayDock] 切换到 " + n.Label() + "（" + n.Address() + "）" + cut)
		if len(chain) > 1 {
			a.QueueLog("[RayDock] 代理链：" + chainLabel(chain))
		}
	case prev.id == n.ID && !sameJSON(cur.run.Chain, t.proc.run.Chain):
		a.QueueLog("[RayDock] " + n.Label() + "（" + n.Address() + "）的配置已更新" + cut)
	case !sameJSON(cur.run.Ports, t.proc.run.Ports):
		a.QueueLog("[RayDock] 专用端口已变更" + cut)
	default:
		a.QueueLog("[RayDock] 应用变更" + cut)
	}
	if opts.TUN.Enabled {
		a.jobs.Go(func() { a.watchLocalNet(ctx) })
	}
	a.startCore(t, func(err error) { a.started(t, err) })
}

// resumeConnect makes the change of the connection that waited for the core files (WaitCore), with the
// settings it was asked with; ch renews the core if it reads files replaced meanwhile.
func (a *App) resumeConnect(ch change) {
	if !a.WaitCore {
		return
	}
	if a.NodeByID(a.ActiveID) == nil {
		a.endConnection("等待核心期间，连接节点已不存在")
		return
	}
	a.ConnErr = ""
	a.connect(a.ActiveID, a.waitOpts, nil, ch)
	if a.WaitCore && a.ConnErr != "" {
		a.endConnection(a.ConnErr)
	}
}

// started ends the start of the connection's core t, unless another plan replaced it: connected, or the
// connection ends with err.
func (a *App) started(t *coreTarget, err error) {
	if t.connSeq != a.connSeq {
		return
	}
	if err != nil {
		a.endConnection(err.Error())
		return
	}
	a.Conn, a.runningPort = StateOn, t.opts.Port
	if t.switched {
		a.Since = time.Now()
	}
	a.routeGen++                  // what the home page checked went through the core before
	a.SettingsDirty = a.pending() // changed while it started
	a.SyncProxy()
	a.changed()
	a.SyncCores() // nodes or dedicated ports changed while it started
}

// warnChain tells about node settings the core cannot honor.
func (a *App) warnChain(chain []*node.Node, toast func(string)) {
	warned := false
	for _, c := range chain {
		for _, w := range xray.Warnings(c) {
			a.QueueLog("[RayDock] " + c.Label() + "：" + w)
			warned = true
		}
	}
	if warned && toast != nil {
		toast("证书校验提示：详见日志")
	}
}

// watchLocalNet follows the physical network while the TUN connection of ctx lasts. The core's outbounds
// stay bound to the interface found when it started, so when another one takes over (Wi-Fi to Ethernet),
// or its DNS servers change, it restarts (SyncCores). A change must last to the next look: one network
// flickering does not restart it each time.
func (a *App) watchLocalNet(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var seen *xray.Local // a change seen once; UI thread only
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		iface, err := localnet.Find()
		if err != nil || iface == nil {
			continue
		}
		a.Post(func() {
			r := a.running.core()
			if ctx.Err() != nil || a.Conn != StateOn || r == nil || r.run.Local == nil {
				return
			}
			l := localXray(iface, r.opts.DNSStrategy)
			switch {
			case sameLocal(l, *r.run.Local):
				seen = nil
			case seen == nil || !sameLocal(l, *seen):
				seen = &l
			default:
				seen = nil
				a.QueueLog("[RayDock] 本地网络已变化（" + l.Interface + "），改经新网络连接")
				a.SyncCores()
			}
		})
	}
}

func sameLocal(x, y xray.Local) bool {
	return x.Interface == y.Interface && x.Strategy == y.Strategy && slices.Equal(x.DNS, y.DNS)
}

// cancelConnect invalidates queued starts before they can create a process. UI only.
func (a *App) cancelConnect() {
	a.connSeq++
	if a.connCancel != nil {
		a.connCancel()
		a.connCancel = nil
	}
}

func (a *App) Disconnect() {
	if a.closing {
		return
	}
	a.cancelConnect() // also cancels an automatic resume while updating
	if a.Conn == StateOff {
		return
	}
	a.endConnection("")
	a.QueueLog("[RayDock] 已断开")
}

// endConnection ends the connection, for reason ("" when disconnected): every way one ends goes here. The
// system proxy is restored first, so that programs go direct rather than to a closing port; then the core
// restarts serving the dedicated ports alone, or stops (syncIdle).
func (a *App) endConnection(reason string) {
	a.cancelConnect()
	a.Conn, a.ActiveID, a.ConnErr, a.WaitCore, a.runningPort = StateOff, "", reason, false, 0
	a.RealTest, a.SettingsDirty = Latency{}, false
	a.restoreProxy()
	a.changed()
	a.syncIdle(change{})
}

// SetTUN toggles TUN mode. Without admin rights the switch stays on and elevation is offered.
func (a *App) SetTUN(on bool) {
	a.State.Settings.TUN.Enabled = on
	a.Save()
	if on && !a.Elevated {
		a.needAdmin(a.Conn != StateOff)
		return
	}
	a.Reconnect()
}

// needAdmin is reached when TUN needs administrator rights RayDock lacks; connect tells whether RayDock is
// to connect with them. Where the admin start at logon can, RayDock restarts with them at once, its task
// asking nothing; otherwise the user is offered the restart (AdminPrompt), as when RayDock already is such
// a restart, still without them.
func (a *App) needAdmin(connect bool) {
	if !a.Relaunched && a.RelaunchAdmin != nil && a.RelaunchAdmin(connect && a.ConnectTarget() != "", false) == nil {
		return
	}
	a.AdminPrompt = true
}

// RestartAsAdmin restarts RayDock with administrator rights, asking through UAC where the admin start at
// logon cannot; restarted, it connects to the node selected.
func (a *App) RestartAsAdmin() error {
	if a.RelaunchAdmin == nil {
		return errors.New("当前环境不支持提权")
	}
	return a.RelaunchAdmin(a.ConnectTarget() != "", true)
}

// ConnectTarget prefers the selected node, then the last connected node; empty means neither exists.
func (a *App) ConnectTarget() string {
	for _, id := range []string{a.State.SelectedID, a.State.LastID} {
		if a.NodeByID(id) != nil {
			return id
		}
	}
	return ""
}

// Reconnect applies pending settings, restarting the core if they change its config.
func (a *App) Reconnect() {
	if a.Conn != StateOff && a.ActiveID != "" {
		a.Connect(a.ActiveID, nil)
	}
}

// RenewCores restarts the core if it reads the files ch tells of: new core files, or new rule data.
func (a *App) RenewCores(ch change) {
	switch {
	case a.Conn == StateOff:
		a.syncIdle(ch)
	case a.WaitCore:
		a.resumeConnect(ch)
	case a.connPlan() != nil:
		a.connect(a.ActiveID, a.connOpts(), nil, ch)
	}
}

// SetMode applies only the routing mode, leaving other pending settings untouched.
func (a *App) SetMode(mode string) {
	a.State.Settings.Mode = mode
	a.Save()
	if a.connPlan() != nil {
		opts := a.connOpts()
		opts.Mode = mode
		a.connect(a.ActiveID, opts, nil, change{})
		return
	}
	a.Reconnect()
}

// pending compares the settings with those of the connection's core (e.g. a TUN option without TUN
// changes nothing); a start in flight counts as pending.
func (a *App) pending() bool {
	if a.Conn == StateOff {
		return false
	}
	r := a.running.core()
	if r == nil || a.planned.core() != r {
		return true
	}
	o := a.xrayOptions()
	if o == a.running.opts {
		return false
	}
	if o.TUN.Enabled != r.opts.TUN.Enabled {
		return true // the physical network the core binds to comes and goes with the TUN
	}
	s := r.coreSpec
	s.opts = o
	return !r.serves(s)
}

// connCore returns the connection's core once it runs the newest plan: checks must not label requests
// through an old core as through a new node.
func (a *App) connCore() *runningCore {
	t, r := a.planned, a.planned.core()
	if a.closing || a.Conn != StateOn || r == nil || t.id != a.ActiveID || t.connSeq != a.connSeq ||
		a.running.core() != r || a.live.Load() != r {
		return nil
	}
	return r
}

// Checkable reports whether the connection can be checked now (TestReal, CheckOverview): connected, and
// with no change planned that the core has yet to carry out.
func (a *App) Checkable() bool { return a.connCore() != nil }

// ProxyPort describes the committed listener while connected; edited settings wait for a reconnect.
func (a *App) ProxyPort() int {
	if a.Conn != StateOff && a.runningPort != 0 {
		return a.runningPort
	}
	return a.State.Settings.Port
}

func (a *App) ProxyAddr() string { return "127.0.0.1:" + strconv.Itoa(a.ProxyPort()) }

// SyncProxy keeps the system proxy on exactly while connected with the switch on.
func (a *App) SyncProxy() {
	if a.Conn == StateOn && a.State.Settings.SystemProxy {
		a.enableProxy()
	} else {
		a.restoreProxy()
	}
}

// Before every write, persist the observed state and every intermediate state Apply may leave.
func (a *App) enableProxy() {
	a.ProxyErr = ""
	cur, err := a.Proxy.Current()
	if err != nil {
		a.ProxyErr = "读取系统代理失败：" + err.Error()
		return
	}
	target := sysproxy.Settings{Enabled: true, Server: a.ProxyAddr(), Override: sysproxy.Bypass}
	backup := store.ProxyBackup{Previous: cur}
	if b := a.State.ProxyBackup; b != nil {
		if !ownsProxy(b, cur) {
			a.ProxyErr = "系统代理已被其他程序修改，未覆盖其设置"
			return
		}
		if b.Transition == nil && b.Owned != nil && cur == target {
			return
		}
		backup = *b
	}
	backup.Pending = target.Server
	backup.Transition = &store.ProxyTransition{From: cur, To: target}
	if err := a.saveProxyBackup(&backup); err != nil {
		a.ProxyErr = "保存代理恢复信息失败，未修改系统代理：" + err.Error()
		return
	}
	if err := a.Proxy.Apply(target); err != nil {
		a.ProxyErr = "设置系统代理失败：" + err.Error()
		return
	}
	settled := backup
	settled.Applied, settled.Pending, settled.Owned, settled.Transition = target.Server, "", &target, nil
	if err := a.saveProxyBackup(&settled); err != nil {
		a.ProxyErr = "保存代理设置结果失败：" + err.Error()
	}
}

func (a *App) saveProxyBackup(b *store.ProxyBackup) error {
	old := a.State.ProxyBackup
	a.State.ProxyBackup = b
	if err := a.saveState(); err != nil {
		a.State.ProxyBackup = old
		return err
	}
	return nil
}

func (a *App) restoreProxy() {
	if a.State.ProxyBackup == nil {
		return
	}
	owned, err := restoreOwnedProxy(a.Proxy, a.State.ProxyBackup, a.saveProxyBackup)
	if err != nil {
		a.ProxyErr = err.Error()
		return
	}
	if !owned {
		a.QueueLog("[RayDock] 系统代理已被其他程序修改，保持不动")
	}
	a.ProxyErr = ""
}

func (a *App) proxyActive() bool { return a.State.ProxyBackup != nil }

// CleanStaleProxy restores a system proxy left behind by an abnormal exit.
func (a *App) CleanStaleProxy() {
	if a.State.ProxyBackup != nil {
		a.QueueLog("[RayDock] 恢复上次未正常退出时的系统代理设置")
		a.restoreProxy()
	}
}

func chainLabel(chain []*node.Node) string {
	names := make([]string, len(chain))
	for i, n := range chain {
		names[i] = n.Label()
	}
	return strings.Join(names, " → ")
}
