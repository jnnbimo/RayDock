package app

import "net/url"

func (a *App) StatePath() string       { return a.statePath() }
func (a *App) CoreExe() string         { return a.coreExe() }
func (a *App) ProxyActive() bool       { return a.proxyActive() }
func (a *App) UpstreamProxy() *url.URL { return a.upstreamProxy() }

var ShortErr = shortErr

func (a *App) Pinning(id string) bool { return a.pinning[id] }

// DescribePorts is how the log names the dedicated ports.
func (a *App) DescribePorts() string { return a.describePorts(a.portRoutes()) }

// BackendConfig is the config of the backend core planned, nil when the cores do not run.
func (a *App) BackendConfig() []byte {
	if a.planned == nil {
		return nil
	}
	return a.planned.run.cfg
}

// FrontConfig is the config of the front core planned, nil when the cores do not run.
func (a *App) FrontConfig() []byte {
	if a.planned == nil {
		return nil
	}
	return a.planned.front.cfg
}
