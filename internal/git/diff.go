package git

import (
	"bytes"
	"strings"
)

func isBinary(a []byte) bool {
	return bytes.IndexByte(a[:min(len(a), 8000)], 0) >= 0
}

// counts is the lines added and removed between two texts (binary → 0, 0).
func counts(old, new []byte) (add, del int) {
	if isBinary(old) || isBinary(new) {
		return 0, 0
	}
	a, b := intern(old, new)
	common := lcs(a, b)
	return len(b) - common, len(a) - common
}

// intern splits both texts into lines (terminators kept) and numbers equal lines alike.
func intern(old, new []byte) ([]int, []int) {
	ids := map[string]int{}
	conv := func(s []byte) []int {
		var out []int
		for len(s) > 0 {
			i := bytes.IndexByte(s, '\n') + 1
			if i == 0 {
				i = len(s)
			}
			id, ok := ids[string(s[:i])]
			if !ok {
				id = len(ids)
				ids[string(s[:i])] = id
			}
			out = append(out, id)
			s = s[i:]
		}
		return out
	}
	return conv(old), conv(new)
}

// lcs is the length of the longest common subsequence of lines.
func lcs(a, b []int) int {
	// Common prefix and suffix are free.
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	a, b = a[p:], b[p:]
	s := 0
	for s < len(a) && s < len(b) && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	a, b = a[:len(a)-s], b[:len(b)-s]
	// Lines missing from the other side can't be common; dropping them keeps rewrites cheap.
	a, b = only(a, b), only(b, a)
	return p + s + (len(a)+len(b)-myers(a, b))/2
}

func only(a, other []int) []int {
	in := make(map[int]bool, len(other))
	for _, x := range other {
		in[x] = true
	}
	out := a[:0:0]
	for _, x := range a {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}

// myers is the edit distance (insertions + deletions) between a and b.
func myers(a, b []int) int {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return n + m
	}
	limit := n + m
	off := limit + 1
	v := make([]int, 2*limit+2)
	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return d
			}
		}
	}
	return limit
}

// patch is one file of `git diff` output: its path, +/- counts and hunk lines.
type patch struct {
	path     string
	add, del int
	binary   bool
	lines    []string
}

// parsePatch splits unified diff output by file.
func parsePatch(out []byte) []patch {
	var ps []patch
	var cur *patch
	var from, to, minus, plus string
	hunk := false
	done := func() {
		if cur == nil {
			return
		}
		switch {
		case from != "" && to != "":
			cur.path = from + " → " + to
		case plus != "" && plus != "/dev/null":
			cur.path = strings.TrimPrefix(plus, "b/")
		case minus != "" && minus != "/dev/null":
			cur.path = strings.TrimPrefix(minus, "a/")
		}
		ps = append(ps, *cur)
	}
	for _, l := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if rest, ok := strings.CutPrefix(l, "diff --git "); ok {
			done()
			// "a/X b/X": exact when the path didn't change; the headers below refine it.
			path := rest
			if n := (len(rest) - 5) / 2; n > 0 && len(rest) == 2*n+5 {
				path = rest[2 : 2+n]
			}
			cur = &patch{path: path}
			from, to, minus, plus, hunk = "", "", "", "", false
			continue
		}
		if cur == nil {
			continue
		}
		if !hunk {
			switch {
			case strings.HasPrefix(l, "@@"):
				hunk = true
				cur.lines = append(cur.lines, l)
			case strings.HasPrefix(l, "rename from "):
				from = l[len("rename from "):]
			case strings.HasPrefix(l, "rename to "):
				to = l[len("rename to "):]
			case strings.HasPrefix(l, "--- "):
				minus = l[4:]
			case strings.HasPrefix(l, "+++ "):
				plus = l[4:]
			case strings.HasPrefix(l, "Binary files "):
				cur.binary = true
			}
			continue
		}
		switch {
		case strings.HasPrefix(l, "+"):
			cur.add++
		case strings.HasPrefix(l, "-"):
			cur.del++
		}
		cur.lines = append(cur.lines, l)
	}
	done()
	return ps
}
