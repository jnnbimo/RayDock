package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// A running backend core switches to other routing rules without a restart, which would end every
// connection through it, direct ones too. Anything else it runs, its outbounds included, takes a new
// backend core beside it (see BuildBackend).

// CanSwitch reports whether a backend core running cur can become one running next by Switch: whether
// they differ in their routing rules alone. Inbounds, outbounds, DNS, the routing's domain strategy and
// everything else are read at start only.
func CanSwitch(cur, next []byte) bool {
	a, errA := switchBase(cur)
	b, errB := switchBase(next)
	return errA == nil && errB == nil && reflect.DeepEqual(a, b)
}

func switchBase(cfg []byte) (obj, error) {
	var m obj
	if err := json.Unmarshal(cfg, &m); err != nil {
		return nil, err
	}
	if r, ok := m["routing"].(map[string]any); ok {
		delete(r, "rules")
	}
	return m, nil
}

func tagOf(o any) string {
	m, _ := o.(map[string]any)
	s, _ := m["tag"].(string)
	return s
}

// SwitchParts returns the routing Switch sends a core to run next.
func SwitchParts(next []byte) ([]byte, error) {
	var m obj
	if err := json.Unmarshal(next, &m); err != nil {
		return nil, err
	}
	return json.Marshal(obj{"routing": m["routing"]})
}

// API reaches a running core's API (Options.APIPort) through "xray api", the core's own client.
type API struct {
	Exe  string // the core
	Addr string // 127.0.0.1:port
	// Ctx, when set, ends the calls with what they are made for (a connection, the app): nil leaves them
	// to their timeout.
	Ctx context.Context
}

// Switch replaces the routing rules of a running core with routing (SwitchParts), in one step.
func (p API) Switch(routing []byte) error { return p.call(routing, "adrules") }

func (p API) call(stdin []byte, cmd string, args ...string) error {
	parent := p.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, p.Exe, append([]string{"api", cmd, "--server=" + p.Addr}, args...)...)
	c.Dir = filepath.Dir(p.Exe)
	c.Env = assetEnv(p.Exe)
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	hideWindow(c)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("xray api %s：%w：%s", cmd, err, lastLines(strings.TrimSpace(string(out)), 3))
	}
	return nil
}
