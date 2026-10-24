package xray

import "fmt"

// A dedicated port is a mixed (SOCKS5 and HTTP) inbound of the front core, listening like the local port,
// whose connections the front core hands over to the backend core as the port's user (portUser), and the
// backend core sends through the port's chain (portRelays).

// portPrefix starts the tags of a dedicated port's inbound and outbounds, and its user.
const portPrefix = "port-"

// portTag starts the tags of the outbounds of port's chain.
func portTag(port int) string { return fmt.Sprintf("%s%d-", portPrefix, port) }

// portUser is the relay user a dedicated port's connections are handed over as.
func portUser(port int) string { return fmt.Sprintf("%s%d", portPrefix, port) }

// portUserID is portUser's VLESS ID: decimal digits are hex digits too.
func portUserID(port int) string { return fmt.Sprintf("6f1b3a52-8d0e-4c47-9a1f-%012d", port) }

// portInTag tags a dedicated port's inbound by what it is, so that a front core switched to listen
// otherwise runs a new inbound (FrontParts).
func portInTag(port int, lan bool) string {
	if lan {
		return fmt.Sprintf("%s%d-lan", portPrefix, port)
	}
	return portUser(port)
}

// portInbound is the front core's inbound of a dedicated port.
func portInbound(port int, lan bool) obj {
	listen := "127.0.0.1"
	if lan {
		listen = "0.0.0.0"
	}
	return obj{"tag": portInTag(port, lan), "protocol": "socks", "listen": listen, "port": port,
		"settings": obj{"auth": "noauth", "udp": true}}
}
