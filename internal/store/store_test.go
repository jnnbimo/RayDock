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

// Before 0.3 subscriptions were the only groups and manual nodes had none.
func TestMigrateGroups(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	old := `{"nodes":[{"id":"a","name":"A"},{"id":"b","subId":"s1","name":"B"},{"id":"c","name":"C"}],
		"subscriptions":[{"id":"s1","name":"机场","url":"https://example.com/sub","count":1}]}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Groups) != 2 || !st.Groups[0].Subscription() || st.Groups[1].Name != DefaultGroup || st.Groups[1].Subscription() {
		t.Fatalf("groups %+v", st.Groups)
	}
	manual := st.Groups[1].ID
	if st.Nodes[0].GroupID != manual || st.Nodes[1].GroupID != "s1" || st.Nodes[2].GroupID != manual {
		t.Fatalf("nodes %+v", st.Nodes)
	}
	// Saved in the new format, loading again changes nothing.
	if err := Save(p, st); err != nil {
		t.Fatal(err)
	}
	again, err := Load(p)
	if err != nil || len(again.Groups) != 2 || again.Nodes[0].GroupID != manual {
		t.Fatalf("round trip %+v %v", again, err)
	}
}
