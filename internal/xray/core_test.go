package xray

import "testing"

func TestStartupError(t *testing.T) {
	out := "Xray 26.10.10 (Xray, Penetrates Everything.) 701af60 (go1.27.2 windows/amd64)\n" +
		"A unified platform for anti-censorship.\n" +
		"2026/10/14 19:47:32.860202 [Info] infra/conf/serial: Reading config: &{Name:C:\\config.json Format:json}\n" +
		"Failed to start: app/proxyman/inbound: failed to listen TCP on 20001 > listen tcp 127.0.0.1:20001: bind: in use\n"
	if got := startupError(out); got != "app/proxyman/inbound: failed to listen TCP on 20001 > listen tcp 127.0.0.1:20001: bind: in use" {
		t.Errorf("reason %q", got)
	}
	if got := startupError("line 1\nline 2"); got != "line 1\nline 2" {
		t.Errorf("without a Failed line: %q", got)
	}
}
