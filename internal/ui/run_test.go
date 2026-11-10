package ui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestMain pins the theme to the terminal's ANSI colours so tests don't depend on the
// developer's Rex theme or macOS appearance.
func TestMain(m *testing.M) {
	os.Setenv("REXDASH_THEME", "terminal")
	ReloadTheme()
	os.Exit(m.Run())
}

// fakePane records what the run loop asks of it.
type fakePane struct {
	Base
	mu        sync.Mutex
	name      string
	title     string
	every     time.Duration
	status    Status
	refreshes int
	refreshed chan struct{}
	refErr    error
	keyErr    error
	keys      []string
	typing    bool
	rethemes  int
	watch     []string
}

func newFake() *fakePane {
	return &fakePane{name: "fake", title: "ctx", every: time.Hour, status: Status{"idle", "all good"}, refreshed: make(chan struct{}, 64)}
}

func (f *fakePane) Name() string    { return f.name }
func (f *fakePane) Title() Line     { return LT(f.title, Style{}) }
func (f *fakePane) Hints() []Hint   { return []Hint{{"j", "down"}} }
func (f *fakePane) Watch() []string { return f.watch }
func (f *fakePane) Every() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.every
}
func (f *fakePane) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}
func (f *fakePane) Typing() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typing
}
func (f *fakePane) Retheme() {
	f.mu.Lock()
	f.rethemes++
	f.mu.Unlock()
}
func (f *fakePane) Refresh() error {
	f.mu.Lock()
	f.refreshes++
	err := f.refErr
	f.mu.Unlock()
	f.refreshed <- struct{}{}
	return err
}
func (f *fakePane) Key(ev *tcell.EventKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, string(ev.Rune()))
	return f.keyErr
}
func (f *fakePane) Draw(b *Buffer, r Rect) {
	f.mu.Lock()
	n := f.refreshes
	f.mu.Unlock()
	b.SetString(r.X, r.Y, "body refreshes="+string(rune('0'+n)), Style{}, r.W)
}
func (f *fakePane) set(fn func(f *fakePane)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}
func (f *fakePane) get(fn func(f *fakePane)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

// syncBuf collects OSC writes from the loop goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lockedScreen serialises drawing with reads of the simulation screen's cells, which
// GetContents hands out without a copy.
type lockedScreen struct {
	tcell.SimulationScreen
	mu sync.Mutex
}

func (l *lockedScreen) Show() { l.mu.Lock(); l.SimulationScreen.Show(); l.mu.Unlock() }
func (l *lockedScreen) Sync() { l.mu.Lock(); l.SimulationScreen.Sync(); l.mu.Unlock() }
func (l *lockedScreen) SetSize(w, h int) {
	l.mu.Lock()
	l.SimulationScreen.SetSize(w, h)
	l.mu.Unlock()
}
func (l *lockedScreen) snapshot() ([]tcell.SimCell, int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cells, w, h := l.SimulationScreen.GetContents()
	return append([]tcell.SimCell(nil), cells...), w, h
}

// harness runs the loop on a simulation screen with test watcher channels.
type harness struct {
	t                         *testing.T
	scr                       *lockedScreen
	p                         *fakePane
	osc                       *syncBuf
	changed, themed, upgraded chan<- struct{}
	watched                   []string
	zooms                     chan struct{}
	reexecArgs                chan []string
	done                      chan error
	exited                    bool
}

var errStubExec = errors.New("stub exec")

func start(t *testing.T, p *fakePane) *harness {
	t.Helper()
	scr := &lockedScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	scr.SetSize(60, 8)
	h := &harness{t: t, scr: scr, p: p, osc: &syncBuf{}, zooms: make(chan struct{}, 8), reexecArgs: make(chan []string, 1), done: make(chan error, 1)}
	saved := out
	out = h.osc
	t.Cleanup(func() { out = saved })
	ready := make(chan struct{}, 3)
	o := runOpts{
		exe:        "/bin/rexdash-test",
		watchDirs:  func(d []string, ch chan<- struct{}) { h.watched, h.changed = d, ch; ready <- struct{}{} },
		watchTheme: func(ch chan<- struct{}) { h.themed = ch; ready <- struct{}{} },
		watchExe: func(exe string, ch chan<- struct{}) {
			if exe != "/bin/rexdash-test" {
				t.Errorf("watchExe(%q)", exe)
			}
			h.upgraded = ch
			ready <- struct{}{}
		},
		zoom: func() { h.zooms <- struct{}{} },
		reexec: func(exe string, args []string) error {
			h.reexecArgs <- append([]string{exe}, args...)
			return errStubExec
		},
	}
	go func() { h.done <- run(scr, p, o) }()
	n := 2
	if len(p.watch) > 0 {
		n = 3
	}
	for i := 0; i < n; i++ {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("watchers not started")
		}
	}
	h.waitRefresh()
	t.Cleanup(func() {
		if !h.exited {
			h.scr.PostEvent(tcell.NewEventKey(tcell.KeyCtrlC, 0, 0))
			select {
			case <-h.done:
			case <-time.After(2 * time.Second):
				t.Error("loop did not exit on Ctrl-C")
			}
		}
	})
	return h
}

func (h *harness) waitRefresh() {
	h.t.Helper()
	select {
	case <-h.p.refreshed:
	case <-time.After(2 * time.Second):
		h.t.Fatal("no refresh")
	}
}

func (h *harness) noRefresh(d time.Duration) {
	h.t.Helper()
	select {
	case <-h.p.refreshed:
		h.t.Fatal("unexpected refresh")
	case <-time.After(d):
	}
}

func (h *harness) key(r rune) { h.scr.PostEvent(tcell.NewEventKey(tcell.KeyRune, r, 0)) }

// rows is the screen as text, one string per row (wide-cell continuations skipped).
func (h *harness) rows() []string {
	cells, w, hh := h.scr.snapshot()
	out := make([]string, hh)
	for y := 0; y < hh; y++ {
		var sb strings.Builder
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Runes) == 0 {
				sb.WriteByte(' ')
				continue
			}
			sb.WriteString(string(c.Runes))
		}
		out[y] = sb.String()
	}
	return out
}

// eventually polls until cond holds on the screen rows, failing after a deadline.
func (h *harness) eventually(what string, cond func(rows []string) bool) []string {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows := h.rows()
		if cond(rows) {
			return rows
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s; screen:\n%s", what, strings.Join(rows, "\n"))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *harness) waitDone() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		h.exited = true
		return err
	case <-time.After(2 * time.Second):
		h.t.Fatal("loop did not exit")
		return nil
	}
}

func refreshes(p *fakePane) (n int) { p.get(func(f *fakePane) { n = f.refreshes }); return }

func TestRunRefreshKeysAndExit(t *testing.T) {
	p := newFake()
	h := start(t, p) // first refresh is immediate
	h.noRefresh(30 * time.Millisecond)

	h.key('r')
	h.waitRefresh()
	if n := refreshes(p); n != 2 {
		t.Fatalf("refreshes = %d, want 2", n)
	}

	h.key('z')
	select {
	case <-h.zooms:
	case <-time.After(2 * time.Second):
		t.Fatal("z did not zoom")
	}
	h.key('x')
	h.eventually("x reaches the pane", func([]string) bool {
		var k []string
		p.get(func(f *fakePane) { k = f.keys })
		return len(k) == 1
	})
	p.get(func(f *fakePane) {
		if strings.Join(f.keys, "") != "x" {
			t.Errorf("pane keys = %q, want only x (r and z are global)", f.keys)
		}
	})

	// While typing, r and z belong to the pane.
	p.set(func(f *fakePane) { f.typing = true })
	h.key('r')
	h.key('z')
	h.eventually("r, z reach the pane", func([]string) bool {
		var k []string
		p.get(func(f *fakePane) { k = f.keys })
		return strings.Join(k, "") == "xrz"
	})
	h.noRefresh(20 * time.Millisecond)
	if len(h.zooms) != 0 {
		t.Error("z zoomed while typing")
	}

	h.scr.PostEvent(tcell.NewEventKey(tcell.KeyCtrlC, 0, 0))
	if err := h.waitDone(); err != nil {
		t.Fatalf("Ctrl-C: %v", err)
	}
	if len(h.reexecArgs) != 0 {
		t.Error("reexec on Ctrl-C")
	}
	if !strings.HasSuffix(h.osc.String(), osc("fake", "idle", "stopped")) {
		t.Errorf("last report %q, want idle/stopped", h.osc.String())
	}
}

func TestRunRefreshesEveryInterval(t *testing.T) {
	p := newFake()
	p.every = 20 * time.Millisecond
	h := start(t, p)
	h.waitRefresh()
	h.waitRefresh()
	h.eventually("header shows the interval", func(r []string) bool { return strings.Contains(r[0], "· 0s") })
}

func osc(name, state, msg string) string {
	return "\x1b]2;" + name + "\x07\x1b]7501;state=" + state + ":app=rexdash:title=" + B64(name) + ":msg=" + B64(msg) + "\x1b\\"
}

func TestRunErrorsAndReports(t *testing.T) {
	p := newFake()
	p.refErr = errors.New("boom")
	h := start(t, p)

	h.eventually("error in footer", func(r []string) bool { return strings.Contains(r[7], BUG.S()+" boom") })
	cells, w, _ := h.scr.snapshot()
	// The footer is red: find the 'b' of boom.
	found := false
	for i := 0; i < w; i++ {
		c := cells[7*w+i]
		if len(c.Runes) > 0 && c.Runes[0] == 'b' {
			fg, _, _ := c.Style.Decompose()
			if fg != Red() {
				t.Errorf("error fg = %v, want red", fg)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no error text")
	}
	if got := h.osc.String(); got != osc("fake", "working", "loading")+osc("fake", "error", "boom") {
		t.Errorf("reports = %q", got)
	}

	// Same error again: no new report. Then success clears the footer and reports once.
	h.key('r')
	h.waitRefresh()
	p.set(func(f *fakePane) { f.refErr = nil })
	h.key('r')
	h.waitRefresh()
	h.eventually("error cleared", func(r []string) bool {
		return !strings.Contains(r[7], "boom") && strings.Contains(r[7], "refresh")
	})
	want := osc("fake", "working", "loading") + osc("fake", "error", "boom") + osc("fake", "idle", "all good")
	if got := h.osc.String(); got != want {
		t.Errorf("reports = %q\nwant      %q", got, want)
	}
	h.key('r')
	h.waitRefresh()
	p.set(func(f *fakePane) { f.status = Status{"working", "line1\nline2"} })
	h.key('r')
	h.waitRefresh()
	h.eventually("status reported", func([]string) bool { return strings.HasSuffix(h.osc.String(), osc("fake", "working", "line1\nline2")) })
	if got := h.osc.String(); strings.Count(got, "state=idle") != 1 {
		t.Errorf("unchanged status re-reported: %q", got)
	}
	// The OSC 7501 payload is base64 with control characters removed.
	dec, _ := base64.StdEncoding.DecodeString(B64("line1\nline2"))
	if string(dec) != "line1line2" {
		t.Errorf("B64 payload = %q", dec)
	}

	// Key errors show too.
	p.set(func(f *fakePane) { f.keyErr = errors.New("bad key") })
	h.key('x')
	h.eventually("key error in footer", func(r []string) bool { return strings.Contains(r[7], BUG.S()+" bad key") })
}

func TestRunFrame(t *testing.T) {
	p := newFake()
	p.every = 5 * time.Minute
	h := start(t, p)
	rows := h.eventually("frame drawn", func(r []string) bool { return strings.Contains(r[1], "body refreshes=1") })
	if !strings.HasPrefix(rows[0], " ctx") {
		t.Errorf("header title: %q", rows[0])
	}
	if !strings.HasSuffix(strings.TrimRight(rows[0], " "), "· 5m") {
		t.Errorf("header interval: %q", rows[0])
	}
	if !regexp.MustCompile(REFRESH.S() + ` \d\d:\d\d:\d\d · 5m`).MatchString(rows[0]) {
		t.Errorf("header refresh time: %q", rows[0])
	}
	foot := rows[7]
	for _, want := range []string{keycap("j", "down"), keycap("r", "refresh"), keycap("z", "zoom")} {
		if !strings.Contains(foot, want) {
			t.Errorf("footer missing %q: %q", want, foot)
		}
	}
	// Centred: equal blank margins, give or take one.
	r := []rune(foot)
	left := len(r) - len([]rune(strings.TrimLeft(foot, " ")))
	right := len(r) - len([]rune(strings.TrimRight(foot, " ")))
	if left == 0 || left-right > 1 || right-left > 1 {
		t.Errorf("footer not centred (left %d right %d): %q", left, right, foot)
	}

	// Resize redraws at the new size: footer on the new last row.
	h.scr.SetSize(40, 5)
	h.scr.PostEvent(tcell.NewEventResize(40, 5))
	h.eventually("redraw at 40x5", func(r []string) bool {
		return len(r) == 5 && len([]rune(r[4])) == 40 && strings.Contains(r[4], "refresh") && strings.Contains(r[0], "ctx")
	})
}

func keycap(k, desc string) string { return CAP_L.S() + KeyIcon(k) + CAP_R.S() + " " + desc }

func TestRunLiveWatchAndTheme(t *testing.T) {
	p := newFake()
	p.watch = []string{"/tmp/a"}
	h := start(t, p)
	if len(h.watched) != 1 || h.watched[0] != "/tmp/a" {
		t.Errorf("watched %v", h.watched)
	}
	h.eventually("live header", func(r []string) bool { return strings.HasSuffix(strings.TrimRight(r[0], " "), "· live") })

	h.changed <- struct{}{}
	h.waitRefresh()

	h.themed <- struct{}{}
	h.eventually("retheme", func([]string) bool {
		n := 0
		p.get(func(f *fakePane) { n = f.rethemes })
		return n == 1
	})
}

func TestRunNoDirWatchWhenNotLive(t *testing.T) {
	h := start(t, newFake())
	if h.changed != nil {
		t.Error("watchDirs called for a pane without Watch dirs")
	}
}

func TestRunUpgradeReexecs(t *testing.T) {
	p := newFake()
	h := start(t, p)
	h.upgraded <- struct{}{}
	err := h.waitDone()
	if !errors.Is(err, errStubExec) || !strings.Contains(err.Error(), "restart after upgrade failed") {
		t.Fatalf("err = %v", err)
	}
	args := <-h.reexecArgs
	if args[0] != "/bin/rexdash-test" || strings.Join(args[1:], " ") != strings.Join(os.Args, " ") {
		t.Errorf("reexec args %v", args)
	}
	if strings.Contains(h.osc.String(), "stopped") {
		t.Error("reported stopped before restarting")
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, d time.Duration) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// settle waits until FSEvents has gone quiet: it may still report the temp dir's own creation.
func settle(t *testing.T, ch <-chan struct{}) {
	time.Sleep(100 * time.Millisecond)
	for waitSignal(t, ch, 400*time.Millisecond) {
	}
}

func realTempDir(t *testing.T) string {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWatchDirsIgnoresNoise(t *testing.T) {
	t.Parallel() // real FSEvents: slow-ish, touches no package state
	dir := realTempDir(t)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	ch := make(chan struct{}, 1)
	watchDirs([]string{dir}, ch)
	settle(t, ch)

	os.WriteFile(filepath.Join(dir, "node_modules", "x.js"), []byte("x"), 0o644)
	if waitSignal(t, ch, 500*time.Millisecond) {
		t.Fatal("signalled for a change under node_modules")
	}
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)
	if !waitSignal(t, ch, 5*time.Second) {
		t.Fatal("no signal for a source change")
	}
}

func TestWatchExeSignalsOnReplace(t *testing.T) {
	t.Parallel()
	dir := realTempDir(t)
	exe := filepath.Join(dir, "rexdash")
	os.WriteFile(exe, []byte("v1"), 0o755)
	ch := make(chan struct{}, 1)
	watchExe(exe, ch)
	settle(t, ch)

	os.WriteFile(filepath.Join(dir, "other"), []byte("x"), 0o644)
	if waitSignal(t, ch, 500*time.Millisecond) {
		t.Fatal("signalled for an unrelated file")
	}
	// Installed the usual way: written next to it, then renamed over it.
	os.WriteFile(exe+".new", []byte("version 2"), 0o755)
	os.Rename(exe+".new", exe)
	if !waitSignal(t, ch, 5*time.Second) {
		t.Fatal("no signal after the binary was replaced")
	}

	watchExe(filepath.Join(dir, "missing"), ch) // nothing to watch: returns quietly
}
