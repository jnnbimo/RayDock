//go:build integration

package xray

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIntegrationBundleTransaction(t *testing.T) {
	exe := coreForTest(t)
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	var packed bytes.Buffer
	zw := zip.NewWriter(&packed)
	for _, name := range CoreFiles() {
		w, e := zw.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		data := []byte("official:" + name)
		if name == ExeName() {
			data = binary
		}
		if _, e = w.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(packed.Bytes())
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zip":
			w.Write(packed.Bytes())
		case "/digest":
			fmt.Fprintln(w, "SHA2-256=", hex.EncodeToString(digest[:]))
		case "/latest":
			if fail.Load() {
				w.WriteHeader(502)
			} else {
				json.NewEncoder(w).Encode(map[string]string{"tag_name": "202610282204"})
			}
		default:
			name := filepath.Base(r.URL.Path)
			data := []byte("full:" + strings.TrimSuffix(name, ".sha256sum"))
			if strings.HasSuffix(name, ".sha256sum") {
				sum := sha256.Sum256(data)
				fmt.Fprintln(w, hex.EncodeToString(sum[:]), strings.TrimSuffix(name, ".sha256sum"))
			} else {
				w.Write(data)
			}
		}
	}))
	defer srv.Close()
	oldAPI, oldDownloads := geoAPI, geoDownloads
	geoAPI, geoDownloads = srv.URL+"/latest", srv.URL+"/files/"
	defer func() { geoAPI, geoDownloads = oldAPI, oldDownloads }()
	dir := filepath.Join(t.TempDir(), "core")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range CoreFiles() {
		if err = os.WriteFile(filepath.Join(dir, name), []byte("original:"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, GeoTagFile), []byte("202610010001"), 0600)
	if err = sealBundle(dir); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, name := range append(bundleFiles(), bundleManifest) {
		before[name], err = fileSum(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	rel := Release{ZipURL: srv.URL + "/zip", DgstURL: srv.URL + "/digest"}
	if err = PrepareBundle(context.Background(), srv.Client(), rel, dir, nil); err == nil {
		t.Fatal("failed full-rule download reported ready")
	}
	for name, sum := range before {
		if got, e := fileSum(filepath.Join(dir, name)); e != nil || got != sum {
			t.Fatalf("failed prepare changed %s", name)
		}
	}
	fail.Store(false)
	if err = PrepareBundle(context.Background(), srv.Client(), rel, dir, nil); err != nil {
		t.Fatal(err)
	}
	if !BundleReady(dir) || ReadGeoTag(dir) != "202610282204" {
		t.Fatal("completed bundle not ready")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "geoip.dat"))
	if string(b) != "full:geoip.dat" {
		t.Fatalf("wrong rule source %q", b)
	}
	os.WriteFile(filepath.Join(dir, "geoip.dat"), []byte("reduced"), 0600)
	if BundleReady(dir) {
		t.Fatal("stale tag/manifest accepted modified rule data")
	}
}
