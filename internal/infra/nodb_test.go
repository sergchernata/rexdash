package infra

import (
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

// A service without a database: RDS is never asked, and the pane shows only ECS.
func TestServiceWithoutDatabase(t *testing.T) {
	e, r, c := healthy()
	r.err = &smithy.GenericAPIError{Code: "DBInstanceNotFound", Message: "should not be called"}
	p := newPane(e, r, c)
	p.db = ""
	if err := p.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := strings.Join(c.returned, ","); got != "cpu,mem,disk" {
		t.Errorf("metrics asked for %s", got)
	}
	if len(c.inputs) != 1 {
		t.Errorf("%d metric calls, want only the 3h ECS one", len(c.inputs))
	}
	if s := p.Status(); s.Msg != "2/2 tasks · CPU 30% · MEM 60%" {
		t.Errorf("status %+v", s)
	}
	text := draw(p).Text()
	if strings.Contains(text, "RDS") {
		t.Errorf("RDS section drawn:\n%s", text)
	}
	find(t, draw(p), "Tasks")
	for _, h := range p.Hints() {
		if h.Key == "d" {
			t.Error("RDS console hint without a database")
		}
	}
}
