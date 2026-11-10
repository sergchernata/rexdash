package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"rexdash/internal/cases"
	"rexdash/internal/ui"
)

// repo is a throwaway repo with one commit.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	r := &repo{t, t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.write("a.txt", "one\ntwo\nthree\n")
	r.git("add", ".")
	r.git("commit", "-qm", "first")
	return r
}

func (r *repo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *repo) write(p, s string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, p), []byte(s), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) pane() *Git {
	r.t.Helper()
	g := at(r.dir, 30*time.Second)
	if err := g.Refresh(); err != nil {
		r.t.Fatal(err)
	}
	return g
}

func refresh(t *testing.T, g *Git) {
	t.Helper()
	if err := g.Refresh(); err != nil {
		t.Fatal(err)
	}
}

func row(t *testing.T, g *Git, p string) fileRow {
	t.Helper()
	for _, f := range g.files {
		if f.path == p {
			return f
		}
	}
	t.Fatalf("%s not listed", p)
	return fileRow{}
}

func TestDiffCounts(t *testing.T) {
	for _, c := range []struct {
		a, b     string
		add, del int
	}{
		{"a\nb\nc\n", "a\nB\nc\nd\n", 2, 1},
		{"", "x\ny\n", 2, 0},
		{"same\n", "same\n", 0, 0},
		{"\x00bin", "text\n", 0, 0},
		{"a\nb\nc\nd\n", "b\na\nd\nc\n", 2, 2},
	} {
		if a, d := counts([]byte(c.a), []byte(c.b)); a != c.add || d != c.del {
			t.Errorf("counts(%q, %q) = %d, %d; want %d, %d", c.a, c.b, a, d, c.add, c.del)
		}
	}
}

func TestShared(t *testing.T) {
	cases.Check(t, "diff_counts", func(a []any) any {
		add, del := counts([]byte(cases.S(a[0])), []byte(cases.S(a[1])))
		return []int{add, del}
	})
}

func TestFileDiffMarksLines(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\n2\nthree\n")
	r.write("bin", "\x00x")
	g := r.pane()
	view := func(p string) []string {
		g.onCommits = false
		for i, f := range g.files {
			if f.path == p {
				g.filesSt.Sel = i
			}
		}
		if err := g.openSelected(); err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, l := range g.view.Lines {
			s = append(s, l.String())
		}
		g.Key(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
		return s
	}
	lines := strings.Join(view("a.txt"), "\n")
	for _, want := range []string{"a.txt +1 -1", "\n-two", "\n+2", "\n@@"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in\n%s", want, lines)
		}
	}
	if got := view("bin"); len(got) != 2 || got[1] != "binary file" {
		t.Errorf("binary: %q", got)
	}
}

func TestCommitDiff(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\nTWO\nthree\n")
	r.write("b.txt", "b\n")
	r.git("add", ".")
	r.git("commit", "-qm", "second\n\nbody")
	g := r.pane()
	g.commitsSt.Sel = 0
	if err := g.openSelected(); err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, l := range g.view.Lines {
		s = append(s, l.String())
	}
	text := strings.Join(s, "\n")
	for _, want := range []string{"Author: t <t@t>", "    second", "    body", " a.txt +1 -1", " b.txt +1 -0", "── b.txt ", "+TWO"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
}

func TestCleanRepo(t *testing.T) {
	g := newRepo(t).pane()
	if len(g.files) != 0 {
		t.Fatalf("files: %+v", g.files)
	}
	if len(g.commits) != 1 || g.commits[0].summary != "first" {
		t.Fatalf("commits: %+v", g.commits)
	}
	if msg := g.Status().Msg; msg != "main no upstream · clean" {
		t.Errorf("status %q", msg)
	}
	if !g.onCommits {
		t.Error("focus moves to commits when nothing changed")
	}
	b := ui.NewBuffer(80, 20)
	g.Draw(b, b.Area())
	if !strings.Contains(b.Text(), "working tree clean") || !strings.Contains(b.Text(), "first") {
		t.Errorf("render:\n%s", b.Text())
	}
}

func TestStagedUnstagedUntracked(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\n2\nthree\nfour\n")
	r.write("new.txt", "fresh\n")
	r.write("staged.txt", "s\n")
	r.git("add", "staged.txt")
	g := r.pane()

	if a := row(t, g, "a.txt"); a.staged != ' ' || a.unstaged != 'M' || a.add != 2 || a.del != 1 {
		t.Errorf("a.txt: %+v", a)
	}
	if n := row(t, g, "new.txt"); n.unstaged != '?' || n.add != 1 {
		t.Errorf("new.txt: %+v", n)
	}
	if s := row(t, g, "staged.txt"); s.staged != 'A' {
		t.Errorf("staged.txt: %+v", s)
	}
	if msg := g.Status().Msg; !strings.Contains(msg, "3 changed +4 -1") {
		t.Errorf("status %q", msg)
	}
}

func TestCountsRefreshWhenFileChangesAgain(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\ntwo\nthree\nfour\n")
	g := r.pane()
	if a := row(t, g, "a.txt").add; a != 1 {
		t.Fatalf("add %d", a)
	}
	// Different size, so the cached count can't be reused even within the same mtime second.
	r.write("a.txt", "one\ntwo\nthree\nfour\nfive\nsix\n")
	refresh(t, g)
	if a := row(t, g, "a.txt").add; a != 3 {
		t.Fatalf("add %d", a)
	}
	// Committing changes HEAD: the file is clean and the cache entry is dropped.
	r.git("commit", "-qam", "second")
	refresh(t, g)
	if len(g.files) != 0 || len(g.counts) != 0 {
		t.Fatalf("files %+v counts %+v", g.files, g.counts)
	}
	if g.commits[0].summary != "second" {
		t.Error("commits reload when HEAD moves")
	}
}

func TestBranchSwitchUpdatesLabel(t *testing.T) {
	r := newRepo(t)
	g := r.pane()
	r.git("checkout", "-qb", "feature")
	refresh(t, g)
	if msg := g.Status().Msg; !strings.HasPrefix(msg, "feature") {
		t.Errorf("same HEAD, new branch name: %s", msg)
	}
}

func TestAheadBehind(t *testing.T) {
	r := newRepo(t)
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")
	g := r.pane()
	if msg := g.Status().Msg; msg != "main = origin · clean" {
		t.Errorf("status %q", msg)
	}
	r.git("commit", "-q", "--allow-empty", "-m", "ahead")
	refresh(t, g)
	if msg := g.Status().Msg; msg != "main ↑1 · clean" {
		t.Errorf("status %q", msg)
	}
}

func TestWatchesWorkTree(t *testing.T) {
	r := newRepo(t)
	if dirs := at(r.dir, time.Second).Watch(); len(dirs) != 1 {
		t.Errorf("git dir inside the work tree isn't watched twice: %v", dirs)
	}
}
