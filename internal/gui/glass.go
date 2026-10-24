package gui

import (
	"image"
	"image/color"
	"math"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
)

// glowBitmap is a soft round glow of col, fading to nothing at its edge. Drawn scaled, it costs a plain
// image, where blurring an element at run time renders and samples far beyond it on every frame.
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

// glow returns the bitmap of a glow of col, made on first use: each theme has its own colors.
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

// backdropCache keeps only the current window's ambient image. The background
// varies over hundreds of DIPs, so four DIPs per texel is enough even at high DPI.
// Compositing the gradient and four glows once avoids resampling all five layers
// across the entire window whenever software rendering loses its idle buffers.
type backdropCache struct {
	canvas     *ui.Canvas
	box, frame ui.Rect
	dark       bool
}

// backdrop paints the window: a gradient under large soft glows, spread so that every pane of glass has
// light to refract. Glass shows only what the window painted under it; over a flat color it would be a
// plain frosted pane.
//
// It is drawn, under the children: absolute elements would paint after all the others, over the panes.
func (a *View) backdrop(c *ui.Context, body func()) { a.backdropIn(c, nil, body) }

// backdropIn is backdrop painted as a window of frame would paint it, frame given the element's box: a
// window showing what a part of the main window shows paints that part of it. nil is the element's box.
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

// pane fills e, a card, with glass. Every pane of glass has a hairline rim: the glass lights only its top
// and bottom edges, which leaves its sides unclear.
func (p palette) pane(e ui.Element) ui.Element {
	return e.Material(glass.Glass{}).Border(1, p.border)
}

// popup fills e, anything floating over the window (a dialog, a menu, the options of a select, a toast,
// the tray menu's content), with glass: lightly tinted toward the background, so that its text reads
// over whatever is under it, and still showing it; with one rim and one shadow for all.
func (p palette) popup(e ui.Element) ui.Element {
	return e.Material(glass.Glass{Tint: p.bg.Alpha(0.22)}).Border(1, raisedEdge(p.dark, p.border)).
		Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.22))
}

// toasts shows the window's toasts as its other floating panels are, glass (popup), at the bottom
// center; one fades out as its time ends.
func (a *View) toasts(c *ui.Context) {
	p, t := a.pal, a.theme
	w, _ := c.Size()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(0, 0, t.Space(6), 0).AlignItems(ui.Center).Gap(t.Space(2))
		for _, ts := range toasts {
			toast := ui.ToastBase(c, ts)
			box := p.popup(toast.Root.Row().AlignItems(ui.Center).Padding(t.Space(2.5), t.Space(4)).Radius(t.Radius + 4).
				MaxWidth(w - t.Space(12)))
			const fade = 200 * time.Millisecond
			if left := toast.Left(); left > 0 && left < fade {
				box.Opacity(float32(left) / float32(fade))
				c.AnimationFrame()
			} else if left > 0 {
				c.After(left - fade)
			}
			box.Children(func() { ui.Text(c, ts.Title).TextColor(p.text) })
		}
	})
}

// choosable makes e an item that may be chosen (chosen) or hovered: it keeps chosen's rim, clear, so
// that neither moves anything. A rim takes room: given only to the chosen item, it would push the items
// after it as the pointer goes over them.
func choosable(e ui.Element) ui.Element { return e.Border(1, ui.Transparent) }

// chosen marks e as the chosen one among several, as iOS does: a lighter capsule of colorless frosted
// glass with a bright rim, in place of a solid accent fill, so that what is on it keeps its colors.
func (p palette) chosen(e ui.Element) ui.Element {
	return lift(p.dark, e.Material(glass.Glass{Tint: p.fillOn}).Border(1, raisedEdge(p.dark, p.border)))
}

// powerColor is the color of what the power button does in state conn, as start and stop buttons are
// colored: green to connect, red to disconnect, amber while connecting (a click cancels); and its tooltip.
func (p palette) powerColor(conn app.ConnState) (ui.Color, string) {
	switch conn {
	case app.StateOn:
		return p.danger, "断开连接"
	case app.StateConnecting:
		return p.warning, "取消连接"
	}
	return p.success, "连接"
}

// signalGlass fills e, a control whose color says what it does, with glass tinted col and a rim of col;
// hovering deepens the tint.
func (p palette) signalGlass(e ui.Element, col ui.Color, hovered bool) ui.Element {
	tint := col.Alpha(0.16)
	if hovered {
		tint = col.Alpha(0.26)
	}
	return lift(p.dark, e.Material(glass.Glass{Tint: tint, Interactive: true}).Border(1, col.Alpha(0.55)))
}

// raisedEdge is the rim of glass raised over other glass, as a chosen row or a prominent button: brighter
// than border on dark glass; on light glass, where white would vanish, border, with lift's shadow.
func raisedEdge(dark bool, border ui.Color) ui.Color {
	if dark {
		return ui.Hex("#ffffff").Alpha(0.34)
	}
	return border
}

// colorless is the theme of a built-in widget that paints its chosen item with the accent, which this
// look leaves colorless: accent stands for it, and what is on the item keeps the text color. The item
// is then marked by chosen.
func (a *View) colorless(accent ui.Color) *ui.Theme {
	t := a.theme
	t.Accent, t.AccentText = accent, a.pal.text
	return &t
}

// glassButton fills a button with colorless glass that grows while pressed, frosted by the theme's
// surface, more for a prominent button; hovering lightens it.
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

// lift shadows a small control of glass in light mode, where its rim alone, over a light window, shows
// only its sides.
func lift(dark bool, e ui.Element) ui.Element {
	if !dark {
		e.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.12))
	}
	return e
}
