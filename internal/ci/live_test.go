//go:build live

package ci

import (
	"fmt"
	"os"
	"testing"
	"time"

	"rexdash/internal/ui"
)

// REPO_DIR=/path/to/repo go test -tags live -run Live -v ./internal/ci/
func TestLive(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	for _, dir := range []string{os.Getenv("REPO_DIR")} {
		if dir == "" {
			t.Skip("set REPO_DIR to a GitHub repo checkout")
		}
		if err := os.Chdir(dir); err != nil {
			t.Skip(err)
		}
		p, err := New("", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Refresh(); err != nil {
			t.Fatal(err)
		}
		b := ui.NewBuffer(110, 30)
		p.Draw(b, b.Area())
		fmt.Printf("%s %+v\n%s", dir, p.Status(), b.Text())
	}
}
