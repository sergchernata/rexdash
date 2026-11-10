package cli

import (
	"testing"

	"rexdash/internal/cases"
)

func TestShared(t *testing.T) {
	cases.Check(t, "parse_args", func(a []any) any {
		r := Parse(cases.Strings(a[0]))
		opt := func(s string) any { return cases.Opt(s, s != "") }
		return map[string]any{"cmd": r.Cmd, "pos": r.Pos, "interval": cases.Opt(r.Interval, r.Interval > 0),
			"repo": opt(r.Repo), "since": r.Since, "hide": r.Hide, "stream": opt(r.Stream)}
	})
}
