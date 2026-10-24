package gui

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"

	"raydock/internal/xray"
)

func (a *View) routeSection(c *ui.Context) {
	s := &a.State.Settings
	a.section(c, "路由", "规则的匹配方式与广告拦截", func(f *form) {
		strategies := []string{"AsIs", "IPIfNonMatch", "IPOnDemand"}
		idx := slices.Index(strategies, s.DomainStrategy)
		if idx < 0 {
			idx = slices.Index(strategies, xray.DefaultOptions().DomainStrategy)
		}
		f.row(c, "域名策略", "按域名未命中规则时，是否解析成 IP 再匹配 IP 规则", func() {
			segmented(c.Key("domain-strategy"), &idx, "仅匹配域名", "未命中时解析", "总是解析").Label("域名策略").
				OnChange(func() {
					s.DomainStrategy = strategies[idx]
					a.SettingChanged()
				})
		})
		f.row(c, "拦截广告", "阻止广告与跟踪域名", func() {
			switchControl(c.Key("block-ads"), &s.BlockAds).Label("拦截广告").OnChange(a.SettingChanged)
		})
	})
}

// modeSegmented picks the routing mode with equal-width segments that fill the row; changing it
// while connected switches it into the running core (SetMode).
func (a *View) modeSegmented(c *ui.Context) {
	t := c.Theme()
	s := &a.State.Settings
	modes := []string{xray.ModeRule, xray.ModeGlobal, xray.ModeDirect}
	labels := []string{"绕过大陆", "全局", "直连"}
	idx := max(slices.Index(modes, s.Mode), 0)
	seg := ui.SegmentedBase(c.Key("mode"), &idx, len(modes))
	segmentTrack(t, seg.Track).Label("路由模式").
		OnChange(func() {
			a.SetMode(modes[idx])
		}).
		Children(func() {
			for i, label := range labels {
				b := segmentItem(t, seg.Segment(i), i == idx).Basis(0).Grow(1).MinWidth(0).Padding(t.Space(1), 0).Label(label).
					Tooltip(modeHint(modes[i])) // each mode explains itself, not the one chosen
				b.Children(func() { ui.Text(c, label).FontSize(12).SingleLine() })
			}
		})
}

func modeHint(mode string) string {
	switch mode {
	case xray.ModeGlobal:
		return "除局域网外全部走代理"
	case xray.ModeDirect:
		return "全部直连，不使用节点"
	}
	return "中国大陆网站与 IP 直连，其余走代理"
}

func (a *View) rulesSection(c *ui.Context) {
	p := a.pal
	s := &a.State.Settings
	tabs := []struct {
		name, hint string
		text       *string
	}{
		{"代理", "这些目标总是走代理", &s.ProxyRules},
		{"直连", "这些目标总是直连", &s.DirectRules},
		{"阻止", "这些目标直接拒绝", &s.BlockRules},
	}
	a.section(c, "自定义规则", "优先于路由模式生效；匹配顺序为 阻止 → 代理 → 直连（全局模式下直连优先）", func(f *form) {
		f.block(c, func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				navTabs(c.Key("rule-tabs"), &a.ruleTab, len(tabs), func(i int) { ui.Text(c, tabs[i].name).SingleLine() }, nil).Label("规则类别")
				ui.Spacer(c)
				ui.Text(c, tabs[a.ruleTab].hint).FontSize(12).TextColor(p.muted).SingleLine()
			})
			tab := tabs[a.ruleTab]
			rules := ui.TextArea(c.Key(fmt.Sprint("rules-", a.ruleTab)), tab.text).Lines(7, 16).Font("monospace").FontSize(12.5).
				Placeholder("每行一条，例如：\nopenai.com\ngeosite:netflix\n10.0.0.0/8\nprocess:steam.exe").
				Label(tab.name + "规则").OnChange(a.SettingChanged)
			a.textMenu(c, rules, fmt.Sprint("rules-", a.ruleTab), tab.text, true, a.SettingChanged)
			_, errs := xray.ParseRules(*tab.text)
			for i, e := range errs {
				if i == 3 {
					ui.Textf(c, "还有 %d 处错误", len(errs)-3).FontSize(12).TextColor(p.danger)
					break
				}
				ui.Text(c, e.Error()).FontSize(12).TextColor(p.danger)
			}
		})
		f.block(c, func() {
			ui.Grid(c).ColumnTracks(ui.FitContent(), ui.Fr(1)).GapX(16).GapY(6).FontSize(12).Children(func() {
				for _, kv := range [][2]string{
					{"example.com", "该域名及其子域名"},
					{"full: / keyword: / regexp:", "完整匹配、包含关键字、正则表达式"},
					{"geosite:netflix", "规则库中的域名分类"},
					{"1.2.3.4、10.0.0.0/8、geoip:jp", "IP、网段、规则库中的国家或地区"},
					{"chrome.exe、process:C:\\app\\x.exe", "进程名或完整路径（TUN 模式下最有用）"},
				} {
					ui.Text(c, kv[0]).Font("monospace").TextColor(p.text)
					ui.Text(c, kv[1]).TextColor(p.muted)
				}
			})
		})
	})
}

func (a *View) dnsSection(c *ui.Context) {
	p := a.pal
	s := &a.State.Settings
	a.section(c, "DNS", "国内域名用国内 DNS 解析，其余经代理用远程 DNS 解析", func(f *form) {
		dnsField := func(key, title, hint string, value *string, placeholder string) {
			f.block(c, func() {
				ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Gap(3).Width(200).Shrink(0).Children(func() {
						ui.Text(c, title).FontSize(13.5)
						ui.Text(c, hint).FontSize(12).TextColor(p.muted)
					})
					ui.Column(c).Gap(4).Grow(1).MinWidth(0).Children(func() {
						a.field(c, key, value, placeholder, a.SettingChanged)
						a.fieldError(c, xray.ValidateDNS(*value))
					})
				})
			})
		}
		dnsField("dns-direct", "国内 DNS", "直连查询", &s.DirectDNS, "223.5.5.5")
		dnsField("dns-remote", "远程 DNS", "经代理查询", &s.RemoteDNS, "8.8.8.8")
		f.block(c, func() {
			ui.Text(c, "支持 IP、IP:端口、https://（DoH）、tcp://、quic+local:// 与 localhost（系统 DNS）。").
				FontSize(12).TextColor(p.muted)
		})
		strategies := []string{"UseIPv4", "UseIP", "UseIPv6"}
		idx := max(slices.Index(strategies, s.DNSStrategy), 0)
		f.row(c, "解析的地址类型", "域名解析返回的 IP 地址类型", func() {
			segmented(c.Key("dns-strategy"), &idx, "仅 IPv4", "IPv4 + IPv6", "仅 IPv6").Label("解析的地址类型").
				OnChange(func() {
					s.DNSStrategy = strategies[idx]
					a.SettingChanged()
				})
		})
		f.row(c, "FakeDNS", "仅 TUN 模式：先返回虚拟地址，连接时再由代理解析", func() {
			switchControl(c.Key("fakedns"), &s.FakeDNS).Label("FakeDNS").OnChange(a.SettingChanged)
		})
		f.block(c, func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "静态解析（Hosts）").FontSize(13.5)
				ui.Spacer(c)
				ui.Text(c, "每行“域名 地址”，也接受 hosts 文件格式").FontSize(12).TextColor(p.muted)
			})
			hosts := ui.TextArea(c.Key("hosts"), &s.Hosts).Lines(3, 10).Font("monospace").FontSize(12.5).
				Placeholder("router.lan 192.168.1.1").Label("静态解析").OnChange(a.SettingChanged)
			a.textMenu(c, hosts, "hosts", &s.Hosts, true, a.SettingChanged)
			_, errs := xray.ParseHosts(s.Hosts)
			if len(errs) > 0 {
				ui.Text(c, errs[0].Error()).FontSize(12).TextColor(p.danger)
			}
		})
	})
}

// geoSection shows the geoip/geosite data the geoip: and geosite: rules use, and updates it.
func (a *View) geoSection(c *ui.Context) {
	p := a.pal
	a.section(c, "规则数据", "geoip: 与 geosite: 规则使用的 IP 与域名分类数据", func(f *form) {
		status := "来自 Loyalsoldier/v2ray-rules-dat，每日发布"
		switch {
		case a.GeoBusy:
			status = "正在下载…"
		case a.GeoMsg != "":
			status = a.GeoMsg
		}
		version := a.GeoVersion()
		f.row(c, "版本", status, func() {
			switch {
			case a.GeoBusy:
				ui.Spinner(c).Size(14, 14)
			case version != "":
				a.chip(c, version, p.success)
			default:
				a.chip(c, "未安装", p.warning)
			}
			if iconButton(c, icRefresh, "更新规则").Disabled(a.GeoBusy || a.CoreBusy || !a.CoreReady()).
				Tooltip("下载最新的 geoip.dat 与 geosite.dat").Clicked() {
				a.UpdateGeo()
			}
		})
		if a.GeoBusy {
			f.block(c, func() {
				ui.Progress(c, a.GeoProgress).Label("规则数据下载进度")
			})
		}
		if a.GeoErr != "" {
			f.block(c, func() {
				a.copyMenu(c, ui.Text(c, a.GeoErr).FontSize(12).TextColor(p.danger).Selectable(), "geo-error", a.GeoErr)
			})
		}
	})
}
