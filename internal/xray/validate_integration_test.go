//go:build integration

package xray

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationPortValidationIncludesHandlerConstruction(t *testing.T) {
	exe := coreForTest(t)
	cfg := []byte(`{"log":{"loglevel":"warning"},"outbounds":[{"protocol":"wireguard","settings":{"secretKey":"1111111111111111111111111111111111111111111111111111111111111111","noKernelTun":true}}]}`)
	path := filepath.Join(t.TempDir(), "syntax.json")
	if err := os.WriteFile(path, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "run", "-test", "-config", path)
	cmd.Env = assetEnv(exe)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "empty peers") {
		t.Fatalf("installed Xray must construct outbound handlers during -test: %v %s", err, out)
	}
	err := ValidateConfig(context.Background(), exe, cfg)
	var bad *ConfigError
	if !errors.As(err, &bad) {
		t.Fatalf("runtime handler-construction error not isolated: %v", err)
	}
}
