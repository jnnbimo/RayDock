package gui

import "math"

// The main window's page has one layout, at MainWidth by MainHeight, designed for a 1080p display at
// 100%. On another display it is zoomed (Context.SetZoom), as a whole, text included: it looks as it does
// there, taking the same share of the display, and is drawn at the zoomed size, not scaled as a picture,
// so that text and glass stay sharp.
const (
	// refWidth and refHeight are the display the layout is designed for, in DIPs.
	refWidth, refHeight = 1920, 1080
	// The page shrinks no further than minZoom to fit a small work area, and scrolls beyond, where its
	// text would be too small to read; it grows no further than maxZoom.
	minZoom, maxZoom = 0.8, 2.0
)

// Zoom returns the zoom of the main window's page on a display of displayW by displayH DIPs whose work
// area leaves roomW by roomH DIPs for it: the share of the display it has on the reference one, but never
// smaller than its size, which the display's scale set as the user chose, unless the room is too small
// for it. It goes in steps of 5%, down, so that it fits.
func Zoom(displayW, displayH, roomW, roomH int) float32 {
	z := max(1, min(float64(displayW)/refWidth, float64(displayH)/refHeight))
	z = min(z, float64(roomW)/MainWidth, float64(roomH)/MainHeight)
	z = math.Floor(z*20+1e-9) / 20
	return float32(min(max(z, minZoom), maxZoom))
}

// SetZoom zooms the page by z (Zoom) from the next frame, and reports whether that changes it; UI thread
// only.
func (a *View) SetZoom(z float32) bool {
	changed := z != a.zoom
	a.zoom = z
	return changed
}
