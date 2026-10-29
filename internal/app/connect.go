package app

import (
	"bytes"
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
// rights. While connected, or while the cores serve the dedicated ports, the change is applied beside the
// running cores (apply): programs keep the local port, the TUN and the dedicated ports; another node ends
// the connections through the old one. The TUN alone restarts the cores. Switching to another node keeps
// the settings that run: those changed since stay pending (SettingsDirty) until reconnecting, which
// applies them.
func (a *App) Connect(id string, toast func(string)) {
	opts := a.xrayOptions()
	if r := a.run; r != nil && a.Conn == StateOn && id != a.ActiveID {
		opts = r.opts
	}
	a.connect(id, opts, toast, change{})
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
		a.AdminPrompt = true
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
	// Beside the running cores: those of the connection, or of the dedicated ports once the core is ready.
	if r := a.run; r != nil && a.frontRun != nil {
		connected := r.b.Connected()
		if (connected && a.Conn != StateOff || !connected && !a.coreHold && a.coreUsable(opts.TUN.Enabled)) &&
			a.apply(n, chain, opts, toast, ch) {
			return
		}
	}
	b, err := a.backendFor(chain, opts)
	if err != nil {
		a.ConnErr = err.Error()
		return
	}
	run, err := newBackend(opts, b)
	if err != nil {
		a.ConnErr = err.Error()
		return
	}
	front, err := a.newFront(run)
	if err != nil {
		a.ConnErr = err.Error()
		return
	}
	a.warnChain(chain, toast)
	a.cancelConnect()
	ctx, cancel := context.WithCancel(a.workCtx)
	a.connCtx, a.connCancel = ctx, cancel
	seq := a.connSeq
	a.Conn, a.ActiveID, a.ConnErr, a.SettingsDirty = StateConnecting, n.ID, "", false
	a.RealTest = Latency{}
	a.State.SelectedID, a.State.LastID = n.ID, n.ID
	a.Save()
	a.changed()
	if a.coreHold || !a.coreUsable(opts.TUN.Enabled) {
		// The core files are being checked or replaced, missing or too old: connect once that is done.
		a.WaitCore = true
		switch {
		case a.CoreBusy || a.coreHold:
			a.QueueLog("[RayDock] Xray 核心文件正在准备，完成后自动连接")
		case a.CoreReady() && opts.TUN.Enabled && a.CoreVersion == "":
			a.EnsureCore() // verify the existing executable before allowing TUN
		default:
			a.QueueLog("[RayDock] Xray 核心尚未就绪或版本过旧，下载完成后自动连接")
			a.InstallCore()
		}
		return
	}
	a.WaitCore = false
	a.logConnect(n, chain, opts)
	if opts.TUN.Enabled {
		go a.watchLocalNet(ctx)
	}
	t := &coreTarget{connSeq: seq, id: n.ID, run: run, front: front, served: a.describePorts(run.b.Ports), drain: true}
	a.startPair(t, func(err error) {
		if seq != a.connSeq {
			return
		}
		if err != nil {
			a.endConnection(err.Error())
			return
		}
		a.Conn, a.Since, a.runningPort = StateOn, time.Now(), opts.Port
		a.SettingsDirty = a.pending() // changed while it started
		a.SyncProxy()
		a.changed()
		a.SyncCores() // nodes or dedicated ports changed while it started
	})
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

// watchLocalNet follows the physical network while the TUN connection of ctx lasts. The front core
// follows it by itself; the backend core stays bound to the interface found when it started, so when
// another one takes over (Wi-Fi to Ethernet), the DNS servers change, or a network appears where there
// was none, it is replaced beside the running one (SyncCores).
func (a *App) watchLocalNet(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
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
			r := a.run
			if ctx.Err() != nil || a.Conn != StateOn || r == nil || !r.b.Connected() {
				return
			}
			cur := r.b.Local
			if l := localXray(iface, r.opts.DNSStrategy); cur == nil || l.Interface != cur.Interface || !slices.Equal(l.DNS, cur.DNS) {
				a.QueueLog("[RayDock] 本地网络已变化（" + l.Interface + "），改经新网络连接")
				a.SyncCores()
			}
		})
	}
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
// system proxy is restored first, so that programs go direct rather than to a closing port; then the cores
// stop, or go on serving the dedicated ports alone (syncIdle), what the connection carried ended.
func (a *App) endConnection(reason string) {
	a.cancelConnect()
	a.resetDrains()
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
		a.AdminPrompt = true
		return
	}
	a.Reconnect()
}

func (a *App) RestartAsAdmin() error {
	if a.RelaunchAdmin == nil {
		return errors.New("当前环境不支持提权")
	}
	return a.RelaunchAdmin(a.ConnectTarget() != "")
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

// Reconnect applies pending settings; only TUN changes require restarting both cores.
func (a *App) Reconnect() {
	if a.Conn != StateOff && a.ActiveID != "" {
		a.Connect(a.ActiveID, nil)
	}
}

// Restart restarts the cores of the connection: when a change could not be applied beside them.
func (a *App) Restart() {
	a.run, a.frontRun, a.planned = nil, nil, nil
	a.Reconnect()
}

// RenewCores replaces the backend to load new core files or rule data.
func (a *App) RenewCores() {
	switch r := a.run; {
	case a.Conn == StateOff:
		a.syncIdle(change{renew: true})
	case r != nil && r.b.Connected():
		a.connect(a.ActiveID, r.opts, nil, change{renew: true})
	default:
		a.Reconnect()
	}
}

// SetMode applies only the routing mode, leaving other pending settings untouched.
func (a *App) SetMode(mode string) {
	a.State.Settings.Mode = mode
	a.Save()
	if r := a.run; r != nil && a.Conn == StateOn {
		opts := r.opts
		opts.Mode = mode
		a.connect(a.ActiveID, opts, nil, change{})
		return
	}
	a.Reconnect()
}

// pending compares settings with committed core configs; an in-flight start counts as pending.
func (a *App) pending() bool {
	if a.Conn == StateOff {
		return false
	}
	r, f := a.run, a.frontRun
	if r == nil || f == nil {
		return true
	}
	o := a.xrayOptions()
	o.APIPort = r.opts.APIPort
	if o == r.opts {
		return false
	}
	backend, err := xray.BuildBackend(o, r.b)
	if err != nil || !bytes.Equal(backend, r.cfg) {
		return true
	}
	front, err := xray.BuildFront(o, f.f)
	return err != nil || !bytes.Equal(front, f.cfg) // e.g. a TUN option without TUN changes nothing
}

// connectionTarget returns the actual serving target only once it agrees with the
// UI's current plan. Checks must not label requests to an old listener as a new node.
// A no-op plan may allocate new snapshots but keeps the same committed target.
func (a *App) connectionTarget() *coreTarget {
	t := a.live.Load()
	if a.closing || a.Conn != StateOn || t == nil || t.id != a.ActiveID ||
		t.seq != a.pairSeq || t.connSeq != a.connSeq || a.run == nil || a.frontRun == nil ||
		t.run.core != a.run.core || !bytes.Equal(t.run.cfg, a.run.cfg) || !bytes.Equal(t.front.cfg, a.frontRun.cfg) {
		return nil
	}
	return t
}

// Checkable reports whether the connection can be checked now (TestReal, CheckOverview): connected, and
// with no change planned that the cores have yet to carry out.
func (a *App) Checkable() bool { return a.connectionTarget() != nil }

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

// Persist both proxy addresses before writing so failures and crashes remain recoverable.
func (a *App) enableProxy() {
	a.ProxyErr = ""
	addr := a.ProxyAddr()
	previousBackup := a.State.ProxyBackup
	backup := store.ProxyBackup{}
	if previousBackup != nil {
		backup = *previousBackup
	}
	if previousBackup == nil {
		prev, err := a.Proxy.Current()
		if err != nil {
			a.ProxyErr = "读取系统代理失败：" + err.Error()
			return
		}
		backup.Previous = prev
	}
	if backup.Applied != addr {
		backup.Pending = addr
	}
	a.State.ProxyBackup = &backup
	if err := a.saveState(); err != nil {
		a.State.ProxyBackup = previousBackup
		a.ProxyErr = "保存代理恢复信息失败，未修改系统代理：" + err.Error()
		return
	}
	if err := a.Proxy.Apply(sysproxy.Settings{Enabled: true, Server: addr, Override: sysproxy.Bypass}); err != nil {
		// Either address may be in place now: both stay recorded.
		a.ProxyErr = "设置系统代理失败：" + err.Error()
		return
	}
	if backup.Pending != "" {
		backup.Applied, backup.Pending = addr, ""
		a.State.ProxyBackup = &backup
		a.Save() // saved or not, the addresses recorded before include addr
	}
}

// restoreProxy restores the previous system proxy unless another program changed it since.
func (a *App) restoreProxy() {
	b := a.State.ProxyBackup
	if b == nil {
		return
	}
	cur, err := a.Proxy.Current()
	if err != nil {
		a.ProxyErr = "读取系统代理失败：" + err.Error()
		return
	}
	if cur.Enabled && b.Owns(cur.Server) {
		if err := a.Proxy.Apply(b.Previous); err != nil {
			a.ProxyErr = "恢复系统代理失败：" + err.Error()
			return
		}
	} else {
		a.QueueLog("[RayDock] 系统代理已被其它程序修改，保持不动")
	}
	a.State.ProxyBackup = nil
	a.ProxyErr = ""
	a.Save()
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
