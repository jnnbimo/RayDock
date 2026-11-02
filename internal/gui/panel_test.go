package gui

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"raydock/internal/app"
	"raydock/internal/app/apptest"
)

func TestPowerKeyboardActivation(t *testing.T) {
	for _, key := range []ui.Key{ui.KeyEnter, ui.KeySpace} {
		t.Run(map[ui.Key]string{ui.KeyEnter: "Enter", ui.KeySpace: "Space"}[key], func(t *testing.T) {
			h := apptest.New(t)
			a := h.A
			t.Cleanup(a.Shutdown)
			a.ImportLinks(apptest.LinkA, "")
			a.State.SelectedID = a.State.Nodes[0].ID
			h.Core.Err = errors.New("test offline")
			tt := ui.NewTester(nodesView(a).Build, 1180, 760)
			reached := false
			for range 20 {
				tt.Key(0, ui.KeyTab)
				tt.Frame()
				if tt.Focused("连接开关") {
					reached = true
					break
				}
			}
			if !reached {
				t.Fatal("connection power control is not reachable by Tab")
			}
			tt.Key(0, key)
			tt.Frame()
			if a.Conn != app.StateConnecting {
				t.Fatalf("keyboard did not activate power: conn=%v", a.Conn)
			}
			h.Wait(t, func() bool { return !a.CoreBusy })
			tt.Frame()
			if a.Conn != app.StateOff || a.ConnErr == "" {
				t.Fatal("keyboard connection did not report the download error")
			}
		})
	}
}
