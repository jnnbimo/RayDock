package gui

import (
	"cmp"
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/egoist/mygo/ui"

	"raydock/internal/sysproxy"
	"raydock/internal/system"
)

var settingsTabs = []string{"常规", "分流", "DNS", "TUN", "关于"}

func (a *View) settingsPage(c *ui.Context) {
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(gapBlock).Padding(4, padPage, 0).Children(func() {
			a.pageHeader(c, "设置", "修改后自动保存", nil)
			navTabs(c.Key("settings-tabs"), &a.settingsTab, len(settingsTabs), func(i int) {
				ui.Text(c, settingsTabs[i]).FontSize(12.5).SingleLine()
			}, nil).Label("设置分栏")
		})
		ui.Scroll(c.Key(fmt.Sprint("settings-", a.settingsTab))).Grow(1).Padding(gapBlock, padPage, padPage).Children(func() {
			ui.Column(c).Gap(22).Children(func() {
				switch a.settingsTab {
				case 0:
					a.generalSection(c)
					a.proxySection(c)
				case 1:
					a.routeSection(c)
					a.rulesSection(c)
					a.geoSection(c)
				case 2:
					a.dnsSection(c)
				case 3:
					a.tunSection(c)
				default:
					a.coreSection(c)
					a.aboutSection(c)
				}
			})
		})
	})
}

func (a *View) coreSection(c *ui.Context) {
	p := a.pal
	a.section(c, "Xray 核心", "负责代理连接的 Xray-core，不会自动更新", func(f *form) {
		f.row(c, "版本", a.coreStatusText(), func() {
			switch {
			case a.CoreBusy || a.CoreChecking:
				ui.Spinner(c).Size(14, 14)
			case a.CoreReady() && a.CoreVersion != "":
				a.chip(c, "Xray "+a.CoreVersion, p.success)
			case a.CoreReady():
				a.chip(c, "已就绪", p.success)
			default:
				a.chip(c, "未就绪", p.warning)
			}
			// Looking for a release installs nothing: an offered one is installed by a second click.
			switch {
			case a.CoreLatest != "" || !a.CoreReady():
				label := "更新到 " + a.CoreLatest
				if a.CoreLatest == "" {
					label = "下载核心"
				}
				if iconButton(c, icDownload, label).Disabled(a.CoreBusy || a.GeoBusy).Clicked() {
					a.InstallCore()
				}
			default:
				if iconButton(c, icRefresh, "检查更新").Disabled(a.CoreBusy || a.CoreChecking).Tooltip("查询最新正式版，不会自动安装").Clicked() {
					a.CheckCoreUpdate()
				}
			}
		})
		if a.CoreBusy {
			f.block(c, func() {
				ui.Progress(c, a.CoreProgress).Label("下载进度")
			})
		}
		f.row(c, "文件位置", a.CoreDir(), func() {
			if iconButton(c, icFolder, "打开目录").Clicked() {
				a.OpenPath(a.CoreDir())
			}
		})
	})
}

func (a *View) coreStatusText() string {
	switch {
	case a.CoreBusy:
		return cmp.Or(a.CoreMsg, "正在准备…")
	case a.CoreErr != "":
		return a.CoreErr
	case a.CoreLatest != "":
		return "有新版本 " + a.CoreLatest
	case a.CoreChecking:
		return "正在检查更新…"
	case a.CoreNote != "":
		return a.CoreNote
	case !a.CoreReady():
		return "连接时会自动下载"
	}
	return "包含 xray 与 wintun.dll"
}

func (a *View) tunSection(c *ui.Context) {
	p := a.pal
	t := &a.State.Settings.TUN
	a.section(c, "TUN 虚拟网卡", "通过虚拟网卡接管所有应用的流量，包括不读取系统代理的程序", func(f *form) {
		f.row(c, "启用 TUN 模式", "需要管理员权限", func() {
			switchControl(c.Key("tun"), &t.Enabled).Label("启用 TUN 模式").OnChange(func() { a.SetTUN(t.Enabled) })
		})
		f.row(c, "管理员权限", "创建虚拟网卡与修改路由需要", func() {
			if a.Elevated {
				a.chip(c, "已获得", p.success)
				return
			}
			a.chip(c, "未获得", p.warning)
			if iconButton(c, icShield, "以管理员身份重启").Clicked() {
				if err := a.RestartAsAdmin(); err != nil {
					c.Toast(err.Error())
				}
			}
		})
		f.row(c, "接管 IPv6", "同时把 IPv6 默认路由指向虚拟网卡", func() {
			switchControl(c.Key("tun-v6"), &t.IPv6).Label("接管 IPv6").OnChange(a.SettingChanged)
		})
		f.row(c, "防止泄漏", "阻止绕过虚拟网卡的 DNS 查询，以及未接管的 IP 协议", func() {
			switchControl(c.Key("tun-strict"), &t.Strict).Label("防止泄漏").OnChange(a.SettingChanged)
		})
		f.row(c, "阻止 QUIC", "让浏览器改用 TCP，以便按域名分流", func() {
			switchControl(c.Key("tun-quic"), &t.BlockQUIC).Label("阻止 QUIC").OnChange(a.SettingChanged)
		})
		f.row(c, "MTU", "虚拟网卡单个数据包的最大字节数", func() {
			mtu := float64(t.MTU)
			if mtu == 0 {
				mtu = 1500
			}
			apply := func() {
				t.MTU = int(mtu)
				a.SettingChanged()
			}
			if a.numberMenu(c, ui.NumberInput(c.Key("tun-mtu"), &mtu, 1280, 9000, 10).Label("MTU"), "tun-mtu", &mtu, 1280, 9000, apply).Changed() {
				apply()
			}
		})
	})
}

func (a *View) proxySection(c *ui.Context) {
	s := &a.State.Settings
	a.section(c, "本地代理", "SOCKS5 与 HTTP 共用一个端口，系统代理指向这里", func(f *form) {
		f.row(c, "端口", "默认 10818", func() {
			port := float64(s.Port)
			apply := func() {
				s.Port = int(port)
				a.SettingChanged()
			}
			if a.numberMenu(c, ui.NumberInput(c.Key("port"), &port, 1024, 65535, 1).Label("本地代理端口"), "port", &port, 1024, 65535, apply).Changed() {
				apply()
			}
		})
		ports := "未分配"
		if k := len(a.PortNodes()); k > 0 {
			ports = fmt.Sprintf("已分配 %d 个", k)
		}
		f.row(c, "节点专用端口", "连接专用端口的程序始终使用对应节点，在节点右键菜单中设置", func() {
			ui.Text(c, ports).FontSize(12.5).TextColor(a.pal.muted)
		})
		f.row(c, "允许局域网连接", "同一网络的其它设备可以使用本地代理与专用端口", func() {
			switchControl(c.Key("lan"), &s.AllowLAN).Label("允许局域网连接").OnChange(a.SettingChanged)
		})
		if !sysproxy.Supported() {
			f.row(c, "系统代理", "当前平台暂不支持自动设置，请手动配置", nil)
		}
	})
}

func (a *View) generalSection(c *ui.Context) {
	s := &a.State.Settings
	a.section(c, "启动与外观", "", func(f *form) {
		a.autostartRow(c, f)
		f.row(c, "启动后自动连接", "连接选中的节点，未选中时连接上次使用的节点", func() {
			switchControl(c.Key("auto"), &s.AutoConnect).Label("启动后自动连接").OnChange(a.Save)
		})
		f.row(c, "外观", "", func() {
			segmented(c.Key("appearance"), &s.Appearance, "跟随系统", "浅色", "深色").Label("外观").OnChange(func() {
				a.Save()
				a.SetAppearance(s.Appearance)
			})
		})
		levels := []string{"warning", "info", "debug"}
		idx := 0
		for i, l := range levels {
			if l == s.LogLevel {
				idx = i
			}
		}
		f.row(c, "日志级别", "info 及以上会记录每条连接", func() {
			segmented(c.Key("loglevel"), &idx, levels...).Label("日志级别").OnChange(func() {
				s.LogLevel = levels[idx]
				a.SettingChanged()
			})
		})
		f.row(c, "关闭窗口", "最小化到托盘继续运行", nil)
	})
}

func (a *View) autostartRow(c *ui.Context, f *form) {
	p := a.pal
	if !system.AutostartSupported() {
		f.row(c, "开机自启", "当前平台暂不支持", nil)
		return
	}
	desc := []string{
		"登录系统后不自动启动",
		"登录后在托盘中启动，开启 TUN 时需要管理员授权",
		"登录后以管理员身份在托盘中启动，开启 TUN 无需授权",
	}[a.StartMode]
	f.row(c, "开机自启", desc, func() {
		if a.StartBusy {
			ui.Spinner(c).Size(14, 14)
		}
		idx := int(a.StartMode)
		segmented(c.Key("autostart"), &idx, "关闭", "普通", "管理员").Label("开机自启").Disabled(a.StartBusy).
			OnChange(func() { a.SetAutostart(system.StartMode(idx)) })
	})
	if a.StartErr != "" {
		f.block(c, func() {
			ui.Text(c, a.StartErr).FontSize(12).TextColor(p.danger)
		})
	}
}

func (a *View) aboutSection(c *ui.Context) {
	p := a.pal
	mygoVersion := "未知"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d.Path == "github.com/egoist/mygo" {
				mygoVersion = d.Version
			}
		}
	}
	a.section(c, "关于", "", func(f *form) {
		f.block(c, func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Image(c, a.logo).Size(40, 40)
				ui.Column(c).Gap(2).Children(func() {
					ui.Text(c, "RayDock").FontSize(15).Bold()
					version := "版本 " + cmp.Or(a.Version, "开发版")
					a.copyMenu(c, ui.Text(c, version).FontSize(12).TextColor(p.muted).Selectable(), "version", version)
				})
			})
			ui.Grid(c).ColumnTracks(ui.FitContent(), ui.Fr(1)).GapX(24).GapY(8).FontSize(12.5).Children(func() {
				for _, kv := range [][2]string{
					{"界面", "MyGo " + mygoVersion + "（自绘原生 UI）"},
					{"Go", runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH},
				} {
					ui.Text(c, kv[0]).TextColor(p.muted)
					a.copyMenu(c, ui.Text(c.Key(kv[0]), kv[1]).Selectable().MinWidth(0), "about-"+kv[0], kv[1])
				}
			})
		})
		f.row(c, "数据目录", a.DataDir, func() {
			if iconButton(c, icFolder, "打开数据目录").Clicked() {
				a.OpenPath(a.DataDir)
			}
		})
	})
}
