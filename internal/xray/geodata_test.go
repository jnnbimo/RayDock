package xray

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallGeo(t *testing.T) {
	files := map[string]string{"geoip.dat": "ip data", "geosite.dat": "site data"}
	broken := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			w.Write([]byte(`{"tag_name": "202610132213"}`))
			return
		}
		name := path.Base(r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/download/202610132213/") {
			http.NotFound(w, r)
			return
		}
		if base, ok := strings.CutSuffix(name, ".sha256sum"); ok {
			sum := sha256.Sum256([]byte(files[base]))
			fmt.Fprintf(w, "%x  %s\n", sum, base)
			return
		}
		data := files[name]
		if broken && name == "geosite.dat" {
			data = "tampered"
		}
		w.Write([]byte(data))
	}))
	defer srv.Close()
	geoAPI, geoDownloads = srv.URL+"/latest", srv.URL+"/download/"
	ctx := context.Background()
	tag, err := LatestGeo(ctx, srv.Client())
	if err != nil || tag != "202610132213" {
		t.Fatalf("tag %q %v", tag, err)
	}
	dir := t.TempDir()
	var last, lastTotal int64
	if err := InstallGeo(ctx, srv.Client(), tag, dir, func(done, total int64) {
		if done < last || done > total {
			t.Errorf("progress went from %d to %d/%d", last, done, total)
		}
		last, lastTotal = done, total
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != want {
			t.Errorf("%s = %q", name, b)
		}
	}
	if last != lastTotal {
		t.Errorf("progress ended at %d/%d", last, lastTotal)
	}
	// A file failing its checksum replaces nothing.
	files["geoip.dat"], broken = "newer ip", true
	if err := InstallGeo(ctx, srv.Client(), tag, dir, nil); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("tampered download: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "geoip.dat")); string(b) != "ip data" {
		t.Errorf("geoip.dat replaced although geosite.dat failed: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.new")); len(left) > 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestInstallGeoRollsBackReplacementFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if base, ok := strings.CutSuffix(name, ".sha256sum"); ok {
			fmt.Fprintf(w, "%x  %s", sha256.Sum256([]byte("new "+base)), base)
			return
		}
		_, _ = w.Write([]byte("new " + name))
	}))
	defer srv.Close()
	previous := geoDownloads
	geoDownloads = srv.URL + "/"
	defer func() { geoDownloads = previous }()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "geoip.dat"), []byte("old ip"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "geosite.dat"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := InstallGeo(context.Background(), srv.Client(), "test", dir, nil); err == nil {
		t.Fatal("non-file destination should fail")
	}
	b, err := os.ReadFile(filepath.Join(dir, "geoip.dat"))
	if err != nil || string(b) != "old ip" {
		t.Fatalf("rule update partially committed: %q %v", b, err)
	}
}
