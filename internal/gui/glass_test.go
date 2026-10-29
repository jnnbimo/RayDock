package gui

import (
	"bytes"
	"testing"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"raydock/internal/app/apptest"
)

// Software rendering must keep real refraction and blur, not approximate them
// with a translucent fill. Compare our surfaces with the plugin over sharp ink.
func TestSoftwareGlassPreserved(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, popup := range []bool{false, true} {
			render := func(reference bool) []byte {
				tt := ui.NewTester(func(c *ui.Context) {
					p := newPalette(c.Theme().Dark)
					theme := p.theme(*c.Theme())
					c.SetTheme(&theme)
					ui.Box(c).Fill().Draw(func(pt *ui.Painter, r ui.Rect) {
						for x := float32(0); x < r.W; x += 8 {
							col := ui.Hex("#16213e")
							if int(x/8)%2 == 0 {
								col = ui.Hex("#f1ccff")
							}
							pt.Fill(ui.Rect{X: x, W: 8, H: r.H}, col, 0)
						}
					}).Children(func() {
						panel := ui.Box(c).Absolute().Left(24).Top(20).Size(152, 80).Radius(14)
						switch {
						case reference && popup:
							panel.Material(glass.Glass{Tint: p.bg.Alpha(0.22)}).Border(1, raisedEdge(p.dark, p.border)).
								Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.22))
						case reference:
							panel.Material(glass.Glass{}).Border(1, p.border)
						case popup:
							p.popup(panel)
						default:
							p.pane(panel)
						}
					})
				}, 200, 120)
				tt.SetDark(dark)
				return tt.Image().Pix
			}
			if !bytes.Equal(render(false), render(true)) {
				t.Fatalf("glass differs from the full plugin in software: dark=%v popup=%v", dark, popup)
			}
		}
	}
}

func TestSoftwareBackdropCache(t *testing.T) {
	h := apptest.New(t)
	v := nodesView(h.A)
	tt := ui.NewTester(v.Build, MainWidth, MainHeight)
	if !v.SoftwareRendering() || v.ambient.canvas == nil {
		t.Fatal("headless rendering must use the cached backdrop")
	}
	first := v.ambient.canvas
	pixels := append([]byte(nil), tt.Image().Pix...)
	tt.Frame()
	if v.ambient.canvas != first || !bytes.Equal(pixels, tt.Image().Pix) {
		t.Fatal("an unchanged backdrop was rebuilt or changed its picture")
	}
	// Content changes do not invalidate ambient light.
	h.A.ImportLinks(apptest.LinkA, "")
	tt.Frame()
	if v.ambient.canvas != first || !tt.HasText("节点A") {
		t.Fatal("content changes must keep the background and update the controls")
	}
	// The image is smooth, not text: the same bounded resolution works at high DPI.
	tt.SetScale(2)
	if v.ambient.canvas != first {
		t.Fatal("DPI changes should not rebuild the low-frequency ambient image")
	}
	tt.SetDark(true)
	if v.ambient.canvas == first || !v.ambient.dark {
		t.Fatal("theme change must replace the cached backdrop")
	}
	dark := v.ambient.canvas
	tt.SetSize(980, 620)
	if v.ambient.canvas == dark || v.ambient.box.W != 980 || v.ambient.box.H != 620 {
		t.Fatal("resize must replace, not accumulate, cached backgrounds")
	}
	w, ht := v.ambient.canvas.Size()
	if w != 245 || ht != 155 {
		t.Fatalf("software backdrop size %dx%d, want 245x155", w, ht)
	}
	// Returning to the original theme and size produces the original ambient image.
	tt.SetScale(1)
	tt.SetDark(false)
	tt.SetSize(MainWidth, MainHeight)
	fresh := ui.NewTester(nodesView(h.A).Build, MainWidth, MainHeight)
	if !bytes.Equal(tt.Image().Pix, fresh.Image().Pix) {
		t.Fatal("resize/theme cycle left stale pixels")
	}
}

func TestSoftwareTrayBackdropCache(t *testing.T) {
	h := apptest.New(t)
	m := &trayMenu{View: &View{App: h.A}, post: func(func()) {}}
	m.height = m.trayHeight()
	tt := ui.NewTester(m.build, trayWidth, int(m.height))
	first := m.ambient.canvas
	if first == nil || m.ambient.frame.Y >= 0 {
		t.Fatal("the tray must cache the cropped main-window backdrop")
	}
	tt.Frame()
	if m.ambient.canvas != first {
		t.Fatal("the tray rebuilt unchanged ambient light")
	}
	tt.SetDark(true)
	if m.ambient.canvas == first || !m.ambient.dark {
		t.Fatal("the tray kept stale theme colors")
	}
}
