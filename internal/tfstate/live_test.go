//go:build live

package tfstate

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

// Reads a real state history; set TFDIR to a directory with a backend "s3" block.
// TFDIR=... go test -tags live -run Live -v ./internal/tfstate/
// Prints only the pane (addresses and counts), never state contents.
func TestLive(t *testing.T) {
	if dir := os.Getenv("TFDIR"); dir != "" {
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
	}
	pane, err := New("", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	show := func() {
		b := ui.NewBuffer(110, 30)
		pane.Draw(b, b.Area())
		fmt.Println(b.Text())
	}
	start := time.Now()
	if err := pane.Refresh(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("first refresh:", time.Since(start))
	start = time.Now()
	if err := pane.Refresh(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("second refresh:", time.Since(start), "status:", pane.Status())
	show()
	pane.Key(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	show()
	pane.Key(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	pane.Key(tcell.NewEventKey(tcell.KeyRune, 't', 0))
	show()
}
