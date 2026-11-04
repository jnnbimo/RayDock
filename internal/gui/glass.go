package gui

import (
	"image"
	"image/color"
	"math"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// glowBitmap precomputes a soft glow instead of blurring it on every frame.
func glowBitmap(col ui.Color) *ui.Bitmap {
	const n = 96
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	const sigma = 0.4 // of the radius
	edge := math.Exp(-1 / (2 * sigma * sigma))
	for y := range n {
		for x := range n {
			dx, dy := (float64(x)+0.5)/n*2-1, (float64(y)+0.5)/n*2-1
			a := (math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)) - edge) / (1 - edge)
			img.SetNRGBA(x, y, color.NRGBA{col.R, col.G, col.B, uint8(255 * max(a, 0))})
		}
	}
	return ui.NewBitmap(img)
}

func (a *View) glow(col ui.Color) *ui.Bitmap {
	if a.glows == nil {
		a.glows = map[ui.Color]*ui.Bitmap{}
	}
	b := a.glows[col]
	if b == nil {
		b = glowBitmap(col)
		a.glows[col] = b
	}
	return b
}

// Cache only the current background at four DIPs per texel; its light varies slowly.
// Text and glass remain full-resolution and are rendered separately.
type backdropCache struct {
	canvas     *ui.Canvas
	box, frame ui.Rect
	dark       bool
}

// Draw the backdrop before children; absolute elements would cover the glass.
func (a *View) backdrop(c *ui.Context, body func()) { a.backdropIn(c, nil, body) }

// backdropIn optionally crops the main-window backdrop for another surface.
func (a *View) backdropIn(c *ui.Context, frame func(ui.Rect) ui.Rect, body func()) {
	p := a.pal
	var bitmaps [len(p.glows)]*ui.Bitmap
	for i, g := range p.glows {
		bitmaps[i] = a.glow(g.color)
	}
	gradient := ui.LinearGradient{From: p.backdrop[0], To: p.backdrop[1], Angle: 135, Oklab: true}
	paint := func(pt *ui.Painter, r ui.Rect, scale float32) {
		pt.FillGradient(r, gradient, 0)
		// Each glow is placed by its center, from the window's corners.
		glow := func(i int, size, cx, cy float32) {
			dst := ui.Rect{X: cx - size/2, Y: cy - size/2, W: size, H: size}
			pt.ImageRegion(bitmaps[i], ui.Rect{}, dst, ui.ImageOptions{Tint: ui.Hex("#ffffff").Alpha(p.glows[i].opacity)})
		}
		right, bottom := r.X+r.W, r.Y+r.H
		glow(0, 900*scale, right-160*scale, r.Y+40*scale)
		glow(1, 720*scale, r.X+150*scale, bottom-30*scale)
		glow(2, 680*scale, right-380*scale, bottom-170*scale)
		glow(3, 560*scale, r.X+560*scale, r.Y+260*scale)
	}
	ui.Box(c).Fill().Draw(func(pt *ui.Painter, box ui.Rect) {
		r := box
		if frame != nil {
			r = frame(box)
		}
		a.software = !pt.Accelerated()
		if !a.software {
			paint(pt, r, 1)
			return
		}
		if box.W <= 0 || box.H <= 0 {
			return
		}
		cache := &a.ambient
		if cache.canvas == nil || cache.box != box || cache.frame != r || cache.dark != p.dark {
			const scale = float32(0.25)
			w, h := int(math.Ceil(float64(box.W*scale))), int(math.Ceil(float64(box.H*scale)))
			cache.canvas = ui.NewCanvas(w, h)
			cache.box, cache.frame, cache.dark = box, r, p.dark
			r = ui.Rect{X: (r.X - box.X) * scale, Y: (r.Y - box.Y) * scale, W: r.W * scale, H: r.H * scale}
			cache.canvas.Draw(pt, func(cp *ui.Painter) { paint(cp, r, scale) })
		}
		pt.Image(cache.canvas.Bitmap(), box, ui.FillBox)
	}).Children(body)
}

// paneClarity lets the backdrop's light read through the window's base panes, the page cards and the
// sidebar's connection panel (and the tray menu, which looks as it does), so they look like glass rather
// than frosted cards; clearer, they would vanish into the light theme's backdrop.
const paneClarity = 0.6

func (p palette) pane(e ui.Element) ui.Element {
	return e.Material(glass.Glass{Clarity: paneClarity}).Border(1, p.border)
}

// popup shares the tint, rim and shadow of floating surfaces.
func (p palette) popup(e ui.Element) ui.Element {
	return e.Material(glass.Glass{Tint: p.bg.Alpha(0.22)}).Border(1, raisedEdge(p.dark, p.border)).
		Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.22))
}

// Reserve the selection border so choosing or hovering never shifts layout.
func choosable(e ui.Element) ui.Element { return e.Border(1, ui.Transparent) }

func (p palette) chosen(e ui.Element) ui.Element {
	return lift(p.dark, e.Material(glass.Glass{Tint: p.fillOn}).Border(1, raisedEdge(p.dark, p.border)))
}

func (p palette) signalGlass(e ui.Element, col ui.Color, hovered bool) ui.Element {
	tint := col.Alpha(0.16)
	if hovered {
		tint = col.Alpha(0.26)
	}
	return lift(p.dark, e.Material(glass.Glass{Tint: tint, Interactive: true}).Border(1, col.Alpha(0.55)))
}

// raisedEdge keeps raised glass visible in both themes.
func raisedEdge(dark bool, border ui.Color) ui.Color {
	if dark {
		return ui.Hex("#ffffff").Alpha(0.34)
	}
	return border
}

// colorless replaces a built-in widget's accent selection with neutral glass.
func (a *View) colorless(accent ui.Color) *ui.Theme {
	t := a.theme
	t.Accent, t.AccentText = accent, a.pal.text
	return &t
}

func glassButton(c *ui.Context, b ui.Element, prominent bool) ui.Element {
	t := c.Theme()
	tint := t.Surface
	switch {
	case prominent && b.Hovered():
		tint = t.SurfacePressed
	case prominent || b.Hovered():
		tint = t.SurfaceHover
	}
	edge := t.Border
	if prominent {
		edge = raisedEdge(t.Dark, t.Border)
	}
	return lift(t.Dark, b.Material(glass.Glass{Tint: tint, Interactive: true}).Border(1, edge).TextColor(t.Text))
}

func lift(dark bool, e ui.Element) ui.Element {
	if !dark {
		e.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.12))
	}
	return e
}
