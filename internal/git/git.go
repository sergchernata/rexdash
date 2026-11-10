// Package git is the git pane, via the git CLI: changed files and recent commits;
// Enter opens a coloured diff.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

type fileRow struct {
	path             string
	staged, unstaged byte
	add, del         int
}

type commitRow struct {
	id, summary, author string
	when                int64
}

// stamp is what a cached diff count depends on: HEAD, file size, file mtime.
type stamp struct {
	head string
	size int64
	mod  time.Time
}

type count struct {
	st       stamp
	add, del int
}

type Git struct {
	ui.Base
	every time.Duration
	dir   string
	// root is the work tree, found on the first refresh; git runs there.
	root    string
	head    string // "" before the first commit
	branch  ui.Line
	name    string
	ab      []int // ahead, behind vs origin/<branch>; nil = no upstream
	files   []fileRow
	commits []commitRow
	// onCommits is the focus: commits table, else files.
	onCommits          bool
	filesSt, commitsSt ui.TableState
	view               *ui.TextView
	// heads is branch, HEAD and upstream at the last commit/ahead-behind load; those only reload when it changes.
	heads  string
	loaded bool
	// counts are +/- per file, reused while HEAD and the file's size and mtime stay the same.
	counts map[string]count
}

func New(every time.Duration) ui.Pane { return at(".", every) }

func at(dir string, every time.Duration) *Git {
	return &Git{every: every, dir: dir, filesSt: ui.NewTableState(), commitsSt: ui.NewTableState(), counts: map[string]count{}}
}

// git runs git in dir; errors carry git's first stderr line.
func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.quotePath=false", "--literal-pathspecs"}, args...)...)
	// Don't refresh the index from status: that write would wake the watcher again.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n"); msg != "" {
			return out, errors.New(msg)
		}
	}
	return out, err
}

func (g *Git) git(args ...string) ([]byte, error) { return git(g.root, args...) }

func branchLine(name string, ab []int) ui.Line {
	l := ui.L(ui.C(name, ui.Green()))
	switch {
	case ab == nil:
		l.Spans = append(l.Spans, ui.S(" no upstream", ui.Style{Dim: true}))
	case ab[0] == 0 && ab[1] == 0:
		l.Spans = append(l.Spans, ui.S(" = origin", ui.Style{Dim: true}))
	default:
		if ab[0] > 0 {
			l.Spans = append(l.Spans, ui.C(fmt.Sprintf(" ↑%d", ab[0]), ui.Green()))
		}
		if ab[1] > 0 {
			l.Spans = append(l.Spans, ui.C(fmt.Sprintf(" ↓%d", ab[1]), ui.Red()))
		}
	}
	return l
}

// status is the parsed `git status --porcelain=v2 -z --branch`.
type status struct {
	head, branch string
	codes        map[string][2]byte // staged, unstaged; ' ' = unchanged
}

func parseStatus(out []byte) status {
	st := status{codes: map[string][2]byte{}}
	code := func(c byte) byte {
		switch c {
		case '.':
			return ' '
		case 'T':
			return 'M'
		case 'C':
			return 'R'
		}
		return c
	}
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if r == "" {
			continue
		}
		switch r[0] {
		case '#':
			if v, ok := strings.CutPrefix(r, "# branch.oid "); ok && v != "(initial)" {
				st.head = v
			} else if v, ok := strings.CutPrefix(r, "# branch.head "); ok {
				st.branch = v
			}
		case '1', '2':
			// "1 XY sub mH mI mW hH hI path"; "2 …" has a score field and the original path next.
			n := 9
			if r[0] == '2' {
				n = 10
				i++
			}
			if f := strings.SplitN(r, " ", n); len(f) == n {
				st.codes[f[n-1]] = [2]byte{code(f[1][0]), code(f[1][1])}
			}
		case 'u':
			if f := strings.SplitN(r, " ", 11); len(f) == 11 {
				st.codes[f[10]] = [2]byte{' ', 'U'}
			}
		case '?':
			st.codes[r[2:]] = [2]byte{' ', '?'}
		}
	}
	return st
}

func (g *Git) Name() string         { return "Git" }
func (g *Git) Every() time.Duration { return g.every }
func (g *Git) Retheme()             { g.branch = branchLine(g.name, g.ab) }

func (g *Git) Title() ui.Line {
	l := ui.L(ui.C(ui.BRANCH.S()+" ", ui.Green()))
	l.Spans = append(l.Spans, g.branch.Spans...)
	return l
}

func (g *Git) Hints() []ui.Hint {
	if g.view != nil {
		return []ui.Hint{{Key: "↑↓", Desc: "scroll"}, {Key: "esc", Desc: "back"}}
	}
	return []ui.Hint{{Key: "↑↓", Desc: "move"}, {Key: "tab", Desc: "files/commits"}, {Key: "⏎", Desc: "diff"}}
}

// Watch is the work tree, plus the git dir when it lives elsewhere (linked worktrees).
func (g *Git) Watch() []string {
	out, err := git(g.dir, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	f := strings.Fields(string(out))
	if err != nil || len(f) != 2 {
		return nil
	}
	if f[1] == f[0] || strings.HasPrefix(f[1], f[0]+"/") {
		return f[:1]
	}
	return f
}

func (g *Git) totals() (add, del int) {
	for _, f := range g.files {
		add += f.add
		del += f.del
	}
	return
}

func (g *Git) Status() ui.Status {
	msg := g.branch.String() + " · clean"
	if len(g.files) > 0 {
		a, d := g.totals()
		msg = fmt.Sprintf("%s · %d changed +%d -%d", g.branch.String(), len(g.files), a, d)
	}
	return ui.Status{State: "idle", Msg: msg}
}

func (g *Git) Refresh() error {
	if g.root == "" {
		out, err := git(g.dir, "rev-parse", "--show-toplevel")
		if err != nil {
			return err
		}
		g.root = strings.TrimSpace(string(out))
	}
	out, err := g.git("status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return err
	}
	st := parseStatus(out)
	g.head = st.head
	up := ""
	if st.branch != "(detached)" {
		out, _ := g.git("rev-parse", "-q", "--verify", "refs/remotes/origin/"+st.branch+"^{commit}")
		up = strings.TrimSpace(string(out))
	}

	if heads := st.branch + "\x00" + st.head + "\x00" + up; !g.loaded || heads != g.heads {
		g.loaded, g.heads = true, heads
		g.name, g.ab = st.branch, nil
		if st.head != "" && up != "" {
			out, _ := g.git("rev-list", "--left-right", "--count", st.head+"..."+up)
			if f := strings.Fields(string(out)); len(f) == 2 {
				a, _ := strconv.Atoi(f[0])
				b, _ := strconv.Atoi(f[1])
				g.ab = []int{a, b}
			}
		}
		g.branch = branchLine(g.name, g.ab)
		g.commits = nil
		if st.head != "" {
			if g.commits, err = g.log(st.head); err != nil {
				return err
			}
		}
	}

	paths := make([]string, 0, len(st.codes))
	for p := range st.codes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	fresh := make(map[string]count, len(paths))
	var stale []string // tracked files whose counts git must recompute
	for _, p := range paths {
		var s stamp
		if fi, err := os.Stat(filepath.Join(g.root, p)); err == nil {
			s = stamp{st.head, fi.Size(), fi.ModTime()}
		} else {
			s = stamp{head: st.head}
		}
		if c, ok := g.counts[p]; ok && c.st == s {
			fresh[p] = c
			continue
		}
		c := count{st: s}
		if st.codes[p][1] == '?' || st.head == "" {
			data, _ := os.ReadFile(filepath.Join(g.root, p))
			c.add, c.del = counts(nil, data)
		} else {
			stale = append(stale, p)
		}
		fresh[p] = c
	}
	if len(stale) > 0 {
		args := []string{"diff", "HEAD", "--numstat", "-z", "--no-renames", "--no-ext-diff"}
		if len(stale) <= 200 {
			args = append(append(args, "--"), stale...)
		}
		out, err := g.git(args...)
		if err != nil {
			return err
		}
		// "add\tdel\tpath\0"; binary files are "-\t-".
		for _, r := range strings.Split(string(out), "\x00") {
			f := strings.SplitN(r, "\t", 3)
			if len(f) != 3 {
				continue
			}
			if c, ok := fresh[f[2]]; ok {
				c.add, _ = strconv.Atoi(f[0])
				c.del, _ = strconv.Atoi(f[1])
				fresh[f[2]] = c
			}
		}
	}
	g.counts = fresh
	g.files = g.files[:0]
	for _, p := range paths {
		c := st.codes[p]
		g.files = append(g.files, fileRow{p, c[0], c[1], fresh[p].add, fresh[p].del})
	}

	ui.Clamp(&g.filesSt.Sel, len(g.files))
	ui.Clamp(&g.commitsSt.Sel, len(g.commits))
	if len(g.files) == 0 {
		g.onCommits = true
	}
	return nil
}

func (g *Git) log(head string) ([]commitRow, error) {
	out, err := g.git("log", "-30", "-z", "--format=%H%x1f%s%x1f%an%x1f%at", head)
	if err != nil {
		return nil, err
	}
	var cs []commitRow
	for _, r := range strings.Split(string(out), "\x00") {
		f := strings.Split(r, "\x1f")
		if len(f) != 4 {
			continue
		}
		when, _ := strconv.ParseInt(f[3], 10, 64)
		cs = append(cs, commitRow{f[0], f[1], f[2], when})
	}
	return cs, nil
}

func (g *Git) Key(ev *tcell.EventKey) error {
	if g.view != nil {
		if ui.IsBack(ev) {
			g.view = nil
			ui.Zoom(false)
		} else {
			g.view.Key(ev)
		}
		return nil
	}
	switch {
	case ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab:
		g.onCommits = !g.onCommits
	case ui.IsEnter(ev):
		return g.openSelected()
	case g.onCommits:
		ui.Nav(&g.commitsSt.Sel, len(g.commits), ev, 10)
	default:
		ui.Nav(&g.filesSt.Sel, len(g.files), ev, 10)
	}
	return nil
}

func (g *Git) openSelected() error {
	var view *ui.TextView
	if g.onCommits {
		i := g.commitsSt.Sel
		if i < 0 || i >= len(g.commits) {
			return nil
		}
		lines, err := g.commitDiff(g.commits[i].id)
		if err != nil {
			return err
		}
		view = ui.NewTextView("commit "+g.commits[i].id[:min(9, len(g.commits[i].id))], lines).WithIcon(ui.COMMIT)
	} else {
		i := g.filesSt.Sel
		if i < 0 || i >= len(g.files) {
			return nil
		}
		f := g.files[i]
		lines := []ui.Line{ui.L(
			ui.S(f.path+" ", ui.Style{Bold: true}),
			ui.C(fmt.Sprintf("+%d", f.add), ui.Green()),
			ui.C(fmt.Sprintf(" -%d", f.del), ui.Red()),
		)}
		body, err := g.fileDiff(f)
		if err != nil {
			return err
		}
		view = ui.NewTextView("diff "+f.path, append(lines, body...))
	}
	g.view = view
	ui.Zoom(true)
	return nil
}

// diffArgs keep git's output plain whatever the user's config says.
var diffArgs = []string{"--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/"}

// fileDiff is the file's work tree against HEAD; untracked files are all additions.
func (g *Git) fileDiff(f fileRow) ([]ui.Line, error) {
	if f.unstaged == '?' || g.head == "" {
		data, _ := os.ReadFile(filepath.Join(g.root, f.path))
		if isBinary(data) {
			return []ui.Line{binaryLine()}, nil
		}
		text := strings.TrimSuffix(string(data), "\n")
		if text == "" {
			return nil, nil
		}
		rows := strings.Split(text, "\n")
		lines := []ui.Line{ui.DiffLine(fmt.Sprintf("@@ -0,0 +1,%d @@", len(rows)))}
		for _, r := range rows {
			lines = append(lines, ui.DiffLine("+"+r))
		}
		return lines, nil
	}
	out, err := g.git(append(append([]string{"diff", "HEAD", "--no-renames"}, diffArgs...), "--", f.path)...)
	if err != nil {
		return nil, err
	}
	var lines []ui.Line
	for _, p := range parsePatch(out) {
		lines = append(lines, hunks(p)...)
	}
	return lines, nil
}

func binaryLine() ui.Line { return ui.LT("binary file", ui.Style{Dim: true}) }

func hunks(p patch) []ui.Line {
	if p.binary {
		return []ui.Line{binaryLine()}
	}
	lines := make([]ui.Line, len(p.lines))
	for i, l := range p.lines {
		lines[i] = ui.DiffLine(l)
	}
	return lines
}

// commitDiff is a commit's header, message, per-file stat and diff against its first parent.
func (g *Git) commitDiff(id string) ([]ui.Line, error) {
	out, err := g.git("show", "-s", "--format=%H%x00%an%x00%ae%x00%at%x00%B", id)
	if err != nil {
		return nil, err
	}
	f := strings.SplitN(string(out), "\x00", 5)
	if len(f) != 5 {
		return nil, fmt.Errorf("unexpected git show output for %s", id)
	}
	at, _ := strconv.ParseInt(f[3], 10, 64)
	lines := []ui.Line{
		ui.LT("commit "+f[0], ui.Fg(ui.Yellow())),
		ui.LT(fmt.Sprintf("Author: %s <%s>", f[1], f[2]), ui.Style{}),
		ui.LT("Date:   "+ui.Local(at, "Mon Jan 02 15:04 2006"), ui.Style{}),
		{},
	}
	for _, l := range strings.Split(strings.TrimRight(f[4], "\n"), "\n") {
		lines = append(lines, ui.LT("    "+l, ui.Style{}))
	}
	lines = append(lines, ui.Line{})

	out, err = g.git(append(append([]string{"show", "--format=", "-M", "--diff-merges=first-parent"}, diffArgs...), id)...)
	if err != nil {
		return nil, err
	}
	var body []ui.Line
	for _, p := range parsePatch(bytes.TrimLeft(out, "\n")) {
		lines = append(lines, ui.L(
			ui.T(" "+p.path+" "),
			ui.C(fmt.Sprintf("+%d", p.add), ui.Green()),
			ui.C(fmt.Sprintf(" -%d", p.del), ui.Red()),
		))
		body = append(body, ui.Line{}, ui.LT("── "+p.path+" ", ui.Style{Fg: ui.Magenta(), Bold: true}))
		body = append(body, hunks(p)...)
	}
	return append(lines, body...), nil
}

func (g *Git) Draw(b *ui.Buffer, area ui.Rect) {
	if g.view != nil {
		g.view.Draw(b, area)
		return
	}
	filesH := 3
	if len(g.files) > 0 {
		filesH = max(min(len(g.files)+2, area.H*45/100), 4)
	}
	parts := ui.Vertical(area, 0, ui.Len(filesH), ui.Min(3))
	fa, ca := parts[0], parts[1]
	focusBorder := func(on bool) ui.Color {
		if on {
			return ui.Accent()
		}
		return ui.Border()
	}

	// Changes
	block := ui.Panel{Title: ui.L(ui.T(" " + ui.DIFF.S() + " Changes ")), BorderColor: focusBorder(!g.onCommits)}
	if len(g.files) == 0 {
		inner := block.Draw(b, fa)
		ui.EmptyState(b, inner, ui.OK.S(), ui.Green(), "working tree clean", "")
	} else {
		add, del := g.totals()
		block.Title.Spans = append(block.Title.Spans, ui.CountBadge(len(g.files)), ui.T(" "))
		block.Right = ui.L(ui.C(fmt.Sprintf(" +%d", add), ui.Green()), ui.C(fmt.Sprintf(" -%d ", del), ui.Red()))
		rows := make([]ui.Row, len(g.files))
		for i, r := range g.files {
			rows[i] = fileCells(r)
		}
		g.table(b, fa, ui.Table{
			Cols:  []ui.Constraint{ui.Len(2), ui.Min(10), ui.Len(11)},
			Rows:  rows,
			Block: &block,
		}, &g.filesSt, !g.onCommits)
	}

	// Commits
	cblock := ui.Panel{Title: ui.L(ui.T(" " + ui.COMMIT.S() + " Commits ")), BorderColor: focusBorder(g.onCommits)}
	rows := make([]ui.Row, len(g.commits))
	for i, c := range g.commits {
		author, _, _ := strings.Cut(strings.TrimSpace(c.author), " ")
		rows[i] = ui.R(
			ui.LT(c.id[:min(7, len(c.id))], ui.Fg(ui.Yellow())),
			ui.LT(c.summary, ui.Style{}),
			ui.LT(author, ui.Fg(ui.Muted())),
			ui.LT(ui.Ago(c.when), ui.Fg(ui.Muted())).RightAligned(),
		)
	}
	g.table(b, ca, ui.Table{
		Cols:  []ui.Constraint{ui.Len(7), ui.Min(10), ui.Len(8), ui.Len(4)},
		Rows:  rows,
		Block: &cblock,
	}, &g.commitsSt, g.onCommits)
	ui.Scrollbar(b, ca, len(g.commits), max(g.commitsSt.Sel, 0), ca.H-2)
}

// table draws with the selection highlighted when focused, else only its marker.
func (g *Git) table(b *ui.Buffer, r ui.Rect, t ui.Table, st *ui.TableState, focused bool) {
	t.Spacing, t.Highlight = 1, true
	if focused {
		t.Draw(b, r, st)
		return
	}
	sel := st.Sel
	st.Sel = -1
	t.Draw(b, r, st)
	st.Sel = sel
	in := t.Block.Inner(r)
	if y := in.Y + sel - st.Offset; sel >= 0 && y >= in.Y && y < in.Bottom() {
		b.SetString(in.X, y, ui.MARK, ui.Fg(ui.Accent()), 1)
	}
}

func fileCells(r fileRow) ui.Row {
	// One status icon: unstaged state wins; green when the change is fully staged.
	code, stagedOnly := r.unstaged, false
	if code == ' ' {
		code, stagedOnly = r.staged, true
	}
	ic, color := ui.MODIFIED, ui.Yellow()
	switch code {
	case '?':
		ic, color = ui.UNTRACKED, ui.Red()
	case 'A':
		ic, color = ui.ADDED, ui.Green()
	case 'D':
		ic, color = ui.REMOVED, ui.Red()
	case 'R':
		ic, color = ui.RENAMED, ui.Cyan()
	case 'U':
		ic, color = ui.CONFLICT, ui.Red()
	}
	if stagedOnly {
		color = ui.Green()
	}
	dir, file := "", r.path
	if i := strings.LastIndex(r.path, "/"); i >= 0 {
		dir, file = r.path[:i+1], r.path[i+1:]
	}
	fi, fc := ui.FileIcon(r.path)
	if fi != "" {
		fi += " "
	}
	mark := ui.T(" ")
	if r.staged != ' ' && r.unstaged != ' ' {
		mark = ui.C("+", ui.Green())
	}
	return ui.R(
		ui.L(ui.S(ic.S(), ui.Style{Fg: color, Bold: true}), mark),
		ui.L(ui.C(fi, fc), ui.C(dir, ui.Muted()), ui.T(file)),
		ui.L(ui.C(fmt.Sprintf("+%d", r.add), ui.Green()), ui.C(fmt.Sprintf(" -%d", r.del), ui.Red())).RightAligned(),
	)
}
