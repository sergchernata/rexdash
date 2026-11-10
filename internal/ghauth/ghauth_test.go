package ghauth

import (
	"testing"

	"rexdash/internal/cases"
)

func TestShared(t *testing.T) {
	cases.Check(t, "slug", func(a []any) any { return Slug(cases.S(a[0])) })
}

func TestURLs(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/web.git": "acme/web",
		"git@github.com:acme/tools.git":   "acme/tools",
		"https://github.com/acme/api":     "acme/api",
	} {
		if got := Slug(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}
