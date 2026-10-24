package gui

import (
	"github.com/egoist/mygo/ui"

	"raydock/internal/node"
)

// palette holds the app colors for the current appearance; built-in controls share them through theme.
//
// The look is Liquid Glass: panes of glass over a colorful backdrop. Everything on the glass is a tone of
// white or black at some alpha rather than an opaque color, so that it reads over whatever the glass shows.
type palette struct {
	dark                     bool
	bg                       ui.Color    // the window's own color, under the backdrop, and the scroll edges'
	backdrop                 [2]ui.Color // the window's gradient, from its top left to its bottom right
	glows                    [4]glow     // the light the glass refracts (ambient)
	fill, fillHover, fillOn  ui.Color    // frosted controls on the glass: inputs, buttons, chosen segments
	border, text, muted      ui.Color
	faint                    ui.Color // dividers; border is also the rim drawn around glass
	accent                   ui.Color // colorless: the glass look marks focus and choice in a tone of the text
	accentText               ui.Color // text on a fill of accent
	accent2                  ui.Color // the log's own lines
	success, warning, danger ui.Color
}

// glow is a soft round light of the backdrop.
type glow struct {
	color   ui.Color
	opacity float32
}

func newPalette(dark bool) palette {
	p := palette{dark: dark, accent2: ui.Hex("#22d3ee")}
	// Status colors are text as often as fills: lighter on dark glass, deeper on light glass.
	if dark {
		p.success, p.warning, p.danger = ui.Hex("#34d399"), ui.Hex("#fbbf24"), ui.Hex("#fb7185")
	} else {
		p.success, p.warning, p.danger = ui.Hex("#059669"), ui.Hex("#d97706"), ui.Hex("#e11d48")
	}
	if dark {
		p.bg, p.backdrop = ui.Hex("#0b0f24"), [2]ui.Color{ui.Hex("#0d1335"), ui.Hex("#1f0d36")}
		p.glows = [4]glow{{ui.Hex("#4f46e5"), 0.85}, {ui.Hex("#06b6d4"), 0.6}, {ui.Hex("#9333ea"), 0.7}, {ui.Hex("#db2777"), 0.45}}
		white := ui.Hex("#ffffff")
		p.fill, p.fillHover, p.fillOn = white.Alpha(0.08), white.Alpha(0.14), white.Alpha(0.2)
		p.border, p.faint = white.Alpha(0.22), white.Alpha(0.12)
		p.text, p.muted = ui.Hex("#f4f6ff"), ui.RGBA(226, 232, 255, 0.64)
		p.accent, p.accentText = ui.Hex("#b4bcdf"), ui.Hex("#0f1430")
	} else {
		p.bg, p.backdrop = ui.Hex("#eef1fb"), [2]ui.Color{ui.Hex("#dfe6ff"), ui.Hex("#fbe7f3")}
		p.glows = [4]glow{{ui.Hex("#818cf8"), 0.75}, {ui.Hex("#22d3ee"), 0.55}, {ui.Hex("#c084fc"), 0.6}, {ui.Hex("#f472b6"), 0.45}}
		ink := ui.RGB(15, 20, 48)
		white := ui.Hex("#ffffff")
		p.fill, p.fillHover, p.fillOn = white.Alpha(0.5), white.Alpha(0.72), white.Alpha(0.9)
		p.border, p.faint = ink.Alpha(0.14), ink.Alpha(0.08)
		p.text, p.muted = ink, ink.Alpha(0.62)
		p.accent, p.accentText = ui.Hex("#3b4262"), white
	}
	return p
}

func (p palette) theme(base ui.Theme) ui.Theme {
	t := base
	t.Background = p.bg
	// Controls are frosted: translucent over the glass they sit on.
	t.Surface, t.SurfaceHover, t.SurfacePressed = p.fill, p.fillHover, p.fillOn
	t.Border = p.border
	t.Text, t.TextMuted = p.text, p.muted
	t.Accent = p.accent
	t.AccentHover = p.accent.Mix(ui.Hex("#ffffff"), 0.12)
	t.AccentPressed = p.accent.Mix(ui.Hex("#000000"), 0.15)
	t.AccentText = p.accentText
	t.Focus = p.accent.Alpha(0.6)
	t.Selection = p.accent.Alpha(0.3)
	t.Success, t.Warning, t.Danger = p.success, p.warning, p.danger
	// Tooltips are drawn in Inverse: the tone of the glass panels (popup), not an inverted one, nearly
	// opaque as a tooltip cannot be glass.
	if p.dark {
		t.Inverse = ui.Hex("#1d2347").Alpha(0.96)
	} else {
		t.Inverse = ui.Hex("#ffffff").Alpha(0.96)
	}
	t.InverseText = p.text
	t.Radius = 8
	return t
}

// on returns col as text or an icon on glass: lightened on dark glass and deepened on light glass, where
// the backdrop showing through may be as colorful as col itself.
func (p palette) on(col ui.Color) ui.Color {
	if p.dark {
		return col.Mix(ui.Hex("#ffffff"), 0.35)
	}
	return col.Mix(ui.Hex("#000000"), 0.2)
}

func protocolColor(protocol string) ui.Color {
	switch protocol {
	case node.Shadowsocks:
		return ui.Hex("#f59e0b")
	case node.VMess:
		return ui.Hex("#a855f7")
	case node.VLESS:
		return ui.Hex("#6366f1")
	case node.Trojan:
		return ui.Hex("#f43f5e")
	case node.Hysteria2:
		return ui.Hex("#10b981")
	case node.WireGuard:
		return ui.Hex("#ea580c")
	case node.MASQUE:
		return ui.Hex("#0ea5e9")
	case node.SOCKS:
		return ui.Hex("#14b8a6")
	case node.HTTP:
		return ui.Hex("#84cc16")
	}
	return ui.Hex("#64748b")
}

// latencyColor: green under 200 ms, yellow under 500 ms, red otherwise.
func (p palette) latencyColor(ms int64) ui.Color {
	switch {
	case ms < 200:
		return p.success
	case ms < 500:
		return p.warning
	}
	return p.danger
}

func icon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	icServer     = icon(`<rect x="2" y="3" width="20" height="7" rx="2"/><rect x="2" y="14" width="20" height="7" rx="2"/><path d="M6 6.5h.01M6 17.5h.01"/>`)
	icRSS        = icon(`<path d="M4 11a9 9 0 0 1 9 9M4 4a16 16 0 0 1 16 16"/><circle cx="5" cy="19" r="1"/>`)
	icTerminal   = icon(`<path d="m4 17 6-5-6-5M12 19h8"/>`)
	icSliders    = icon(`<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>`)
	icPower      = icon(`<path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.8 0"/>`)
	icPlus       = icon(`<path d="M12 5v14M5 12h14"/>`)
	icClip       = icon(`<rect x="8" y="2" width="8" height="4" rx="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>`)
	icZap        = icon(`<path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/>`)
	icRefresh    = icon(`<path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.5 9a9 9 0 0 1 14.9-3.4L23 10M1 14l4.6 4.4A9 9 0 0 0 20.5 15"/>`)
	icTrash      = icon(`<path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>`)
	icCopy       = icon(`<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`)
	icFolder     = icon(`<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/>`)
	icDownload   = icon(`<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>`)
	icGlobe      = icon(`<circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15 15 0 0 1 4 10 15 15 0 0 1-4 10 15 15 0 0 1-4-10 15 15 0 0 1 4-10z"/>`)
	icShield     = icon(`<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>`)
	icArrowUp    = icon(`<path d="M12 19V5M5 12l7-7 7 7"/>`)
	icArrowDown  = icon(`<path d="M12 5v14M19 12l-7 7-7-7"/>`)
	icFolderPlus = icon(`<path d="M12 10v6M9 13h6"/><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/>`)
	icPencil     = icon(`<path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/>`)
	icAlert      = icon(`<circle cx="12" cy="12" r="10"/><path d="M12 8v4M12 16h.01"/>`)
)
