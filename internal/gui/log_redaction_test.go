package gui

import (
	"strings"
	"testing"
)

func TestExportRedactsKnownCredentials(t *testing.T) {
	line := `核心启动失败 trojan://example-secret@sample.invalid:443 原因：无法解析 {"Password":"escaped\"secret","token":"abcd","detail":"invalid config"}`
	got := redactLog(line)
	for _, sensitive := range []string{"example-secret", "escaped", "abcd"} {
		if strings.Contains(got, sensitive) {
			t.Fatalf("export retained %q: %s", sensitive, got)
		}
	}
	for _, diagnostic := range []string{"核心启动失败", "无法解析", "invalid config"} {
		if !strings.Contains(got, diagnostic) {
			t.Fatalf("export lost %q: %s", diagnostic, got)
		}
	}
}
