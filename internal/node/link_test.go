package node

import (
	"encoding/base64"
	"strings"
	"testing"
)

// A node's share link reads back to the same settings, for every protocol with a link format.
func TestShareLinkRoundTrip(t *testing.T) {
	vmess := `{"v":"2","ps":"VM 香港","add":"v.example.com","port":"443","id":"0b5a8f3e-1111-2222-3333-444455556666","aid":"0",` +
		`"scy":"auto","net":"grpc","type":"multi","path":"svc","tls":"tls","sni":"s.example.com","alpn":"h2","fp":"chrome"}`
	vmessKCP := `{"v":"2","ps":"K","add":"k.example.com","port":"9000","id":"u","aid":"2","net":"kcp","type":"wechat-video","path":"seed"}`
	links := []string{
		"vless://0b5a8f3e-1111-2222-3333-444455556666@example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.apple.com&fp=chrome&pbk=AbC+def/123&sid=6ba8&spx=%2F&type=tcp#%E9%A6%99%E6%B8%AF%2001",
		"vless://uuid-1@[2001:db8::1]:8443?type=ws&security=tls&path=%2Fray%3Fed%3D2048&host=cdn.example.com&alpn=h2%2Chttp%2F1.1&pcs=AA%3ABB&ech=x",
		"vless://u@x.example.com:443?type=xhttp&security=tls&path=%2Fx&mode=stream-up&extra=%7B%22a%22%3A1%7D&fm=%7B%22udp%22%3A%5B%5D%7D",
		"vless://u@g.example.com:443?type=grpc&serviceName=svc&authority=a.example.com&security=reality&pbk=k&pqv=PQ&encryption=mlkem768x25519plus.native.0rtt.abc",
		"vless://u@k.example.com:9000?type=kcp&headerType=wechat-video&seed=s3cr3t#K",
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(vmess)),
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessKCP)),
		"trojan://pa%3Ass%40%2F%3F%23@t.example.com:8443?sni=s.example.com&allowInsecure=1&type=httpupgrade&path=%2Fu#T%20x",
		"ss://" + base64.StdEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:a+b/c==")) + "@1.2.3.4:8388#SS",
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:p")) + "@1.2.3.4:8388?type=ws&path=%2Fs&security=tls#SS%20WS",
		"hysteria2://a:b%40c@[2001:db8::1]:443,20000-30000/?obfs=salamander&obfs-password=gawr&hop_interval=45s&sni=h.example.com&insecure=1&pinSHA256=AA:BB&alpn=h3#J",
		"socks://" + base64.StdEncoding.EncodeToString([]byte("user:pa/ss")) + "@1.2.3.4:1080#S",
		"socks5://[::1]:1080",
		"http://u%3Ax:p%40@proxy.example.com:3128#H",
		"https://proxy.example.com:443",
		"wireguard://cGsrL2s%3D@162.159.192.1:2408/?publickey=bmo%2BcQ%3D%3D&presharedkey=psk&address=172.16.0.2%2F32%2C2606%3A4700%3A%3A2%2F128&reserved=1%2C2%2C3&mtu=1280#WARP",
	}
	for _, link := range links {
		n, err := Parse(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		out := ShareLink(&n)
		m, err := Parse(out)
		if err != nil {
			t.Fatalf("%s\n-> %s: %v", link, out, err)
		}
		if !SameConfig(&n, &m) || n.Name != m.Name {
			t.Errorf("%s\n-> %s\n%+v\n%+v", link, out, n, m)
		}
	}
	if s := ShareLink(&Node{Protocol: MASQUE, Outbound: `{"protocol":"masque"}`}); s != "" {
		t.Errorf("JSON nodes have no link: %s", s)
	}
}

// Settings filled in by hand are checked and stored as importing their link would.
func TestNormalize(t *testing.T) {
	n, err := Normalize(Node{ID: "id", GroupID: "g", Name: " 手动 ", Protocol: Trojan, Host: " t.example.com ", Port: 443, Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "id" || n.GroupID != "g" || n.Name != "手动" || n.Host != "t.example.com" || n.Security != "tls" ||
		n.Network != "tcp" || !strings.HasPrefix(n.Link, "trojan://p@t.example.com:443") {
		t.Fatalf("%+v", n)
	}
	for _, bad := range []struct {
		n    Node
		want string
	}{
		{Node{Protocol: Trojan, Port: 443, Password: "p"}, "服务器地址"},
		{Node{Protocol: Trojan, Host: "h", Port: 70000, Password: "p"}, "端口"},
		{Node{Protocol: Trojan, Host: "h", Port: 443}, "缺少密码"},
		{Node{Protocol: VLESS, Host: "h", Port: 443}, "用户 ID"},
		{Node{Protocol: Hysteria2, Host: "h", Port: 443, Password: "p", HopPorts: "30000-20000"}, "端口"},
		{Node{Protocol: WireGuard, Host: "h", Port: 51820, SecretKey: "k", PublicKey: "p", Reserved: "1,2"}, "reserved"},
		{Node{Protocol: Hysteria2, Host: "h", Port: 443, Password: "p", Obfs: "salamander"}, "obfs-password"},
	} {
		if _, err := Normalize(bad.n); err == nil || !strings.Contains(err.Error(), bad.want) || strings.Contains(err.Error(), "链接无效") {
			t.Errorf("%+v: %v", bad.n, err)
		}
	}
	// A JSON node: one proxy outbound, the name given kept.
	j, err := Normalize(Node{ID: "j", Name: "MQ", Protocol: MASQUE, Outbound: `{"protocol":"masque","tag":"x","settings":{"address":"m.example.com","port":443}}`})
	if err != nil || j.ID != "j" || j.Name != "MQ" || j.Host != "m.example.com" || strings.Contains(j.Outbound, `"tag"`) {
		t.Fatalf("%+v %v", j, err)
	}
	if _, err := Normalize(Node{Outbound: `[{"protocol":"trojan","settings":{"servers":[{"address":"a","port":1}]}},{"protocol":"trojan","settings":{"servers":[{"address":"b","port":1}]}}]`}); err == nil {
		t.Error("two outbounds for one node")
	}
}
