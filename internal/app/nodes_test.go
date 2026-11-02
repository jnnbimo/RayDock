package app

import (
	"strings"
	"testing"
)

func TestImportKeepsDifferentSecurity(t *testing.T) {
	a, _, _ := newTestApp(t)
	base := "vless://11111111-2222-3333-4444-555555555555@a.example:443?type=ws&path=%2Fws&security="
	msg := a.ImportLinks(base+"none#plain\n"+base+"tls#tls", "").String()
	if len(a.State.Nodes) != 2 {
		t.Fatalf("distinct connection configs deduplicated: %s", msg)
	}
}

// Importing says why nothing was imported, so the input can be corrected.
func TestImportResult(t *testing.T) {
	a, _, _ := newTestApp(t)
	if r := a.ImportLinks("not a link", ""); r.Added != 0 || len(r.Invalid) != 1 || r.Problem() == "" {
		t.Fatalf("invalid input: %+v %q", r, r.Problem())
	}
	link := "socks://127.0.0.1:9#a"
	if r := a.ImportLinks(link+"\n"+link, ""); r.Added != 1 || r.Duplicate != 1 || r.Problem() != "" {
		t.Fatalf("a link twice: %+v", r)
	}
	if r := a.ImportLinks(link, ""); r.Added != 0 || r.Duplicate != 1 || !strings.Contains(r.Problem(), "已存在") {
		t.Fatalf("a link again: %+v %q", r, r.Problem())
	}
}
