package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// releaseServer serves RayDock's latest release as tag, or fails with status when it is not 200.
func releaseServer(t *testing.T, tag *string, status *int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if *status != http.StatusOK {
			w.WriteHeader(*status)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"` + *tag + `","html_url":"https://example.com/elsewhere"}`))
	}))
	t.Cleanup(srv.Close)
	old := releaseAPI
	releaseAPI = srv.URL
	t.Cleanup(func() { releaseAPI = old })
}

// Asked, RayDock offers a newer release and opens its download page, never downloading it; it also says
// when nothing is newer, or why the lookup failed.
func TestCheckUpdate(t *testing.T) {
	a, posts, _ := newTestApp(t)
	var opened []string
	a.Version, a.OpenURL = "0.1.7", func(u string) { opened = append(opened, u) }
	tag, status := "v0.1.7", http.StatusOK
	releaseServer(t, &tag, &status)
	check := func() {
		t.Helper()
		a.CheckUpdate()
		if !a.UpdateChecking {
			t.Fatal("not checking")
		}
		waitFor(t, posts, func() bool { return !a.UpdateChecking })
	}

	check()
	if a.UpdateLatest != "" || a.UpdateNote != "已是最新版本" {
		t.Fatalf("same version: offered %q note %q", a.UpdateLatest, a.UpdateNote)
	}

	status = http.StatusInternalServerError
	check()
	if a.UpdateLatest != "" || !strings.Contains(a.UpdateNote, "HTTP 500") {
		t.Fatalf("failed check: offered %q note %q", a.UpdateLatest, a.UpdateNote)
	}

	status, tag = http.StatusOK, "v0.1.10" // compared as numbers, not text
	check()
	if a.UpdateLatest != "v0.1.10" || a.UpdateNote != "" {
		t.Fatalf("newer: offered %q note %q", a.UpdateLatest, a.UpdateNote)
	}
	a.OpenReleases()
	if len(opened) != 1 || opened[0] != ReleasesPage {
		t.Fatalf("opened %q", opened)
	}

	tag = "latest"
	a.UpdateLatest = ""
	check()
	if a.UpdateLatest != "" || !strings.Contains(a.UpdateNote, "无法识别") {
		t.Fatalf("bad tag: offered %q note %q", a.UpdateLatest, a.UpdateNote)
	}
}

// A build without a version, as from go run, does not look for one.
func TestCheckUpdateWithoutVersion(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.CheckUpdate()
	if a.UpdateChecking {
		t.Fatal("checking without a version")
	}
}
