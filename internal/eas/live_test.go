//go:build live

package eas

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

// EXPO_DIR=/path/to/expo/app go test -tags live -run Live -v ./internal/eas/
func TestLive(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	dir := os.Getenv("EXPO_DIR")
	if dir == "" {
		t.Skip("set EXPO_DIR to an Expo app directory")
	}
	if err := os.Chdir(dir); err != nil {
		t.Skip(err)
	}
	for _, m := range []Mode{Runs, Builds} {
		p, err := New(m, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Refresh(); err != nil {
			t.Fatal(err)
		}
		b := ui.NewBuffer(110, 30)
		p.Draw(b, b.Area())
		fmt.Printf("%s %+v\n%s", p.Name(), p.Status(), b.Text())
		if m == Runs {
			p.Key(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
			b = ui.NewBuffer(110, 20)
			p.Draw(b, b.Area())
			fmt.Print(b.Text())
		}
	}
}
