package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func rowsOf(b *Buffer) []string { return strings.Split(strings.TrimSuffix(b.Text(), "\n"), "\n") }

func mustFind(t *testing.T, b *Buffer, s string) (int, int) {
	t.Helper()
	x, y, ok := b.Find(s)
	if !ok {
		t.Fatalf("%q not found in\n%s", s, b.Text())
	}
	return x, y
}

func TestPanel(t *testing.T) {
	b := NewBuffer(20, 4)
	in := Panel{Title: LT("Title", Style{}), Right: LT("R", Style{}), Bottom: LT("bot", Style{})}.Draw(b, b.Area())
	want := []string{
		"╭Title────────────R╮",
		"│                  │",
		"│                  │",
		"╰───────────────bot╯",
	}
	if got := rowsOf(b); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("panel:\n%s", b.Text())
	}
	if in != (Rect{2, 1, 16, 2}) {
		t.Errorf("padded inner = %v", in)
	}
	if !b.StyleAt(1, 0).Bold || b.StyleAt(18, 0).Bold {
		t.Error("title should be bold, right title not")
	}
	if b.StyleAt(0, 0).Fg != Border() {
		t.Error("default border colour")
	}
	if got := (Panel{NoPad: true}).Inner(Rect{0, 0, 20, 4}); got != (Rect{1, 1, 18, 2}) {
		t.Errorf("NoPad inner = %v", got)
	}
	if got := (Panel{}).Inner(Rect{0, 0, 3, 3}); got.W != 0 {
		t.Errorf("tiny inner width = %d", got.W)
	}

	// Custom border colour; a long title is cut to the space left of the right title.
	b = NewBuffer(10, 3)
	Panel{Title: LT("a very long title", Style{}), Right: LT("RR", Style{}), BorderColor: tcell.ColorRed}.Draw(b, b.Area())
	if got := rowsOf(b)[0]; got != "╭a veryRR╮" {
		t.Errorf("truncated title row %q", got)
	}
	if b.StyleAt(0, 1).Fg != tcell.ColorRed {
		t.Error("border colour not applied")
	}

	// Too small to draw: nothing drawn, empty content area.
	b = NewBuffer(5, 5)
	if got := (Panel{Title: LT("x", Style{})}).Draw(b, Rect{1, 1, 1, 4}); !got.Empty() || strings.TrimSpace(b.Text()) != "" {
		t.Errorf("1-wide panel drew %v\n%s", got, b.Text())
	}
}

func TestTableScrollAndStyles(t *testing.T) {
	var rows []Row
	for i := 0; i < 10; i++ {
		r := R(LT(fmt.Sprint(i), Style{}), LT(fmt.Sprintf("row%d", i), Style{}))
		if i == 3 {
			r.Style = Fg(tcell.ColorRed)
		}
		rows = append(rows, r)
	}
	tb := Table{Cols: []Constraint{Len(2), Min(4)}, Header: []Line{LT("N", Style{}), LT("Name", Style{})}, Rows: rows, Spacing: 1, Highlight: true}

	// Header + 3 visible rows; selecting row 7 scrolls so it is the last visible one.
	b := NewBuffer(12, 4)
	st := TableState{Sel: 7}
	tb.Draw(b, b.Area(), &st)
	if st.Offset != 5 {
		t.Errorf("offset = %d, want 5", st.Offset)
	}
	want := " N  Name\n 5  row5\n 6  row6\n▌7  row7\n"
	if b.Text() != want {
		t.Errorf("table:\n%q\nwant\n%q", b.Text(), want)
	}
	if hs := b.StyleAt(1, 0); hs.Fg != Muted() || !hs.Bold {
		t.Errorf("header style %+v", hs)
	}
	if s := b.StyleAt(5, 3); s.Bg != SelBg() || !s.Bold {
		t.Errorf("selected row style %+v", s)
	}
	if b.StyleAt(0, 3).Fg != Accent() {
		t.Error("marker colour")
	}

	// Moving the selection above the window scrolls back up; row styles apply.
	st.Sel = 2
	b = NewBuffer(12, 4)
	tb.Draw(b, b.Area(), &st)
	if st.Offset != 2 || !strings.HasPrefix(rowsOf(b)[1], "▌2") {
		t.Errorf("offset = %d\n%s", st.Offset, b.Text())
	}
	_, y := mustFind(t, b, "row3")
	if b.StyleAt(5, y).Fg != tcell.ColorRed {
		t.Error("row style not applied")
	}

	// A selection past the end is clamped to the last row; an offset past the end is pulled back.
	st = TableState{Sel: 99, Offset: 99}
	b = NewBuffer(12, 4)
	tb.Draw(b, b.Area(), &st)
	if st.Offset != 7 || !strings.HasPrefix(rowsOf(b)[3], "▌9") {
		t.Errorf("clamped: offset %d\n%s", st.Offset, b.Text())
	}

	// No state: rows from the top, no marker; no header.
	tb.Header, tb.Highlight = nil, false
	b = NewBuffer(12, 2)
	tb.Draw(b, b.Area(), nil)
	if b.Text() != "0  row0\n1  row1\n" {
		t.Errorf("stateless:\n%q", b.Text())
	}

	// A block too small for content draws nothing inside.
	tb.Block = &Panel{}
	b = NewBuffer(12, 2)
	tb.Draw(b, b.Area(), &st)
	if strings.Contains(b.Text(), "row") {
		t.Errorf("rows drawn in an empty block:\n%s", b.Text())
	}
}

func TestLineGauge(t *testing.T) {
	b := NewBuffer(10, 1)
	on, off := Fg(tcell.ColorGreen), Fg(tcell.ColorGray)
	LineGauge(b, b.Area(), 0.5, on, off)
	if b.Text() != strings.Repeat("─", 10)+"\n" {
		t.Errorf("gauge %q", b.Text())
	}
	for x := 0; x < 10; x++ {
		want := off
		if x < 5 {
			want = on
		}
		if b.StyleAt(x, 0) != want {
			t.Errorf("cell %d style %+v", x, b.StyleAt(x, 0))
		}
	}
	b = NewBuffer(4, 1)
	LineGauge(b, b.Area(), 7, on, off) // clamped to full
	if b.StyleAt(3, 0) != on {
		t.Error("ratio > 1 not clamped")
	}
	LineGauge(b, Rect{}, 1, on, off) // empty rect: no panic
}

func TestBarChart(t *testing.T) {
	b := NewBuffer(12, 5)
	data := []Bar{
		{Value: 2, Label: "a", Text: "2"},
		{Value: 4, Label: "b", Text: "4000"}, // too wide for its bar: not shown
		{Value: 1, Label: "c"},
	}
	BarChart(b, b.Area(), data, 3, 1, Style{})
	// Scaled to the max (4): 4 rows of 8 eighths each.
	want := []string{
		"    ███",
		"    ███",
		"███ ███",
		"█2█ ███ ███",
		" a   b   c",
	}
	if got := strings.Join(rowsOf(b), "\n"); got != strings.Join(want, "\n") {
		t.Errorf("bars:\n%s\nwant\n%s", got, strings.Join(want, "\n"))
	}
	// Partial eighths; a bar past the right edge is skipped (but still sets the scale).
	b = NewBuffer(8, 3)
	BarChart(b, b.Area(), []Bar{{Value: 3, Label: "a"}, {Value: 16, Label: "b"}, {Value: 1, Label: "d"}}, 3, 1, Style{})
	if got := strings.Join(rowsOf(b), "\n"); got != "    ███\n▃▃▃ ███\n a   b" {
		t.Errorf("partial bars:\n%s", got)
	}
	// All zero: no bars, labels still drawn; too short: nothing.
	b = NewBuffer(4, 3)
	BarChart(b, b.Area(), []Bar{{Label: "z"}}, 3, 0, Style{})
	if b.Text() != "\n\n z\n" {
		t.Errorf("zero bars %q", b.Text())
	}
	b = NewBuffer(4, 1)
	BarChart(b, b.Area(), []Bar{{Value: 1, Label: "z"}}, 3, 0, Style{})
	if strings.TrimSpace(b.Text()) != "" {
		t.Error("drew in a 1-row chart")
	}
}

func TestScrollbar(t *testing.T) {
	thumb := func(pos int) []int {
		b := NewBuffer(3, 12) // track rows 1..10
		Scrollbar(b, b.Area(), 100, pos, 10)
		var at []int
		for y := 0; y < 12; y++ {
			if b.cells[y*b.W+2].str == "┃" {
				at = append(at, y)
			}
		}
		return at
	}
	for _, c := range []struct{ pos, row int }{{0, 1}, {45, 5}, {90, 10}, {500, 10}} {
		if got := thumb(c.pos); len(got) != 1 || got[0] != c.row {
			t.Errorf("pos %d: thumb at %v, want row %d", c.pos, got, c.row)
		}
	}
	// Thumb size follows view/total; the track fills the rest.
	b := NewBuffer(1, 6)
	Scrollbar(b, b.Area(), 8, 0, 4)
	if b.Text() != "\n┃\n┃\n│\n│\n\n" {
		t.Errorf("track %q", b.Text())
	}
	// Everything fits: no bar.
	b = NewBuffer(1, 6)
	Scrollbar(b, b.Area(), 4, 0, 4)
	if strings.TrimSpace(b.Text()) != "" {
		t.Error("scrollbar drawn when content fits")
	}
}

func TestParagraph(t *testing.T) {
	b := NewBuffer(5, 4)
	Paragraph(b, b.Area(), []Line{LT("abcdefgh", Style{}), LT("xy", Style{}), LT("more", Style{}), LT("cut", Style{})}, true)
	if b.Text() != "abcde\nfgh\nxy\nmore\n" {
		t.Errorf("wrapped %q", b.Text())
	}
	b = NewBuffer(5, 2)
	Paragraph(b, b.Area(), []Line{LT("abcdefghijkl", Style{})}, true)
	if b.Text() != "abcde\nfghij\n" {
		t.Errorf("wrap cut at bottom %q", b.Text())
	}
	b = NewBuffer(5, 2)
	Paragraph(b, b.Area(), []Line{LT("abcdefgh", Style{}), LT("xy", Style{}), LT("zz", Style{})}, false)
	if b.Text() != "abcde\nxy\n" {
		t.Errorf("unwrapped %q", b.Text())
	}
	// Wide characters wrap on cell width.
	parts := WrapLine(LT("日本語", Style{}), 4)
	if len(parts) != 2 || parts[0].String() != "日本" || parts[1].String() != "語" {
		t.Errorf("wide wrap %v", parts)
	}
	if got := WrapLine(LT("abc", Style{}), 0); len(got) != 1 {
		t.Errorf("w=0 wrap %v", got)
	}
}

func TestEmptyState(t *testing.T) {
	b := NewBuffer(20, 7)
	EmptyState(b, b.Area(), "@", tcell.ColorRed, "Nothing here", "press n")
	rows := rowsOf(b)
	want := []string{"", "         @", "", "    Nothing here", "      press n", "", ""}
	if strings.Join(rows, "|") != strings.Join(want, "|") {
		t.Errorf("multi-line:\n%s", b.Text())
	}
	if s := b.StyleAt(9, 1); s.Fg != tcell.ColorRed || !s.Bold {
		t.Errorf("icon style %+v", s)
	}
	if b.StyleAt(6, 4).Fg != Muted() {
		t.Error("hint muted")
	}

	b = NewBuffer(20, 3)
	EmptyState(b, b.Area(), "@", tcell.ColorRed, "Nothing", "ignored hint")
	if got := rowsOf(b); got[0] != "" || got[1] != "     @  Nothing" || got[2] != "" {
		t.Errorf("one-line:\n%q", b.Text())
	}
	if b.StyleAt(8, 1).Fg != Muted() {
		t.Error("one-line message muted")
	}
}

func key(k tcell.Key) *tcell.EventKey { return tcell.NewEventKey(k, 0, 0) }
func rkey(r rune) *tcell.EventKey     { return tcell.NewEventKey(tcell.KeyRune, r, 0) }

func TestTextView(t *testing.T) {
	var lines []Line
	for i := 1; i <= 50; i++ {
		lines = append(lines, LT(fmt.Sprintf("line %d", i), Style{}))
	}
	v := NewTextView("Diff", lines)
	b := NewBuffer(30, 12) // 10 content rows
	v.Draw(b, b.Area())
	mustFind(t, b, "Diff")
	mustFind(t, b, " 10/50 ")
	if _, y := mustFind(t, b, "line 1 "); y != 1 {
		t.Errorf("first line on row %d", y)
	}
	steps := []struct {
		ev  *tcell.EventKey
		top int
	}{
		{rkey('j'), 1}, {key(tcell.KeyDown), 2}, {rkey('k'), 1}, {key(tcell.KeyUp), 0}, {rkey('k'), 0},
		{rkey(' '), 8}, {rkey('d'), 16}, {key(tcell.KeyPgDn), 24}, {rkey('b'), 16}, {rkey('u'), 8}, {key(tcell.KeyPgUp), 0},
		{rkey('G'), 40}, {rkey('j'), 40}, {rkey('g'), 0}, {key(tcell.KeyEnd), 40}, {key(tcell.KeyHome), 0},
	}
	for i, s := range steps {
		if !v.Key(s.ev) {
			t.Fatalf("step %d: key not used", i)
		}
		if v.Top != s.top {
			t.Fatalf("step %d (%v): top %d want %d", i, s.ev.Name(), v.Top, s.top)
		}
	}
	if v.Key(rkey('x')) {
		t.Error("x is not a scroll key")
	}

	v = NewTextView("Log", lines).WithIcon(LOGS).AtEnd()
	b = NewBuffer(30, 12)
	v.Draw(b, b.Area())
	if v.Top != 40 {
		t.Errorf("AtEnd top = %d", v.Top)
	}
	mustFind(t, b, " 50/50 ")
	mustFind(t, b, LOGS.S())
	if _, y := mustFind(t, b, "line 50"); y != 10 {
		t.Errorf("last line on row %d", y)
	}
	// A 2-row thumb (10 of 50 lines on a 10-row track) at the bottom of the track.
	if b.cells[9*30+29].str != "┃" || b.cells[10*30+29].str != "┃" || b.cells[8*30+29].str != "│" {
		t.Errorf("scrollbar thumb not at the bottom:\n%s", b.Text())
	}

	// Short content: no scrolling, counter shows the total.
	v = NewTextView("x", lines[:3])
	b = NewBuffer(30, 12)
	v.Draw(b, b.Area())
	mustFind(t, b, " 3/3 ")
	v.Key(rkey('G'))
	if v.Top != 0 {
		t.Errorf("short view scrolled to %d", v.Top)
	}
}

func TestDiffLineAndBadge(t *testing.T) {
	cases := []struct {
		in     string
		fg, bg Color
		dim    bool
	}{
		{"@@ -1 +1 @@", Cyan(), 0, true},
		{"+added", Green(), AddBg(), false},
		{"-gone", Red(), DelBg(), false},
		{" same", 0, 0, false},
	}
	for _, c := range cases {
		l := DiffLine(c.in + "\n")
		st := l.Spans[0].Style
		if l.String() != c.in || st.Fg != c.fg || st.Bg != c.bg || st.Dim != c.dim {
			t.Errorf("%q: %q %+v", c.in, l.String(), st)
		}
	}
	if got := DiffLine("+\tx").String(); got != "+    x" {
		t.Errorf("tab expansion %q", got)
	}
	if s := CountBadge(7); s.Text != " 7 " || s.Style.Bg != Accent() || s.Style.Fg != OnAccent() {
		t.Errorf("badge %+v", s)
	}
}
