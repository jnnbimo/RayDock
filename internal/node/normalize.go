package node

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Normalize validates the complete model, independently of the subset a share format can express.
// JSON nodes retain their outbound. Link is empty when no lossless share link is available.
func Normalize(n Node) (Node, error) {
	if n.Outbound != "" {
		nodes, errs := ParseJSON(n.Outbound)
		if len(errs) > 0 {
			return Node{}, errs[0]
		}
		if len(nodes) != 1 {
			return Node{}, errors.New("JSON 中应恰好有一个代理出站")
		}
		m := nodes[0]
		m.ID, m.GroupID = n.ID, n.GroupID
		if name := strings.TrimSpace(n.Name); name != "" {
			m.Name = name
		}
		return m, nil
	}
	m, err := normalizeFields(n)
	if err != nil {
		return Node{}, err
	}
	m.Name = strings.TrimSpace(m.Name)
	m.AutoPin = "" // derived trust is retained by the app only when the configuration is unchanged
	m.Link = ShareLink(&m)
	return m, nil
}

// normalizeFields is shared by link imports and structured input. Credentials remain opaque strings.
func normalizeFields(n Node) (Node, error) {
	n.Protocol = strings.ToLower(strings.TrimSpace(n.Protocol))
	n.Host = strings.TrimSpace(n.Host)
	if n.Host == "" {
		return Node{}, errors.New("请填写服务器地址")
	}
	if strings.ContainsAny(n.Host, "[]/?#@ \t\r\n") {
		return Node{}, errors.New("服务器地址格式错误")
	}
	if strings.Contains(n.Host, ":") {
		if _, err := netip.ParseAddr(n.Host); err != nil {
			return Node{}, errors.New("服务器地址格式错误")
		}
	}
	if n.Port <= 0 || n.Port > 65535 {
		return Node{}, errors.New("端口应为 1–65535")
	}
	switch n.Protocol {
	case VLESS, VMess:
		if n.UUID == "" {
			return Node{}, errors.New("缺少用户 ID")
		}
		if n.Encryption == "none" {
			n.Encryption = ""
		}
	case Trojan:
		if n.Password == "" {
			return Node{}, errors.New("缺少密码")
		}
	case Shadowsocks:
		if n.Cipher == "" || n.Password == "" {
			return Node{}, errors.New("缺少加密方法或密码")
		}
	case SOCKS, HTTP:
	case Hysteria2:
		n.Network, n.Security = "", "tls"
		if _, err := firstPort(n.HopPorts); err != nil {
			return Node{}, err
		}
		if n.HopInterval < 0 {
			return Node{}, errors.New("跳跃间隔不能为负数")
		}
		if n.Obfs == "none" {
			n.Obfs = ""
		}
		switch n.Obfs {
		case "":
		case "salamander":
			if n.ObfsPassword == "" {
				return Node{}, errors.New("salamander 混淆缺少密码 obfs-password")
			}
		default:
			return Node{}, fmt.Errorf("不支持的混淆 %q", n.Obfs)
		}
	case WireGuard:
		if n.SecretKey == "" || n.PublicKey == "" {
			return Node{}, errors.New("缺少本端私钥或对端公钥")
		}
		if n.MTU != 0 && (n.MTU < 576 || n.MTU > 65535) {
			return Node{}, errors.New("MTU 应为 576–65535")
		}
		if n.Reserved != "" {
			parts := strings.Split(n.Reserved, ",")
			if len(parts) != 3 {
				return Node{}, errors.New("reserved 应为三个 0–255 的数")
			}
			for i, part := range parts {
				parts[i] = strings.TrimSpace(part)
				v, err := strconv.Atoi(parts[i])
				if err != nil || v < 0 || v > 255 {
					return Node{}, errors.New("reserved 应为三个 0–255 的数")
				}
			}
			n.Reserved = strings.Join(parts, ",")
		}
		return n, nil
	default:
		return Node{}, fmt.Errorf("不支持手动填写 %s 节点", n.ProtocolName())
	}
	if n.Protocol != Hysteria2 {
		var err error
		if n.Network, err = network(n.Network); err != nil {
			return Node{}, err
		}
		n.Security = strings.ToLower(n.Security)
		if n.Security == "" {
			n.Security = "none"
			if n.Protocol == Trojan {
				n.Security = "tls"
			}
		}
		if n.Protocol == VMess && n.Security == "none" {
			n.Security = ""
		}
	}
	if err := validJSON("fm", n.FinalMask); err != nil {
		return Node{}, err
	}
	if err := validJSON("extra", n.Extra); err != nil {
		return Node{}, err
	}
	return n, nil
}
