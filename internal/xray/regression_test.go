package xray

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitFilesRollsBackInstallFailure(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "one.new"), filepath.Join(dir, "two.new")}
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths[0], []byte("new one"), 0600); err != nil {
		t.Fatal(err)
	}
	// The second staged file is missing: failure occurs after the first install, not during preparation.
	if err := commitFiles(paths); err == nil {
		t.Fatal("missing staged file should fail")
	}
	for _, name := range []string{"one", "two"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != "old "+name {
			t.Fatalf("rollback %s=%q: %v", name, b, err)
		}
	}
}

func TestCommitFilesRollbackRemovesNewDestinations(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.new")
	if err := os.WriteFile(first, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := commitFiles([]string{first, filepath.Join(dir, "two.new")}); err == nil {
		t.Fatal("missing staged file should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "one")); !os.IsNotExist(err) {
		t.Fatalf("failed new install left destination: %v", err)
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

func TestRulePrefixesBeforeHeuristics(t *testing.T) {
	for _, line := range []string{`regexp:^(foo/bar|example[.]com)$`, "ext:custom/domains.dat:category", "full:download.exe", `regexp:.*[.]exe`} {
		rs, errs := ParseRules(line)
		if len(errs) > 0 || len(rs.Domains) != 1 || len(rs.Processes) > 0 {
			t.Errorf("valid domain rule %q misclassified: %+v %v", line, rs, errs)
		}
	}
	for _, line := range []string{"bad/24", "ip:bad/24"} {
		if _, errs := ParseRules(line); len(errs) == 0 {
			t.Errorf("invalid IP rule %q accepted", line)
		}
	}
}
