package ui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/fsnotify/fsevents"
	"github.com/gdamore/tcell/v2"
)

// Status is what a pane reports to Rex (OSC 7501).
type Status struct {
	State string // idle | working | done | blocked | error
	Msg   string
}

// Hint is a footer keycap: key and what it does.
type Hint struct{ Key, Desc string }

// Pane is one rexdash display. Embed Base for the optional methods.
type Pane interface {
	// Name is the terminal title and the label in Rex's pane list.
	Name() string
	// Title is the context line in the header.
	Title() Line
	Hints() []Hint
	Every() time.Duration
	Status() Status
	Refresh() error
	Key(ev *tcell.EventKey) error
	Draw(b *Buffer, area Rect)
	// Typing is true while the pane reads text (a filter box), so global keys pass through.
	Typing() bool
	// Retheme is called after the theme changes, for panes that cache coloured text.
	Retheme()
	// Watch lists directories whose changes trigger a refresh; Every becomes the fallback.
	Watch() []string
}

// Base gives a pane the default optional methods.
type Base struct{}

func (Base) Typing() bool    { return false }
func (Base) Retheme()        {}
func (Base) Watch() []string { return nil }

var out io.Writer = os.Stdout

func report(name string, st Status) {
	fmt.Fprintf(out, "\x1b]2;%s\x07\x1b]7501;state=%s:app=rexdash:title=%s:msg=%s\x1b\\", name, st.State, B64(name), B64(st.Msg))
}

// Run shows a pane until Ctrl-C. When a new build replaces this binary, it restarts into it.
var traceOn = os.Getenv("REXDASH_TRACE") != ""

func trace(msg string) {
	if f := os.Getenv("REXDASH_TRACE"); f != "" {
		if fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(fh, "%d %s %s\n", os.Getpid(), time.Now().Format("15:04:05.000"), msg)
			fh.Close()
		}
	}
}

func Run(p Pane) error {
	trace("start")
	scr, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := scr.Init(); err != nil {
		return err
	}
	trace("init done")
	if tty, ok := scr.Tty(); ok {
		out = tty
	}
	events := make(chan tcell.Event, 16)
	go func() {
		for {
			ev := scr.PollEvent()
			if ev == nil {
				return
			}
			events <- ev
		}
	}()
	report(p.Name(), Status{"working", "loading"})

	changed := make(chan struct{}, 1)
	dirs := p.Watch()
	live := len(dirs) > 0
	if live {
		watchDirs(dirs, changed)
	}
	themed := make(chan struct{}, 1)
	WatchTheme(themed)
	upgraded := make(chan struct{}, 1)
	exe, _ := os.Executable()
	if exe != "" {
		watchExe(exe, upgraded)
	}

	timer := time.NewTimer(0)
	var errMsg, updated, lastReport string
	updated = "loading…"
	upgrade := false
	lastFree := time.Now()
	redraw := func() {
		w, h := scr.Size()
		b := NewBuffer(w, h)
		frame(b, p, errMsg, updated, live)
		scr.Clear()
		b.Flush(scr)
		scr.Show()
	}
	refreshNow := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(0)
	}

loop:
	for {
		redraw()
		select {
		case <-timer.C:
			errMsg = ""
			if e := p.Refresh(); e != nil {
				errMsg = e.Error()
			}
			updated = time.Now().Format("15:04:05")
			st := p.Status()
			if errMsg != "" {
				st = Status{"error", errMsg}
			}
			if key := p.Name() + st.State + st.Msg; key != lastReport {
				report(p.Name(), st)
				lastReport = key
			}
			timer.Reset(p.Every())
			if traceOn {
				// Live heap after a collection, and goroutines: a leak climbs refresh after refresh.
				runtime.GC()
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				trace(fmt.Sprintf("refreshed heap=%d goroutines=%d", ms.HeapAlloc, runtime.NumGoroutine()))
			}
			// Hand freed memory back to macOS now and then, so idle panes stay small.
			if time.Since(lastFree) > 30*time.Second {
				debug.FreeOSMemory()
				lastFree = time.Now()
			}
		case <-changed:
			refreshNow()
		case <-themed:
			p.Retheme()
		case <-upgraded:
			upgrade = true
			break loop
		case ev := <-events:
			switch ev := ev.(type) {
			case *tcell.EventResize:
				scr.Sync()
			case *tcell.EventKey:
				if ev.Key() == tcell.KeyCtrlC {
					break loop
				}
				if !p.Typing() {
					if IsRune(ev, 'r') {
						refreshNow()
						continue
					}
					if IsRune(ev, 'z') {
						RexZoom(nil)
						continue
					}
				}
				if e := p.Key(ev); e != nil {
					errMsg = e.Error()
				}
			}
		}
	}
	trace("fini")
	scr.Fini()
	trace("fini done")
	out = os.Stdout
	if upgrade && exe != "" {
		trace("exec")
		err := Reexec(exe, os.Args)
		trace("exec returned: " + err.Error())
		return fmt.Errorf("restart after upgrade failed: %w", err)
	}
	report(p.Name(), Status{"idle", "stopped"})
	return nil
}

func frame(b *Buffer, p Pane, errMsg, updated string, live bool) {
	rows := Vertical(b.Area(), 0, Len(1), Min(1), Len(1))
	interval := "· " + EveryLabel(p.Every()) + " "
	if live {
		interval = "· live "
	}
	right := L(C(REFRESH.S()+" ", Border()), C(updated+" ", Muted()), C(interval, Border()))
	head := Horizontal(rows[0], 0, Min(1), Len(right.Width()))
	// Rex's pane title already names the pane, so the header carries only its context.
	title := p.Title()
	title.Spans = append([]Span{T(" ")}, title.Spans...)
	b.SetLine(head[0], title)
	b.SetLine(head[1], right)
	p.Draw(b, rows[1])
	var foot Line
	if errMsg != "" {
		foot = LT(" "+BUG.S()+" "+errMsg, Fg(Red()))
	} else {
		hints := append(p.Hints(), Hint{"r", "refresh"}, Hint{"z", "zoom"})
		for i, h := range hints {
			cp := Surface()
			if i > 0 {
				foot.Spans = append(foot.Spans, T("   "))
			}
			foot.Spans = append(foot.Spans,
				C(CAP_L.S(), cp),
				S(KeyIcon(h.Key), Style{Fg: FgC(), Bg: cp, Bold: true}),
				C(CAP_R.S(), cp),
				C(" "+h.Desc, Muted()))
		}
		foot.Align = Center
	}
	b.SetLine(rows[2], foot)
}

// watchDirs signals ch once per burst of non-noisy changes under dirs.
func watchDirs(dirs []string, ch chan<- struct{}) {
	es := &fsevents.EventStream{Paths: dirs, Latency: 250 * time.Millisecond, Flags: fsevents.FileEvents}
	if es.Start() != nil {
		return
	}
	go func() {
		for evs := range es.Events {
			for _, e := range evs {
				if !Noisy(e.Path) {
					select {
					case ch <- struct{}{}:
					default:
					}
					break
				}
			}
		}
	}()
}

type fileStamp struct {
	size int64
	mod  time.Time
}

func stamp(path string) (fileStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{fi.Size(), fi.ModTime()}, true
}

// watchExe signals once the executable has been replaced by a different, complete file.
func watchExe(exe string, ch chan<- struct{}) {
	start, ok := stamp(exe)
	if !ok {
		return
	}
	name := filepath.Base(exe)
	es := &fsevents.EventStream{Paths: []string{filepath.Dir(exe)}, Latency: 300 * time.Millisecond, Flags: fsevents.FileEvents}
	if es.Start() != nil {
		return
	}
	go func() {
		for evs := range es.Events {
			hit := false
			for _, e := range evs {
				if filepath.Base(e.Path) == name || strings.HasPrefix(filepath.Base(e.Path), name+".") {
					hit = true
				}
			}
			if !hit {
				continue
			}
			// Let the install finish, then only restart into a complete, different file.
			time.Sleep(800 * time.Millisecond)
			if now, ok := stamp(exe); ok && now != start && now.size > 0 {
				ch <- struct{}{}
				es.Stop()
				return
			}
		}
	}()
}

func getenv(k string) string { return os.Getenv(k) }
