package infra

import (
	"math"
	"testing"

	"rexdash/internal/cases"
	"rexdash/internal/ui"
)

func TestRDSMemoryByClass(t *testing.T) {
	for class, want := range map[string]float64{
		"db.t4g.micro": 1, "db.t4g.medium": 4, "db.t4g.large": 8,
		"db.t4g.xlarge":  16, // xlarge is not a memory-optimised family
		"db.m6g.2xlarge": 32, "db.r6g.large": 16, "db.x2g.xlarge": 32,
	} {
		if got, ok := ClassGiB(class); !ok || got != want {
			t.Errorf("%s: got %v %v want %v", class, got, ok, want)
		}
	}
	if _, ok := ClassGiB("db.serverless"); ok {
		t.Error("serverless has no fixed size")
	}
}

func TestStorageGrowthPerDay(t *testing.T) {
	if _, ok := PerDay(nil); ok {
		t.Error("empty")
	}
	if _, ok := PerDay(make([]float64, 24)); ok {
		t.Error("needs more than a day")
	}
	week := make([]float64, 168)
	for h := range week {
		week[h] = 300 + float64(h)*0.5/24
	}
	if d, ok := PerDay(week); !ok || math.Abs(d-0.5) > 1e-9 {
		t.Errorf("got %v", d)
	}
}

func TestLoadThresholds(t *testing.T) {
	if LoadColor(10) != ui.Green() || LoadColor(70) != ui.Yellow() || LoadColor(95) != ui.Red() {
		t.Error("load colours")
	}
}

func TestShared(t *testing.T) {
	cases.Check(t, "class_gib", func(a []any) any { return cases.Opt(ClassGiB(cases.S(a[0]))) })
	cases.Check(t, "per_day", func(a []any) any { return cases.Opt(PerDay(cases.Floats(a[0]))) })
	cases.Check(t, "max_conns", func(a []any) any {
		l, ok := MaxConns(cases.S(a[0]), cases.S(a[1]))
		return cases.Opt(l.String(), ok)
	})
	cases.Check(t, "load_level", func(a []any) any { return LoadLevel(cases.F(a[0])).String() })
}
