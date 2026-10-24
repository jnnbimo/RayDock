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

func TestCopyCore(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "core")
	for _, f := range append(CoreFiles(), "LICENSE") {
		if err := os.WriteFile(filepath.Join(src, f), []byte("new "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CopyCore(src, dst, nil); err != nil {
		t.Fatal(err)
	}
	if m := Missing(dst); len(m) > 0 {
		t.Fatalf("missing after copy: %v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "LICENSE")); string(b) != "new LICENSE" {
		t.Errorf("license %q", b)
	}
	// Incomplete source: an error, existing files untouched, no temp files left.
	os.Remove(filepath.Join(src, ExeName()))
	os.WriteFile(filepath.Join(src, "geoip.dat"), []byte("changed"), 0o644)
	if err := CopyCore(src, dst, nil); err == nil {
		t.Fatal("missing core in source should fail")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "geoip.dat")); string(b) != "new geoip.dat" {
		t.Errorf("failure should not replace existing files: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dst, "*.new")); len(left) > 0 {
		t.Errorf("temp files left: %v", left)
	}
}

// Rule data updated on its own is kept when the core is replaced.
func TestCopyCoreKeeps(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	for _, f := range CoreFiles() {
		os.WriteFile(filepath.Join(src, f), []byte("new "+f), 0o644)
	}
	os.WriteFile(filepath.Join(dst, "geosite.dat"), []byte("updated"), 0o644)
	if err := CopyCore(src, dst, GeoFiles); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "geosite.dat")); string(b) != "updated" {
		t.Errorf("geosite.dat %q should be kept", b)
	}
	// A file kept but missing is still installed.
	if b, _ := os.ReadFile(filepath.Join(dst, "geoip.dat")); string(b) != "new geoip.dat" {
		t.Errorf("geoip.dat %q", b)
	}
}

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
