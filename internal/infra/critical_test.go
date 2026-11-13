package infra

import (
	"strings"
	"testing"
)

// Any gauge at 85% or more (where its colour turns orange) puts the pane, and so its Rex tab,
// in the error state, and the status says which gauge is hot.
func TestCriticalGaugesRaiseError(t *testing.T) {
	for _, c := range []struct {
		key  string
		v    float64
		want string
	}{
		{"cpu", 86, "CPU 86%"},
		{"mem", 90, "MEM 90%"},
		{"disk", 88, "storage 88%"},
		{"dbcpu", 97, "DB CPU 97%"},
		{"dbfreemem", 1 * gib, "DB MEM 94%"}, // db.r6g.large: 1 of 16 GiB free
		{"dbconn", 19, "DB conns 19/20"},
	} {
		e, r, cw := healthy()
		withGroup(r, "20")
		p := newPane(e, r, cw)
		if err := p.Refresh(); err != nil {
			t.Fatal(err)
		}
		if s := p.Status(); s.State != "idle" {
			t.Fatalf("%s: healthy pane %+v", c.key, s)
		}
		p.m[c.key] = []float64{c.v}
		if s := p.Status(); s.State != "error" || !strings.Contains(s.Msg, c.want) {
			t.Errorf("%s at %v: %+v, want error naming %q", c.key, c.v, s, c.want)
		}
	}
}

// Just under the line is still fine.
func TestBusyGaugesStayIdle(t *testing.T) {
	e, r, cw := healthy()
	withGroup(r, "20")
	p := newPane(e, r, cw)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cpu", "mem", "disk", "dbcpu"} {
		p.m[k] = []float64{84}
	}
	p.m["dbconn"] = []float64{16}
	if s := p.Status(); s.State != "idle" {
		t.Errorf("busy but not critical: %+v", s)
	}
}

func TestCritical(t *testing.T) {
	for v, want := range map[float64]bool{0: false, 84.9: false, 85: true, 100: true} {
		if Critical(v) != want {
			t.Errorf("Critical(%v) = %v", v, !want)
		}
	}
}
