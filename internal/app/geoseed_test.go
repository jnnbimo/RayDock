package app

import (
	"os"
	"path/filepath"
	"testing"

	"raydock/internal/xray"
)

const bundledGeo = "202610282204"

// bundleWithGeo is an installer's core directory whose files read "bundled", its rule data of release
// bundledGeo.
func bundleWithGeo(t *testing.T) string {
	t.Helper()
	dir := coreFilesIn(t, "bundled")
	if err := os.WriteFile(filepath.Join(dir, xray.GeoTagFile), []byte(bundledGeo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An upgrade brings the installer's rule data to an installation whose rule data is older or of unknown
// release, and leaves newer rule data alone. The core, of the same version, stays.
func TestBundledRuleData(t *testing.T) {
	for _, c := range []struct {
		installed string
		seeded    bool
	}{{"", true}, {"202610212206", true}, {bundledGeo, false}, {"202610302205", false}} {
		t.Run(c.installed, func(t *testing.T) {
			a, posts, _ := newRegressionApp(t)
			placeholderCore(t, a)
			a.State.GeoTag = c.installed
			a.BundledCore = bundleWithGeo(t)
			a.ReadCoreVersion = func(string) (string, error) { return "26.10.21", nil }
			a.EnsureCore()
			waitRegression(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy })
			geo, _ := os.ReadFile(filepath.Join(a.CoreDir(), "geosite.dat"))
			exe, _ := os.ReadFile(a.coreExe())
			want := c.installed
			if c.seeded {
				want = bundledGeo
			}
			if (string(geo) == "bundled") != c.seeded || string(exe) == "bundled" || a.State.GeoTag != want || a.GeoErr != "" {
				t.Fatalf("rule data %q core %q tag %q err %q", geo, exe, a.State.GeoTag, a.GeoErr)
			}
		})
	}
}

// A first start installs the installer's core with its rule data, whose release it records: the rule data
// is not installed a second time.
func TestBundledCoreRecordsRuleData(t *testing.T) {
	a, posts, _ := newRegressionApp(t)
	a.BundledCore = bundleWithGeo(t)
	a.ReadCoreVersion = func(string) (string, error) { return "26.10.21", nil }
	a.EnsureCore()
	waitRegression(t, posts, func() bool { return !a.CoreBusy && !a.GeoBusy })
	geo, _ := os.ReadFile(filepath.Join(a.CoreDir(), "geosite.dat"))
	if string(geo) != "bundled" || a.State.GeoTag != bundledGeo || a.GeoMsg != "" || a.CoreErr != "" {
		t.Fatalf("rule data %q tag %q geo %q core err %q", geo, a.State.GeoTag, a.GeoMsg, a.CoreErr)
	}
}

func TestGeoTags(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]string{"202610282204\n": "202610282204", "latest": "", "2026102822045": ""} {
		if err := os.WriteFile(filepath.Join(dir, xray.GeoTagFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := xray.ReadGeoTag(dir); got != want {
			t.Errorf("%q: %q", content, got)
		}
	}
	if xray.NewerGeo("", "") || !xray.NewerGeo(bundledGeo, "") || xray.NewerGeo(bundledGeo, bundledGeo) || xray.NewerGeo(bundledGeo, "202611010000") {
		t.Error("NewerGeo")
	}
}
