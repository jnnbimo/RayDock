package xray

import (
	"os"
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

func TestPickRelease(t *testing.T) {
	list := `[
		{"tag_name":"v27.0.0","draft":true,"assets":[{"name":"X.zip","browser_download_url":"d"},{"name":"X.zip.dgst","browser_download_url":"d"}]},
		{"tag_name":"v26.10.11","prerelease":true,"assets":[{"name":"Other.zip","browser_download_url":"o"}]},
		{"tag_name":"v26.10.10","prerelease":true,"assets":[{"name":"X.zip","browser_download_url":"z"},{"name":"X.zip.dgst","browser_download_url":"g"}]},
		{"tag_name":"v26.3.27","assets":[{"name":"X.zip","browser_download_url":"old"},{"name":"X.zip.dgst","browser_download_url":"old"}]}
	]`
	rel, err := pickRelease(strings.NewReader(list), "X.zip")
	if err != nil || rel != (Release{Tag: "v26.10.10", ZipURL: "z", DgstURL: "g"}) {
		t.Fatalf("%+v %v", rel, err)
	}
	if _, err := pickRelease(strings.NewReader(`[]`), "X.zip"); err == nil {
		t.Error("no usable release should fail")
	}
}

func TestParseDigest(t *testing.T) {
	d, err := parseDigest(strings.NewReader("MD5= 3a\nSHA1= 42\nSHA2-256= d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad\nSHA2-512= 5b\n"))
	if err != nil || d != "d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad" {
		t.Fatalf("%q %v", d, err)
	}
}

func TestCopyGeoIsIndependentAndTransactional(t *testing.T) {
	dir, src := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ExeName()), []byte("keep executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range GeoFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, GeoFiles[0]), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyGeo(src, dir); err == nil {
		t.Fatal("an incomplete rule set was committed")
	}
	for _, name := range GeoFiles {
		if data, _ := os.ReadFile(filepath.Join(dir, name)); string(data) != "old" {
			t.Fatalf("incomplete download changed %s", name)
		}
	}
	if err := os.WriteFile(filepath.Join(src, GeoFiles[1]), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyGeo(src, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range GeoFiles {
		if data, _ := os.ReadFile(filepath.Join(dir, name)); string(data) != "new" {
			t.Fatalf("%s was not committed", name)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ExeName())); string(data) != "keep executable" {
		t.Fatal("rule update changed the executable")
	}
}
