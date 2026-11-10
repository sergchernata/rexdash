package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestKeysTonesAndTimes(t *testing.T) {
	for _, ev := range []*tcell.EventKey{key(tcell.KeyEscape), key(tcell.KeyLeft), key(tcell.KeyBackspace2), rkey('q'), rkey('h')} {
		if !IsBack(ev) {
			t.Errorf("IsBack(%s) false", ev.Name())
		}
	}
	if IsBack(rkey('x')) || IsEnter(rkey('x')) {
		t.Error("x is neither back nor enter")
	}
	for _, ev := range []*tcell.EventKey{key(tcell.KeyEnter), key(tcell.KeyRight), rkey('l')} {
		if !IsEnter(ev) {
			t.Errorf("IsEnter(%s) false", ev.Name())
		}
	}

	for s, want := range map[string]struct {
		c  Color
		ic string
	}{
		"SUCCESS": {Green(), OK.S()}, "FAILURE": {Red(), FAIL.S()},
		"QUEUED": {Yellow(), RUNNING.S()}, "whatever": {Muted(), PENDING.S()},
	} {
		sp := StatusIcon(s)
		if StatusColor(s) != want.c || sp.Style.Fg != want.c || !sp.Style.Bold || sp.Text != want.ic {
			t.Errorf("%s: %v %+v", s, StatusColor(s), sp)
		}
	}

	if ParseTime("2026-01-02T03:04:05Z") != 1767323045 || ParseTime("nope") != 0 {
		t.Error("ParseTime")
	}
	if Local(0, "15:04") != "" || Local(1767323045, "2006") != "2026" {
		t.Error("Local")
	}
}
