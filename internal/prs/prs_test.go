package prs

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rexdash/internal/ui"
)

func TestRFC3339Timestamps(t *testing.T) {
	if v, ok := ts(gjson.Parse(`"1970-01-01T00:01:00Z"`)); !ok || v != 60 {
		t.Errorf("got %d %v", v, ok)
	}
	for _, s := range []string{`null`, `"yesterday"`} {
		if _, ok := ts(gjson.Parse(s)); ok {
			t.Errorf("%s parsed", s)
		}
	}
}

const sample = `{"open":{"nodes":[
 {"number":12,"title":"Fix login","url":"u12","isDraft":false,"headRefName":"fix","updatedAt":"2026-11-09T10:00:00Z","author":{"login":"ann"},
  "reviewDecision":"APPROVED","additions":10,"deletions":2,"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}},
 {"number":13,"title":"WIP","url":"u13","isDraft":true,"headRefName":"wip","updatedAt":"2026-11-09T10:00:00Z","author":{"login":"bob"},
  "reviewDecision":"REVIEW_REQUIRED","additions":1,"deletions":0,"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING"}}}]}}]},
 "merged":{"nodes":[]}}`

func TestStatusAndDraw(t *testing.T) {
	r := gjson.Parse(sample)
	p := &pane{slug: "o/r", open: r.Get("open.nodes").Array(), branch: "fix", st: ui.TableState{Sel: 0}}
	if st := p.Status(); st.Msg != "2 open · 1 failing CI · 1 running" || st.State != "working" {
		t.Errorf("status %+v", st)
	}
	b := ui.NewBuffer(100, 12)
	p.Draw(b, b.Area())
	txt := b.Text()
	for _, want := range []string{"Open 2", "#12", "▶ Fix login", "+10 -2", "ann"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in\n%s", want, txt)
		}
	}
	// The current branch without a PR is called out.
	p.branch = "other"
	b = ui.NewBuffer(100, 12)
	p.Draw(b, b.Area())
	if !strings.Contains(b.Text(), "other has no PR") {
		t.Errorf("no missing-PR note:\n%s", b.Text())
	}
	// Merged tab, empty.
	p.showMerged = true
	b = ui.NewBuffer(100, 12)
	p.Draw(b, b.Area())
	if !strings.Contains(b.Text(), "No merged pull requests") {
		t.Errorf("no empty state:\n%s", b.Text())
	}
}

func TestDetailDraw(t *testing.T) {
	p := &pane{detail: &detail{
		info:   []ui.Line{ui.LT("#12 Fix login", ui.Style{})},
		checks: []check{{name: "lint", state: "FAILURE"}, {name: "test", state: "SUCCESS", took: "1m02s"}},
		st:     ui.TableState{Sel: 0},
	}}
	b := ui.NewBuffer(80, 16)
	p.Draw(b, b.Area())
	txt := b.Text()
	for _, want := range []string{"Pull request", "1 failing", "1/2 passing", "lint", "failure", "1m02s"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in\n%s", want, txt)
		}
	}
}
