// Package apptest builds an app.App with fakes for the network, clipboard, system proxy and
// start at logon, so tests never touch the real machine.
package apptest

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"raydock/internal/app"
	"raydock/internal/node"
	"raydock/internal/store"
	"raydock/internal/sysproxy"
	"raydock/internal/system"
	"raydock/internal/xray"
)

const (
	LinkA = "vless://11111111-2222-3333-4444-555555555555@a.example.com:443?security=tls&type=ws&path=%2Fws#节点A"
	LinkB = "trojan://secret@b.example.com:443#节点B"
)

// Geo stands in for GitHub's rule data releases: it writes placeholder files, or fails with Err.
type Geo struct {
	Tag      string
	Err      error
	Installs atomic.Int32 // written by the download goroutine, read by the test
	// Gate, when set, holds an install until it is closed, as a long download would.
	Gate chan struct{}
}

func (f *Geo) LatestGeo(context.Context, *http.Client) (string, error) { return f.Tag, f.Err }

func (f *Geo) InstallGeo(ctx context.Context, _ *http.Client, tag, dir string, progress func(done, total int64)) error {
	f.Installs.Add(1)
	if f.Gate != nil {
		select {
		case <-f.Gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, name := range xray.GeoFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("geo "+tag), 0o644); err != nil {
			return err
		}
	}
	progress(100, 100)
	return nil
}

// Bundle writes a core of placeholder files to a new directory, as the installer's: they are not
// executables, so a core started from them fails to start.
func Bundle(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range xray.CoreFiles() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type Clip struct{ Text string }

func (f *Clip) ReadText() string   { return f.Text }
func (f *Clip) WriteText(s string) { f.Text = s }

type Proxy struct {
	Cur     sysproxy.Settings
	Applied int
}

func (f *Proxy) Current() (sysproxy.Settings, error) { return f.Cur, nil }
func (f *Proxy) Apply(s sysproxy.Settings) error {
	f.Cur = s
	f.Applied++
	return nil
}

type Autostart struct {
	Mode system.StartMode
	Err  error
	Sets []system.StartMode
}

func (f *Autostart) Get() system.StartMode { return f.Mode }
func (f *Autostart) Set(m system.StartMode) error {
	f.Sets = append(f.Sets, m)
	if f.Err == nil {
		f.Mode = m
	}
	return f.Err
}

type Harness struct {
	// Pin is what fetching a server certificate gives; with "" it fails, as offline.
	Pin   string
	Pins  atomic.Int32 // certificates fetched
	A     *app.App
	Clip  *Clip
	Proxy *Proxy
	Geo   *Geo
	Start *Autostart
	URLs  []string // web pages opened
	posts chan func()
}

func New(t *testing.T) *Harness {
	t.Helper()
	h := &Harness{Clip: &Clip{}, Proxy: &Proxy{}, Geo: &Geo{}, Start: &Autostart{}, posts: make(chan func(), 256)}
	h.A = app.New(app.Deps{
		Post:            func(fn func()) { h.posts <- fn },
		Clipboard:       h.Clip,
		Proxy:           h.Proxy,
		SetAppearance:   func(int) {},
		OpenPath:        func(string) {},
		OpenURL:         func(u string) { h.URLs = append(h.URLs, u) },
		DataDir:         t.TempDir(),
		GeoSource:       h.Geo,
		BundledCore:     Bundle(t, "placeholder"),
		ReadCoreVersion: func(string) (string, error) { return xray.MinTUNVersion, nil },
		CertPin: func(context.Context, *node.Node) (string, error) {
			h.Pins.Add(1)
			if h.Pin == "" {
				return "", errors.New("offline")
			}
			return h.Pin, nil
		},
		Autostart: h.Start,
	}, store.State{Settings: store.DefaultSettings()})
	// Release workers/cores before t.TempDir removes their files, even when a test fails early.
	t.Cleanup(h.A.Shutdown)
	return h
}

// Wait runs functions posted by background work on the "UI thread" until cond holds.
func (h *Harness) Wait(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case fn := <-h.posts:
			fn()
		case <-deadline:
			t.Fatal("timed out")
		}
	}
}
