package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// The front core holds what programs see: the local port, the TUN and the dedicated ports. It routes
// nothing itself: it hands every connection to the backend core over loopback (Backend.Mixed and TUN), as
// the relay user that says which of RayDock's process rules it matched, since only the front core sees the
// program behind it, or which dedicated port it came in by. It restarts only for the TUN; anything else it
// runs, the local port, LAN access, the dedicated ports, the process rules and the backend core it hands
// over to, is switched in through its API (FrontParts).

// relayUsers are the users a front core hands connections over as, named after the backend outbound
// the matched process rule sends them to; plain matched none. Each dedicated port has one too (portUser).
var relayUsers = []struct{ email, id string }{
	{"plain", relayID},
	{tagProxy, "6f1b3a52-8d0e-4c47-9a1e-2b5d7c3e9f11"},
	{tagDirect, "6f1b3a52-8d0e-4c47-9a1e-2b5d7c3e9f12"},
	{tagBlock, "6f1b3a52-8d0e-4c47-9a1e-2b5d7c3e9f13"},
}

// relayUserID returns the VLESS ID of relay user email.
func relayUserID(email string) string {
	for _, u := range relayUsers {
		if u.email == email {
			return u.id
		}
	}
	var port int
	fmt.Sscanf(email, portPrefix+"%d", &port)
	return portUserID(port)
}

// procRule is a rule of processes and the relay user that marks the connections it matches.
type procRule struct {
	user      string
	processes []string
}

// Front is what a front core is built with besides the options.
type Front struct {
	// Backend has the relay ports it hands over to, whether it serves a connection, and the dedicated ports.
	Backend Backend
	// PortsLAN has the dedicated ports listen on every address, not only on loopback: it applies to them
	// at once, while the local port's (Options.AllowLAN) waits for a reconnect.
	PortsLAN bool
	Gen      int // tags its outbounds, so that the next ones can be added beside them
	APIPort  int
}

// BuildFront returns the front core's config. Without a connection (Backend.Connected) it serves the
// dedicated ports alone: no local port, no TUN.
func BuildFront(o Options, f Front) ([]byte, error) {
	conn := f.Backend.Connected()
	if !conn {
		o.TUN.Enabled = false
	} else if o.Port <= 0 || o.Port > 65535 {
		return nil, fmt.Errorf("本地端口 %d 无效", o.Port)
	}
	if f.Backend.Mixed <= 0 || o.TUN.Enabled && f.Backend.TUN <= 0 || f.APIPort <= 0 {
		return nil, errors.New("缺少后端核心的转交端口")
	}
	var procs []procRule
	if conn {
		rules, err := parseAllRules(o)
		if err != nil {
			return nil, err
		}
		_, procs = routing(o, rules, dnsServer{}, dnsServer{}, tagProxy)
	}
	gen := fmt.Sprintf("f%d/", f.Gen)
	mixed := mixedTag(o)

	var outs, list []any
	relay := func(in, user string) string {
		tag := gen + in + "-" + user
		if !slices.ContainsFunc(outs, func(o any) bool { return tagOf(o) == tag }) {
			port := f.Backend.Mixed
			if in == tagTUN {
				port = f.Backend.TUN
			}
			outs = append(outs, obj{"tag": tag, "protocol": "vless", "settings": obj{"vnext": []any{obj{
				"address": "127.0.0.1", "port": port, "users": []any{obj{"id": relayUserID(user), "encryption": "none"}},
			}}}})
		}
		return tag
	}
	add := func(out string, kv ...any) {
		rule := obj{"type": "field", "outboundTag": out}
		for i := 0; i+1 < len(kv); i += 2 {
			rule[kv[i].(string)] = kv[i+1]
		}
		list = append(list, rule)
	}
	var inbounds []any
	for _, r := range f.Backend.Ports {
		in := portInbound(r.Port, f.PortsLAN)
		inbounds = append(inbounds, in)
		add(relay(tagMixed, portUser(r.Port)), "inboundTag", []string{tagOf(in)})
	}
	if conn {
		inbounds = append(frontInbounds(o, mixed), inbounds...)
		ins := []string{mixed}
		if o.TUN.Enabled {
			ins = append(ins, tagTUN)
			// As in the backend core, DNS queries entering the TUN come first: they go to its DNS module.
			add(relay(tagTUN, "plain"), "inboundTag", []string{tagTUN}, "port", "53")
			if o.TUN.BlockQUIC {
				block := gen + tagBlock
				outs = append(outs, obj{"tag": block, "protocol": "blackhole", "settings": obj{}})
				add(block, "inboundTag", []string{tagTUN}, "network", "udp", "port", "443")
			}
		}
		kind := func(in string) string { // the backend inbound of a front inbound
			if in == tagTUN {
				return tagTUN
			}
			return tagMixed
		}
		for _, p := range procs {
			for _, in := range ins {
				add(relay(kind(in), p.user), "inboundTag", []string{in}, "process", p.processes)
			}
		}
		for _, in := range ins {
			add(relay(kind(in), "plain"), "inboundTag", []string{in})
		}
	}
	cfg := obj{
		// The backend core logs connections; a log level of its own would make the front core restart for it.
		"log":       obj{"loglevel": "warning", "access": "none"},
		"inbounds":  inbounds,
		"outbounds": outs,
		"routing":   obj{"domainStrategy": "AsIs", "rules": list},
		"api":       apiConfig(f.APIPort),
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// mixedTag tags the local port's inbound by what it is, so that a front core switched to another port or
// listen address runs the new inbound beside the old one until it is removed.
func mixedTag(o Options) string {
	if o.AllowLAN {
		return fmt.Sprintf("%s-%d-lan", tagMixed, o.Port)
	}
	return fmt.Sprintf("%s-%d", tagMixed, o.Port)
}

// frontBase is a front config without what FrontParts switches: the inbounds but the TUN, the outbounds and
// the routing rules.
func frontBase(cfg []byte) (obj, error) {
	var m obj
	if err := json.Unmarshal(cfg, &m); err != nil {
		return nil, err
	}
	m["inbounds"] = tunInbounds(m, true)
	delete(m, "outbounds")
	if r, ok := m["routing"].(map[string]any); ok {
		delete(r, "rules")
	}
	return m, nil
}

// tunInbounds returns m's TUN inbound when tun, else its other inbounds: the local port and dedicated ports.
func tunInbounds(m obj, tun bool) []any {
	ins, _ := m["inbounds"].([]any)
	return slices.DeleteFunc(slices.Clone(ins), func(in any) bool { return (tagOf(in) == tagTUN) != tun })
}

// CanSwitchFront reports whether a front core running cur can become one running next through its API
// (FrontParts): whether they differ in no more than the local port's and dedicated ports' inbounds, the
// outbounds and the routing rules. The TUN, the log and the API are read at start only.
func CanSwitchFront(cur, next []byte) bool {
	a, errA := frontBase(cur)
	b, errB := frontBase(next)
	return errA == nil && errB == nil && reflect.DeepEqual(a, b)
}

// FrontSwitch is what turns a running front core into another (FrontParts, API.SwitchFront).
type FrontSwitch struct {
	RemoveFirst  []string // inbounds going whose port a new one takes, which must be free first
	AddInbounds  []byte   // the inbounds new or changed; nil for none
	Outbounds    []byte   // all of next's
	Routing      []byte   // next's
	OldOutbounds []string // cur's tags
	RemoveAfter  []string // the other inbounds going
}

// FrontParts returns what switches a front core running cur to next.
func FrontParts(cur, next []byte) (FrontSwitch, error) {
	var c, n obj
	if err := json.Unmarshal(cur, &c); err != nil {
		return FrontSwitch{}, err
	}
	if err := json.Unmarshal(next, &n); err != nil {
		return FrontSwitch{}, err
	}
	var s FrontSwitch
	curIns, nextIns := tunInbounds(c, false), tunInbounds(n, false)
	stays := func(in any, list []any) bool {
		return slices.ContainsFunc(list, func(x any) bool { return reflect.DeepEqual(x, in) })
	}
	var add []any
	taken := map[any]bool{} // ports and tags of the inbounds added
	for _, in := range nextIns {
		if !stays(in, curIns) {
			add = append(add, in)
			taken[tagOf(in)], taken[in.(map[string]any)["port"]] = true, true
		}
	}
	for _, in := range curIns {
		if stays(in, nextIns) {
			continue
		}
		if taken[tagOf(in)] || taken[in.(map[string]any)["port"]] {
			s.RemoveFirst = append(s.RemoveFirst, tagOf(in))
		} else {
			s.RemoveAfter = append(s.RemoveAfter, tagOf(in))
		}
	}
	var err error
	if len(add) > 0 {
		if s.AddInbounds, err = json.Marshal(obj{"inbounds": add}); err != nil {
			return s, err
		}
	}
	outs, _ := c["outbounds"].([]any)
	for _, o := range outs {
		s.OldOutbounds = append(s.OldOutbounds, tagOf(o))
	}
	if s.Outbounds, err = json.Marshal(obj{"outbounds": n["outbounds"]}); err == nil {
		s.Routing, err = json.Marshal(obj{"routing": n["routing"]})
	}
	return s, err
}

// SwitchFront switches a running front core (FrontParts): the new outbounds are added, the routing replaced
// in one step to use them, then the new inbounds open and the old outbounds and inbounds go. Until the
// routing is replaced the old outbounds take the connections, so none goes unrouted; the connections they
// carry go on after they are removed. An inbound whose port a new one takes leaves it a moment before.
func (p API) SwitchFront(s FrontSwitch) error {
	if len(s.RemoveFirst) > 0 {
		if err := p.call(nil, "rmi", s.RemoveFirst...); err != nil {
			return err
		}
	}
	if err := p.call(s.Outbounds, "ado"); err != nil {
		return err
	}
	if err := p.call(s.Routing, "adrules"); err != nil {
		return err
	}
	if s.AddInbounds != nil {
		if err := p.call(s.AddInbounds, "adi"); err != nil {
			return err
		}
	}
	if len(s.OldOutbounds) > 0 {
		if err := p.call(nil, "rmo", s.OldOutbounds...); err != nil {
			return err
		}
	}
	if len(s.RemoveAfter) > 0 {
		return p.call(nil, "rmi", s.RemoveAfter...)
	}
	return nil
}
