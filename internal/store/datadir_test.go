package store

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMoveLegacyDataMovesEverything(t *testing.T) {
	root := t.TempDir()
	legacy, dir := filepath.Join(root, "legacy"), filepath.Join(root, "app", "data")
	writeTestFile(t, filepath.Join(legacy, "state.json"), `{"nodes":[]}`)
	writeTestFile(t, filepath.Join(legacy, "traffic.json"), `{}`)
	writeTestFile(t, filepath.Join(legacy, "core", "xray.exe"), "core")
	writeTestFile(t, filepath.Join(legacy, "config.json"), "{}") // written anew at every start
	writeTestFile(t, filepath.Join(legacy, "front.json"), "{}")  // 0.1.3's, read by no version since
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	warning, err := moveLegacyData(legacy, dir)
	if err != nil || warning != nil {
		t.Fatalf("moveLegacyData = %v, %v", warning, err)
	}
	for name, want := range map[string]string{"state.json": `{"nodes":[]}`, "traffic.json": `{}`, filepath.Join("core", "xray.exe"): "core"} {
		if got := readTestFile(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"config.json", "front.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s was moved: %v", name, err)
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy directory remains: %v", err)
	}
}

// Data already beside the executable is never replaced by an earlier version's.
func TestMoveLegacyDataKeepsCurrentData(t *testing.T) {
	root := t.TempDir()
	legacy, dir := filepath.Join(root, "legacy"), filepath.Join(root, "data")
	writeTestFile(t, filepath.Join(legacy, "state.json"), "old")
	writeTestFile(t, filepath.Join(dir, "state.json"), "new")
	if _, err := moveLegacyData(legacy, dir); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(dir, "state.json")); got != "new" {
		t.Errorf("state.json = %q, want the current data", got)
	}
	if got := readTestFile(t, filepath.Join(legacy, "state.json")); got != "old" {
		t.Errorf("legacy state.json = %q, want it untouched", got)
	}
}

// A move cut short leaves no state.json beside the executable, so it is done again, and keeps the
// earlier data.
func TestMoveLegacyDataFailureKeepsLegacy(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	writeTestFile(t, filepath.Join(legacy, "state.json"), "old")
	writeTestFile(t, filepath.Join(legacy, "traffic.json"), "{}")
	dir := filepath.Join(root, "missing", "data") // its parent does not exist: every copy fails
	if _, err := moveLegacyData(legacy, dir); err == nil {
		t.Fatal("moveLegacyData succeeded into a missing directory")
	}
	if fileExists(filepath.Join(dir, "state.json")) {
		t.Error("state.json was written by a failed move")
	}
	if got := readTestFile(t, filepath.Join(legacy, "state.json")); got != "old" {
		t.Errorf("legacy state.json = %q, want it untouched", got)
	}
}

func TestPrepareDataDirCreatesDirectory(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir()) // no earlier data
	t.Setenv("XDG_CONFIG_HOME", os.Getenv("APPDATA"))
	dir := filepath.Join(t.TempDir(), dataDirName)
	if _, err := PrepareDataDir(dir); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "core", "x"), "x") // the user may still write what it holds
	if _, err := PrepareDataDir(dir); err != nil {
		t.Fatalf("an existing directory: %v", err)
	}
}

func TestGoBuildDir(t *testing.T) {
	for dir, want := range map[string]bool{
		filepath.Join(`C:\tmp`, "go-build123", "b001", "exe"): true,
		filepath.Join(`C:\tmp`, "go-build123", "b001"):        true,
		filepath.Join(`E:\Apps`, "RayDock"):                   false,
	} {
		if got := goBuildDir(dir); got != want {
			t.Errorf("goBuildDir(%s) = %v, want %v", dir, got, want)
		}
	}
}
