//go:build live

package fleet

import (
	"fmt"
	"testing"
	"time"

	"rexdash/internal/ui"
)

// AWS_PROFILE=default AWS_REGION=us-east-1 go test -tags live -run Live -v ./internal/fleet/
func TestLive(t *testing.T) {
	p, err := New(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	b := ui.NewBuffer(110, 30)
	p.Draw(b, b.Area())
	fmt.Println(p.Title().String(), "|", p.Status())
	fmt.Print(b.Text())
}
