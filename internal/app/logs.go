package app

import (
	"slices"
	"strings"
	"time"
)

func (a *App) QueueLog(line string) {
	a.logMu.Lock()
	a.logQueued = append(a.logQueued, line)
	a.logMu.Unlock()
}

// FlushLogs moves queued lines into the list and reports whether any arrived.
func (a *App) FlushLogs() bool {
	a.logMu.Lock()
	q := a.logQueued
	a.logQueued = nil
	a.logMu.Unlock()
	if len(q) == 0 {
		return false
	}
	for _, s := range q {
		lv := 0
		switch {
		case strings.Contains(s, "[Error]") || strings.Contains(s, "失败"):
			lv = 2
		case strings.Contains(s, "[Warning]"):
			lv = 1
		}
		a.Logs = append(a.Logs, LogLine{Text: s, Level: lv})
	}
	if over := len(a.Logs) - MaxLogs; over > 0 {
		a.Logs = slices.Delete(a.Logs, 0, over)
	}
	return true
}

// logFlush is how often queued output moves into Logs: a burst of core output repaints once.
const logFlush = 200 * time.Millisecond

// StartLogs moves queued output into Logs every logFlush until shutdown.
func (a *App) StartLogs() {
	if a.closing {
		return
	}
	a.jobs.Go(func() {
		t := time.NewTicker(logFlush)
		defer t.Stop()
		for {
			select {
			case <-a.workCtx.Done():
				return
			case <-t.C:
			}
			if a.logsPending() {
				a.Post(func() { a.FlushLogs() })
			}
		}
	})
}

// logsPending reports whether core output is waiting for FlushLogs.
func (a *App) logsPending() bool {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	return len(a.logQueued) > 0
}
