package ci

import (
	"strings"
	"testing"

	"rexdash/internal/cases"
	"rexdash/internal/ui"
)

func str(s string) *string { return &s }

func TestRunStates(t *testing.T) {
	for _, c := range []struct {
		status string
		concl  *string
		want   string
		tone   ui.Tone
	}{
		{"completed", str("success"), "SUCCESS", ui.ToneOk},
		{"completed", str("failure"), "FAILURE", ui.ToneBad},
		{"completed", str("cancelled"), "CANCELLED", ui.ToneBad},
		{"completed", str("skipped"), "SKIPPED", ui.ToneIdle},
		{"in_progress", nil, "IN_PROGRESS", ui.ToneBusy},
		{"queued", nil, "QUEUED", ui.ToneBusy},
	} {
		got := runState(c.status, c.concl)
		if got != c.want || ui.ToneOf(got) != c.tone {
			t.Errorf("runState(%q) = %q (%v), want %q (%v)", c.status, got, ui.ToneOf(got), c.want, c.tone)
		}
	}
}

func TestShared(t *testing.T) {
	cases.Check(t, "run_state", func(a []any) any {
		var c *string
		if s, ok := a[1].(string); ok {
			c = &s
		}
		return runState(cases.S(a[0]), c)
	})
	cases.Check(t, "ci_parse", func(a []any) any {
		out := []any{}
		for _, r := range parseRuns(cases.JSON(a[0]), int64(cases.I(a[1]))) {
			out = append(out, map[string]any{"workflow": r.Workflow, "title": r.Title, "branch": r.Branch, "event": r.Event,
				"state": r.State, "started": r.Started, "secs": r.Secs, "url": r.URL})
		}
		return out
	})
}

func TestDraw(t *testing.T) {
	p := &pane{repo: "o/r", st: ui.NewTableState(), loaded: true}
	p.runs = parseRuns([]byte(`{"workflow_runs":[{"name":"CI","status":"completed","conclusion":"success","head_branch":"main","event":"push"}]}`), 0)
	ui.Clamp(&p.st.Sel, len(p.runs))
	b := ui.NewBuffer(100, 10)
	p.Draw(b, b.Area())
	if txt := b.Text(); !strings.Contains(txt, "Workflow") || !strings.Contains(txt, "CI") {
		t.Errorf("unexpected render:\n%s", txt)
	}
}
