// Package app is the startup shared by rexdash and rexdash-aws.
package app

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"rexdash/internal/cli"
	"rexdash/internal/session"
	"rexdash/internal/ui"
)

// TuneMemory keeps idle panes small: collect garbage sooner and run fewer threads (and
// per-thread caches). No hard memory limit: one below the live heap makes the collector
// run nonstop.
func TuneMemory() {
	debug.SetGCPercent(25)
	runtime.GOMAXPROCS(2)
}

// Every is -i SECS, else def.
func Every(a cli.Args, def time.Duration) time.Duration {
	if a.Interval > 0 {
		return time.Duration(a.Interval) * time.Second
	}
	return def
}

// Pos is the i-th positional argument, else def.
func Pos(a cli.Args, i int, def string) string {
	if i < len(a.Pos) {
		return a.Pos[i]
	}
	return def
}

// Show runs a pane (or reports err) and exits non-zero on failure. It records how the
// pane was started so it can be relaunched after Rex restarts.
func Show(p ui.Pane, err error) {
	if err == nil {
		session.RecordPane()
		err = ui.Run(p)
		session.ForgetPane()
	}
	Fail(err)
}

// Fail prints err and exits 1; does nothing for nil.
func Fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "rexdash:", err)
		os.Exit(1)
	}
}
