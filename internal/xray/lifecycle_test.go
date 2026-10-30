package xray

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDNSResolversKeepDistinctRoles(t *testing.T) {
	for _, endpoint := range []string{"1.1.1.1", "1.1.1.1:5353", "https://dns.google/dns-query"} {
		for _, mode := range []string{ModeRule, ModeGlobal, ModeDirect} {
			t.Run(mode+"/"+endpoint, func(t *testing.T) {
				o := DefaultOptions()
				o.DirectDNS, o.RemoteDNS, o.Mode = endpoint, endpoint, mode
				m := decodeMain(t, o)
				if at(m, "dns.servers.0.tag") != tagDNS || at(m, "dns.servers.1.tag") != tagRemoteDNS {
					t.Fatal("resolver roles were lost on the shared endpoint")
				}
				rules := rulesIn(m)
				if _, tag := findRule(rules, "inboundTag", tagDNS); tag != tagDirectDNS {
					t.Fatalf("domestic resolver goes to %q", tag)
				}
				want := testProxy
				if mode == ModeDirect {
					want = tagDirectDNS
				}
				if _, tag := findRule(rules, "inboundTag", tagRemoteDNS); tag != want {
					t.Fatalf("remote resolver goes to %q, want %q", tag, want)
				}
			})
		}
	}
}

func TestCommitPreservesRecoveryBackups(t *testing.T) {
	dir, src := t.TempDir(), t.TempDir()
	// Unmarked and legacy rollback directories must be conservative recovery data.
	for _, name := range []string{"rollback-recovery-needed", "rollback-legacy"} {
		backup := filepath.Join(dir, name)
		if err := os.Mkdir(backup, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backup, ExeName()), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	retired := filepath.Join(dir, "retired-previous-success")
	if err := os.Mkdir(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "old"), []byte("disposable"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range CoreFiles() {
		if err := os.WriteFile(filepath.Join(src, name), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := CopyCore(src, dir, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"rollback-recovery-needed", "rollback-legacy"} {
		if data, err := os.ReadFile(filepath.Join(dir, name, ExeName())); err != nil || string(data) != "original" {
			t.Fatalf("recovery data was swept: %s %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("a confirmed retired backup was not collected: %v", err)
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
