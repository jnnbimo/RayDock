package store

import (
	"os"
	"path/filepath"
	"testing"
)

// A state.json from an older version lacks TUN, rules and other fields: existing fields stay, new ones default.
func TestLoadOldState(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	old := `{"nodes":[],"settings":{"port":20000,"allowLan":true,"mode":"global","logLevel":"info",
		"directDns":"119.29.29.29","remoteDns":"1.1.1.1","systemProxy":false,"corePath":"D:\\x\\xray.exe"}}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	s := st.Settings
	if s.Port != 20000 || !s.AllowLAN || s.Mode != "global" || s.DirectDNS != "119.29.29.29" || s.RemoteDNS != "1.1.1.1" || s.SystemProxy {
		t.Errorf("old fields %+v", s)
	}
	def := DefaultSettings()
	if s.TUN != def.TUN || s.DNSStrategy != def.DNSStrategy || s.DomainStrategy != def.DomainStrategy {
		t.Errorf("new fields should default: %+v", s)
	}
	if err := Save(p, st); err != nil {
		t.Fatal(err)
	}
	again, err := Load(p)
	if err != nil || again.Settings != st.Settings {
		t.Errorf("round trip %+v %v", again.Settings, err)
	}
}
