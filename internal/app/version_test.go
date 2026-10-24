package app

import (
	"raydock/internal/xray"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		tag, cur string
		want     bool
	}{
		{"v26.10.10", "26.3.27", true},
		{"v26.3.27", "26.3.27", false},
		{"v26.3.27", "26.10.10", false},
		{"v27.1.1", "26.12.31", true},
		{"nightly", "26.3.27", false},
	} {
		if got := newerVersion(c.tag, c.cur); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.tag, c.cur, got)
		}
	}
	if !tunTooOld("26.3.27") || tunTooOld("26.10.10") || tunTooOld(xray.MinTUNVersion) || !tunTooOld("") || !tunTooOld("nightly") {
		t.Error("tunTooOld is wrong")
	}
}
