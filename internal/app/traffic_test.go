package app

import (
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"raydock/internal/probe"
	"raydock/internal/store"
	"raydock/internal/xray"
)

// What moved adds up by day and by month, rates cover the last minute, rising from zero after an idle
// spell, and the totals survive a restart without the days too old to keep.
func TestTrafficTotals(t *testing.T) {
	a, _, _ := newRegressionApp(t)
	day := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	sec := func(n int) time.Time { return day.Add(time.Duration(n) * time.Second) }
	a.addTraffic(sec(0), time.Second, xray.Traffic{ProxyUp: 100, ProxyDown: 1000, DirectDown: 10})
	a.addTraffic(sec(1), time.Second, xray.Traffic{ProxyDown: 2000})
	a.addTraffic(sec(30), time.Second, xray.Traffic{DirectUp: 4, DirectDown: 6})
	a.addTraffic(day.AddDate(0, 0, -1), time.Second, xray.Traffic{ProxyDown: 7})
	a.addTraffic(day.AddDate(0, -1, 0), time.Second, xray.Traffic{ProxyDown: 9})

	if got := a.TrafficOn(day); got != (xray.Traffic{ProxyUp: 100, ProxyDown: 3000, DirectUp: 4, DirectDown: 16}) {
		t.Fatalf("today %+v", got)
	}
	if got := a.TrafficMonth(day); got.ProxyDown != 3007 {
		t.Fatalf("month %+v", got)
	}
	// The rates before the last minute go, but one where the line enters the window.
	a.Rates = nil
	a.addTraffic(sec(0), time.Second, xray.Traffic{ProxyDown: 1 << 20})
	a.addTraffic(sec(1), time.Second, xray.Traffic{})
	a.addTraffic(sec(40), 2*time.Second, xray.Traffic{ProxyDown: 4 << 20})
	if len(a.Rates) != 4 || a.Rates[2].At != sec(38) || a.Rates[2].Down != 0 || a.Rates[3].Down != 2<<20 {
		t.Fatalf("rates %+v", a.Rates)
	}
	a.addTraffic(sec(100), time.Second, xray.Traffic{})
	if len(a.Rates) != 4 || a.Rates[0].At != sec(38) || a.Rates[1].At != sec(40) || a.Rates[2].At != sec(99) {
		t.Fatalf("pruned rates %+v", a.Rates)
	}

	old := time.Now().AddDate(0, 0, -trafficDays-1).Format(time.DateOnly)
	a.Traffic.Days[old] = xray.Traffic{ProxyUp: 1}
	a.saveTraffic()
	if a.trafficDue {
		t.Fatal("saved")
	}
	b := New(Deps{DataDir: a.DataDir, Post: func(func()) {}}, store.State{Settings: store.DefaultSettings()})
	t.Cleanup(b.Shutdown)
	if b.TrafficOn(day) != a.TrafficOn(day) {
		t.Fatalf("reloaded %+v", b.Traffic)
	}
	if _, ok := b.Traffic.Days[old]; ok {
		t.Fatal("too old to keep")
	}
}

// A file that cannot be read is set aside, not overwritten.
func TestTrafficCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traffic.json")
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(Deps{DataDir: dir, Post: func(func()) {}}, store.State{Settings: store.DefaultSettings()})
	t.Cleanup(a.Shutdown)
	bad, _ := filepath.Glob(path + ".bad-*")
	if len(bad) != 1 || a.trafficBroken || !strings.Contains(strings.Join(a.logQueued, "\n"), "已备份") {
		t.Fatalf("set aside %v, broken %v, log %q", bad, a.trafficBroken, a.logQueued)
	}
	if data, _ := os.ReadFile(bad[0]); string(data) != "{bad" {
		t.Fatalf("kept %q", data)
	}
}

// The loop reads each backend core's counts and reports what moved since its last reading; a core gone
// is dropped.
func TestTrafficLoop(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	var down atomic.Int64
	core := &xray.Core{}
	a.procs.add(core, statsAPI(t, down.Load))
	down.Store(500)
	a.StartTraffic()
	waitRegression(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 500 })
	down.Store(800)
	waitRegression(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 800 })
	if n := len(a.Rates); n == 0 || a.Rates[n-1].Down <= 0 {
		t.Fatalf("rates %+v", a.Rates)
	}
	a.procs.remove(core)
	waitRegression(t, posts, func() bool { n := len(a.Rates); return a.Rates[n-1].Down == 0 })
}

// The check of the connection lands only in the check it was made for; another node checks again.
func TestOverviewCheck(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
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
	a.run = &runningCore{opts: xray.Options{Port: a.runningPort}}
	a.frontRun = &frontCore{}
	target := &coreTarget{seq: a.pairSeq, connSeq: a.connSeq, id: a.ActiveID, run: a.run, front: a.frontRun, ctx: a.workCtx}
	a.live.Store(target)
	a.CheckOverview(false)
	if !a.Overview.Testing() || !a.OverviewCurrent() {
		t.Fatalf("checking %+v", a.Overview)
	}
	waitRegression(t, posts, func() bool { return !a.Overview.Testing() })
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
	a.live.Store(&next)
	if a.OverviewCurrent() {
		t.Fatal("stale")
	}
	a.CheckOverview(false)
	if !a.OverviewCurrent() || !a.Overview.Testing() {
		t.Fatalf("checking n2: %+v", a.Overview)
	}
	waitRegression(t, posts, func() bool { return !a.Overview.Testing() })
	// Disconnected, nothing is current.
	a.Conn = StateOff
	if a.OverviewCurrent() {
		t.Fatal("not connected")
	}
}

// statsAPI answers QueryStats as a backend core's API does (xray.ReadTraffic), over HTTP/2 without TLS: the
// proxy has moved down() bytes down. It returns the API's port.
func statsAPI(t *testing.T, down func() int64) int {
	t.Helper()
	field := func(num int, b []byte) []byte {
		out := binary.AppendUvarint(nil, uint64(num)<<3|2)
		return append(binary.AppendUvarint(out, uint64(len(b))), b...)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s := field(1, []byte("outbound>>>proxy>>>traffic>>>downlink"))
		s = binary.AppendUvarint(append(s, 2<<3), uint64(down()))
		msg := field(1, s)
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.Write(binary.BigEndian.AppendUint32([]byte{0}, uint32(len(msg))))
		w.Write(msg)
		w.Header().Set("Grpc-Status", "0")
	}))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}
