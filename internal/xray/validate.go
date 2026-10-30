package xray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ConfigError is a configuration rejected by Xray itself, not a failure to execute the validator.
type ConfigError struct{ Detail string }

func (e *ConfigError) Error() string { return e.Detail }

// ValidateConfig uses the installed core's schema and handler-construction checks without starting
// listeners or a TUN. Xray run -test calls core.New but not server.Start. The validator exits before a
// candidate starts, so this cannot add a third simultaneous handover core.
func ValidateConfig(ctx context.Context, exe string, cfg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f, err := os.CreateTemp(filepath.Dir(exe), "check-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(cfg); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "run", "-test", "-config", path)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = assetEnv(exe)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &ConfigError{Detail: startupError(string(out))}
		}
		return fmt.Errorf("无法执行配置校验：%w", err)
	}
	return nil
}
