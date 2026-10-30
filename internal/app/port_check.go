package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"raydock/internal/xray"
)

type portCheck struct {
	port int
	key  string
	cfg  []byte
	err  string
}

// portCheckFor identifies the full port-chain configuration and the installed core revision. A changed
// chain, LAN binding or core version cannot reuse an earlier validation result.
func (a *App) portCheckFor(r xray.PortRoute, lan bool) (portCheck, error) {
	cfg, err := xray.Build(xray.Options{AllowLAN: lan}, xray.Run{Ports: []xray.PortRoute{r}})
	if err != nil {
		return portCheck{}, err
	}
	return portCheck{port: r.Port, key: fmt.Sprintf("%d:%x", a.coreRevision, sha256.Sum256(cfg)), cfg: cfg}, nil
}

// validatePorts runs on the serialized core queue, using immutable snapshots only. Rejected ports are
// quarantined on the UI and replanned while the old, healthy process stays running.
func (a *App) validatePorts(ctx context.Context, checks []portCheck) ([]portCheck, bool, error) {
	results := make([]portCheck, 0, len(checks))
	bad := false
	for _, c := range checks {
		err := xray.ValidateConfig(ctx, a.coreExe(), c.cfg)
		if err != nil {
			var rejected *xray.ConfigError
			if !errors.As(err, &rejected) {
				return results, false, err
			}
			c.err = err.Error()
			bad = true
		}
		c.cfg = nil
		results = append(results, c)
	}
	return results, bad, nil
}

func (a *App) recordPortChecks(checks []portCheck) {
	if a.portChecks == nil {
		a.portChecks = map[int]portCheck{}
	}
	for _, c := range checks {
		a.portChecks[c.port] = c
	}
}
