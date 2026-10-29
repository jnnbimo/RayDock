package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"raydock/internal/localnet"
	"raydock/internal/node"
	"raydock/internal/probe"
	"raydock/internal/xray"
)

// speedWindow is how long each direction is measured.
const speedWindow = 6 * time.Second

// Speed is the throughput through a node, in bytes per second.
type Speed struct {
	Down, Up float64
	Phase    string // while testing: 排队, 启动, 下载 or 上传
	Err      string
}

func (s Speed) Testing() bool { return s.Phase != "" }

// Tests measure from the local network: they reach node servers through the physical interface and
// resolve their names with its DNS servers, never through RayDock's connection, its TUN or another VPN's.

// TestLatency measures each node's latency from the local network, concurrently, and the exit IP sites see
// through it. A node reached over TCP by itself: the time to open a connection to its server. A node
// reached over UDP (Hysteria2, WireGuard, mKCP, MASQUE over HTTP/3), which a TCP handshake cannot reach,
// or one in a group with a front or back proxy, whose traffic never goes to its server directly: a round
// trip through its whole chain, so the result is the node's as used, and a broken node fails even when
// its front proxy works. Every node that answers is then asked its exit IP through its whole chain.
func (a *App) TestLatency(ids []string) error {
	if a.LatencyBusy {
		return errors.New("延迟测试进行中")
	}
	if a.closing || a.coreHold || !a.CoreReady() {
		return errors.New("Xray 核心尚未就绪，请稍后再试")
	}
	var jobs []latencyJob
	invalid := map[string]Latency{}
	for _, id := range ids {
		n := a.NodeByID(id)
		if n == nil {
			continue
		}
		a.Latency[id] = Latency{Testing: true}
		chain, err := a.Chain(n)
		if err != nil {
			a.QueueLog("[RayDock] 延迟测试 " + n.Label() + "：" + err.Error())
			invalid[id] = Latency{Err: "代理链无效"}
			continue
		}
		// A node the core refuses cannot be used at all, and in the latency core it would fail the others.
		if i := slices.IndexFunc(chain, func(c *node.Node) bool { return xray.Check(c) != nil }); i >= 0 {
			a.QueueLog(fmt.Sprintf("[RayDock] 延迟测试 %s：%s：%v", n.Label(), chain[i].Label(), xray.Check(chain[i])))
			invalid[id] = Latency{Err: "不可用"}
			continue
		}
		j := latencyJob{id: id, through: len(chain) > 1 || n.UDP(), source: a.measureSource(id)}
		for _, c := range chain {
			j.chain = append(j.chain, *c)
		}
		jobs = append(jobs, j)
	}
	if len(jobs)+len(invalid) == 0 {
		return nil
	}
	maps.Copy(a.Latency, invalid)
	if len(jobs) == 0 {
		return nil
	}
	a.LatencyBusy = true
	exe, strategy := a.coreExe(), a.State.Settings.DNSStrategy
	a.tests.Add(1)
	go func() {
		defer a.tests.Done()
		// Each result shows as soon as it is in.
		a.measureLatency(exe, strategy, jobs, func(j latencyJob, l Latency) {
			a.Post(func() {
				if a.measurementCurrent(j.id, j.source) {
					a.Latency[j.id] = l
				} else if a.Latency[j.id].Testing {
					delete(a.Latency, j.id) // measured with settings since changed: no result
				}
			})
		})
		a.Post(func() {
			a.LatencyBusy = false
			// Nodes whose result never came, the test ended early: they are not testing any more.
			for _, j := range jobs {
				if a.Latency[j.id].Testing {
					delete(a.Latency, j.id)
				}
			}
		})
	}()
	return nil
}

// measuredSource records the three possible hops even when a required hop is missing. A missing
// hop retains its ID, so a changed invalid route cannot accept an older task's generic error.
type measuredSource struct {
	nodes    [3]node.Node // front, target, back; unused slots are empty
	strategy string
}

func (a *App) measureSource(id string) measuredSource {
	s := measuredSource{strategy: a.State.Settings.DNSStrategy}
	if n := a.NodeByID(id); n != nil {
		s.nodes[1] = *n
		if g := a.GroupByID(n.GroupID); g != nil {
			for _, hop := range []struct {
				slot int
				id   string
			}{{0, g.Front}, {2, g.Back}} {
				s.nodes[hop.slot].ID = hop.id
				if n := a.NodeByID(hop.id); n != nil {
					s.nodes[hop.slot] = *n
				}
			}
		}
	}
	return s
}

// measurementCurrent runs only on the UI thread; metadata-only edits do not invalidate results.
func (a *App) measurementCurrent(id string, snapshot measuredSource) bool {
	if a.NodeByID(id) == nil {
		return false
	}
	current := a.measureSource(id)
	if current.strategy != snapshot.strategy {
		return false
	}
	for i, n := range current.nodes {
		was := &snapshot.nodes[i]
		if n.ID != was.ID || !node.SameConfig(&n, was) || n.AutoPin != was.AutoPin {
			return false
		}
	}
	return true
}

// latencyJob is a node to measure (copies: the goroutine must not read app state).
type latencyJob struct {
	source  measuredSource
	id      string
	chain   []node.Node // front proxy, the node, back proxy
	through bool        // timed through its chain rather than by a TCP handshake with its server
}

// measureLatency measures jobs and reports each with set.
func (a *App) measureLatency(exe, strategy string, jobs []latencyJob, set func(latencyJob, Latency)) {
	ports, err := a.startLatencyCore(exe, filepath.Join(a.DataDir, "latency.json"), strategy, jobs)
	if err != nil {
		a.QueueLog("[RayDock] 延迟测试：" + err.Error())
	} else {
		defer a.latencyCore.Stop()
	}
	var dialer *net.Dialer
	var resolver *net.Resolver
	var dialErr error
	if slices.ContainsFunc(jobs, func(j latencyJob) bool { return !j.through }) {
		if dialer, resolver, dialErr = a.localDialer(exe, filepath.Join(a.DataDir, "resolver.json"), strategy); dialErr != nil {
			a.QueueLog("[RayDock] 延迟测试：" + dialErr.Error())
		} else {
			defer a.dnsCore.Stop()
		}
	}
	family := ipFamily(strategy)
	run := func(j latencyJob) {
		port, routed := ports[j.id]
		var l Latency
		switch {
		case j.through && !routed, !j.through && dialErr != nil:
			l.Err = "失败"
		case j.through:
			l = throughLatency(a.workCtx, port)
		default:
			n := j.chain[0]
			l = tcpLatency(a.workCtx, dialer, resolver, family, n.Host, n.Port)
		}
		if l.Err == "" && routed {
			if ip, err := probe.ExitIP(a.workCtx, port, 8*time.Second); err == nil {
				l.IP = ip.String()
			} else {
				l.IPErr = "失败"
			}
		}
		set(j, l)
	}
	// The two kinds run side by side, each within its own limit (latencyLimits).
	var tcp, through []latencyJob
	for _, j := range jobs {
		if j.through {
			through = append(through, j)
		} else {
			tcp = append(tcp, j)
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() { forEach(a.workCtx, tcp, tcpTests, run) })
	wg.Go(func() { forEach(a.workCtx, through, throughTests, run) })
	wg.Wait()
}

// Latency tests run in parallel, but not so many at once that a home router or the network suffers:
// a test by TCP handshake is one short connection; a test through a chain sets up a whole proxy
// session, often over QUIC, and then asks the exit IP through it.
const (
	tcpTests     = 8
	throughTests = 4
	// testSpacing is the least time between starting two tests of a kind, so that a long list does not
	// open its first connections in one burst.
	testSpacing = 30 * time.Millisecond
)

// startLatencyCore runs the latency core: each job's chain (checked by TestLatency) on a local port, its
// first node reached through the local network (its name resolved there too). It returns the port of
// each job.
func (a *App) startLatencyCore(exe, cfgPath, strategy string, jobs []latencyJob) (map[string]int, error) {
	var routes []xray.PortRoute
	ports := map[string]int{}
	for _, j := range jobs {
		chain := make([]*node.Node, len(j.chain))
		for k := range j.chain {
			chain[k] = &j.chain[k]
		}
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		routes = append(routes, xray.PortRoute{Port: port, Chain: chain})
		ports[j.id] = port
	}
	iface, err := localnet.Find()
	if err != nil {
		return nil, err
	}
	var local *xray.Local
	if iface != nil {
		l := localXray(iface, strategy)
		local = &l
	}
	cfg, err := xray.BuildProbe(routes, local)
	if err == nil {
		err = a.latencyCore.Start(exe, cfgPath, cfg)
	}
	if err != nil {
		return nil, err
	}
	return ports, nil
}

// tcpLatency times opening a TCP connection to host:port through the local network.
func tcpLatency(ctx context.Context, dialer *net.Dialer, resolver *net.Resolver, family, host string, port int) Latency {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ip, err := lookup(ctx, resolver, family, host)
	if err != nil {
		return Latency{Err: "解析失败"}
	}
	d, err := probe.TCP(ctx, dialer, netip.AddrPortFrom(ip, uint16(port)), 3*time.Second)
	if err != nil {
		return Latency{Err: "超时"}
	}
	return Latency{Duration: d}
}

// throughLatency times a request through the latency core's port once the session is set up.
func throughLatency(ctx context.Context, port int) Latency {
	d, err := probe.Through(ctx, port, probe.TestURL, 8*time.Second)
	switch {
	case err == nil:
		return Latency{Duration: d}
	case shortErr(err) == "超时":
		return Latency{Err: "超时"}
	}
	return Latency{Err: "失败"}
}

// forEach runs fn for every item, at most limit at a time and starting them testSpacing apart, until
// ctx ends.
func forEach[T any](ctx context.Context, items []T, limit int, fn func(T)) {
	if len(items) == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, limit)
	pace := time.NewTicker(testSpacing)
	defer pace.Stop()
	for _, n := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		select {
		case <-pace.C:
		case <-ctx.Done():
			<-sem
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			fn(n)
		})
	}
	wg.Wait()
}

// localDialer returns a dialer bound to the local network and a resolver asking its DNS servers. Where
// RayDock runs a TUN (Windows) the queries go through a resolver core: outside a strict TUN only Xray
// may send DNS. It runs until dnsCore is stopped.
func (a *App) localDialer(exe, cfgPath, strategy string) (*net.Dialer, *net.Resolver, error) {
	iface, err := localnet.Find()
	if err != nil {
		return nil, nil, err
	}
	r, err := localResolver(exe, cfgPath, a.dnsCore, iface, strategy)
	return localnet.Dialer(iface), r, err
}

// localResolver is shared by measurements and certificate pinning. Each caller owns its core lifetime.
func localResolver(exe, cfgPath string, core *xray.Core, iface *localnet.Interface, strategy string) (*net.Resolver, error) {
	if iface == nil {
		return net.DefaultResolver, nil
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cfg, err := xray.BuildResolver(port, localXray(iface, strategy))
	if err != nil {
		return nil, err
	}
	if err := core.Start(exe, cfgPath, cfg); err != nil {
		return nil, err
	}
	server := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(port))
	return localnet.Resolver(localnet.Dialer(nil).DialContext, server), nil
}

func localXray(iface *localnet.Interface, strategy string) xray.Local {
	l := xray.Local{Interface: iface.Name, Strategy: strategy}
	for _, s := range iface.DNS {
		l.DNS = append(l.DNS, s.Addr().String())
	}
	return l
}

// ipFamily maps the DNS strategy setting to the address family to look up.
func ipFamily(strategy string) string {
	switch strategy {
	case "UseIPv6":
		return "ip6"
	case "UseIP":
		return "ip"
	}
	return "ip4"
}

func lookup(ctx context.Context, r *net.Resolver, family, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip, nil
	}
	ips, err := r.LookupNetIP(ctx, family, host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, errors.New("no address")
	}
	return ips[0].Unmap(), nil
}

// TestSpeed measures download and upload through each node in turn, including its group's front and back
// proxies. Each test runs its own core on a free port, so the connection in use is not touched; nodes are
// tested one at a time because parallel tests would share the bandwidth.
func (a *App) TestSpeed(ids []string) error {
	if a.SpeedBusy {
		return errors.New("测速进行中")
	}
	if a.closing || a.coreHold || !a.CoreReady() {
		return errors.New("Xray 核心尚未就绪，请稍后再试")
	}
	type job struct {
		id, name string
		source   measuredSource
		chain    []node.Node // copies: the goroutine must not read app state
		err      error
	}
	var jobs []job
	for _, id := range ids {
		n := a.NodeByID(id)
		if n == nil {
			continue
		}
		j := job{id: id, name: n.Label(), source: a.measureSource(id)}
		chain, err := a.Chain(n)
		j.err = err
		for _, c := range chain {
			j.chain = append(j.chain, *c)
		}
		jobs = append(jobs, j)
		a.Speed[id] = Speed{Phase: "排队"}
	}
	if len(jobs) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(a.workCtx)
	a.SpeedBusy, a.speedCancel = true, cancel
	exe, cfgPath, strategy := a.coreExe(), filepath.Join(a.DataDir, "speedtest.json"), a.State.Settings.DNSStrategy
	a.tests.Add(1)
	go func() {
		defer a.tests.Done()
		iface, ifaceErr := localnet.Find()
		defer a.Post(func() {
			cancel()
			a.SpeedBusy, a.speedCancel = false, nil
			for id, s := range a.Speed {
				if s.Testing() {
					delete(a.Speed, id) // stopped before its turn
				}
			}
		})
		for _, j := range jobs {
			if ctx.Err() != nil {
				return
			}
			phase := func(p string) {
				a.Post(func() {
					if a.measurementCurrent(j.id, j.source) {
						a.Speed[j.id] = Speed{Phase: p}
					}
				})
			}
			var s Speed
			switch {
			case ifaceErr != nil:
				s.Err = "无网络"
				a.QueueLog("[RayDock] 测速 " + j.name + "：" + ifaceErr.Error())
			case j.err != nil:
				s.Err = "代理链无效"
				a.QueueLog("[RayDock] 测速 " + j.name + "：" + j.err.Error())
			default:
				ptrs := make([]*node.Node, len(j.chain))
				for i := range j.chain {
					ptrs[i] = &j.chain[i]
				}
				var local *xray.Local
				if iface != nil {
					l := localXray(iface, strategy)
					local = &l
				}
				s = a.measure(ctx, exe, cfgPath, ptrs, local, phase)
				if s.Err != "" && ctx.Err() == nil {
					a.QueueLog("[RayDock] 测速 " + j.name + " 失败：" + s.Err)
				}
			}
			if ctx.Err() != nil {
				return
			}
			a.Post(func() {
				if a.measurementCurrent(j.id, j.source) {
					a.Speed[j.id] = s
				}
			})
		}
	}()
	return nil
}

// measure runs the speed-test core for chain and measures both directions through it.
func (a *App) measure(ctx context.Context, exe, cfgPath string, chain []*node.Node, local *xray.Local, phase func(string)) Speed {
	phase("启动")
	port, err := freePort()
	if err != nil {
		return Speed{Err: shortErr(err)}
	}
	cfg, err := xray.BuildProbe([]xray.PortRoute{{Port: port, Chain: chain}}, local)
	if err != nil {
		return Speed{Err: err.Error()}
	}
	if err := a.speedCore.Start(exe, cfgPath, cfg); err != nil {
		a.QueueLog("[RayDock] 测速：" + err.Error())
		return Speed{Err: "核心启动失败"}
	}
	defer a.speedCore.Stop()
	client := probe.ProxyClient(port)
	phase("下载")
	down, err := probe.Download(ctx, client, probe.DownloadURL, speedWindow)
	if err != nil {
		return Speed{Err: "下载失败：" + shortErr(err)}
	}
	phase("上传")
	up, err := probe.Upload(ctx, client, probe.UploadURL, speedWindow)
	if err != nil {
		return Speed{Down: down, Err: "上传失败：" + shortErr(err)}
	}
	return Speed{Down: down, Up: up}
}

// StopSpeedTest cancels the running speed test; results so far stay.
func (a *App) StopSpeedTest() {
	if a.speedCancel != nil {
		a.speedCancel()
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
