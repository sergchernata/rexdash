// Package ui is the shared terminal shell for every rexdash pane: a cell buffer with
// ratatui-style widgets (panels, tables, sparklines, gauges), the theme, icons, the run
// loop (keys, refresh timer, file watching, self-upgrade) and Rex integration.
package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// Color 0 means "unset" (inherit), so styles can be layered like ratatui's.
type Color = tcell.Color

// Style is a patchable style: zero fields leave what's underneath.
type Style struct {
	Fg, Bg                  Color
	Bold, Dim, Italic, Ulin bool
}

// Patch lays o over s.
func (s Style) Patch(o Style) Style {
	if o.Fg != 0 {
		s.Fg = o.Fg
	}
	if o.Bg != 0 {
		s.Bg = o.Bg
	}
	s.Bold = s.Bold || o.Bold
	s.Dim = s.Dim || o.Dim
	s.Italic = s.Italic || o.Italic
	s.Ulin = s.Ulin || o.Ulin
	return s
}

func (s Style) WithFg(c Color) Style { s.Fg = c; return s }
func (s Style) WithBg(c Color) Style { s.Bg = c; return s }
func (s Style) B() Style             { s.Bold = true; return s }
func (s Style) D() Style             { s.Dim = true; return s }

func (s Style) tcell() tcell.Style {
	t := tcell.StyleDefault
	if s.Fg != 0 {
		t = t.Foreground(s.Fg)
	}
	if s.Bg != 0 {
		t = t.Background(s.Bg)
	}
	return t.Bold(s.Bold).Dim(s.Dim).Italic(s.Italic).Underline(s.Ulin)
}

// Fg is a style with only a foreground.
func Fg(c Color) Style { return Style{Fg: c} }

// Span is styled text; Line is a row of spans.
type Span struct {
	Text  string
	Style Style
}

type Align int

const (
	Left Align = iota
	Center
	Right
)

type Line struct {
	Spans []Span
	Style Style // patched under every span
	Align Align
}

// S makes a span.
func S(text string, st Style) Span { return Span{text, st} }

// T makes an unstyled span.
func T(text string) Span { return Span{Text: text} }

// C makes a span with a foreground colour.
func C(text string, c Color) Span { return Span{text, Style{Fg: c}} }

// L makes a line from spans.
func L(spans ...Span) Line { return Line{Spans: spans} }

// LT makes a one-span line.
func LT(text string, st Style) Line { return Line{Spans: []Span{{text, st}}} }

func (l Line) Centered() Line         { l.Align = Center; return l }
func (l Line) RightAligned() Line     { l.Align = Right; return l }
func (l Line) WithStyle(s Style) Line { l.Style = l.Style.Patch(s); return l }

// Width in terminal cells.
func (l Line) Width() int {
	w := 0
	for _, s := range l.Spans {
		w += StrWidth(s.Text)
	}
	return w
}

func (l Line) String() string {
	var b strings.Builder
	for _, s := range l.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// StrWidth is the display width of s.
func StrWidth(s string) int { return uniseg.StringWidth(s) }

type cell struct {
	str   string // grapheme cluster; "" for the second half of a wide one
	style Style
}

// Buffer is a grid of cells drawn into and then flushed to the screen.
type Buffer struct {
	W, H  int
	cells []cell
}

func NewBuffer(w, h int) *Buffer {
	b := &Buffer{W: w, H: h, cells: make([]cell, w*h)}
	for i := range b.cells {
		b.cells[i].str = " "
	}
	return b
}

func (b *Buffer) at(x, y int) *cell {
	if x < 0 || y < 0 || x >= b.W || y >= b.H {
		return nil
	}
	return &b.cells[y*b.W+x]
}

// Area is the whole buffer.
func (b *Buffer) Area() Rect { return Rect{0, 0, b.W, b.H} }

// SetStyle patches the style of every cell in r.
func (b *Buffer) SetStyle(r Rect, st Style) {
	r = r.Intersect(b.Area())
	for y := r.Y; y < r.Bottom(); y++ {
		for x := r.X; x < r.Right(); x++ {
			c := b.at(x, y)
			c.style = c.style.Patch(st)
		}
	}
}

// SetString draws s at (x, y), no wider than maxW cells; returns the x after it.
func (b *Buffer) SetString(x, y int, s string, st Style, maxW int) int {
	end := x + maxW
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		cl := g.Str()
		if cl == "\t" {
			cl = " "
		}
		if r := []rune(cl); len(r) == 1 && r[0] < 32 {
			continue
		}
		w := g.Width()
		if w == 0 {
			w = runewidth.StringWidth(cl)
		}
		if w == 0 {
			continue
		}
		if x+w > end {
			break
		}
		if c := b.at(x, y); c != nil {
			c.str = cl
			c.style = c.style.Patch(st)
		}
		for i := 1; i < w; i++ {
			if c := b.at(x+i, y); c != nil {
				c.str = ""
				c.style = c.style.Patch(st)
			}
		}
		x += w
	}
	return x
}

// SetLine draws a line in the first row of r, aligned and truncated.
func (b *Buffer) SetLine(r Rect, l Line) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	b.SetStyle(Rect{r.X, r.Y, r.W, 1}, l.Style)
	x := r.X
	if w := l.Width(); w < r.W {
		switch l.Align {
		case Center:
			x += (r.W - w) / 2
		case Right:
			x += r.W - w
		}
	}
	for _, s := range l.Spans {
		if x >= r.Right() {
			break
		}
		x = b.SetString(x, r.Y, s.Text, l.Style.Patch(s.Style), r.Right()-x)
	}
}

// Flush writes the buffer to the screen.
func (b *Buffer) Flush(s tcell.Screen) {
	for y := 0; y < b.H; y++ {
		for x := 0; x < b.W; x++ {
			c := b.cells[y*b.W+x]
			if c.str == "" {
				continue
			}
			r := []rune(c.str)
			s.SetContent(x, y, r[0], r[1:], c.style.tcell())
		}
	}
}

// Text returns the buffer as plain text, for tests.
func (b *Buffer) Text() string {
	var out strings.Builder
	for y := 0; y < b.H; y++ {
		var row strings.Builder
		for x := 0; x < b.W; x++ {
			row.WriteString(b.cells[y*b.W+x].str)
		}
		out.WriteString(strings.TrimRight(row.String(), " "))
		out.WriteByte('\n')
	}
	return out.String()
}

// Find is where text starts in the buffer (first match), for tests.
func (b *Buffer) Find(text string) (x, y int, ok bool) {
	want := []rune(text)
	for y := 0; y < b.H; y++ {
		for x := 0; x+len(want) <= b.W; x++ {
			match := true
			for i, r := range want {
				if b.cells[y*b.W+x+i].str != string(r) {
					match = false
					break
				}
			}
			if match {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

// StyleAt is a cell's style, for tests.
func (b *Buffer) StyleAt(x, y int) Style {
	if c := b.at(x, y); c != nil {
		return c.style
	}
	return Style{}
}
