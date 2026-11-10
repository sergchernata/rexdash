package session

import (
	"testing"

	"rexdash/internal/cases"
)

func TestShared(t *testing.T) {
	cases.Check(t, "sh_quote", func(a []any) any { return shQuote(cases.S(a[0])) })
	cases.Check(t, "is_shell", func(a []any) any { return isShell(cases.S(a[0])) })
	cases.Check(t, "relaunch_command", func(a []any) any {
		v, _ := a[0].(map[string]any)
		cmd, enter, ok := relaunchCommand(v)
		return cases.Opt([]any{cmd, enter}, ok)
	})
}

// The agent forgets panes that were closed, so a long-running agent doesn't grow.
func TestChildrenForgetClosedPanes(t *testing.T) {
	kids := children{"a": 1, "b": 2, "c": 3}
	kids.keep(map[string]bool{"b": true, "d": true})
	if len(kids) != 1 || kids["b"] != 2 {
		t.Fatalf("kept %v, want only b", kids)
	}
}
