package xray

import "fmt"

// A dedicated port is a mixed (SOCKS5 and HTTP) inbound, listening like the local port, whose connections
// go through the port's chain before any other rule (dedicated).

// portTag tags a dedicated port's inbound.
func portTag(port int) string { return fmt.Sprintf("port-%d", port) }

func portInbound(port int, lan bool) obj {
	listen := "127.0.0.1"
	if lan {
		listen = "0.0.0.0"
	}
	return exclusiveInbound(obj{"tag": portTag(port), "protocol": "socks", "listen": listen, "port": port,
		"settings": obj{"auth": "noauth", "udp": true}})
}
