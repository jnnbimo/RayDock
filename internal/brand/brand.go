// Package brand draws the RayDock logo, a white lighthouse on a gradient squircle (a ray of light
// at a dock). The app, window, tray and sidebar icons all come from this one drawing.
package brand

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"golang.org/x/image/vector"
)

type Style int

const (
	Active Style = iota // theme gradient: app icon, connected
	Idle                // gray: tray when disconnected
)

// Theme colors, matching the UI accent.
var (
	accent  = color.NRGBA{0x63, 0x66, 0xf1, 0xff}
	accent2 = color.NRGBA{0x22, 0xd3, 0xee, 0xff}
	idle    = color.NRGBA{0x94, 0xa3, 0xb8, 0xff}
	idle2   = color.NRGBA{0x5b, 0x67, 0x7d, 0xff}
	white   = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// margin is the space around the squircle as a fraction of the size, matching the visual size of other icons.
const margin = 0.035

// Image draws the logo at size×size pixels.
func Image(size int, style Style) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	c := canvas{dst: img, size: float64(size)}
	from, to := accent, accent2
	if style == Idle {
		from, to = idle, idle2
	}

	// Background: a superellipse (continuous curvature) with a 135° gradient.
	c.fill(linear{c.px(0, 0), c.px(1, 1), from, to}, func(p pen) { p.squircle(0.5, 0.5, 0.5, 5) })
	c.zoom = 1
	// Optical sizing: at 32 px and below drop the glow and side beam and thicken the shapes.
	small := size <= 32
	w := 1.0 // stroke weight
	if small {
		w = 1.25
		c.zoom = 1.12 // enlarge the glyph to use more pixels
	}
	if !small {
		// Glow behind the lamp and the weaker beam to the left.
		c.fill(radial{c.px(lampX, lampY), 0.30 * c.scale(), white, 0.28}, func(p pen) { p.circle(lampX, lampY, 0.30) })
		c.fill(fade{c.px(lampX, lampY), 0.40 * c.scale(), 0.45}, func(p pen) {
			p.poly([2]float64{lampX, lampY - 0.025}, [2]float64{0.08, 0.27}, [2]float64{0.08, 0.41}, [2]float64{lampX, lampY + 0.025})
		})
	}
	if size > 24 {
		// Main beam to the upper right, fading out.
		c.fill(fade{c.px(lampX, lampY), 0.46 * c.scale(), 0.9}, func(p pen) {
			p.poly([2]float64{lampX, lampY - 0.03}, [2]float64{0.97, 0.13}, [2]float64{0.97, 0.37}, [2]float64{lampX, lampY + 0.03})
		})
	}

	solid := image.NewUniform(white)
	c.fill(solid, func(p pen) {
		p.poly([2]float64{0.5 - 0.095*w, 0.292}, [2]float64{0.5, 0.208}, [2]float64{0.5 + 0.095*w, 0.292}) // roof
		p.circle(lampX, lampY, 0.056*w)                                                                    // lamp
		p.roundRect(0.5-0.105*w, 0.425, 0.5+0.105*w, 0.425+0.032*w, 0.016*w)                               // gallery
		// Tower: a tapering trapezoid with a diagonal gap stripe, left out when small.
		if small {
			p.poly(towerPt(0, towerTop, w), towerPt(1, towerTop, w), towerPt(1, towerBottom, w), towerPt(0, towerBottom, w))
		} else {
			p.poly(towerPt(0, towerTop, w), towerPt(1, towerTop, w), towerPt(1, 0.575, w), towerPt(0, 0.615, w))
			p.poly(towerPt(0, 0.665, w), towerPt(1, 0.625, w), towerPt(1, towerBottom, w), towerPt(0, towerBottom, w))
		}
		p.roundRect(0.5-0.285*w, 0.805, 0.5+0.285*w, 0.805+0.045*w, 0.0225*w) // pier
	})
	return img
}

func PNG(size int, style Style) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, Image(size, style))
	return b.Bytes()
}

const (
	lampX, lampY          = 0.5, 0.36
	towerTop, towerBottom = 0.475, 0.785
)

// towerPt is the left (side=0) or right (side=1) edge of the tower at height y, scaled by w.
func towerPt(side int, y, w float64) [2]float64 {
	t := (y - towerTop) / (towerBottom - towerTop)
	half := (0.065 + (0.125-0.065)*t) * w
	if side == 0 {
		return [2]float64{0.5 - half, y}
	}
	return [2]float64{0.5 + half, y}
}

// canvas maps the logo's unit square (inside the margin) to pixels, zooming the glyph around (0.5, 0.53).
type canvas struct {
	dst  *image.RGBA
	size float64
	zoom float64
}

func (c canvas) scale() float64 { return c.size * (1 - 2*margin) }

func (c canvas) px(x, y float64) [2]float64 {
	if c.zoom != 0 && c.zoom != 1 {
		x, y = 0.5+(x-0.5)*c.zoom, 0.53+(y-0.53)*c.zoom
	}
	return [2]float64{c.size*margin + x*c.scale(), c.size*margin + y*c.scale()}
}

func (c canvas) fill(src image.Image, build func(p pen)) {
	r := vector.NewRasterizer(c.dst.Bounds().Dx(), c.dst.Bounds().Dy())
	r.DrawOp = draw.Over
	build(pen{r, c})
	r.Draw(c.dst, c.dst.Bounds(), src, image.Point{})
}

type pen struct {
	r *vector.Rasterizer
	c canvas
}

func (p pen) move(x, y float64) {
	q := p.c.px(x, y)
	p.r.MoveTo(float32(q[0]), float32(q[1]))
}

func (p pen) line(x, y float64) {
	q := p.c.px(x, y)
	p.r.LineTo(float32(q[0]), float32(q[1]))
}

func (p pen) poly(pts ...[2]float64) {
	p.move(pts[0][0], pts[0][1])
	for _, q := range pts[1:] {
		p.line(q[0], q[1])
	}
	p.r.ClosePath()
}

// squircle draws a superellipse with exponent n.
func (p pen) squircle(cx, cy, rad, n float64) {
	const steps = 256
	for i := range steps {
		a := 2 * math.Pi * float64(i) / steps
		cos, sin := math.Cos(a), math.Sin(a)
		x := cx + rad*math.Copysign(math.Pow(math.Abs(cos), 2/n), cos)
		y := cy + rad*math.Copysign(math.Pow(math.Abs(sin), 2/n), sin)
		if i == 0 {
			p.move(x, y)
		} else {
			p.line(x, y)
		}
	}
	p.r.ClosePath()
}

func (p pen) circle(cx, cy, rad float64) {
	const steps = 96
	for i := range steps {
		a := 2 * math.Pi * float64(i) / steps
		x, y := cx+rad*math.Cos(a), cy+rad*math.Sin(a)
		if i == 0 {
			p.move(x, y)
		} else {
			p.line(x, y)
		}
	}
	p.r.ClosePath()
}

func (p pen) roundRect(x0, y0, x1, y1, rad float64) {
	corners := [4][3]float64{{x1 - rad, y0 + rad, -90}, {x1 - rad, y1 - rad, 0}, {x0 + rad, y1 - rad, 90}, {x0 + rad, y0 + rad, 180}}
	const steps = 12
	first := true
	for _, k := range corners {
		for i := 0; i <= steps; i++ {
			a := (k[2] + 90*float64(i)/steps) * math.Pi / 180
			x, y := k[0]+rad*math.Cos(a), k[1]+rad*math.Sin(a)
			if first {
				p.move(x, y)
				first = false
			} else {
				p.line(x, y)
			}
		}
	}
	p.r.ClosePath()
}

type linear struct {
	a, b     [2]float64
	from, to color.NRGBA
}

func (g linear) ColorModel() color.Model { return color.NRGBAModel }
func (g linear) Bounds() image.Rectangle { return image.Rect(-1e6, -1e6, 1e6, 1e6) }
func (g linear) At(x, y int) color.Color {
	dx, dy := g.b[0]-g.a[0], g.b[1]-g.a[1]
	t := ((float64(x)+0.5-g.a[0])*dx + (float64(y)+0.5-g.a[1])*dy) / (dx*dx + dy*dy)
	return mix(g.from, g.to, t)
}

// radial fades col from alpha at center to transparent at rad.
type radial struct {
	center [2]float64
	rad    float64
	col    color.NRGBA
	alpha  float64
}

func (g radial) ColorModel() color.Model { return color.NRGBAModel }
func (g radial) Bounds() image.Rectangle { return image.Rect(-1e6, -1e6, 1e6, 1e6) }
func (g radial) At(x, y int) color.Color {
	d := math.Hypot(float64(x)+0.5-g.center[0], float64(y)+0.5-g.center[1]) / g.rad
	a := g.alpha * smooth(1-d)
	c := g.col
	c.A = uint8(255 * a)
	return c
}

// fade is a white beam fading from alpha to 0 within reach of the lamp.
type fade struct {
	center [2]float64
	reach  float64
	alpha  float64
}

func (g fade) ColorModel() color.Model { return color.NRGBAModel }
func (g fade) Bounds() image.Rectangle { return image.Rect(-1e6, -1e6, 1e6, 1e6) }
func (g fade) At(x, y int) color.Color {
	d := math.Hypot(float64(x)+0.5-g.center[0], float64(y)+0.5-g.center[1]) / g.reach
	return color.NRGBA{0xff, 0xff, 0xff, uint8(255 * g.alpha * smooth(1-d))}
}

func smooth(t float64) float64 {
	t = math.Max(0, math.Min(1, t))
	return t * t * (3 - 2*t)
}

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	t = math.Max(0, math.Min(1, t))
	l := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), l(a.A, b.A)}
}
