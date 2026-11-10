//go:build live

package infra

import (
	"fmt"
	"os"
	"testing"
	"time"

	"rexdash/internal/ui"
)

// CLUSTER=my-cluster SERVICE=my-service DB=my-db AWS_PROFILE=... go test -tags live -run Live -v ./internal/infra/
func TestLive(t *testing.T) {
	cluster := os.Getenv("CLUSTER")
	if cluster == "" {
		t.Skip("set CLUSTER (and SERVICE, DB)")
	}
	p, err := New(cluster, os.Getenv("SERVICE"), os.Getenv("DB"), time.Minute)
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
