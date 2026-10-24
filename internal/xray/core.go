package xray

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// readyTimeout bounds the wait for the ready line. A process still running after it counts as started,
// so a reworded log line cannot block connecting.
const readyTimeout = 5 * time.Second

// Core manages one xray process. Its methods may be called from any goroutine.
type Core struct {
	// OnLog receives each output line, on the goroutine reading the output.
	OnLog func(line string)
	// OnExit is called when the core exits unexpectedly (not after Stop).
	OnExit func(err error)

	// run makes Start and Stop take turns: a Stop during a Start waits for it and ends what it started,
	// so a port the core is about to take is free once Stop returns.
	run     sync.Mutex
	mu      sync.Mutex
	cmd     *exec.Cmd
	done    chan struct{} // closed when the process exits
	stopped bool          // ended by Stop
}

// Start writes the config and starts the core, stopping a running one first. It returns once the core
// reports ready, exits or times out.
func (c *Core) Start(exe, configPath string, config []byte) error {
	return c.StartWithExit(exe, configPath, config, c.OnExit)
}

// StartWithExit binds an exit handler to this process, not a later replacement.
func (c *Core) StartWithExit(exe, configPath string, config []byte, onExit func(error)) error {
	c.run.Lock()
	defer c.run.Unlock()
	c.stop()
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("找不到 Xray 核心：%s", exe)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return fmt.Errorf("写入配置失败：%w", err)
	}

	cmd := exec.Command(exe, "run", "-config", configPath)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = assetEnv(exe)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // one pipe keeps the log order

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动核心失败：%w", err)
	}
	newProcessGuard().add(cmd.Process.Pid) // ends the core with this process
	c.mu.Lock()
	done := make(chan struct{})
	c.cmd, c.done, c.stopped = cmd, done, false
	c.mu.Unlock()

	ready := make(chan struct{})
	var launched atomic.Bool // Start reported success; any later exit is unexpected
	var once sync.Once
	var startup strings.Builder // startup output, used as the error on failure
	var startupMu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			startupMu.Lock()
			if startup.Len() < 8*1024 {
				startup.WriteString(line + "\n")
			}
			startupMu.Unlock()
			// e.g. "2026/10/14 10:00:00 [Warning] core: Xray 26.3.27 started"
			if strings.Contains(line, "core: Xray") && strings.HasSuffix(line, " started") {
				once.Do(func() { close(ready) })
			}
			if c.OnLog != nil {
				c.OnLog(line)
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
		c.mu.Lock()
		unexpected := c.cmd == cmd && !c.stopped
		if c.cmd == cmd {
			c.cmd = nil
		}
		c.mu.Unlock()
		// An exit during startup is reported by Start, not OnExit.
		if unexpected && launched.Load() && onExit != nil {
			onExit(exitError(waitErr))
		}
	}()

	failed := func() error {
		startupMu.Lock()
		out := strings.TrimSpace(startup.String())
		startupMu.Unlock()
		if out == "" {
			out = exitError(waitErr).Error()
		}
		return fmt.Errorf("核心启动失败：%s", startupError(out))
	}
	select {
	case <-ready:
	case <-done:
		return failed()
	case <-time.After(readyTimeout):
	}
	// Mark first, then check done: paired with the waiter closing done before reading the mark, an exit
	// is reported either here or through OnExit.
	launched.Store(true)
	select {
	case <-done:
		return failed()
	default:
		return nil
	}
}

// Stop ends the core and waits for it to exit, after any Start in progress.
func (c *Core) Stop() {
	c.run.Lock()
	defer c.run.Unlock()
	c.stop()
}

func (c *Core) stop() {
	c.mu.Lock()
	cmd, done := c.cmd, c.done
	c.stopped = true
	c.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// PID returns the running process's ID, or 0.
func (c *Core) PID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

func (c *Core) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil
}

// Version runs "xray version" and returns e.g. "26.3.27".
func Version(exe string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// The first line reads "Xray 26.3.27 (Xray, Penetrates Everything.) ..."
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "Xray" {
		return "", errors.New("无法识别的版本输出")
	}
	return fields[1], nil
}

// Test checks with "xray run -test" whether the core accepts config.
func Test(exe string, config []byte) error {
	f, err := os.CreateTemp("", "raydock-test-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(config); err != nil {
		f.Close()
		return err
	}
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "run", "-test", "-config", f.Name())
	cmd.Env = assetEnv(exe)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, lastLines(strings.TrimSpace(string(out)), 6))
	}
	return nil
}

// assetEnv is the environment of a core run from exe, which finds its rule data next to it.
func assetEnv(exe string) []string {
	return append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(exe))
}

func exitError(err error) error {
	if err == nil {
		return errors.New("核心已退出")
	}
	return fmt.Errorf("核心已退出：%w", err)
}

// startupError picks the reason from the output of a core that failed to start: Xray's
// "Failed to start: …" line, else the last lines.
func startupError(out string) string {
	for line := range strings.Lines(out) {
		if reason, ok := strings.CutPrefix(strings.TrimSpace(line), "Failed to start:"); ok {
			return strings.TrimSpace(reason)
		}
	}
	return lastLines(out, 6)
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
