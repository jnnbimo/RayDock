package app

import (
	"fmt"
	"time"

	"raydock/internal/probe"
)

// The home page checks the connection: what its exit IP is, and which services it reaches. Results belong
// to the core they were checked through; another start of it makes them stale.

// checkTimeout bounds each check.
const checkTimeout = 10 * time.Second

// ServiceCheck is a service of probe.Services as checked through the connection.
type ServiceCheck struct {
	Name    string
	AI      bool
	Testing bool
	Latency time.Duration
	Refused string // the service turns the exit IP away: why
	Err     string // the service was not reached
}

// Overview is the last check of the connection.
type Overview struct {
	Key       string // the connection checked (OverviewKey)
	IPTesting bool
	IP        probe.IPInfo
	IPErr     string
	Services  []ServiceCheck
}

// Testing reports whether a check runs.
func (o *Overview) Testing() bool {
	if o.IPTesting {
		return true
	}
	for _, s := range o.Services {
		if s.Testing {
			return true
		}
	}
	return false
}

// OverviewKey names the core the connection's requests take: the connection (its node and start) and
// the starts of its core since (routeGen). A change planned is made, and changes the key, before a check
// can start through it (Checkable).
func (a *App) OverviewKey() string {
	return fmt.Sprintf("%s@%d#%d", a.ActiveID, a.Since.UnixNano(), a.routeGen)
}

// OverviewCurrent reports whether the overview belongs to the displayed connection generation.
func (a *App) OverviewCurrent() bool { return a.Conn == StateOn && a.Overview.Key == a.OverviewKey() }

// CheckOverview checks the connection, unless it was checked already and not again.
func (a *App) CheckOverview(again bool) {
	target := a.connCore()
	if target == nil || a.OverviewCurrent() && (!again || a.Overview.Testing()) {
		return
	}
	key, ctx, port := a.OverviewKey(), a.connCtx, target.opts.Port
	o := Overview{Key: key, IPTesting: true, Services: make([]ServiceCheck, len(probe.Services))}
	for i, s := range probe.Services {
		o.Services[i] = ServiceCheck{Name: s.Name, AI: s.AI, Testing: true}
	}
	a.Overview = o
	// Results only land in the check they were made for, through the core they were made through.
	post := func(fn func(o *Overview)) {
		a.Post(func() {
			if a.Overview.Key == key && a.OverviewCurrent() {
				fn(&a.Overview)
			}
		})
	}
	go func() {
		info, err := probe.ExitInfo(ctx, port, checkTimeout)
		post(func(o *Overview) {
			o.IPTesting, o.IP, o.IPErr = false, info, ""
			if err != nil {
				o.IPErr = shortErr(err)
			}
		})
	}()
	for i, s := range probe.Services {
		go func() {
			r, err := probe.CheckService(ctx, port, s, checkTimeout)
			post(func(o *Overview) {
				c := &o.Services[i]
				c.Testing, c.Latency, c.Refused = false, r.Latency, r.Refused
				if err != nil {
					c.Err = shortErr(err)
				}
			})
		}()
	}
}
