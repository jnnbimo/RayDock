package gui

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"raydock/internal/node"
	"raydock/internal/store"
)

// nodeForm is the state of the add/edit node dialog. Fields of the node are edited in place in n; its
// numbers are edited as floats, as number inputs take them.
type nodeForm struct {
	open  bool
	id    string // node edited, "" when adding
	group string // group a new node goes to, "" for a new default group
	mode  int    // adding: 0 pastes share links or JSON, 1 fills in a node
	links string
	n     node.Node
	proto string // protocol chosen, which n takes once the choice is made
	port  float64
	aid   float64 // VMess alterId
	mtu   float64 // WireGuard, 0 for the default
	hop   float64 // Hysteria2 port hopping interval in seconds, 0 for the default
	json  string  // outbound of a node kept as Xray JSON
	err   string
}

// formProtocols are the protocols a node can be filled in with: those with a share link format.
var formProtocols = []string{
	node.VLESS, node.VMess, node.Trojan, node.Shadowsocks, node.Hysteria2, node.SOCKS, node.HTTP, node.WireGuard,
}

// newFormNode is an empty node of protocol proto with the defaults its share links have.
func newFormNode(proto string) node.Node {
	n := node.Node{Protocol: proto, Port: 443, Network: "tcp", Security: "none"}
	switch proto {
	case node.Trojan:
		n.Security = "tls"
	case node.Hysteria2:
		n.Network, n.Security = "", "tls"
	case node.Shadowsocks:
		n.Cipher = "aes-128-gcm"
		n.Port = 8388
	case node.SOCKS:
		n.Port = 1080
	case node.HTTP:
		n.Port = 8080
	case node.WireGuard:
		n.Network, n.Security, n.Port = "", "", 51820
	case node.VMess:
		n.Security = ""
	}
	return n
}

// addGroup returns the group the add dialog puts nodes in: the normal group shown, or "" when there are
// no groups yet (a default group is created). ok is false when the group shown is a subscription.
func (a *View) addGroup() (id string, ok bool) {
	g := a.GroupByID(a.group)
	switch {
	case g == nil && len(a.State.Groups) == 0:
		return "", true
	case g == nil || g.Subscription():
		return "", false
	}
	return g.ID, true
}

func (a *View) openAddNode() {
	group, ok := a.addGroup()
	if !ok {
		return
	}
	a.nf = nodeForm{open: true, group: group}
	a.setFormNode(newFormNode(node.VLESS))
}

func (a *View) openEditNode(id string) {
	n := a.NodeByID(id)
	if n == nil {
		return
	}
	a.nf = nodeForm{open: true, id: id, mode: 1}
	a.setFormNode(*n)
	if n.Outbound != "" {
		var b bytes.Buffer
		if json.Indent(&b, []byte(n.Outbound), "", "  ") == nil {
			a.nf.json = b.String()
		} else {
			a.nf.json = n.Outbound
		}
	}
}

func (a *View) setFormNode(n node.Node) {
	f := &a.nf
	f.n, f.proto = n, n.Protocol
	f.port, f.aid, f.mtu, f.hop = float64(n.Port), float64(n.AlterID), float64(n.MTU), float64(n.HopInterval)
}

// formNode returns the node the form describes.
func (f *nodeForm) formNode() node.Node {
	n := f.n
	n.Port, n.AlterID, n.MTU, n.HopInterval = int(f.port), int(f.aid), int(f.mtu), int(f.hop)
	if n.Outbound != "" {
		n.Outbound = f.json
	}
	return n
}

func (a *View) nodeDialog(c *ui.Context) {
	p := a.pal
	f := &a.nf
	adding := f.id == ""
	d := dialog{width: 580, title: "编辑节点", err: &f.err, actions: func() { a.saveNodeButton(c) }}
	if adding {
		d.title = "添加节点"
		d.head = func() {
			target := store.DefaultGroup + "（新建）"
			if g := a.GroupByID(f.group); g != nil {
				target = g.Name
			}
			a.chip(c, "添加到「"+target+"」", p.accent).MaxWidth(320)
		}
	}
	if adding && f.mode == 0 {
		d.start = func() {
			if iconButton(c, icClip, "粘贴").Clicked() {
				if clip := strings.TrimSpace(a.Clipboard.ReadText()); clip != "" {
					f.links = strings.TrimSpace(f.links + "\n" + clip)
				}
			}
		}
	}
	d.body = func() {
		if adding {
			segmented(c.Key("node-mode"), &f.mode, "粘贴链接", "手动填写").Label("添加方式").OnChange(func() { f.err = "" })
		} else if g := a.GroupByID(f.n.GroupID); g != nil && g.Subscription() {
			ui.Text(c, "这是订阅分组的节点：更新订阅时，它的配置会被订阅中的内容覆盖。").FontSize(12.5).TextColor(p.warning)
		}
		switch {
		case adding && f.mode == 0:
			a.pasteForm(c)
		case f.n.Outbound != "":
			a.jsonForm(c)
		default:
			a.nodeFields(c)
		}
	}
	a.dialog(c, &f.open, d)
}

// saveNodeButton imports the links pasted, or adds or saves the node filled in.
func (a *View) saveNodeButton(c *ui.Context) {
	f := &a.nf
	switch {
	case f.id == "" && f.mode == 0:
		if primaryButton(c, "导入").Disabled(strings.TrimSpace(f.links) == "").Clicked() {
			r := a.ImportLinks(f.links, f.group)
			if msg := r.Problem(); msg != "" {
				f.err = msg // nothing imported: the input stays for correcting
				return
			}
			c.Toast(r.String())
			f.open = false
			a.showAddedGroup(f.group)
		}
	case f.id == "":
		if primaryButton(c, "添加").Clicked() {
			n := f.formNode()
			id, err := a.AddNode(f.group, n)
			if err != nil {
				f.err = err.Error()
				return
			}
			f.open = false
			a.showAddedGroup(f.group)
			c.Toast("已添加 " + a.NodeByID(id).Label())
		}
	default:
		if primaryButton(c, "保存").Clicked() {
			if err := a.EditNode(f.formNode()); err != nil {
				f.err = err.Error()
				return
			}
			f.open = false
			a.savedToast(c, a.NodeByID(f.id).Label())
		}
	}
}

// showAddedGroup shows the group nodes were added to: group, or the default group just created.
func (a *View) showAddedGroup(group string) {
	if group == "" {
		for _, g := range a.State.Groups {
			if !g.Subscription() {
				group = g.ID
				break
			}
		}
	}
	a.group = group
}

func (a *View) pasteForm(c *ui.Context) {
	ui.Text(c, "每行一条分享链接：vless、vmess、trojan、ss、hysteria2、socks、http(s)、wireguard。"+
		"也可以粘贴 Xray JSON（单个出站或完整配置），包括 MASQUE 等没有分享链接的协议。").
		FontSize(12.5).TextColor(a.pal.muted).LineHeight(1.5)
	links := ui.TextArea(c.Key("links"), &a.nf.links).Lines(6, 12).Font("monospace").FontSize(12).AutoFocus().
		Placeholder("vless://…\n{\"protocol\": \"masque\", …}").Label("分享链接或 JSON")
	a.textMenu(c, links, "links", &a.nf.links, true, nil)
}

// jsonForm edits a node kept as Xray outbound JSON: its name and the outbound.
func (a *View) jsonForm(c *ui.Context) {
	f := &a.nf
	a.fieldRow(c, a.text(c, "node-name", "名称", &f.n.Name, "显示名称"))
	ui.Text(c, "该节点从 Xray 出站 JSON 导入，按 JSON 原样使用。").FontSize(12.5).TextColor(a.pal.muted)
	a.textMenu(c, ui.TextArea(c.Key("node-json"), &f.json).Lines(10, 16).Font("monospace").FontSize(12).Label("出站 JSON"),
		"node-json", &f.json, true, nil)
}

// nodeFields fills in the settings of the form's protocol.
func (a *View) nodeFields(c *ui.Context) {
	f := &a.nf
	n := &f.n
	var ids, names []string
	for _, pr := range formProtocols {
		ids, names = append(ids, pr), append(names, node.ProtocolName(pr))
	}
	if f.proto != n.Protocol {
		// Another protocol starts from its defaults; the server and the name stay.
		fresh := newFormNode(f.proto)
		fresh.ID, fresh.GroupID, fresh.Name, fresh.Host = n.ID, n.GroupID, n.Name, n.Host
		a.setFormNode(fresh)
		f.err = ""
	}
	a.fieldRow(c, a.pick(c, "node-proto", "协议", &f.proto, ids, names), a.text(c, "node-name", "名称", &n.Name, "可留空，默认为地址"))
	a.fieldRow(c, a.text(c, "node-host", "服务器地址", &n.Host, "域名或 IP"), a.number(c, "node-port", "端口", &f.port, 1, 65535))

	switch n.Protocol {
	case node.VLESS:
		a.fieldRow(c, a.text(c, "node-uuid", "用户 ID（UUID）", &n.UUID, ""))
		a.fieldRow(c,
			a.pick(c, "node-flow", "流控（flow）", &n.Flow, []string{"", "xtls-rprx-vision", "xtls-rprx-vision-udp443"}, []string{"无", "xtls-rprx-vision", "xtls-rprx-vision-udp443"}),
			a.text(c, "node-enc", "加密（encryption）", &n.Encryption, "none"))
	case node.VMess:
		a.fieldRow(c, a.text(c, "node-uuid", "用户 ID（UUID）", &n.UUID, ""))
		a.fieldRow(c,
			a.pick(c, "node-scy", "加密方式", &n.Cipher, []string{"", "auto", "aes-128-gcm", "chacha20-poly1305", "none", "zero"},
				[]string{"默认", "auto", "aes-128-gcm", "chacha20-poly1305", "none", "zero"}),
			a.number(c, "node-aid", "额外 ID（alterId）", &f.aid, 0, 65535))
	case node.Trojan:
		a.fieldRow(c, a.text(c, "node-pass", "密码", &n.Password, ""))
	case node.Shadowsocks:
		ciphers := []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "xchacha20-ietf-poly1305",
			"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305", "none"}
		a.fieldRow(c, a.pick(c, "node-method", "加密方法", &n.Cipher, ciphers, ciphers), a.text(c, "node-pass", "密码", &n.Password, ""))
	case node.Hysteria2:
		a.fieldRow(c, a.text(c, "node-pass", "认证密码", &n.Password, ""))
		a.fieldRow(c, a.text(c, "node-sni", "SNI", &n.SNI, "默认为服务器地址"), a.text(c, "node-alpn", "ALPN", &n.ALPN, "h3"))
		a.fieldRow(c, a.text(c, "node-pin", "证书 SHA-256 指纹（自签名证书时填写）", &n.PinSHA256, "可留空"))
		a.autoPinNote(c)
		obfs := []func(){a.pick(c, "node-obfs", "混淆", &n.Obfs, []string{"", "salamander"}, []string{"无", "salamander"})}
		if n.Obfs != "" {
			obfs = append(obfs, a.text(c, "node-obfs-pass", "混淆密码", &n.ObfsPassword, ""))
		}
		a.fieldRow(c, obfs...)
		a.fieldRow(c, a.text(c, "node-hop", "端口跳跃", &n.HopPorts, "例如 20000-30000，可留空"),
			a.number(c, "node-hop-interval", "跳跃间隔（秒，0 为默认）", &f.hop, 0, 3600))
	case node.SOCKS, node.HTTP:
		a.fieldRow(c, a.text(c, "node-user", "用户名", &n.Username, "可留空"), a.text(c, "node-pass", "密码", &n.Password, ""))
		if n.Protocol == node.HTTP {
			a.fieldRow(c, a.pick(c, "node-security", "安全层", &n.Security, []string{"none", "tls"}, []string{"无（HTTP）", "TLS（HTTPS）"}))
		}
	case node.WireGuard:
		a.fieldRow(c, a.text(c, "node-key", "本端私钥", &n.SecretKey, ""))
		a.fieldRow(c, a.text(c, "node-peer", "对端公钥", &n.PublicKey, ""))
		a.fieldRow(c, a.text(c, "node-psk", "预共享密钥", &n.PreSharedKey, ""))
		a.fieldRow(c, a.text(c, "node-addr", "本端地址", &n.LocalAddress, "例如 10.0.0.2/32,fd00::2/128"),
			a.text(c, "node-reserved", "reserved", &n.Reserved, "例如 1,2,3，可留空"))
		a.fieldRow(c, a.number(c, "node-mtu", "MTU（0 为默认）", &f.mtu, 0, 65535))
	}
	switch n.Protocol {
	case node.VLESS, node.VMess, node.Trojan:
		a.transportFields(c)
		a.securityFields(c)
	}
}

// transportFields fills in the transport of VLESS, VMess and Trojan.
func (a *View) transportFields(c *ui.Context) {
	n := &a.nf.n
	vmess := n.Protocol == node.VMess // its link carries fewer transport settings
	a.formHeading(c, "传输")
	nets := []string{"tcp", "ws", "grpc", "xhttp", "httpupgrade", "kcp"}
	netNames := []string{"TCP", "WebSocket", "gRPC", "XHTTP", "HTTPUpgrade", "mKCP"}
	network := a.pick(c, "node-net", "传输方式", &n.Network, nets, netNames)
	switch n.Network {
	case "ws", "httpupgrade":
		a.fieldRow(c, network)
		a.fieldRow(c, a.text(c, "node-path", "路径", &n.Path, "/"), a.text(c, "node-host-header", "Host", &n.HostHeader, "可留空"))
	case "grpc":
		a.fieldRow(c, network, a.pick(c, "node-grpc-mode", "模式", &n.Mode, []string{"", "multi"}, []string{"gun", "multi"}))
		fields := []func(){a.text(c, "node-path", "服务名（serviceName）", &n.Path, "")}
		if !vmess {
			fields = append(fields, a.text(c, "node-authority", "Authority", &n.Authority, "可留空"))
		}
		a.fieldRow(c, fields...)
	case "xhttp":
		a.fieldRow(c, network, a.pick(c, "node-xhttp-mode", "模式", &n.Mode, []string{"", "packet-up", "stream-up", "stream-one"},
			[]string{"auto", "packet-up", "stream-up", "stream-one"}))
		a.fieldRow(c, a.text(c, "node-path", "路径", &n.Path, "/"), a.text(c, "node-host-header", "Host", &n.HostHeader, "可留空"))
		if !vmess {
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "extra（JSON，可留空）").FontSize(12.5).TextColor(a.pal.muted)
				extra := ui.TextArea(c.Key("node-extra"), &n.Extra).Lines(2, 6).Font("monospace").FontSize(12).Label("XHTTP extra")
				a.textMenu(c, extra, "node-extra", &n.Extra, true, nil)
			})
		}
	case "kcp":
		a.fieldRow(c, network, a.pick(c, "node-kcp-header", "伪装", &n.HeaderType,
			[]string{"", "srtp", "utp", "wechat-video", "dtls", "wireguard", "dns"},
			[]string{"无", "srtp", "utp", "wechat-video", "dtls", "wireguard", "dns"}))
		a.fieldRow(c, a.text(c, "node-seed", "混淆密码（seed）", &n.Seed, ""))
	default:
		a.fieldRow(c, network, a.pick(c, "node-tcp-header", "伪装", &n.HeaderType, []string{"", "http"}, []string{"无", "HTTP"}))
		if n.HeaderType == "http" {
			a.fieldRow(c, a.text(c, "node-path", "路径", &n.Path, "/"), a.text(c, "node-host-header", "Host", &n.HostHeader, "可留空"))
		}
	}
}

// securityFields fills in TLS or REALITY of VLESS, VMess and Trojan.
func (a *View) securityFields(c *ui.Context) {
	n := &a.nf.n
	a.formHeading(c, "安全")
	values, names := []string{"none", "tls", "reality"}, []string{"无", "TLS", "REALITY"}
	if n.Protocol == node.VMess {
		values, names = []string{"", "tls"}, []string{"无", "TLS"}
	}
	security := a.pick(c, "node-security", "安全层", &n.Security, values, names)
	fps := []string{"", "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized"}
	fpNames := slices.Clone(fps)
	fpNames[0] = "默认（chrome）"
	switch n.Security {
	case "tls":
		a.fieldRow(c, security, a.pick(c, "node-fp", "TLS 指纹", &n.Fingerprint, fps, fpNames))
		a.fieldRow(c, a.text(c, "node-sni", "SNI", &n.SNI, "默认为服务器地址"), a.text(c, "node-alpn", "ALPN", &n.ALPN, "例如 h2,http/1.1"))
		if n.Protocol != node.VMess {
			a.fieldRow(c, a.text(c, "node-pin", "证书 SHA-256 指纹（自签名证书时填写）", &n.PinSHA256, "可留空"))
		}
		a.autoPinNote(c)
	case "reality":
		a.fieldRow(c, security, a.pick(c, "node-fp", "TLS 指纹", &n.Fingerprint, fps, fpNames))
		a.fieldRow(c, a.text(c, "node-sni", "SNI", &n.SNI, "例如 www.microsoft.com"), a.text(c, "node-sid", "ShortID", &n.ShortID, "可留空"))
		a.fieldRow(c, a.text(c, "node-pbk", "公钥（pbk）", &n.PublicKey, ""))
		a.fieldRow(c, a.text(c, "node-spx", "SpiderX", &n.SpiderX, "/"), a.text(c, "node-pqv", "ML-DSA-65 验证（pqv）", &n.MLDSA65, "可留空"))
	default:
		a.fieldRow(c, security)
	}
}

// autoPinNote tells which certificate RayDock pinned for a node whose link skips verification.
func (a *View) autoPinNote(c *ui.Context) {
	n := &a.nf.n
	switch {
	case n.PinSHA256 != "" || n.VerifyName != "":
	case n.AutoPin != "":
		note := "链接要求跳过证书验证，已改为固定首次连接时的证书：" + n.AutoPin
		a.copyMenu(c, ui.Text(c, note).FontSize(12).TextColor(a.pal.muted).Selectable(), "auto-pin", note)
	case n.Insecure:
		ui.Text(c, "链接要求跳过证书验证，保存后会改为固定服务器证书。").FontSize(12).TextColor(a.pal.warning)
	}
}

func (a *View) formHeading(c *ui.Context, title string) {
	ui.Text(c, title).FontSize(13).Bold().Margin(4, 0, 0)
}

// fieldRow lays fields out side by side, sharing the width equally.
func (a *View) fieldRow(c *ui.Context, fields ...func()) {
	ui.Row(c).Gap(12).AlignItems(ui.End).Children(func() {
		for _, field := range fields {
			ui.Column(c).Gap(6).Grow(1).Basis(0).MinWidth(0).Children(field)
		}
	})
}

// text, number and pick build a labeled field for fieldRow.
func (a *View) text(c *ui.Context, key, label string, value *string, placeholder string) func() {
	return func() {
		ui.Text(c, label).FontSize(12.5).TextColor(a.pal.muted).SingleLine()
		a.textMenu(c, ui.TextInput(c.Key(key), value).Height(controlHeight).Placeholder(placeholder).Label(label), key, value, false, nil)
	}
}

func (a *View) number(c *ui.Context, key, label string, value *float64, lo, hi float64) func() {
	return func() {
		ui.Text(c, label).FontSize(12.5).TextColor(a.pal.muted).SingleLine()
		a.numberMenu(c, ui.NumberInput(c.Key(key), value, lo, hi, 1).Label(label), key, value, lo, hi, nil)
	}
}

// pick chooses one of values into *value, showing names. A value not among them (from an imported
// node) is offered as itself, so the form never shows another choice than the node's.
func (a *View) pick(c *ui.Context, key, label string, value *string, values, names []string) func() {
	return func() {
		ui.Text(c, label).FontSize(12.5).TextColor(a.pal.muted).SingleLine()
		a.choice(c, key, label, value, values, names)
	}
}

// choice is the window's one drop-down: it chooses one of values into *value, showing names, which are
// display only, so equal names still choose distinct values. Its options are as wide as it, a name too
// long ending in an ellipsis and showing whole in a tooltip. They stay in the dialog it is in (popupArea),
// else in the window: below it, scrolling when they are many, and above it where there is too little room
// below and more above. Typing while they show goes to the first whose name has what was typed. It
// reports a new choice.
func (a *View) choice(c *ui.Context, key, label string, value *string, values, names []string) bool {
	p, t := a.pal, a.theme
	if !slices.Contains(values, *value) {
		values, names = append(slices.Clone(values), *value), append(slices.Clone(names), *value)
	}
	// name shows a name in room DIPs, on one line, whole in a tooltip where it does not fit.
	name := func(s string, room float32) ui.Element {
		e := ui.Text(c, s).SingleLine().Grow(1).MinWidth(0)
		if a.textWidth(c, t.FontSize, 0, "", s) > room {
			e.Tooltip(s)
		}
		return e
	}
	sel := ui.SelectBase(c.Key(key), value)
	trigger := sel.Trigger.Label(label).Height(controlHeight).Padding(0, t.Space(3)).Radius(t.Radius).Border(1, p.border).
		Background(t.Surface).Cursor(ui.CursorPointer).Justify(ui.SpaceBetween)
	if trigger.Pressed() {
		trigger.Background(t.SurfacePressed)
	} else if trigger.Hovered() {
		trigger.Background(t.SurfaceHover)
	}
	b := trigger.Bounds()
	trigger.Children(func() {
		name(names[slices.Index(values, *value)], b.W-2*t.Space(3)-2-arrowWidth)
		ui.Icon(c, icArrowDown).FontSize(12).TextColor(p.muted)
	})
	// Input comes between frames, when this frame's elements are gone: what is typed is kept for the next.
	open := sel.Open()
	trigger.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputText || !open {
			return false
		}
		a.typeahead.add(key, ev)
		return true
	})
	if i := a.typeahead.take(key, names); i >= 0 && open {
		sel.Highlight(values[i])
	}
	sel.Popup(func(panel ui.Element) {
		gap, pad, itemX, itemY := t.Space(1), t.Space(1), t.Space(2.5), t.Space(1.5)
		area := a.popupArea
		if area.W == 0 {
			w, h := c.Size()
			area = ui.Rect{W: w, H: h}
		}
		// The options' room on either side of the field: the area's, less the gap, the panel's padding
		// and border, and the margin kept to the area's edge. They open below while it has room for all,
		// or for minOptions, else on the side with more.
		chrome := gap + 2*pad + 2 + popupMargin
		below, above := area.Y+area.H-(b.Y+b.H)-chrome, b.Y-area.Y-chrome
		_, line := c.MeasureText(0, ui.Span{Text: names[0], Size: t.FontSize})
		all := float32(len(values))*(line+2*itemY+1) - 1
		room := below
		if below < min(all, minOptions) && above > below {
			room = above
			panel.AttachTo(trigger, ui.AnchorTopStart, ui.AnchorBottomStart).Margin(0, 0, gap, 0)
		} else {
			panel.Margin(gap, 0, 0, 0)
		}
		p.popup(panel.Width(b.W).Padding(pad).Radius(t.Radius + 4))
		ui.Scroll(c.Key(key + "-options")).MaxHeight(max(min(room, maxOptions), 0)).Children(func() {
			ui.Column(c).Gap(1).Children(func() {
				for i, v := range values {
					item := choosable(sel.Item(v)).Padding(itemY, itemX).Radius(t.Radius)
					if item.Highlighted() {
						p.chosen(item)
					} else if v == *value {
						item.Background(t.SurfacePressed)
					}
					item.Children(func() { name(names[i], b.W-2-2*pad-2*itemX) })
				}
			})
		})
	})
	return trigger.Changed()
}

// The options of a choice, in DIPs: as high as the room on their side allows, within these, and that far
// from the edge of their area. arrowWidth is the width of the arrow of its field.
const (
	minOptions  = 160
	maxOptions  = 320
	popupMargin = 12
	arrowWidth  = 12
)

// typeahead is what was typed into the open options of a choice, to go to the first that has it.
type typeahead struct {
	key     string
	text    string
	at      time.Time
	pending bool // typed since the choice last looked
}

// add adds the text of ev, typed into the options of choice key. A pause of a second starts over.
func (ta *typeahead) add(key string, ev ui.InputEvent) {
	if ta.key != key || ev.Time.Sub(ta.at) > time.Second {
		ta.text = ""
	}
	ta.key, ta.at, ta.text, ta.pending = key, ev.Time, ta.text+strings.ToLower(ev.Text), true
}

// take returns the first of names of choice key that has what was typed since it last looked, ignoring
// case, or -1.
func (ta *typeahead) take(key string, names []string) int {
	if !ta.pending || ta.key != key {
		return -1
	}
	ta.pending = false
	for i, n := range names {
		if strings.HasPrefix(strings.ToLower(n), ta.text) {
			return i
		}
	}
	for i, n := range names {
		if strings.Contains(strings.ToLower(n), ta.text) {
			return i
		}
	}
	return -1
}
