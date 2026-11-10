package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"rexdash/internal/cases"
)

func TestShared(t *testing.T) {
	cases.Check(t, "every_label", func(a []any) any { return EveryLabel(time.Duration(cases.I(a[0])) * time.Second) })
	cases.Check(t, "dur", func(a []any) any { return Dur(int64(cases.I(a[0]))) })
	cases.Check(t, "ago", func(a []any) any { return Ago(Now() - int64(cases.I(a[0]))) })
	cases.Check(t, "noisy", func(a []any) any { return Noisy(cases.S(a[0])) })
	cases.Check(t, "wrapped_rows", func(a []any) any { return WrappedRows(cases.S(a[0]), cases.I(a[1])) })
	cases.Check(t, "middle", func(a []any) any {
		r := cases.Floats(a[0])
		m := Middle(Rect{int(r[0]), int(r[1]), int(r[2]), int(r[3])}, cases.I(a[1]))
		return []int{m.X, m.Y, m.W, m.H}
	})
	cases.Check(t, "b64", func(a []any) any { return B64(cases.S(a[0])) })
	cases.Check(t, "stretch", func(a []any) any {
		var d []uint64
		for _, f := range cases.Floats(a[0]) {
			d = append(d, uint64(f))
		}
		return Stretch(d, cases.I(a[1]))
	})
	cases.Check(t, "tone", func(a []any) any { return ToneOf(cases.S(a[0])).String() })
	cases.Check(t, "nav", func(a []any) any {
		sel := -1
		if a[0] != nil {
			sel = cases.I(a[0])
		}
		var ev *tcell.EventKey
		switch k := cases.S(a[2]); k {
		case "down":
			ev = tcell.NewEventKey(tcell.KeyDown, 0, 0)
		case "up":
			ev = tcell.NewEventKey(tcell.KeyUp, 0, 0)
		case "end":
			ev = tcell.NewEventKey(tcell.KeyEnd, 0, 0)
		case "home":
			ev = tcell.NewEventKey(tcell.KeyHome, 0, 0)
		case "pageup":
			ev = tcell.NewEventKey(tcell.KeyPgUp, 0, 0)
		case "pagedown":
			ev = tcell.NewEventKey(tcell.KeyPgDn, 0, 0)
		default:
			ev = tcell.NewEventKey(tcell.KeyRune, []rune(k)[0], 0)
		}
		ok := Nav(&sel, cases.I(a[1]), ev, cases.I(a[3]))
		return []any{ok, cases.Opt(sel, sel >= 0)}
	})
	cases.Check(t, "clamp", func(a []any) any {
		sel := -1
		if a[0] != nil {
			sel = cases.I(a[0])
		}
		Clamp(&sel, cases.I(a[1]))
		return cases.Opt(sel, sel >= 0)
	})
	cases.Check(t, "hex", func(a []any) any {
		c, ok := hex(cases.S(a[0]))
		return cases.Opt([]int{int(c[0]), int(c[1]), int(c[2])}, ok)
	})
	cases.Check(t, "mix", func(a []any) any {
		t := func(v any) rgb { f := cases.Floats(v); return rgb{uint8(f[0]), uint8(f[1]), uint8(f[2])} }
		c := mixRGB(t(a[0]), t(a[1]), cases.F(a[2]))
		return []int{int(c[0]), int(c[1]), int(c[2])}
	})
	cases.Check(t, "file_icon", func(a []any) any { s, _ := FileIcon(cases.S(a[0])); return s })
	cases.Check(t, "stage_icon", func(a []any) any { return StageIcon(cases.S(a[0])).Glyph })
	cases.Check(t, "key_icon", func(a []any) any { return KeyIcon(cases.S(a[0])) })
}

func TestSizes(t *testing.T) {
	eq := func(got, want []int) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v want %v", got, want)
			}
		}
	}
	eq(Sizes(10, 0, []Constraint{Len(1), Min(1), Len(1)}), []int{1, 8, 1})
	eq(Sizes(10, 1, []Constraint{Fill(1), Fill(1)}), []int{4, 5})
	eq(Sizes(12, 0, []Constraint{Fill(2), Fill(1), Fill(1)}), []int{6, 3, 3})
	eq(Sizes(5, 0, []Constraint{Len(4), Len(4)}), []int{4, 1})
	eq(Sizes(10, 0, []Constraint{Len(3), Len(3)}), []int{3, 7})
}

func TestPanelAndTable(t *testing.T) {
	b := NewBuffer(30, 6)
	tb := Table{
		Cols:      []Constraint{Len(3), Min(5)},
		Header:    []Line{LT("ID", Style{}), LT("Name", Style{})},
		Rows:      []Row{R(LT("1", Style{}), LT("alpha", Style{})), R(LT("2", Style{}), LT("beta", Style{}))},
		Spacing:   1,
		Highlight: true,
		Block:     &Panel{Title: LT(" Things ", Style{}), Right: LT(" 2 ", Style{})},
	}
	st := TableState{Sel: 1}
	tb.Draw(b, b.Area(), &st)
	got := b.Text()
	for _, want := range []string{"╭ Things", " 2 ╮", "ID  Name", "▌2   beta", "╰"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestSparkAndWrap(t *testing.T) {
	b := NewBuffer(4, 2)
	Sparkline(b, b.Area(), []uint64{0, 4, 8, 16}, 16, Style{})
	if got := b.Text(); got != "   █\n ▄██\n" {
		t.Errorf("sparkline:\n%q", got)
	}
	parts := WrapLine(L(T("abcdef"), T("gh")), 3)
	var s []string
	for _, p := range parts {
		s = append(s, p.String())
	}
	if strings.Join(s, "|") != "abc|def|gh" {
		t.Errorf("wrap: %v", s)
	}
}
