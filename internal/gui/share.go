package gui

import (
	"github.com/egoist/mygo/ui"
	"rsc.io/qr"
)

// nodeShare shows a node's share link as a QR code above the link itself.
type nodeShare struct {
	open bool
	node string
	link string   // the link shown and encoded
	code *qr.Code // nil when the link exceeds a QR code's capacity
}

const (
	qrQuiet   = 4   // light margin scanners need around a code, in modules
	qrMaxSide = 264 // the tile's largest side, in DIPs
)

var (
	qrLight = ui.RGBA(255, 255, 255, 1)
	qrDark  = ui.RGBA(0, 0, 0, 1)
)

// encodeLink prefers medium error correction and falls back to low for long links.
func encodeLink(link string) (*qr.Code, error) {
	code, err := qr.Encode(link, qr.M)
	if err != nil {
		code, err = qr.Encode(link, qr.L)
	}
	return code, err
}

func (a *View) openShare(id string) { a.share = nodeShare{open: true, node: id} }

func (a *View) shareDialog(c *ui.Context) {
	p := a.pal
	s := &a.share
	n := a.NodeByID(s.node)
	if s.open && (n == nil || n.Link == "") {
		s.open = false
	}
	// Encode on opening, and again when a subscription update replaces the link.
	if s.open && n.Link != s.link {
		s.link = n.Link
		s.code, _ = encodeLink(s.link)
	}
	label := ""
	if n != nil {
		label = n.Label()
	}
	d := dialog{width: 420, title: "节点分享", icon: icQR, tone: p.accent}
	d.body = func() {
		ui.Column(c).Gap(12).AlignItems(ui.Center).Children(func() {
			if s.code != nil {
				qrTile(c, s.code)
			} else {
				ui.Text(c, "链接过长，无法生成二维码，请复制链接分享。").FontSize(12.5).TextColor(p.on(p.warning))
			}
			ui.Text(c, label).Bold().SingleLine().MaxWidth(340)
			box := ui.Box(c).AlignSelf(ui.Stretch).Padding(10, 12).Radius(10).Background(p.fill).Border(1, p.faint)
			box.Children(func() {
				a.copyMenu(c, ui.Text(c, s.link).Font("monospace").FontSize(12).LineHeight(1.45).Selectable(),
					"share-link", s.link)
			})
			ui.Text(c, "扫码或复制链接即可导入。分享内容包含节点凭据，请勿公开。").
				FontSize(12.5).TextColor(p.muted).TextAlign(ui.Center)
		})
	}
	d.actions = func() {
		if primaryButton(c, "复制链接").Clicked() {
			a.Clipboard.WriteText(s.link)
			c.Toast("已复制分享链接")
		}
	}
	a.dialog(c, &s.open, d)
}

// qrTile draws code dark on light in every theme, as scanners expect. Modules span whole DIPs, and
// painted edges snap to device pixels, so adjacent modules leave no seams.
func qrTile(c *ui.Context, code *qr.Code) ui.Element {
	n := code.Size + 2*qrQuiet
	m := float32(max(2, qrMaxSide/n))
	side := m * float32(n)
	return ui.Box(c).Size(side, side).Radius(12).Background(qrLight).Shrink(0).Label("节点二维码").
		Draw(func(pt *ui.Painter, r ui.Rect) {
			for y := range code.Size {
				for x := 0; x < code.Size; {
					if !code.Black(x, y) {
						x++
						continue
					}
					start := x
					for x < code.Size && code.Black(x, y) {
						x++
					}
					pt.Fill(ui.Rect{X: r.X + float32(qrQuiet+start)*m, Y: r.Y + float32(qrQuiet+y)*m,
						W: float32(x-start) * m, H: m}, qrDark, 0)
				}
			}
		})
}
