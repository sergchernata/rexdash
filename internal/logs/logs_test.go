package logs

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"rexdash/internal/cases"
	"rexdash/internal/ui"
)

func text(lines []ui.Line) []string {
	out := []string{}
	for _, l := range lines {
		out = append(out, l.String())
	}
	return out
}

func spans(texts ...string) []ui.Span {
	var s []ui.Span
	for _, t := range texts {
		s = append(s, ui.T(t))
	}
	return s
}

func TestShared(t *testing.T) {
	cases.Check(t, "since_secs", func(a []any) any { return sinceSecs(cases.S(a[0])) })
	cases.Check(t, "contains_ci", func(a []any) any { return containsCI(cases.S(a[0]), cases.S(a[1])) })
	cases.Check(t, "level", func(a []any) any { return levelOf(cases.S(a[0])).String() })
	cases.Check(t, "log_layout", func(a []any) any {
		return text(layout(spans(cases.Strings(a[0])...), cases.I(a[1]), cases.B(a[2])))
	})
}

func TestLayoutTruncatesOrWraps(t *testing.T) {
	segs := spans("abcdef", "ghij")
	for _, c := range []struct {
		w    int
		wrap bool
		want string
	}{
		{4, false, "abcd"},
		{4, true, "abcd|  ef|  gh|  ij"},
		{20, true, "abcdefghij"},
	} {
		if got := strings.Join(text(layout(segs, c.w, c.wrap)), "|"); got != c.want {
			t.Errorf("layout(%d, %v) = %q; want %q", c.w, c.wrap, got, c.want)
		}
	}
}

func TestHighlight(t *testing.T) {
	l := &Logs{filter: "err"}
	segs := l.renderEntry(&entry{ts: 1, msg: "Ünï ERR here err", level: errLevel})
	var hits []string
	for _, s := range segs[2:] {
		if s.Style.Bg != 0 {
			hits = append(hits, s.Text)
		}
	}
	if strings.Join(hits, ",") != "ERR,err" {
		t.Errorf("highlights %q in %+v", hits, segs)
	}
}

func key(r rune) *tcell.EventKey { return tcell.NewEventKey(tcell.KeyRune, r, 0) }

func TestScrollFilterAndDraw(t *testing.T) {
	now := time.Now().UnixMilli()
	l := &Logs{group: "g", seen: map[string]bool{}, page: 20}
	for i, m := range []string{"[info] GET /a", "[error] boom", "[warning] slow", "[info] GET /b"} {
		l.lines = append(l.lines, entry{now + int64(i), m, levelOf(m)})
	}
	b := ui.NewBuffer(60, 10)
	l.Draw(b, b.Area())
	out := b.Text()
	for _, want := range []string{"Live", "4 lines", "1 errors/15m", "GET /b", "boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if st := l.Status().Msg; st != "g · 1 errors in 15m" {
		t.Errorf("status %q", st)
	}

	l.Key(key('e'))
	if l.total() != 2 {
		t.Errorf("errors only shows %d", l.total())
	}
	l.Key(key('e'))

	l.Key(tcell.NewEventKey(tcell.KeyUp, 0, 0))
	b = ui.NewBuffer(60, 10)
	l.Draw(b, b.Area())
	if out := b.Text(); !strings.Contains(out, "Paused") || strings.Contains(out, "GET /b") {
		t.Errorf("paused view:\n%s", out)
	}

	l.Key(key('/'))
	for _, r := range "GET" {
		l.Key(key(r))
	}
	if !l.Typing() || l.offset != 0 {
		t.Fatal("typing resets the scroll")
	}
	l.Key(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	b = ui.NewBuffer(60, 12)
	l.Draw(b, b.Area())
	if out := b.Text(); !strings.Contains(out, "Filter · 2 matches") || strings.Contains(out, "boom") {
		t.Errorf("filtered view:\n%s", out)
	}
	l.Key(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	if l.filter != "" {
		t.Error("esc clears the filter")
	}
	l.Key(key('c'))
	b = ui.NewBuffer(60, 12)
	l.Draw(b, b.Area())
	if !strings.Contains(b.Text(), "Waiting for log events") {
		t.Errorf("cleared view:\n%s", b.Text())
	}
}

// The Live / Paused / errors-only markers are a coloured icon and plain text, like the other
// panes' titles: no filled block behind them. Regression: they were white-on-green badges.
func TestStatusMarkersHaveNoBackground(t *testing.T) {
	l := &Logs{group: "g", seen: map[string]bool{}, page: 20, errsOnly: true}
	for i := 0; i < 30; i++ { // more than fits, so ↑ scrolls and pauses
		l.lines = append(l.lines, entry{time.Now().UnixMilli() + int64(i), "[error] boom", levelOf("[error] boom")})
	}
	check := func(word string) {
		t.Helper()
		b := ui.NewBuffer(60, 8)
		l.Draw(b, b.Area())
		x, y, ok := b.Find(word)
		if !ok {
			t.Fatalf("%q not drawn:\n%s", word, b.Text())
		}
		for i := x - 2; i < x+len(word); i++ { // the icon, the gap and the word
			if st := b.StyleAt(i, y); st.Bg != 0 {
				t.Errorf("%q: cell %d has a background", word, i)
			}
		}
		if st := b.StyleAt(x, y); st.Fg != ui.FgC() {
			t.Errorf("%q is not in the normal text colour", word)
		}
	}
	check("Live")
	check("errors only")
	l.Key(tcell.NewEventKey(tcell.KeyUp, 0, 0))
	check("Paused")
}

// The filter opens with f (shown in the footer) and with / for vi habits.
func TestFilterKeys(t *testing.T) {
	for _, r := range "f/" {
		l := &Logs{}
		l.Key(key(r))
		if !l.Typing() {
			t.Errorf("%q did not open the filter", r)
		}
	}
	if h := (&Logs{}).Hints()[0]; h.Key != "f" || h.Desc != "filter" {
		t.Errorf("first hint %+v, want f filter", h)
	}
}
