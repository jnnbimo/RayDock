package gui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app/apptest"
)

func TestZoom(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		displayW, displayH, roomW, roomH int
		want                             float32
	}{
		{"1080p at 100%, or 4K at 200%", 1920, 1080, 1920, 1032, 1},
		{"1440p at 100%, or 4K at 150%", 2560, 1440, 2560, 1392, 1.3},
		{"ultrawide 3440x1440", 3440, 1440, 3440, 1392, 1.3},
		{"4K at 100%", 3840, 2160, 3840, 2112, 2},
		{"1080p at 125%: the user's scale is kept", 1536, 864, 1536, 816, 1},
		{"768p at 100%: fits as it is", 1366, 768, 1366, 728, 1},
		{"768p at 125%: shrinks to fit", 1093, 614, 1093, 566, 0.85},
		{"720p at 150%: no smaller than 80%, scrolls", 853, 480, 853, 432, 0.8},
	} {
		z := Zoom(tc.displayW, tc.displayH, tc.roomW, tc.roomH)
		if z != tc.want {
			t.Errorf("%s: zoom %v, want %v", tc.name, z, tc.want)
		}
		if z > minZoom && (MainWidth*float64(z) > float64(tc.roomW)+0.5 || MainHeight*float64(z) > float64(tc.roomH)+0.5) {
			t.Errorf("%s: the page zoomed by %v does not fit %dx%d", tc.name, z, tc.roomW, tc.roomH)
		}
	}
}

// Zoomed, the page keeps the layout it has at 100%, only larger or smaller: everything is where it is
// there, by the zoom, and it does not scroll.
func TestZoomedPage(t *testing.T) {
	place := func(z float32) map[string]ui.Rect {
		h := apptest.New(t)
		v := New(h.A)
		v.SetZoom(z)
		tt := ui.NewTester(v.Build, int(MainWidth*z+0.99), int(MainHeight*z+0.99))
		tt.Frame()
		tt.Frame()
		out := map[string]ui.Rect{}
		for _, s := range []string{"首页", "流量统计", "连接后检测常用网站与 AI 服务", h.A.ProxyAddr(), "\ue8bb"} {
			r, ok := tt.Find(s)
			if !ok {
				t.Fatalf("zoom %v: no %q", z, s)
			}
			out[s] = r
		}
		return out
	}
	base := place(1)
	for _, z := range []float32{0.85, 1.3, 2} {
		for s, r := range place(z) {
			b := base[s]
			if d := max(abs(r.X-b.X*z), abs(r.Y-b.Y*z), abs(r.W-b.W*z), abs(r.H-b.H*z)); d > 2*z {
				t.Errorf("zoom %v: %q at %v, want %v zoomed", z, s, r, b)
			}
		}
	}
}

func abs(x float32) float32 { return max(x, -x) }
