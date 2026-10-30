package xray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The helper exercises the actual process/pipe lifecycle without installing or starting Xray.
func TestMain(m *testing.M) {
	if os.Getenv("RAYDOCK_TEST_CORE_HELPER") == "1" && len(os.Args) == 4 && os.Args[1] == "run" {
		mode, err := os.ReadFile(os.Args[3])
		if err != nil {
			os.Exit(2)
		}
		switch string(mode) {
		case "ready":
			time.Sleep(150 * time.Millisecond)
			fmt.Println("[Warning] core: Xray test started")
		case "fail":
			fmt.Print(strings.Repeat("startup detail\n", 1000))
			fmt.Println("Failed to start: final error from helper")
			os.Exit(7)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartRequiresReady(t *testing.T) {
	t.Setenv("RAYDOCK_TEST_CORE_HELPER", "1")
	for _, mode := range []string{"ready", "no-ready", "fail"} {
		t.Run(mode, func(t *testing.T) {
			var c Core
			defer c.Stop()
			start := time.Now()
			err := c.start(context.Background(), os.Args[0], filepath.Join(t.TempDir(), "core.json"), []byte(mode), nil, time.Second)
			if mode == "ready" {
				if err != nil || time.Since(start) < 150*time.Millisecond || !c.Running() {
					t.Fatalf("ready: duration %v, error %v, running %v", time.Since(start), err, c.Running())
				}
			} else {
				if err == nil || c.Running() {
					t.Fatalf("%s: error %v, running %v", mode, err, c.Running())
				}
				if mode == "fail" && !strings.Contains(err.Error(), "final error from helper") {
					t.Fatalf("the final error line was lost: %v", err)
				}
			}
		})
	}
}

func TestStartupCancellation(t *testing.T) {
	t.Setenv("RAYDOCK_TEST_CORE_HELPER", "1")
	var c Core
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	defer c.Stop()
	start := time.Now()
	err := c.Start(ctx, os.Args[0], filepath.Join(t.TempDir(), "cancel.json"), []byte("no-ready"), nil)
	if !errors.Is(err, context.DeadlineExceeded) || c.Running() || time.Since(start) > 2*time.Second {
		t.Fatalf("startup cancellation: %v, running %v, elapsed %v", err, c.Running(), time.Since(start))
	}
}

func TestStartupError(t *testing.T) {
	out := "Xray 26.10.10 (Xray, Penetrates Everything.) 701af60 (go1.27.2 windows/amd64)\n" +
		"A unified platform for anti-censorship.\n" +
		"2026/10/14 19:47:32.860202 [Info] infra/conf/serial: Reading config: &{Name:C:\\config.json Format:json}\n" +
		"Failed to start: app/proxyman/inbound: failed to listen TCP on 20001 > listen tcp 127.0.0.1:20001: bind: in use\n"
	if got := startupError(out); got != "app/proxyman/inbound: failed to listen TCP on 20001 > listen tcp 127.0.0.1:20001: bind: in use" {
		t.Errorf("reason %q", got)
	}
	if got := startupError("line 1\nline 2"); got != "line 1\nline 2" {
		t.Errorf("without a Failed line: %q", got)
	}
}
