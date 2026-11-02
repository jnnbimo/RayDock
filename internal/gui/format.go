package gui

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(d.Hours()/24))
}

// rateUnit picks the unit rates up to peak are shown in.
func rateUnit(peak float64) (float64, string) {
	switch {
	case peak >= 1<<20:
		return 1 << 20, "MB/s"
	case peak >= 1<<10:
		return 1 << 10, "KB/s"
	}
	return 1, "B/s"
}

func formatSpeed(bps float64) string {
	unit, name := rateUnit(bps)
	return trimFloat(math.Round(bps/unit*10)/10) + " " + name
}

func formatBytes(n int64) string {
	const k = 1024
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= k && i < len(units)-1 {
		v /= k
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// trimFloat writes v with at most a decimal, none when it is whole.
func trimFloat(v float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}

// joinNonEmpty joins the parts that are not blank, a part repeating the one before once.
func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" && (len(out) == 0 || out[len(out)-1] != s) {
			out = append(out, s)
		}
	}
	return strings.Join(out, sep)
}
