package certpin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"raydock/internal/node"
)

// selfSigned returns a certificate for the name, as servers set up for insecure links have, and its pin.
func selfSigned(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "www.bing.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:])
}

func TestNeeded(t *testing.T) {
	for _, c := range []struct {
		n    node.Node
		want bool
	}{
		{node.Node{Protocol: node.Hysteria2, Insecure: true}, true},
		{node.Node{Protocol: node.Hysteria2, Insecure: true, Obfs: "salamander"}, true},
		{node.Node{Protocol: node.Hysteria2}, false},
		{node.Node{Protocol: node.Hysteria2, Insecure: true, PinSHA256: "x"}, false},
		{node.Node{Protocol: node.Hysteria2, Insecure: true, AutoPin: "x"}, false},
		// A name to verify the certificate against is no case for a pin of the certificate merely seen.
		{node.Node{Protocol: node.Trojan, Security: "tls", Insecure: true, VerifyName: "real.example.com"}, false},
		{node.Node{Protocol: node.Hysteria2, Insecure: true, FinalMask: "{}"}, false}, // layers Fetch does not speak
		{node.Node{Protocol: node.Trojan, Security: "tls", Network: "ws", Insecure: true}, true},
		{node.Node{Protocol: node.VLESS, Security: "reality", Insecure: true}, false},
		{node.Node{Protocol: node.SOCKS, Insecure: true}, false},
	} {
		if got := Needed(&c.n); got != c.want {
			t.Errorf("%+v: %v", c.n, got)
		}
	}
}

// The certificate of a Hysteria2 server is read over QUIC, with Salamander obfuscation when the node has
// it, and of a TLS server over TCP.
func TestFetch(t *testing.T) {
	cert, pin := selfSigned(t)
	conf := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h3"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, obfs := range []string{"", "salamander"} {
		pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var spc net.PacketConn = plainConn{pc}
		if obfs != "" {
			spc = &salamander{PacketConn: spc, key: []byte("gawr")}
		}
		ln, err := quic.Listen(spc, conf, nil)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := ln.Accept(context.Background())
				if err != nil {
					return
				}
				go func() { <-c.Context().Done() }()
			}
		}()
		n := node.Node{Protocol: node.Hysteria2, Host: "127.0.0.1", Port: pc.LocalAddr().(*net.UDPAddr).Port,
			SNI: "www.bing.com", Insecure: true, Obfs: obfs, ObfsPassword: "gawr"}
		got, err := Fetch(ctx, &n, nil, nil)
		if err != nil || got != pin {
			t.Fatalf("obfs %q: %q %v, want %q", obfs, got, err, pin)
		}
		// The wrong obfuscation password gets no answer.
		if obfs != "" {
			n.ObfsPassword = "wrong"
			short, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			if _, err := Fetch(short, &n, nil, nil); err == nil {
				t.Error("a wrong salamander password should not reach the server")
			}
			cancel()
		}
		ln.Close()
		pc.Close()
	}

	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	n := node.Node{Protocol: node.Trojan, Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Security: "tls", Insecure: true}
	if got, err := Fetch(ctx, &n, nil, nil); err != nil || got != pin {
		t.Fatalf("TCP: %q %v", got, err)
	}
}
