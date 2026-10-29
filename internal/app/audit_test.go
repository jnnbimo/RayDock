package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"raydock/internal/node"
	"raydock/internal/xray"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMeasurementsRejectEditedConfiguration(t *testing.T) {
	for _, speed := range []bool{false, true} {
		t.Run(map[bool]string{false: "latency", true: "speed"}[speed], func(t *testing.T) {
			a, posts, _ := newRegressionApp(t)
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
			waitRegression(t, posts, func() bool { return !a.LatencyBusy && !a.SpeedBusy })
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
	a, _, _ := newRegressionApp(t)
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
	a, _, _ := newRegressionApp(t)
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

func TestPinEditedWhileInFlight(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	a.CertPin = func(ctx context.Context, n *node.Node) (string, error) {
		if n.Host == "old.localhost" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "old-pin", nil
		}
		return "new-pin", nil
	}
	a.ImportLinks("trojan://p@old.localhost:443?allowInsecure=1", "")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	id := a.State.Nodes[0].ID
	edited := *a.NodeByID(id)
	edited.Host = "new.localhost"
	if err := a.EditNode(edited); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitRegression(t, posts, func() bool { return a.NodeByID(id).AutoPin == "new-pin" })
	if a.pinning[id] {
		t.Fatal("completed pin still busy")
	}
}

func TestHotSwitchRejectsOldRealTest(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(502) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	a.ImportLinks("socks://127.0.0.1:9#a\nsocks://127.0.0.1:10#b", "")
	first, second := &a.State.Nodes[0], &a.State.Nodes[1]
	a.State.Settings.Port = port
	defer runningFake(t, a, first.ID)()
	a.live.Store(a.planned) // the old listener is already committed, not merely planned
	a.TestReal()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("not started")
	}
	seq := a.connSeq
	a.Connect(second.ID, nil)
	if a.ActiveID != second.ID || a.connSeq != seq {
		close(release)
		t.Fatalf("not a hot switch: %s", a.ConnErr)
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

func TestTUNWaitsForVersionBeforeStarting(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.Elevated = true
	a.State.Settings.TUN.Enabled = true
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 1)}
	gate := make(chan struct{})
	a.ReadCoreVersion = func(string) (string, error) { <-gate; return xray.MinTUNVersion, nil }
	a.ImportLinks("socks://127.0.0.1:9", "")
	if a.coreUsable(true) {
		t.Fatal("unknown version passed gate")
	}
	a.EnsureCore()
	a.Connect(a.State.Nodes[0].ID, nil)
	if !a.CoreBusy || !a.WaitCore {
		t.Fatal("did not wait for version")
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatal("core started before verification", err)
	}
	close(gate)
	waitRegression(t, posts, func() bool { return !a.CoreBusy && a.Conn == StateOff }) // placeholder fails only after verification
	if a.CoreVersion != xray.MinTUNVersion || a.WaitCore {
		t.Fatalf("version=%q wait=%v", a.CoreVersion, a.WaitCore)
	}
}

func TestTUNStartupUsesNewerBundle(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	if err := os.WriteFile(a.coreExe(), []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	a.BundledCore = t.TempDir()
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(a.BundledCore, name), []byte("new"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	a.ReadCoreVersion = func(exe string) (string, error) {
		b, err := os.ReadFile(exe)
		if err != nil {
			return "", err
		}
		if string(b) == "old" {
			return "26.3.27", nil
		}
		return "26.10.10", nil
	}
	a.Elevated = true
	a.State.Settings.TUN.Enabled = true
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.EnsureCore()
	a.Connect(a.State.Nodes[0].ID, nil)
	// The first callback must replace, not start, the unsupported old core.
	select {
	case f := <-posts:
		f()
	case <-time.After(time.Second):
		t.Fatal("no local version result")
	}
	if !a.WaitCore || !a.CoreBusy {
		t.Fatal("old core escaped pending installation")
	}
	if err := coreConfigWritten(a); err != nil {
		t.Fatal("old core started", err)
	}
	waitRegression(t, posts, func() bool { return !a.CoreBusy && a.Conn == StateOff })
	if a.CoreVersion != "26.10.10" {
		t.Fatal("wrong installed version", a.CoreVersion)
	}
	b, err := os.ReadFile(a.coreExe())
	if err != nil || string(b) != "new" {
		t.Fatalf("bundle not installed: %q %v", b, err)
	}
}

type gatedReleaseSource struct {
	OfficialCore
	started, release chan struct{}
}

func (s gatedReleaseSource) Latest(ctx context.Context, _ *http.Client) (xray.Release, error) {
	close(s.started)
	select {
	case <-s.release:
		return xray.Release{Tag: "v99.1.1"}, nil
	case <-ctx.Done():
		return xray.Release{}, ctx.Err()
	}
}

func TestStaleReleaseCannotOverrideInstallation(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.CoreVersion = "26.10.10"
	source := gatedReleaseSource{started: make(chan struct{}), release: make(chan struct{})}
	a.CoreSource = source
	a.CheckCoreUpdate()
	select {
	case <-source.started:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup not started")
	}
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 4)}
	a.InstallCore()
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
	revision, installErr := a.coreRevision, a.CoreErr
	close(source.release)
	select {
	case f := <-posts:
		f()
	case <-time.After(5 * time.Second):
		t.Fatal("no old release result")
	}
	if a.CoreBusy || a.coreRevision != revision || a.CoreLatest != "" || a.CoreErr != installErr {
		t.Fatal("stale release changed newer installation state")
	}
}

func TestInstalledVersionMustBeVerified(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	a.CoreVersion = "26.10.10"
	a.ReadCoreVersion = func(string) (string, error) { return "99.1.1", errors.New("invalid executable") }
	src := coreFilesIn(t, "new")
	a.coreJob("install", "安装", func(*http.Client) (string, error) { return src, nil })
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
	if a.CoreVersion != "" || a.coreUsable(true) || !strings.Contains(a.CoreErr, "invalid executable") {
		t.Fatalf("unverified version trusted: %q %q", a.CoreVersion, a.CoreErr)
	}
}

func TestCoreReplacementWaitsForPinWorkers(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	started, release, installed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a.CertPin = func(ctx context.Context, _ *node.Node) (string, error) {
		close(started)
		select {
		case <-release:
			return "pin", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	a.ReadCoreVersion = func(string) (string, error) { return xray.MinTUNVersion, nil }
	a.ImportLinks("trojan://p@localhost:443?allowInsecure=1", "")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pin not started")
	}
	src := coreFilesIn(t, "new")
	a.coreJob("install", "安装", func(*http.Client) (string, error) { close(installed); return src, nil })
	<-installed // the download is complete; the replacement waits for the worker
	waitRegression(t, posts, func() bool { return a.coreHold })
	waitOperation(t, a.coreDo(func() {}))
	if b, _ := os.ReadFile(a.coreExe()); string(b) == "new" {
		t.Fatal("files replaced while pin worker used core")
	}
	close(release)
	waitRegression(t, posts, func() bool { return !a.CoreBusy && a.State.Nodes[0].AutoPin == "pin" })
	if b, _ := os.ReadFile(a.coreExe()); string(b) != "new" {
		t.Fatal("replacement never completed")
	}
}

func TestLocalReadinessDoesNotWaitForReleaseLookup(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	placeholderCore(t, a)
	gate := make(chan struct{})
	defer close(gate)
	a.CoreSource = inspectingCoreSource{observed: make(chan string, 1), gate: gate}
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.10", nil }
	a.EnsureCore()
	waitRegression(t, posts, func() bool { return !a.CoreBusy })
	if a.CoreVersion != "26.10.10" {
		t.Fatal("version not published")
	}
	a.ImportLinks("socks://127.0.0.1:9", "")
	a.Connect(a.State.Nodes[0].ID, nil)
	waitRegression(t, posts, func() bool { return a.Conn == StateOff })
	if !strings.Contains(a.ConnErr, "核心") {
		t.Fatal("did not try verified core", a.ConnErr)
	}
}
