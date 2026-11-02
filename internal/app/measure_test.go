package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"raydock/internal/node"
	"raydock/internal/xray"
)

// Latency tests never run more than their limit at once, and start spaced out.
func TestForEachLimitsAndSpaces(t *testing.T) {
	var running, most atomic.Int32
	var mu sync.Mutex
	var starts []time.Time
	items := make([]int, 12)
	forEach(context.Background(), items, 3, func(int) {
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
	})
	if len(starts) != len(items) || most.Load() > 3 {
		t.Fatalf("ran %d, at most %d at once", len(starts), most.Load())
	}
	// Ticks may bunch after a slow consumer, but the whole run cannot be faster than its spacing.
	if span := starts[len(starts)-1].Sub(starts[0]); span < time.Duration(len(items)-2)*testSpacing {
		t.Fatalf("12 starts within %v", span)
	}
}

// An ended context stops starting tests and returns once those running finish.
func TestForEachStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int32
	done := make(chan struct{})
	go func() {
		forEach(ctx, make([]int, 100), 2, func(int) {
			if ran.Add(1) == 2 {
				cancel()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forEach did not return after cancel")
	}
	if n := ran.Load(); n >= 100 {
		t.Fatalf("kept starting after cancel: %d", n)
	}
}

func TestMeasurementsRejectEditedConfiguration(t *testing.T) {
	for _, speed := range []bool{false, true} {
		t.Run(map[bool]string{false: "latency", true: "speed"}[speed], func(t *testing.T) {
			a, posts, _ := newTestApp(t)
			placeholderCore(t, a)
			a.ImportLinks("hy2://old@127.0.0.1:9#test", "")
			id := a.State.Nodes[0].ID
			var err error
			if speed {
				err = a.TestSpeed([]string{id})
			} else {
				err = a.TestLatency([]string{id})
			}
			if err != nil {
				t.Fatal(err)
			}
			edited := *a.NodeByID(id)
			edited.Password = "new"
			if err := a.EditNode(edited); err != nil {
				t.Fatal(err)
			}
			waitFor(t, posts, func() bool { return !a.LatencyBusy && !a.SpeedBusy })
			if l, ok := a.Latency[id]; ok {
				t.Fatalf("old latency restored: %+v", l)
			}
			if s, ok := a.Speed[id]; ok {
				t.Fatalf("old speed restored: %+v", s)
			}
		})
	}
}

func TestMeasurementChainInvalidation(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.ImportLinks("socks://127.0.0.1:9#front", "")
	front := a.State.Nodes[0].ID
	group, err := a.AddGroup(GroupInput{Name: "chained", Front: front})
	if err != nil {
		t.Fatal(err)
	}
	a.ImportLinks("socks://127.0.0.1:10#member", group)
	member := a.State.Nodes[1].ID
	a.ImportLinks("socks://127.0.0.1:11#unrelated", "")
	unrelated := a.State.Nodes[2].ID
	snapshot := a.measureSource(member)
	if !a.measurementCurrent(member, snapshot) {
		t.Fatal("fresh snapshot not current")
	}
	for _, id := range []string{front, member, unrelated} {
		a.Latency[id] = Latency{Duration: time.Second}
		a.Speed[id] = Speed{Down: 1}
	}
	edited := *a.NodeByID(front)
	edited.Port = 12
	if err := a.EditNode(edited); err != nil {
		t.Fatal(err)
	}
	if a.measurementCurrent(member, snapshot) {
		t.Fatal("changed hop accepted")
	}
	if _, ok := a.Latency[member]; ok {
		t.Fatal("dependent latency retained")
	}
	if _, ok := a.Speed[member]; ok {
		t.Fatal("dependent speed retained")
	}
	if _, ok := a.Latency[unrelated]; !ok {
		t.Fatal("unrelated result invalidated")
	}
	a.Latency[member] = Latency{Duration: time.Second}
	if err := a.EditGroup(group, GroupInput{Name: "chained"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Latency[member]; ok {
		t.Fatal("chain edit retained result")
	}
}

func TestMeasurementSourceIncludesMissingHops(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.ImportLinks("socks://127.0.0.1:9#test", "")
	id := a.State.Nodes[0].ID
	g := a.GroupByID(a.State.Nodes[0].GroupID)
	g.Front = "missing-one"
	snapshot := a.measureSource(id)
	if !a.measurementCurrent(id, snapshot) {
		t.Fatal("unchanged invalid route should still report its error")
	}
	g.Front = "missing-two"
	if a.measurementCurrent(id, snapshot) {
		t.Fatal("changed missing reference accepted old result")
	}
	g.Front = ""
	snapshot = a.measureSource(id)
	a.State.Nodes[0].Name = "renamed"
	if !a.measurementCurrent(id, snapshot) {
		t.Fatal("metadata invalidated a measurement")
	}
	a.State.Settings.DNSStrategy = "UseIPv6"
	if a.measurementCurrent(id, snapshot) {
		t.Fatal("DNS strategy change accepted old result")
	}
}

// A test through the core before a switch reports nothing for the node switched to.
func TestSwitchRejectsOldRealTest(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	started, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(502) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	a.ImportLinks("socks://127.0.0.1:9#a\nsocks://127.0.0.1:10#b", "")
	first, second := &a.State.Nodes[0], &a.State.Nodes[1]
	a.State.Settings.Port = port
	defer runningFake(t, a, first.ID)()
	liveFake(a, a.planned) // the old listener is already committed, not merely planned
	a.TestReal()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("not started")
	}
	a.Connect(second.ID, nil)
	if a.ActiveID != second.ID || a.planned.id != second.ID {
		close(release)
		t.Fatalf("not switched: %s", a.ConnErr)
	}
	close(release)
	select {
	case f := <-posts:
		f()
	case <-time.After(time.Second):
		t.Fatal("no result")
	}
	if a.RealTest != (Latency{}) {
		t.Fatalf("old result entered new connection: %+v", a.RealTest)
	}
}

// A plan that changes nothing the connection runs (a pin, a subscription, a dedicated port of nothing)
// leaves the core: the end-to-end test in flight still reports. A test overtaken by a restart ends without
// a result rather than testing forever.
func TestRealTestOutlivesReplanning(t *testing.T) {
	a, posts, _ := newTestApp(t)
	connectedFake(t, a)
	plan := a.planned
	a.TestReal()
	a.SyncCores()
	if a.planned != plan || a.Conn != StateOn {
		t.Fatal("the no-op plan should leave the core")
	}
	waitFor(t, posts, func() bool { return !a.RealTest.Testing })
	if a.RealTest.Err == "" {
		t.Fatalf("result lost: %+v", a.RealTest)
	}

	a.TestReal()
	a.SetMode(xray.ModeGlobal) // a restart
	waitFor(t, posts, func() bool { return !a.RealTest.Testing })
	if a.RealTest != (Latency{}) {
		t.Fatalf("a result of the core replaced must not show: %+v", a.RealTest)
	}

	// The restart has now committed; only the latest test sets its result.
	commitFake(a, a.planned)
	a.TestReal()
	a.TestReal()
	waitFor(t, posts, func() bool { return !a.RealTest.Testing })
	time.Sleep(100 * time.Millisecond)
	drainPosts(posts)
	if a.RealTest.Testing || a.RealTest.Err == "" {
		t.Fatalf("latest test: %+v", a.RealTest)
	}
}

// Results measured with settings changed during the test are dropped, and their nodes stop testing.
func TestLatencyStaleResultEndsTesting(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("trojan://p@127.0.0.1:1#n", "")
	id := a.State.Nodes[0].ID
	if err := a.TestLatency([]string{id}); err != nil {
		t.Fatal(err)
	}
	if !a.Latency[id].Testing {
		t.Fatal("testing")
	}
	a.State.Settings.DNSStrategy = "UseIP"
	a.SettingChanged()
	waitFor(t, posts, func() bool { return !a.LatencyBusy })
	if l, ok := a.Latency[id]; ok {
		t.Fatalf("a result of other settings, or none, must not stay: %+v", l)
	}
}

// A node in a group with a front proxy is measured through its whole chain, never by connecting to its
// own server: the latency core gets the chain (the placeholder core then fails to run, which fails the
// node instead of reporting the front proxy's reachability). Every node gets a route there, to look its
// exit IP up through; a node Xray refuses is marked unusable and left out, so it cannot fail the others.
func TestLatencyThroughChain(t *testing.T) {
	a, posts, _ := newTestApp(t)
	placeholderCore(t, a)
	a.ImportLinks("socks://front.example:1080#front", "")
	front := a.State.Nodes[0].ID
	g, err := a.AddGroup(GroupInput{Name: "chained", Front: front})
	if err != nil {
		t.Fatal(err)
	}
	a.ImportLinks("socks://member.example:3333#member", g)
	member := a.State.Nodes[1].ID
	a.ImportLinks("socks://plain.example:1080#plain", "")
	plain := a.State.Nodes[2].ID
	// VLESS in plain text to a public address: Xray refuses to start with it.
	a.State.Nodes = append(a.State.Nodes, node.Node{ID: "refused", GroupID: a.State.Nodes[2].GroupID, Name: "refused",
		Protocol: node.VLESS, Host: "203.0.113.1.nip.io", Port: 80, UUID: "u", Network: "ws"})
	if err := a.TestLatency([]string{member, plain, "refused"}); err != nil {
		t.Fatal(err)
	}
	if l := a.Latency["refused"]; l.Err != "不可用" || l.Testing {
		t.Fatalf("refused node: %+v", l)
	}
	waitFor(t, posts, func() bool { return !a.LatencyBusy })
	cfg, err := os.ReadFile(filepath.Join(a.DataDir, "latency.json"))
	if err != nil {
		t.Fatalf("not measured through the chain: %v", err)
	}
	for _, want := range []string{`"front.example"`, `"member.example"`, `"dialerProxy"`, `"plain.example"`} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("latency config lacks %s:\n%s", want, cfg)
		}
	}
	if strings.Contains(string(cfg), "nip.io") {
		t.Errorf("the refused node reached the latency core:\n%s", cfg)
	}
	if l := a.Latency[member]; l.Err != "失败" || l.Duration != 0 || l.IP != "" {
		t.Fatalf("latency %+v", l)
	}
}
