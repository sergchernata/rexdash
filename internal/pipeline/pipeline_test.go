package pipeline

import (
	"testing"
	"unicode/utf8"

	"rexdash/internal/cases"
)

func TestCommitMsg(t *testing.T) {
	for in, want := range map[string]string{
		`{"ProviderType":"GitHub","CommitMessage":"Fix login\n\nbody"}`: "Fix login\n\nbody",
		"Manual rerun": "Manual rerun",
		`{"other":1}`:  `{"other":1}`,
		"":             "",
	} {
		if got := commitMsg(in); got != want {
			t.Errorf("commitMsg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShort(t *testing.T) {
	if short("abc") != "abc" {
		t.Error("short changed a short string")
	}
	out := short(repeat("é", 70))
	if utf8.RuneCountInString(out) != 60 || out[len(out)-len("…"):] != "…" {
		t.Errorf("short = %q", out)
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func TestShared(t *testing.T) {
	cases.Check(t, "commit_msg", func(a []any) any { return commitMsg(cases.S(a[0])) })
	cases.Check(t, "short", func(a []any) any { return short(cases.S(a[0])) })
}
