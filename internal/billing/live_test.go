//go:build live

package billing

import (
	"fmt"
	"testing"
	"time"

	"rexdash/internal/ui"
)

// Renders from the existing disk cache only (a long interval keeps Refresh from fetching).
// AWS_PROFILE=default go test -tags live -run LiveCache -v ./internal/billing/
func TestLiveCache(t *testing.T) {
	p, err := New(100000 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bp := p.(*Billing)
	if _, ok := bp.loadCache(); !ok {
		t.Fatal("no usable cache at " + bp.cache)
	}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	b := ui.NewBuffer(110, 30)
	p.Draw(b, b.Area())
	fmt.Println(p.Title().String(), "|", p.Status())
	fmt.Print(b.Text())
}
