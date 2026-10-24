// Package node defines proxy nodes and parses share links, Xray outbound JSON and subscriptions.
package node

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
)

// Protocols: every proxy protocol Xray supports as a client outbound.
const (
	Shadowsocks = "ss"
	VMess       = "vmess"
	VLESS       = "vless"
	Trojan      = "trojan"
	Hysteria2   = "hysteria2"
	SOCKS       = "socks"
	HTTP        = "http"
	WireGuard   = "wireguard"
	MASQUE      = "masque" // no common share link format; imported from outbound JSON
)

// Node is a proxy server. Link keeps the imported share link for export.
//
// Nodes imported from Xray JSON keep the outbound verbatim in Outbound; the other fields are
// read from it for display and latency tests only.
type Node struct {
	ID       string `json:"id"`
	GroupID  string `json:"groupId,omitempty"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`

	UUID       string `json:"uuid,omitempty"`       // vmess / vless
	AlterID    int    `json:"alterId,omitempty"`    // vmess
	Cipher     string `json:"cipher,omitempty"`     // vmess security (scy) or ss method
	Username   string `json:"username,omitempty"`   // socks / http
	Password   string `json:"password,omitempty"`   // ss / trojan / hysteria2 / socks / http
	Flow       string `json:"flow,omitempty"`       // vless
	Encryption string `json:"encryption,omitempty"` // vless, empty means none

	SecretKey    string `json:"secretKey,omitempty"` // local private key; the peer key is PublicKey
	PreSharedKey string `json:"preSharedKey,omitempty"`
	LocalAddress string `json:"localAddress,omitempty"` // comma-separated, e.g. 10.0.0.2/32,fd00::2/128
	Reserved     string `json:"reserved,omitempty"`     // three bytes, comma-separated
	MTU          int    `json:"mtu,omitempty"`

	Network    string `json:"network,omitempty"`    // tcp kcp ws grpc xhttp httpupgrade
	Path       string `json:"path,omitempty"`       // ws/xhttp/httpupgrade path, grpc serviceName
	HostHeader string `json:"hostHeader,omitempty"` // ws/xhttp/httpupgrade Host
	Mode       string `json:"mode,omitempty"`       // xhttp mode or grpc multi
	HeaderType string `json:"headerType,omitempty"` // tcp http header, mKCP header
	Seed       string `json:"seed,omitempty"`       // mKCP obfuscation password
	Authority  string `json:"authority,omitempty"`  // grpc
	Extra      string `json:"extra,omitempty"`      // xhttp extra (JSON)
	FinalMask  string `json:"fm,omitempty"`         // finalmask (JSON), the fm link parameter

	Obfs         string `json:"obfs,omitempty"` // salamander
	ObfsPassword string `json:"obfsPassword,omitempty"`
	HopPorts     string `json:"hopPorts,omitempty"`    // hop ports, e.g. 20000-30000,40000
	HopInterval  int    `json:"hopInterval,omitempty"` // seconds, 0 for the default

	Security    string `json:"security,omitempty"` // none tls reality
	SNI         string `json:"sni,omitempty"`
	Fingerprint string `json:"fp,omitempty"`
	ALPN        string `json:"alpn,omitempty"` // comma-separated
	Insecure    bool   `json:"insecure,omitempty"`
	PublicKey   string `json:"pbk,omitempty"` // REALITY key, or the WireGuard peer key
	ShortID     string `json:"sid,omitempty"`
	SpiderX     string `json:"spx,omitempty"`
	MLDSA65     string `json:"pqv,omitempty"` // REALITY mldsa65Verify
	PinSHA256   string `json:"pin,omitempty"` // pinnedPeerCertSha256
	VerifyName  string `json:"vcn,omitempty"` // verifyPeerCertByName
	ECHConfig   string `json:"ech,omitempty"` // echConfigList
	// AutoPin is the SHA-256 of the certificate RayDock saw on first use, for a node asking to skip
	// certificate verification, which Xray 26 no longer offers. Not a setting of the node: PinSHA256 is.
	AutoPin string `json:"autoPin,omitempty"`

	Outbound string `json:"outbound,omitempty"` // verbatim Xray outbound JSON
	Link     string `json:"link,omitempty"`
}

func (n *Node) Label() string {
	if n.Name != "" {
		return n.Name
	}
	return n.Address()
}

func (n *Node) Address() string { return net.JoinHostPort(n.Host, strconv.Itoa(n.Port)) }

// Pin is the certificate SHA-256 the outbound pins: the node's own, or else AutoPin, unless the node names
// whom its certificate must be issued to (VerifyName): Xray accepts a pinned leaf certificate as it is,
// without that verification, so a certificate merely seen once must not stand in for it.
func (n *Node) Pin() string {
	if n.PinSHA256 != "" || n.VerifyName != "" {
		return n.PinSHA256
	}
	return n.AutoPin
}

func (n *Node) ProtocolName() string { return ProtocolName(n.Protocol) }

// ProtocolName is the full name of protocol p as the interface shows it.
func ProtocolName(p string) string {
	switch p {
	case Shadowsocks:
		return "Shadowsocks"
	case VMess:
		return "VMess"
	case VLESS:
		return "VLESS"
	case Trojan:
		return "Trojan"
	case Hysteria2:
		return "Hysteria2"
	case SOCKS:
		return "SOCKS5"
	case HTTP:
		return "HTTP"
	case WireGuard:
		return "WireGuard"
	case MASQUE:
		return "MASQUE"
	}
	return p
}

// Transport summarizes transport and security, e.g. "ws · tls".
func (n *Node) Transport() string {
	nw := n.Network
	switch {
	case n.Protocol == Hysteria2:
		nw = "quic"
	case n.Protocol == WireGuard:
		nw = "udp"
	case nw == "":
		nw = "tcp"
	}
	if n.Security != "" && n.Security != "none" {
		return nw + " · " + n.Security
	}
	return nw
}

// UDP reports whether the node reaches its server over UDP, where a TCP handshake cannot measure latency.
func (n *Node) UDP() bool {
	switch n.Protocol {
	case Hysteria2, WireGuard:
		return true
	case MASQUE:
		return !strings.Contains(n.ALPN, "h2") // HTTP/3 unless ALPN is only h2
	}
	switch n.Network {
	case "kcp", "mkcp", "hysteria":
		return true
	}
	return false
}

func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// SameEndpoint matches subscription identities so server settings can change without
// losing selection, ordering, dedicated ports or chain references. Not for deduplication.
func SameEndpoint(a, b *Node) bool {
	return a.Protocol == b.Protocol && a.Host == b.Host && a.Port == b.Port &&
		a.UUID == b.UUID && a.Username == b.Username && a.Password == b.Password && a.SecretKey == b.SecretKey &&
		a.Network == b.Network && a.Path == b.Path && a.Outbound == b.Outbound
}

// SameConfig compares all connection settings, ignoring only identity, display metadata and AutoPin.
func SameConfig(a, b *Node) bool { return ConfigKey(a) == ConfigKey(b) }

// ConfigKey is n's connection settings alone: nodes with the same key are the same configuration
// (SameConfig), so it indexes them.
func ConfigKey(n *Node) Node {
	k := *n
	k.ID, k.GroupID, k.Name, k.Link, k.AutoPin = "", "", "", "", ""
	return k
}
