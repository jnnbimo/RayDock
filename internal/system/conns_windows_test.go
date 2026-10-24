package system

import (
	"net"
	"testing"
)

// Connections counts the connections accepted on a port while they last.
func TestConnections(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if n := Connections(port); n != 0 {
		t.Fatalf("a listener alone counts %d", n)
	}
	var clients []net.Conn
	for range 3 {
		c, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		s, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		clients = append(clients, c)
	}
	if n := Connections(port, 1); n != 3 {
		t.Fatalf("3 connections count %d", n)
	}
	for _, c := range clients {
		c.Close()
	}
	if n := Connections(port); n != 0 {
		t.Fatalf("closed connections count %d", n)
	}
}
