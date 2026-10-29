package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"raydock/internal/store"
	"raydock/internal/xray"
)

// Traffic statistics: the backend cores count what leaves through the proxy and direct, their APIs serve
// the counts (xray.ReadTraffic),
// a loop reads their counts every second, and the UI thread adds what moved to the day's total and to
// the rates of the last minute. The totals are kept in traffic.json, the rates only while RayDock runs.

const (
	// RateWindow is how far back Rates go.
	RateWindow = time.Minute
	// trafficDays is how many days of totals are kept: enough for this month and the one before.
	trafficDays = 62
	// trafficSaveDelay is how long traffic waits to be saved: it moves every second.
	trafficSaveDelay = time.Minute
)

// Rate is the traffic of the second ending At, in bytes per second; Up is sent.
type Rate struct {
	At       time.Time
	Up, Down float64
}

func (a *App) trafficPath() string { return filepath.Join(a.DataDir, "traffic.json") }

// loadTraffic reads the saved totals; a corrupt file is renamed aside, so it is not overwritten.
func (a *App) loadTraffic() {
	path := a.trafficPath()
	t, err := store.LoadTraffic(path)
	if err != nil {
		bad := fmt.Sprintf("%s.bad-%s", path, time.Now().Format("20060102-150405"))
		if rerr := os.Rename(path, bad); rerr != nil {
			// Saving would overwrite what could not be read.
			a.trafficBroken = true
			a.QueueLog(fmt.Sprintf("[RayDock] 读取流量统计失败，本次运行的流量不会保存：%v；备份失败：%v", err, rerr))
			return
		}
		a.QueueLog(fmt.Sprintf("[RayDock] 读取流量统计失败，已备份为 %s：%v", bad, err))
	}
	a.Traffic = t
}

// trafficSample remains queued until the UI consumes it, including during Shutdown.
type trafficSample struct {
	at      time.Time
	elapsed time.Duration
	moved   xray.Traffic
}

// readTraffic and recordTraffic require coreSet.mu. Retirement takes the final reading
// before forgetting the API and stopping the process. Unexpected exits can still lose
// bytes the dead process no longer exposes; normal retirement must not lose a whole tick.
func (s *coreSet) readTraffic(ctx context.Context, c *xray.Core) xray.Traffic {
	port := s.apis[c]
	if port <= 0 {
		return xray.Traffic{}
	}
	if s.client == nil {
		s.client = xray.StatsClient()
	}
	cur, err := xray.ReadTraffic(ctx, s.client, port)
	if err != nil {
		return xray.Traffic{} // keep the previous counters so a later successful reading catches up
	}
	if s.counts == nil {
		s.counts = map[*xray.Core]xray.Traffic{}
	}
	moved := cur.Since(s.counts[c])
	s.counts[c] = cur
	return moved
}

func (s *coreSet) recordTraffic(moved xray.Traffic, tick bool) {
	now := time.Now()
	if moved.Zero() && (!tick || !s.moving) {
		if tick {
			s.readAt = now
		}
		return
	}
	elapsed := time.Second
	if !s.readAt.IsZero() {
		elapsed = now.Sub(s.readAt)
	}
	s.readAt, s.moving = now, !moved.Zero()
	s.samples = append(s.samples, trafficSample{now, elapsed, moved})
}

func (s *coreSet) sampleTraffic(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var moved xray.Traffic
	for _, c := range s.cores {
		moved = moved.Add(s.readTraffic(ctx, c))
	}
	s.recordTraffic(moved, true)
	return len(s.samples) > 0
}

func (s *coreSet) takeTraffic() []trafficSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	samples := s.samples
	s.samples = nil
	return samples
}

func (s *coreSet) closeTraffic() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
}

// StartTraffic is idempotent; shutdown waits for the one sampler before its final save.
func (a *App) StartTraffic() {
	if a.closing || a.trafficDone != nil {
		return
	}
	a.trafficDone = make(chan struct{})
	go func() {
		defer close(a.trafficDone)
		a.trafficLoop()
	}()
}

func (a *App) trafficLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-a.workCtx.Done():
			return
		case <-tick.C:
		}
		if a.procs.sampleTraffic(a.workCtx) {
			a.Post(a.flushTraffic)
		}
	}
}

func (a *App) flushTraffic() {
	for _, s := range a.procs.takeTraffic() {
		a.addTraffic(s.at, s.elapsed, s.moved)
	}
}

// addTraffic counts what moved in the elapsed time up to now.
func (a *App) addTraffic(now time.Time, elapsed time.Duration, moved xray.Traffic) {
	day := now.Format(time.DateOnly)
	if a.Traffic.Days == nil {
		a.Traffic.Days = map[string]xray.Traffic{}
	}
	a.Traffic.Days[day] = a.Traffic.Days[day].Add(moved)

	// After an idle spell the rate rises from zero a second before, not across the spell.
	if n := len(a.Rates); n > 0 && now.Sub(a.Rates[n-1].At) > elapsed+500*time.Millisecond {
		a.Rates = append(a.Rates, Rate{At: now.Add(-elapsed)})
	}
	sec := max(elapsed.Seconds(), 0.001)
	a.Rates = append(a.Rates, Rate{At: now,
		Up: float64(moved.ProxyUp+moved.DirectUp) / sec, Down: float64(moved.ProxyDown+moved.DirectDown) / sec})
	// One before the window is kept, where the line enters it.
	keep := 0
	for keep+1 < len(a.Rates) && a.Rates[keep+1].At.Before(now.Add(-RateWindow)) {
		keep++
	}
	a.Rates = a.Rates[keep:]

	a.trafficDue = true
	a.scheduleTrafficSave()
}

func (a *App) scheduleTrafficSave() {
	if !a.closing && !a.trafficBroken && a.trafficTimer == nil {
		a.trafficTimer = time.AfterFunc(trafficSaveDelay, func() {
			a.Post(func() {
				a.trafficTimer = nil
				a.saveTraffic()
			})
		})
	}
}

// saveTraffic writes the totals, without the days too old to keep.
func (a *App) saveTraffic() {
	if !a.trafficDue || a.trafficBroken {
		return
	}
	oldest := time.Now().AddDate(0, 0, -trafficDays).Format(time.DateOnly)
	for day := range a.Traffic.Days {
		if day < oldest {
			delete(a.Traffic.Days, day)
		}
	}
	if err := store.SaveTraffic(a.trafficPath(), a.Traffic); err != nil {
		a.QueueLog("[RayDock] 保存流量统计失败：" + err.Error())
		a.scheduleTrafficSave()
		return
	}
	a.trafficDue = false
}

// TrafficOn returns the traffic of the day of t.
func (a *App) TrafficOn(t time.Time) xray.Traffic { return a.Traffic.Days[t.Format(time.DateOnly)] }

// TrafficMonth returns the traffic of the month of t.
func (a *App) TrafficMonth(t time.Time) xray.Traffic {
	var sum xray.Traffic
	month := t.Format("2006-01-")
	for day, u := range a.Traffic.Days {
		if len(day) > len(month) && day[:len(month)] == month {
			sum = sum.Add(u)
		}
	}
	return sum
}
