package xray

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// RuleSet is a set of custom routing rules split by Xray match field: fields within one Xray rule are
// ANDed, so domains, IPs and processes need separate rules.
type RuleSet struct {
	Domains   []string
	IPs       []string
	Processes []string
}

func (r RuleSet) Empty() bool { return len(r.Domains)+len(r.IPs)+len(r.Processes) == 0 }

// LineError reports a problem on a line, counted from 1.
type LineError struct {
	Line int
	Text string
	Msg  string
}

func (e LineError) Error() string {
	return fmt.Sprintf("第 %d 行“%s”：%s", e.Line, e.Text, e.Msg)
}

var domainPrefixes = []string{"domain:", "full:", "regexp:", "keyword:", "geosite:", "dotless:", "ext:"}

// ParseRules parses one rule per line:
//
//	google.com                         domain and subdomains (domain:google.com)
//	full: keyword: regexp: geosite:    Xray domain matchers
//	1.2.3.4, 10.0.0.0/8, geoip:cn      IPs and ranges
//	process:chrome.exe, chrome.exe     process name or full path
//
// Blank lines and lines starting with # are ignored.
func ParseRules(text string) (RuleSet, []LineError) {
	var rs RuleSet
	var errs []LineError
	for i, line := range splitLines(text) {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		bad := func(msg string) { errs = append(errs, LineError{Line: i + 1, Text: s, Msg: msg}) }
		lower := strings.ToLower(s)
		switch {
		case strings.HasPrefix(lower, "process:"):
			v := strings.TrimSpace(s[len("process:"):])
			if v == "" {
				bad("缺少进程名")
				continue
			}
			rs.Processes = append(rs.Processes, v)
		case strings.HasPrefix(lower, "geoip:"):
			if len(s) == len("geoip:") {
				bad("缺少国家或类别代码")
				continue
			}
			rs.IPs = append(rs.IPs, lower)
		case strings.HasPrefix(lower, "ip:"):
			v := strings.TrimSpace(s[3:])
			if !validIP(v) {
				bad("不是有效的 IP 或网段")
				continue
			}
			rs.IPs = append(rs.IPs, v)
		case validIP(s):
			rs.IPs = append(rs.IPs, s)
		case hasAnyPrefix(lower, domainPrefixes):
			prefix, value, _ := strings.Cut(s, ":")
			if strings.TrimSpace(value) == "" {
				bad("缺少域名")
				continue
			}
			if strings.EqualFold(prefix, "regexp") {
				if _, err := regexp.Compile(value); err != nil {
					bad("正则表达式无效")
					continue
				}
			}
			rs.Domains = append(rs.Domains, strings.ToLower(prefix)+":"+strings.TrimSpace(value))
		case strings.HasSuffix(lower, ".exe"):
			rs.Processes = append(rs.Processes, s)
		case strings.Contains(s, "/"):
			bad("不是有效的网段")
		case strings.Contains(s, ":"):
			bad("未知的前缀")
		case strings.ContainsAny(s, " \t,;"):
			bad("一行只能写一条规则")
		default:
			rs.Domains = append(rs.Domains, "domain:"+strings.TrimPrefix(lower, "."))
		}
	}
	return rs, errs
}

// ParseHosts parses static hosts, one "domain address…" or hosts-file "IP domain…" entry per line.
func ParseHosts(text string) (map[string][]string, []LineError) {
	hosts := map[string][]string{}
	var errs []LineError
	for i, line := range splitLines(text) {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		f := strings.Fields(s)
		if len(f) < 2 {
			errs = append(errs, LineError{Line: i + 1, Text: s, Msg: "需要“域名 地址”两部分"})
			continue
		}
		if validAddr(f[0]) {
			for _, name := range f[1:] {
				hosts[strings.ToLower(name)] = append(hosts[strings.ToLower(name)], f[0])
			}
			continue
		}
		name := strings.ToLower(f[0])
		hosts[name] = append(hosts[name], f[1:]...)
	}
	return hosts, errs
}

func validIP(s string) bool {
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	return validAddr(s)
}

func validAddr(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
