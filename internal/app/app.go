// Package app holds RayDock's state and logic: connections, nodes and groups, the core,
// logs and start at logon. It has no UI; package gui renders it.
package app

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"raydock/internal/node"
	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/system"
	"raydock/internal/xray"
)

// Deps connects the app to the outside world; tests replace them with fakes.
type Deps struct {
	Post      func(fn func()) // runs fn on the UI thread and redraws
	Clipboard interface {
		ReadText() string
		WriteText(string)
	}
	Proxy interface {
		Current() (sysproxy.Settings, error)
		Apply(sysproxy.Settings) error
	}
	SetAppearance func(int) // 0 system, 1 light, 2 dark
	OpenPath      func(string)
	DataDir       string
	Elevated      bool
	// RelaunchAdmin restarts elevated and quits; connect makes the new process connect at once.
	RelaunchAdmin func(connect bool) error
	FlushDNS      func()
	Autostart     interface {
		Get() system.StartMode
		Set(system.StartMode) error
	}
	CoreSource      CoreSource
	ReadCoreVersion func(exe string) (string, error) // nil uses xray.Version; tests can replace process I/O
	// CertPin returns the SHA-256 of the certificate n's server presents (see PinCertificates); nil for
	// certpin.Fetch through the local network.
	CertPin     func(ctx context.Context, n *node.Node) (string, error)
	BundledCore string // core shipped with the installer, may be empty
	Version     string
}

type ConnState int

const (
	StateOff ConnState = iota
	StateConnecting
	StateOn
)

type Latency struct {
	Duration time.Duration
	Err      string
	Testing  bool
	IP       string // exit IP sites see through the node (and its chain), when found
	IPErr    string // why the exit IP is unknown though the node answered
}

type LogLine struct {
	Text  string
	Level int // 0 info, 1 warning, 2 error
}

const MaxLogs = 2000

// App is only accessed on the UI thread; background work reports back through post.
type App struct {
	Deps
	State       store.State
	coreOps     chan func()        // the core operations, in UI issue order
	coreWant    latest[coreTarget] // the newest change of the running cores planned, for reconcile
	connCtx     context.Context    // ends with the connection
	connCancel  context.CancelFunc
	runningPort int // committed local port, distinct from edited settings
	workCtx     context.Context
	workCancel  context.CancelFunc
	jobs        sync.WaitGroup // core/rule-data replacement jobs
	closing     bool

	Conn     ConnState
	connSeq  int // bumped on every connect/disconnect to drop stale results
	ActiveID string
	Since    time.Time // connected to ActiveID since
	routeGen int       // bumped as a change of where the connection's requests go is made (coreTarget.reroute)
	ConnErr  string
	ProxyErr string
	RealTest Latency // end-to-end test through the proxy
	// realTests counts the tests started, so that only the latest sets RealTest.
	realTests int
	WaitCore  bool // connecting, waiting for the core download
	tunOn     bool // the running front core has TUN enabled
	// The cores of the connection and the dedicated ports (see cores.go) as planned: as they run once the
	// changes planned are carried out, nil until they run.
	front    *xray.Core // the front core's process, which only a restart replaces
	frontRun *frontCore
	run      *runningCore               // the backend core
	planned  *coreTarget                // the newest target set for the cores (or the one they start with)
	live     atomic.Pointer[coreTarget] // actual committed target; written by core operations, read by the UI
	procs    coreSet                    // the backend core processes, those finishing their connections too
	pairSeq  int                        // bumped whenever the cores start afresh or stop, to drop stale results
	// pairCtx ends when the cores start afresh or stop, ending the API calls made to them.
	pairCtx    context.Context
	pairCancel context.CancelFunc
	// drainCtx ends with the cores, stopping the backend cores finishing their connections; drains counts them.
	drainCtx    context.Context
	drainCancel context.CancelFunc
	drains      sync.WaitGroup

	Latency     map[string]Latency
	LatencyBusy bool

	// Core output is queued and handed to the UI thread in batches.
	Logs      []LogLine
	logMu     sync.Mutex
	logQueued []string

	pinning map[string]bool // nodes whose certificate is being fetched
	pinMu   sync.Mutex      // serializes background ownership of pinCore
	pinCore *xray.Core      // physical-network DNS for certificate pinning

	SubBusy map[string]bool // subscription group IDs being fetched
	subJobs map[string]*subscriptionJob

	// tests counts running latency and speed tests: they run cores from the core directory, which a core
	// install waits for. Tests start only while no install runs.
	tests sync.WaitGroup

	Speed       map[string]Speed
	SpeedBusy   bool
	speedCancel context.CancelFunc
	speedCore   *xray.Core // separate from the connection's core
	dnsCore     *xray.Core // resolves for latency tests through the local network
	latencyCore *xray.Core // serves the chains latency tests measure through, from the local network

	PortErrs    map[string]string // why a node's dedicated port does not work, by node ID
	frontPorts  map[int]bool      // the dedicated ports the front core may hold (portTaken)
	servedPorts string            // the dedicated ports last logged (notePorts)
	portBasis   portSettings      // the settings SyncCores last built from

	saveDue   bool        // the state changed since it was saved, which saveSoon will
	saveTimer *time.Timer // saveSoon's

	coreRevision int    // local version checks and installations invalidate older discovery results
	CoreVersion  string // verified installed version; empty means unknown
	CoreBusy     bool   // a core job runs: a version check, or a download or copy and the replacement
	// coreHold keeps everything that runs the core files (connections, ports, tests) from starting while
	// the files are being checked or replaced; a core download leaves them alone until it is complete.
	coreHold      bool
	CoreProgress  float64
	CoreMsg       string
	CoreErr       string // last download failure
	CoreLatest    string // newer release offered: found by CheckCoreUpdate, or bundled with a RayDock upgrade
	latestBundled bool   // CoreLatest is the bundled core, which UpdateCore installs offline
	CoreChecking  bool   // CheckCoreUpdate is looking for a release
	CoreNote      string // result of the last CheckCoreUpdate that offered nothing

	GeoBusy     bool
	GeoProgress float64
	GeoMsg      string // result of the last rule data update
	GeoErr      string
	afterGeo    func() // a core install waiting for the rule data update

	Overview Overview // the home page's check of the connection (overview.go)

	// Traffic by day, and the rates of the last minute (traffic.go).
	Traffic       store.Traffic
	Rates         []Rate
	trafficDue    bool          // changed since saved
	trafficBroken bool          // the saved traffic could not be read, nor set aside: it is not overwritten
	trafficTimer  *time.Timer   // saves the traffic a while after it moved
	trafficDone   chan struct{} // closes after the sampler exits; nil before StartTraffic

	AdminPrompt   bool // TUN needs elevation; the UI offers an elevated restart
	StartMode     system.StartMode
	StartBusy     bool
	StartErr      string
	SettingsDirty bool // changed while connected, needs a reconnect

	OnChange func() // notifies the tray of connection changes
}

func New(d Deps, st store.State) *App {
	if d.ReadCoreVersion == nil {
		d.ReadCoreVersion = xray.Version
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{
		Deps:        d,
		State:       st,
		front:       &xray.Core{},
		coreOps:     newOperationQueue(),
		workCtx:     ctx,
		workCancel:  cancel,
		subJobs:     map[string]*subscriptionJob{},
		Latency:     map[string]Latency{},
		SubBusy:     map[string]bool{},
		pinning:     map[string]bool{},
		pinCore:     &xray.Core{},
		Speed:       map[string]Speed{},
		speedCore:   &xray.Core{},
		dnsCore:     &xray.Core{},
		latencyCore: &xray.Core{},
		PortErrs:    map[string]string{},
	}
	// Background results may be queued before shutdown but delivered afterwards.
	a.Post = func(fn func()) {
		d.Post(func() {
			if !a.closing {
				fn()
			}
		})
	}
	a.front.OnLog = a.QueueLog
	a.drainCtx, a.drainCancel = context.WithCancel(ctx)
	a.pairCtx, a.pairCancel = context.WithCancel(ctx)
	a.loadTraffic()
	return a
}

func (a *App) changed() {
	if a.OnChange != nil {
		a.OnChange()
	}
}

func (a *App) statePath() string  { return filepath.Join(a.DataDir, "state.json") }
func (a *App) configPath() string { return filepath.Join(a.DataDir, "config.json") }
func (a *App) CoreDir() string    { return filepath.Join(a.DataDir, "core") }
func (a *App) coreExe() string    { return filepath.Join(a.CoreDir(), xray.ExeName()) }

func (a *App) CoreReady() bool { return len(xray.Missing(a.CoreDir())) == 0 }

func (a *App) Save() { _ = a.saveState() }

func (a *App) saveState() error {
	a.saveDue = false
	err := store.Save(a.statePath(), a.State)
	if err != nil {
		a.QueueLog("[RayDock] 保存设置失败：" + err.Error())
	}
	return err
}

func (a *App) NodeByID(id string) *node.Node {
	for i := range a.State.Nodes {
		if a.State.Nodes[i].ID == id {
			return &a.State.Nodes[i]
		}
	}
	return nil
}

func (a *App) Shutdown() {
	if a.closing {
		return
	}
	a.closing = true
	a.workCancel()
	a.cancelConnect()
	a.restoreProxy() // before the cores stop, as on disconnecting: programs go direct, not to a closed port
	a.StopSpeedTest()
	<-a.coreDo(a.stopCores)
	a.drains.Wait() // workCtx ended: they stop at once
	if a.trafficDone != nil {
		<-a.trafficDone
	}
	a.flushTraffic() // includes samples queued before closing and every backend's final reading
	a.procs.closeTraffic()
	a.endTUN()
	a.Conn, a.ActiveID, a.WaitCore, a.runningPort = StateOff, "", false, 0
	a.run, a.frontRun, a.planned = nil, nil, nil
	a.tests.Wait() // workers own their temporary cores until they finish
	a.speedCore.Stop()
	a.dnsCore.Stop()
	a.latencyCore.Stop()
	a.pinCore.Stop()
	a.jobs.Wait()
	_ = os.RemoveAll(a.geoDownloadDir())
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	if a.trafficTimer != nil {
		a.trafficTimer.Stop()
	}
	close(a.coreOps)
	a.Save()
	a.saveTraffic()
}

func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") {
		return "超时"
	}
	s := err.Error()
	if utf8.RuneCountInString(s) > 80 {
		s = string([]rune(s)[:80]) + "…"
	}
	return s
}

// saveDelay is how long saveSoon waits for edits to pause.
const saveDelay = 500 * time.Millisecond

// saveSoon saves once the edits pause: typing calls it on every key, and a save writes the whole state
// to disk. Shutdown saves what is left.
func (a *App) saveSoon() {
	a.saveDue = true
	if a.saveTimer == nil {
		a.saveTimer = time.AfterFunc(saveDelay, func() {
			a.Post(func() {
				if a.saveDue {
					a.Save()
				}
			})
		})
		return
	}
	a.saveTimer.Reset(saveDelay)
}

// portSettings are the settings the dedicated ports use.
type portSettings struct {
	allowLAN bool
	port     int
}

// SettingChanged saves soon (typing in a rule or DNS field calls it on every key); settings changed while
// connected take effect after a reconnect.
func (a *App) SettingChanged() {
	a.saveSoon()
	a.SettingsDirty = a.pending()
	// LAN access applies to the dedicated ports at once; a local port taking one of theirs sets it aside.
	if s := a.State.Settings; (portSettings{s.AllowLAN, s.Port}) != a.portBasis {
		a.SyncCores()
	}
}
