package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"raydock/internal/probe"
	"raydock/internal/xray"
)

// A check through a core that restarted while it ran is dropped, and the page checks again.
func TestOverviewDropsResultsAcrossRestart(t *testing.T) {
	a, posts, _ := newTestApp(t)
	oldURL, oldServices := probe.IPInfoURL, probe.Services
	t.Cleanup(func() { probe.IPInfoURL, probe.Services = oldURL, oldServices })
	probe.IPInfoURL, probe.Services = "http://ip.example/info", nil
	entered, release := make(chan struct{}, 1), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Write([]byte(`{"ip":"203.0.113.1","fraudScore":12}`))
	}))
	defer proxy.Close()
	defer close(release)
	connectedFake(t, a)
	a.running.core().opts.Port = proxy.Listener.Addr().(*net.TCPAddr).Port
	a.CheckOverview(false)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("overview did not start")
	}
	oldKey := a.Overview.Key
	a.RenewCores(change{geo: true})
	commitFake(a, a.planned)
	if a.OverviewCurrent() || oldKey == a.OverviewKey() {
		t.Fatal("overview did not follow the restart")
	}
	// End the request and consume its completion, without starting another check.
	release <- struct{}{}
	select {
	case post := <-posts:
		post()
	case <-time.After(time.Second):
		t.Fatal("overview did not finish")
	}
	if a.Overview.IP.IP != "" {
		t.Fatalf("result %q across a restart", a.Overview.IP.IP)
	}
}

// The check of the connection lands only in the check it was made for; another node checks again.
func TestOverviewCheck(t *testing.T) {
	a, posts, _ := newTestApp(t)
	old, oldServices := probe.IPInfoURL, probe.Services
	t.Cleanup(func() { probe.IPInfoURL, probe.Services = old, oldServices })
	probe.IPInfoURL = "http://ippure.example/v1/info"
	probe.Services = []probe.Service{{Name: "Site", Method: http.MethodGet, URL: "http://site.example/"}}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "ippure.example" {
			w.Write([]byte(`{"ip":"203.0.113.9","fraudScore":12,"isResidential":true}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	a.CheckOverview(false)
	if a.Overview.Key != "" {
		t.Fatal("nothing to check while not connected")
	}
	a.Conn, a.ActiveID, a.Since = StateOn, "n1", time.Now()
	a.runningPort = proxy.Listener.Addr().(*net.TCPAddr).Port
	a.connCtx = a.workCtx
	conn := &runningCore{coreSpec: coreSpec{opts: xray.Options{Port: a.runningPort}}, core: a.newCore()}
	target := &coreTarget{seq: a.coreSeq, connSeq: a.connSeq, id: a.ActiveID, ctx: a.workCtx, proc: conn}
	a.planned, a.running = target, target
	a.live.Store(conn)
	a.CheckOverview(false)
	if !a.Overview.Testing() || !a.OverviewCurrent() {
		t.Fatalf("checking %+v", a.Overview)
	}
	waitFor(t, posts, func() bool { return !a.Overview.Testing() })
	o := a.Overview
	if o.IP.IP != "203.0.113.9" || *o.IP.FraudScore != 12 || o.IPErr != "" || o.Services[0].Err != "" || o.Services[0].Name != "Site" {
		t.Fatalf("%+v", o)
	}
	a.CheckOverview(false)
	if a.Overview.Testing() {
		t.Fatal("checked already")
	}
	// Another node: the results checked before are stale, and a check of it starts.
	a.ActiveID = "n2"
	next := *target
	next.id = a.ActiveID
	a.planned, a.running = &next, &next
	if a.OverviewCurrent() {
		t.Fatal("stale")
	}
	a.CheckOverview(false)
	if !a.OverviewCurrent() || !a.Overview.Testing() {
		t.Fatalf("checking n2: %+v", a.Overview)
	}
	waitFor(t, posts, func() bool { return !a.Overview.Testing() })
	// Disconnected, nothing is current.
	a.Conn = StateOff
	if a.OverviewCurrent() {
		t.Fatal("not connected")
	}
}
