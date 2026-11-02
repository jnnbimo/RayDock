package xray

import (
	"strings"
	"testing"
)

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

func TestParseRules(t *testing.T) {
	rs, errs := ParseRules("Example.COM\n.cn.example\nfull:a.b\nregexp:^x$\n1.1.1.1\nip:2001:db8::/32\ngeoip:US\nprocess:C:\\Apps\\x.exe\n\n  # c")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if strings.Join(rs.Domains, " ") != "domain:example.com domain:cn.example full:a.b regexp:^x$" ||
		strings.Join(rs.IPs, " ") != "1.1.1.1 2001:db8::/32 geoip:us" || rs.Processes[0] != `C:\Apps\x.exe` {
		t.Errorf("%+v", rs)
	}
	_, errs = ParseRules("ok.com\n1.2.3.4/40\nfoo:bar\nregexp:(\na b")
	if len(errs) != 4 || errs[0].Line != 2 || errs[3].Line != 5 {
		t.Errorf("%v", errs)
	}
}
