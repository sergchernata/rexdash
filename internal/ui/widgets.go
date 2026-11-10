package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// MARK is the selected-row marker.
const MARK = "▌"

func SelectedStyle() Style { return Style{Bg: SelBg(), Bold: true} }
func HeaderStyle() Style   { return Style{Fg: Muted(), Bold: true} }

// ---------- panel ----------

// Panel is a rounded box with a bold title on the left, an optional title on the right
// and one bottom-right; content is padded one cell left and right.
type Panel struct {
	Title, Right, Bottom Line
	BorderColor          Color // 0 = theme border
	NoPad                bool
}

// P makes a panel with a title.
func P(title Line) Panel { return Panel{Title: title} }

// Inner is the content area for r.
func (p Panel) Inner(r Rect) Rect {
	in := r.Inset(1, 1)
	if !p.NoPad {
		in.X++
		in.W -= 2
		if in.W < 0 {
			in.W = 0
		}
	}
	return in
}

// Draw draws the box and returns the content area.
func (p Panel) Draw(b *Buffer, r Rect) Rect {
	if r.W < 2 || r.H < 2 {
		return Rect{r.X, r.Y, 0, 0}
	}
	bc := p.BorderColor
	if bc == 0 {
		bc = Border()
	}
	st := Fg(bc)
	x1, y1 := r.Right()-1, r.Bottom()-1
	b.SetString(r.X, r.Y, "╭", st, 1)
	b.SetString(x1, r.Y, "╮", st, 1)
	b.SetString(r.X, y1, "╰", st, 1)
	b.SetString(x1, y1, "╯", st, 1)
	for x := r.X + 1; x < x1; x++ {
		b.SetString(x, r.Y, "─", st, 1)
		b.SetString(x, y1, "─", st, 1)
	}
	for y := r.Y + 1; y < y1; y++ {
		b.SetString(r.X, y, "│", st, 1)
		b.SetString(x1, y, "│", st, 1)
	}
	top := Rect{r.X + 1, r.Y, r.W - 2, 1}
	if len(p.Right.Spans) > 0 {
		rw := min(p.Right.Width(), top.W)
		b.SetLine(Rect{top.Right() - rw, r.Y, rw, 1}, p.Right)
		top.W -= rw
	}
	if len(p.Title.Spans) > 0 {
		t := p.Title
		t.Align = Left
		t.Style = t.Style.Patch(Style{Bold: true})
		b.SetLine(top, t)
	}
	if len(p.Bottom.Spans) > 0 {
		bw := min(p.Bottom.Width(), r.W-2)
		b.SetLine(Rect{x1 - bw, y1, bw, 1}, p.Bottom)
	}
	return p.Inner(r)
}

// ---------- text ----------

// Paragraph draws lines top-down in r; wrap breaks long lines onto the next rows.
func Paragraph(b *Buffer, r Rect, lines []Line, wrap bool) {
	y := r.Y
	for _, l := range lines {
		if y >= r.Bottom() {
			return
		}
		if !wrap || l.Width() <= r.W {
			b.SetLine(Rect{r.X, y, r.W, 1}, l)
			y++
			continue
		}
		for _, part := range WrapLine(l, r.W) {
			if y >= r.Bottom() {
				return
			}
			b.SetLine(Rect{r.X, y, r.W, 1}, part)
			y++
		}
	}
}

// WrapLine cuts a styled line into lines at most w cells wide.
func WrapLine(l Line, w int) []Line {
	if w <= 0 {
		return []Line{l}
	}
	var out []Line
	cur := Line{Style: l.Style, Align: l.Align}
	used := 0
	for _, s := range l.Spans {
		var buf strings.Builder
		for _, ch := range s.Text {
			cw := StrWidth(string(ch))
			if used+cw > w && used > 0 {
				cur.Spans = append(cur.Spans, Span{buf.String(), s.Style})
				buf.Reset()
				out = append(out, cur)
				cur = Line{Style: l.Style, Align: l.Align}
				used = 0
			}
			buf.WriteRune(ch)
			used += cw
		}
		if buf.Len() > 0 {
			cur.Spans = append(cur.Spans, Span{buf.String(), s.Style})
		}
	}
	return append(out, cur)
}

// WrappedRows is how many rows text takes when wrapped to width (close enough for centring).
func WrappedRows(text string, width int) int {
	w := max(width, 1)
	n := 0
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for _, l := range lines {
		c := len([]rune(l))
		n += max((c+w-1)/w, 1)
	}
	return max(n, 1)
}

// EmptyState draws an icon, a message and an optional hint centred in r;
// one line when the box is short.
func EmptyState(b *Buffer, r Rect, icon string, color Color, msg, hint string) {
	var lines []Line
	if r.H >= 5 {
		lines = []Line{LT(icon, Style{Fg: color, Bold: true}), {}, LT(msg, Fg(FgC()))}
		if hint != "" {
			lines = append(lines, LT(hint, Fg(Muted())))
		}
	} else {
		lines = []Line{L(S(icon+"  ", Style{Fg: color, Bold: true}), C(msg, Muted()))}
	}
	for i := range lines {
		lines[i].Align = Center
	}
	Paragraph(b, Middle(r, len(lines)), lines, false)
}

// ---------- table ----------

// Row is one table row.
type Row struct {
	Cells []Line
	Style Style
}

// R makes a row from cells.
func R(cells ...Line) Row { return Row{Cells: cells} }

// TableState is the selection (-1 = none) and scroll offset.
type TableState struct {
	Sel    int
	Offset int
}

func NewTableState() TableState { return TableState{Sel: -1} }

// Table draws rows in columns; with Highlight, the selected row gets SelectedStyle and a marker.
type Table struct {
	Cols      []Constraint
	Header    []Line
	Rows      []Row
	Spacing   int
	Highlight bool
	Block     *Panel
}

// Draw draws the table; st may be nil when nothing is selectable.
func (t Table) Draw(b *Buffer, r Rect, st *TableState) {
	area := r
	if t.Block != nil {
		area = t.Block.Draw(b, r)
	}
	if area.Empty() {
		return
	}
	markW := 0
	if t.Highlight {
		markW = StrWidth(MARK)
	}
	cols := Horizontal(Rect{area.X + markW, area.Y, area.W - markW, 1}, t.Spacing, t.Cols...)
	y := area.Y
	if len(t.Header) > 0 {
		hs := HeaderStyle()
		b.SetStyle(Rect{area.X, y, area.W, 1}, hs)
		for i, c := range t.Header {
			if i < len(cols) {
				b.SetLine(Rect{cols[i].X, y, cols[i].W, 1}, c.WithStyle(hs))
			}
		}
		y++
	}
	view := area.Bottom() - y
	if view <= 0 {
		return
	}
	off := 0
	sel := -1
	if st != nil {
		sel = st.Sel
		if sel >= len(t.Rows) {
			sel = len(t.Rows) - 1
		}
		off = st.Offset
		if sel >= 0 {
			if sel < off {
				off = sel
			}
			if sel >= off+view {
				off = sel - view + 1
			}
		}
		off = max(0, min(off, max(0, len(t.Rows)-view)))
		st.Offset = off
	}
	for i := off; i < len(t.Rows) && y < area.Bottom(); i++ {
		row := t.Rows[i]
		rr := Rect{area.X, y, area.W, 1}
		b.SetStyle(rr, row.Style)
		if t.Highlight && i == sel {
			b.SetStyle(rr, SelectedStyle())
			b.SetString(area.X, y, MARK, Fg(Accent()), markW)
		}
		for c, cell := range row.Cells {
			if c < len(cols) {
				b.SetLine(Rect{cols[c].X, y, cols[c].W, 1}, cell.WithStyle(row.Style))
			}
		}
		y++
	}
}

// ---------- charts ----------

var bars = []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// Sparkline draws vertical bars, one per column, scaled to maxV (0 = the data's max).
func Sparkline(b *Buffer, r Rect, data []uint64, maxV uint64, st Style) {
	if r.Empty() {
		return
	}
	if maxV == 0 {
		for _, d := range data {
			maxV = max(maxV, d)
		}
	}
	if maxV == 0 {
		maxV = 1
	}
	for i := 0; i < len(data) && i < r.W; i++ {
		h := int(min(data[i], maxV) * uint64(r.H) * 8 / maxV)
		for row := r.H - 1; row >= 0 && h > 0; row-- {
			sym := bars[min(h, 8)]
			b.SetString(r.X+i, r.Y+row, sym, st, 1)
			h -= 8
		}
	}
}

// Stretch resamples data to exactly width columns so a short series fills its box.
func Stretch(data []uint64, width int) []uint64 {
	if len(data) == 0 {
		return []uint64{}
	}
	out := make([]uint64, width)
	for i := 0; i < width; i++ {
		out[i] = data[i*len(data)/max(width, 1)]
	}
	return out
}

// LineGauge draws a horizontal line, the first ratio of it in filled.
func LineGauge(b *Buffer, r Rect, ratio float64, filled, unfilled Style) {
	if r.Empty() {
		return
	}
	ratio = max(0, min(1, ratio))
	n := int(float64(r.W) * ratio)
	for x := 0; x < r.W; x++ {
		st := unfilled
		if x < n {
			st = filled
		}
		b.SetString(r.X+x, r.Y, "─", st, 1)
	}
}

// Bar is one bar of a BarChart.
type Bar struct {
	Value      uint64
	Label      string
	Text       string // shown in the bar's bottom row
	Style      Style  // bar colour (Fg)
	ValueStyle Style
}

// BarChart draws vertical bars of the given width with labels underneath.
func BarChart(b *Buffer, r Rect, data []Bar, width, gap int, labelStyle Style) {
	if r.H < 2 || len(data) == 0 {
		return
	}
	var maxV uint64
	for _, d := range data {
		maxV = max(maxV, d.Value)
	}
	if maxV == 0 {
		maxV = 1
	}
	barH := r.H - 1
	x := r.X
	for _, d := range data {
		if x+width > r.Right() {
			break
		}
		h := int(d.Value * uint64(barH) * 8 / maxV)
		for row := barH - 1; row >= 0 && h > 0; row-- {
			for c := 0; c < width; c++ {
				b.SetString(x+c, r.Y+row, bars[min(h, 8)], d.Style, 1)
			}
			h -= 8
		}
		if d.Text != "" && StrWidth(d.Text) <= width {
			b.SetLine(Rect{x, r.Y + barH - 1, width, 1}, LT(d.Text, d.ValueStyle).Centered())
		}
		b.SetLine(Rect{x, r.Bottom() - 1, width, 1}, LT(d.Label, labelStyle).Centered())
		x += width + gap
	}
}

// Scrollbar draws a track and thumb along the right border of r (inside its top and bottom border).
func Scrollbar(b *Buffer, r Rect, total, pos, view int) {
	if total <= view || r.H < 3 {
		return
	}
	track := r.H - 2
	x := r.Right() - 1
	thumb := max(1, track*view/total)
	span := max(1, total-view)
	start := (track - thumb) * min(pos, span) / span
	for i := 0; i < track; i++ {
		if i >= start && i < start+thumb {
			b.SetString(x, r.Y+1+i, "┃", Fg(Accent()), 1)
		} else {
			b.SetString(x, r.Y+1+i, "│", Fg(Border()), 1)
		}
	}
}

// ---------- scrollable text ----------

// TextView is a full-pane scrollable view (diffs, logs, details).
type TextView struct {
	Icon   Ic
	Title  string
	Lines  []Line
	Top    int
	height int
}

func NewTextView(title string, lines []Line) *TextView {
	return &TextView{Icon: DIFF, Title: title, Lines: lines, height: 20}
}

func (v *TextView) WithIcon(i Ic) *TextView { v.Icon = i; return v }

// AtEnd starts scrolled to the bottom.
func (v *TextView) AtEnd() *TextView { v.Top = 1 << 30; return v }

// Key scrolls; false if the key wasn't a scroll key.
func (v *TextView) Key(ev *tcell.EventKey) bool {
	maxTop := max(0, len(v.Lines)-v.height)
	page := max(1, v.height-2)
	v.Top = min(v.Top, maxTop)
	switch {
	case ev.Key() == tcell.KeyDown || IsRune(ev, 'j'):
		v.Top = min(v.Top+1, maxTop)
	case ev.Key() == tcell.KeyUp || IsRune(ev, 'k'):
		v.Top = max(0, v.Top-1)
	case ev.Key() == tcell.KeyPgDn || IsRune(ev, ' ') || IsRune(ev, 'd'):
		v.Top = min(v.Top+page, maxTop)
	case ev.Key() == tcell.KeyPgUp || IsRune(ev, 'b') || IsRune(ev, 'u'):
		v.Top = max(0, v.Top-page)
	case ev.Key() == tcell.KeyHome || IsRune(ev, 'g'):
		v.Top = 0
	case ev.Key() == tcell.KeyEnd || IsRune(ev, 'G'):
		v.Top = maxTop
	default:
		return false
	}
	return true
}

func (v *TextView) Draw(b *Buffer, r Rect) {
	p := Panel{Title: L(C(" "+v.Icon.S()+" ", Accent()), C(v.Title, Accent()), T(" "))}
	// Size and clamp before the counter, so it matches this frame (not the previous one).
	v.height = p.Inner(r).H
	if r.W < 2 || r.H < 2 {
		v.height = 0
	}
	v.Top = min(v.Top, max(0, len(v.Lines)-v.height))
	p.Bottom = LT(fmt.Sprintf(" %d/%d ", min(v.Top+v.height, len(v.Lines)), len(v.Lines)), Fg(Muted()))
	in := p.Draw(b, r)
	end := min(len(v.Lines), v.Top+v.height)
	Paragraph(b, in, v.Lines[min(v.Top, end):end], false)
	Scrollbar(b, r, len(v.Lines), v.Top, v.height)
}

// DiffLine colours a unified diff line by its prefix.
func DiffLine(l string) Line {
	s := strings.ReplaceAll(strings.TrimRight(l, "\n"), "\t", "    ")
	switch {
	case strings.HasPrefix(s, "@@"):
		return LT(s, Style{Fg: Cyan(), Dim: true})
	case strings.HasPrefix(s, "+"):
		return LT(s, Style{Fg: Green(), Bg: AddBg()})
	case strings.HasPrefix(s, "-"):
		return LT(s, Style{Fg: Red(), Bg: DelBg()})
	}
	return LT(s, Style{})
}

// CountBadge is " n " on the accent colour.
func CountBadge(n int) Span {
	return S(fmt.Sprintf(" %d ", n), Style{Fg: OnAccent(), Bg: Accent()})
}
