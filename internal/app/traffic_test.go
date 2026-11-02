package app

import (
	"encoding/binary"
	"fmt"
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

	"raydock/internal/store"
	"raydock/internal/xray"
)

// What moved adds up by day and by month, rates cover the last minute, rising from zero after an idle
// spell, and the totals survive a restart without the days too old to keep.
func TestTrafficTotals(t *testing.T) {
	a, _, _ := newTestApp(t)
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

// The loop reads each core's counts and reports what moved since its last reading; a core gone
// is dropped.
func TestTrafficLoop(t *testing.T) {
	a, posts, _ := newTestApp(t)
	var down atomic.Int64
	core := &xray.Core{}
	port, client := statsAPI(t, down.Load)
	a.procs.add(core, port, client)
	down.Store(500)
	a.StartTraffic()
	waitFor(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 500 })
	down.Store(800)
	waitFor(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 800 })
	if n := len(a.Rates); n == 0 || a.Rates[n-1].Down <= 0 {
		t.Fatalf("rates %+v", a.Rates)
	}
	a.procs.remove(core)
	waitFor(t, posts, func() bool { n := len(a.Rates); return a.Rates[n-1].Down == 0 })
}

// statsAPI answers QueryStats as the core's API does (xray.ReadTraffic), over HTTP/2 without TLS: the
// proxy has moved down() bytes down. It returns the API's port and a client for it.
func statsAPI(t *testing.T, down func() int64) (int, *http.Client) {
	t.Helper()
	field := func(num int, b []byte) []byte {
		out := binary.AppendUvarint(nil, uint64(num)<<3|2)
		return append(binary.AppendUvarint(out, uint64(len(b))), b...)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s := field(1, []byte("outbound>>>node-0a1b2c3d-proxy>>>traffic>>>downlink"))
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
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return srv.Listener.Addr().(*net.TCPAddr).Port, &http.Client{Timeout: time.Second, Transport: &http.Transport{Protocols: p}}
}

func TestTrafficSaveRetriesAfterFailure(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.addTraffic(time.Now(), time.Second, xray.Traffic{ProxyDown: 123})
	if err := os.Mkdir(a.trafficPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	a.saveTraffic()
	if !a.trafficDue || a.trafficTimer == nil {
		t.Fatal("failed save consumed the pending data or its retry")
	}
	if err := os.Remove(a.trafficPath()); err != nil {
		t.Fatal(err)
	}
	a.Shutdown() // no new traffic: final save still has to retry
	saved, err := store.LoadTraffic(a.trafficPath())
	if err != nil || saved.Days[time.Now().Format(time.DateOnly)].ProxyDown != 123 || a.trafficDue {
		t.Fatalf("retry did not persist the pending bytes: %+v %v", saved, err)
	}
}

func TestTrafficSettlesRetiredCores(t *testing.T) {
	for _, take := range []bool{false, true} {
		t.Run(fmt.Sprint("take=", take), func(t *testing.T) {
			a, posts, _ := newTestApp(t)
			var down atomic.Int64
			c := &xray.Core{}
			port, client := statsAPI(t, down.Load)
			a.procs.add(c, port, client)
			down.Store(500)
			a.StartTraffic()
			a.StartTraffic() // must not create a second sampler
			waitFor(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 500 })
			down.Store(800)
			if take {
				a.procs.take()
			} else {
				a.procs.remove(c)
			}
			waitFor(t, posts, func() bool { return a.TrafficOn(time.Now()).ProxyDown == 800 })
			a.Shutdown()
			if a.TrafficOn(time.Now()).ProxyDown != 800 {
				t.Fatal("final reading was lost or double-counted")
			}
		})
	}
}

func TestShutdownSavesQueuedAndFinalTraffic(t *testing.T) {
	a, _, _ := newTestApp(t)
	var down atomic.Int64
	c := &xray.Core{}
	port, client := statsAPI(t, down.Load)
	a.procs.add(c, port, client)
	down.Store(500)
	a.procs.sampleTraffic(a.workCtx) // intentionally not yet delivered to the UI
	down.Store(800)
	a.Shutdown() // canceled workCtx must not cancel the final reading
	saved, err := store.LoadTraffic(a.trafficPath())
	if err != nil || saved.Days[time.Now().Format(time.DateOnly)].ProxyDown != 800 {
		t.Fatalf("shutdown lost queued or final bytes: %+v %v", saved, err)
	}
}
