package ui

// Rect is a cell area.
type Rect struct{ X, Y, W, H int }

func (r Rect) Right() int  { return r.X + r.W }
func (r Rect) Bottom() int { return r.Y + r.H }
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Inset shrinks r by dx on the left and right and dy on the top and bottom.
func (r Rect) Inset(dx, dy int) Rect {
	r.X += dx
	r.Y += dy
	r.W -= 2 * dx
	r.H -= 2 * dy
	if r.W < 0 {
		r.W = 0
	}
	if r.H < 0 {
		r.H = 0
	}
	return r
}

func (r Rect) Intersect(o Rect) Rect {
	x0, y0 := max(r.X, o.X), max(r.Y, o.Y)
	x1, y1 := min(r.Right(), o.Right()), min(r.Bottom(), o.Bottom())
	if x1 <= x0 || y1 <= y0 {
		return Rect{x0, y0, 0, 0}
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Row is the i-th row of r as its own rect.
func (r Rect) Row(i int) Rect { return Rect{r.X, r.Y + i, r.W, 1} }

// Middle is the h rows in the vertical middle of r.
func Middle(r Rect, h int) Rect {
	h = min(h, r.H)
	return Rect{r.X, r.Y + (r.H-h)/2, r.W, h}
}

type ckind int

const (
	cLen ckind = iota
	cMin
	cFill
	cPct
)

// Constraint sizes one part of a split.
type Constraint struct {
	kind ckind
	v    int
}

func Len(n int) Constraint  { return Constraint{cLen, n} }
func Min(n int) Constraint  { return Constraint{cMin, n} }
func Fill(w int) Constraint { return Constraint{cFill, w} }
func Pct(p int) Constraint  { return Constraint{cPct, p} }

// Vertical splits r into rows.
func Vertical(r Rect, spacing int, cs ...Constraint) []Rect { return split(r, true, spacing, cs) }

// Horizontal splits r into columns.
func Horizontal(r Rect, spacing int, cs ...Constraint) []Rect { return split(r, false, spacing, cs) }

// Sizes solves constraints for a total length: lengths, percentages and minimums first,
// then spare room to Fill parts by weight, else to Min parts, else to the last part.
// When there isn't enough room, parts shrink from the last one back.
func Sizes(total, spacing int, cs []Constraint) []int {
	n := len(cs)
	out := make([]int, n)
	if n == 0 {
		return out
	}
	avail := total - spacing*(n-1)
	if avail < 0 {
		avail = 0
	}
	used := 0
	for i, c := range cs {
		switch c.kind {
		case cLen, cMin:
			out[i] = c.v
		case cPct:
			out[i] = total * c.v / 100
		}
		used += out[i]
	}
	spare := avail - used
	if spare > 0 {
		weights, idx := 0, []int{}
		for i, c := range cs {
			if c.kind == cFill {
				weights += max(c.v, 1)
				idx = append(idx, i)
			}
		}
		if len(idx) > 0 {
			given := 0
			for k, i := range idx {
				share := spare * max(cs[i].v, 1) / weights
				if k == len(idx)-1 {
					share = spare - given
				}
				out[i] += share
				given += share
			}
		} else {
			for i, c := range cs {
				if c.kind == cMin {
					idx = append(idx, i)
				}
			}
			if len(idx) > 0 {
				per, extra := spare/len(idx), spare%len(idx)
				for k, i := range idx {
					out[i] += per
					if k < extra {
						out[i]++
					}
				}
			} else {
				out[n-1] += spare
			}
		}
	} else if spare < 0 {
		over := -spare
		for i := n - 1; i >= 0 && over > 0; i-- {
			cut := min(out[i], over)
			out[i] -= cut
			over -= cut
		}
	}
	return out
}

func split(r Rect, vertical bool, spacing int, cs []Constraint) []Rect {
	total := r.W
	if vertical {
		total = r.H
	}
	sizes := Sizes(total, spacing, cs)
	out := make([]Rect, len(cs))
	pos := 0
	for i, s := range sizes {
		if vertical {
			out[i] = Rect{r.X, r.Y + pos, r.W, s}
		} else {
			out[i] = Rect{r.X + pos, r.Y, s, r.H}
		}
		pos += s + spacing
		if pos > total {
			pos = total
		}
	}
	return out
}
