package app

import (
	"context"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"raydock/internal/certpin"
	"raydock/internal/localnet"
	"raydock/internal/node"
)

// PinCertificates fetches certificates for the requested nodes (all with no ids). File replacement
// waits for these workers; work deferred during replacement is reconsidered when the core is ready.
func (a *App) PinCertificates(ids ...string) {
	if a.closing || a.coreHold || (a.CertPin == nil && !a.CoreReady()) {
		return
	}
	var todo []node.Node
	for i := range a.State.Nodes {
		n := &a.State.Nodes[i]
		if (len(ids) == 0 || slices.Contains(ids, n.ID)) && certpin.Needed(n) && !a.pinning[n.ID] {
			a.pinning[n.ID] = true
			todo = append(todo, *n)
		}
	}
	if len(todo) == 0 {
		return
	}
	fetch, strategy := a.CertPin, a.State.Settings.DNSStrategy
	a.tests.Add(1)
	go func() {
		defer a.tests.Done()
		pins, errs := a.fetchPins(todo, fetch, strategy)
		a.Post(func() { a.pinned(todo, pins, errs) })
	}()
}

// Default batches serialize ownership of the dedicated resolver core; latency tests own another core.
func (a *App) fetchPins(todo []node.Node, fetch func(context.Context, *node.Node) (string, error), strategy string) ([]string, []error) {
	pins, errs := make([]string, len(todo)), make([]error, len(todo))
	if fetch == nil {
		a.pinMu.Lock()
		defer a.pinMu.Unlock()
		var resolver *net.Resolver
		iface, err := localnet.Find()
		if err == nil {
			err = a.workCtx.Err()
		}
		if err == nil {
			resolver, err = localResolver(a.coreExe(), filepath.Join(a.DataDir, "pin-resolver.json"), a.pinCore, iface, strategy)
		}
		defer a.pinCore.Stop()
		if err != nil {
			for i := range errs {
				errs[i] = err
			}
			return pins, errs
		}
		fetch = func(ctx context.Context, n *node.Node) (string, error) { return certpin.Fetch(ctx, n, iface, resolver) }
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range todo {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-a.workCtx.Done():
				errs[i] = a.workCtx.Err()
				return
			}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(a.workCtx, 8*time.Second)
			defer cancel()
			pins[i], errs[i] = fetch(ctx, &todo[i])
		})
	}
	wg.Wait()
	return pins, errs
}

// A stale completion frees its slot and reschedules the current configuration, not the old snapshot.
func (a *App) pinned(nodes []node.Node, pins []string, errs []error) {
	var changed, retry []string
	for i, was := range nodes {
		delete(a.pinning, was.ID)
		n := a.NodeByID(was.ID)
		if n == nil || !certpin.Needed(n) {
			continue
		}
		if !node.SameConfig(n, &was) {
			retry = append(retry, n.ID)
			continue
		}
		if errs[i] != nil {
			a.QueueLog("[RayDock] 读取 " + certpin.Describe(n) + " 的证书失败：" + errs[i].Error())
			continue
		}
		n.AutoPin = pins[i]
		a.nodeChanged(n.ID)
		changed = append(changed, n.ID)
		a.QueueLog("[RayDock] 已固定 " + certpin.Describe(n) + " 的证书 " + pins[i] + "（链接要求跳过证书验证，Xray 26 已不支持，改为只信任此证书）")
	}
	if len(retry) > 0 {
		a.PinCertificates(retry...)
	}
	if len(changed) == 0 {
		return
	}
	a.Save()
	a.SyncCores() // the cores take the pin if the connection or a dedicated port uses the node
}
