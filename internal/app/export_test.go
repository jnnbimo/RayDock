package app

import "net/url"

func (a *App) StatePath() string       { return a.statePath() }
func (a *App) CoreExe() string         { return a.coreExe() }
func (a *App) ProxyActive() bool       { return a.proxyActive() }
func (a *App) UpstreamProxy() *url.URL { return a.upstreamProxy() }

var ShortErr = shortErr

func (a *App) Pinning(id string) bool { return a.pinning[id] }

// DescribePorts is how the log names the dedicated ports.
func (a *App) DescribePorts() string { return a.describePorts(a.portRoutes(a.State.Settings.Port)) }

// CoreConfig is the config of the core planned, on the first adapter; nil without one.
func (a *App) CoreConfig() []byte {
	if r := a.planned.core(); r != nil {
		return r.cfg
	}
	return nil
}
