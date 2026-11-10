package fleet

import (
	"testing"

	"rexdash/internal/cases"
)

func TestSparklines(t *testing.T) {
	hundred := 100.0
	for _, c := range []struct {
		in   []float64
		max  *float64
		want string
	}{
		{[]float64{0, 50, 100}, &hundred, "▁▅█"},
		{[]float64{2, 4, 3}, nil, "▁█▅"}, // counts scale to their own range
		{[]float64{32, 32}, nil, "▄▄"},   // steady count is a flat mid line
		{[]float64{150}, &hundred, "█"},  // clamped
		{nil, nil, ""},
	} {
		if got := Spark(c.in, c.max); got != c.want {
			t.Errorf("Spark(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShared(t *testing.T) {
	cases.Check(t, "spark", func(a []any) any {
		var max *float64
		if a[1] != nil {
			m := cases.F(a[1])
			max = &m
		}
		return Spark(cases.Floats(a[0]), max)
	})
}
