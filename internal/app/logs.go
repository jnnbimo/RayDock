package app

import (
	"slices"
	"strings"
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

// LogsPending reports whether core output is waiting for FlushLogs.
func (a *App) LogsPending() bool {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	return len(a.logQueued) > 0
}
