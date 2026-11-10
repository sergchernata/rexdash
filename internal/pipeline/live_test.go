//go:build live

package pipeline

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

// PIPELINE=name go test -tags live -run Live -v ./internal/pipeline/
func TestLive(t *testing.T) {
	name := os.Getenv("PIPELINE")
	if name == "" {
		t.Skip("set PIPELINE")
	}
	pane, err := New(name, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := pane.Refresh(); err != nil {
		t.Fatal(err)
	}
	show := func() {
		b := ui.NewBuffer(110, 30)
		pane.Draw(b, b.Area())
		fmt.Println(b.Text())
	}
	show()
	fmt.Println("status:", pane.Status())
	// Open the newest run's actions, then a CodeBuild log if there is one.
	p := pane.(*Pipeline)
	if err := pane.Key(tcell.NewEventKey(tcell.KeyEnter, 0, 0)); err != nil {
		t.Fatal(err)
	}
	show()
	for i, a := range p.view.actions {
		if a.buildID != "" {
			p.view.st.Sel = i
			if err := pane.Key(tcell.NewEventKey(tcell.KeyEnter, 0, 0)); err != nil {
				t.Fatal(err)
			}
			fmt.Println("log lines:", len(p.view.log.Lines))
			show()
			break
		}
	}
}
