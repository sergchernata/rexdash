package main

// End-to-end regression tests: the real binary in a real pseudo-terminal.

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"rexdash/internal/app"
)

// openPty returns a pseudo-terminal pair sized 100x30.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		t.Fatal(err)
	}
	name := make([]byte, 128)
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		t.Fatal(e)
	}
	s, err := os.OpenFile(string(name[:bytes.IndexByte(name, 0)]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 100}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(); s.Close() })
	return m, s
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// traceLines waits until the trace log has at least n lines containing msg.
func traceLines(t *testing.T, path, msg string, n int, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		data, _ := os.ReadFile(path)
		var hits []string
		for _, l := range strings.Split(string(data), "\n") {
			if strings.Contains(l, msg) {
				hits = append(hits, l)
			}
		}
		if len(hits) >= n {
			return hits
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited %v for %d× %q in trace:\n%s", within, n, msg, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// cpuSeconds is a process's total CPU time so far.
func cpuSeconds(t *testing.T, pid int) float64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("pid %d gone: %v", pid, err)
	}
	// "M:SS.ss" or "H:MM:SS.ss"
	var secs float64
	for _, part := range strings.Split(strings.TrimSpace(string(out)), ":") {
		v, _ := strconv.ParseFloat(part, 64)
		secs = secs*60 + v
	}
	return secs
}

// A pane whose binary is replaced restarts into the new build in the same process (same
// pane), draws again, and then sits idle. Regression: Go's exec waited forever for
// preemption signals that FSEvents' signal-blocking threads never receive, so every pane
// went blank and spun at ~35% CPU after an upgrade.
func TestUpgradeRestartsInPlace(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary and runs it in a pty")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "rexdash")
	run(t, ".", "go", "build", "-o", bin, ".")

	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "git", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "first")

	master, slave := openPty(t)
	go io.Copy(io.Discard, master) // keep the pane's output flowing
	trace := filepath.Join(dir, "trace.log")
	cmd := exec.Command(bin, "git")
	cmd.Dir = repo
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// Own home: no theme prefs, no writes to the real pane records; no Rex to talk to.
	cmd.Env = append(os.Environ(), "HOME="+dir, "REXDASH_TRACE="+trace, "REX_SESSION=", "REX_BLOCK=")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	traceLines(t, trace, "init done", 1, 10*time.Second)

	// Keep file events coming, as a busy repo (or ~/Library/Preferences) does: the hang
	// needs FSEvents callbacks running when the restart happens.
	stop := make(chan struct{})
	var once sync.Once
	quiet := func() { once.Do(func() { close(stop) }) }
	defer quiet()
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				os.WriteFile(filepath.Join(repo, "churn.txt"), []byte(strconv.Itoa(i)), 0o644)
			}
		}
	}()
	time.Sleep(time.Second)

	// Install a new build the way an install does: write beside it, then rename over it.
	data, _ := os.ReadFile(bin)
	os.WriteFile(bin+".new", data, 0o755)
	time.Sleep(1100 * time.Millisecond) // a different mtime
	os.Chtimes(bin+".new", time.Now(), time.Now())
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}

	starts := traceLines(t, trace, "init done", 2, 10*time.Second)
	pid := cmd.Process.Pid
	for _, l := range starts {
		if !strings.HasPrefix(l, strconv.Itoa(pid)+" ") {
			t.Fatalf("restart ran as another process, not in place: %q (pane pid %d)", l, pid)
		}
	}

	// Idle after the restart: a spin (or GC thrash) shows up as CPU time.
	quiet()
	time.Sleep(2 * time.Second)
	before := cpuSeconds(t, pid)
	time.Sleep(3 * time.Second)
	if used := cpuSeconds(t, pid) - before; used > 0.15 {
		t.Fatalf("restarted pane used %.2fs CPU in 3s idle", used)
	}
}

// Memory tuning must not cap the heap below what a pane needs. Regression: a 24 MB limit
// sat under the panes' live heap, so the collector ran nonstop and panes used ~35% CPU each.
func TestMemoryTuningHasNoTightLimit(t *testing.T) {
	prevGC, prevLimit, prevProcs := debug.SetGCPercent(100), debug.SetMemoryLimit(-1), runtime.GOMAXPROCS(0)
	defer func() { debug.SetGCPercent(prevGC); debug.SetMemoryLimit(prevLimit); runtime.GOMAXPROCS(prevProcs) }()
	app.TuneMemory()
	if limit := debug.SetMemoryLimit(-1); limit < 256<<20 {
		t.Fatalf("memory limit %d MB is low enough to thrash the collector", limit>>20)
	}
	if gc := debug.SetGCPercent(-1); gc <= 0 {
		t.Fatalf("garbage collection disabled (GOGC %d)", gc)
	}
}

// Only the AWS panes load the AWS SDK: rexdash (git, prs, ci, eas, builds, session) must not
// link it, since every SDK package allocates its API model at startup (~4 MB per pane).
func TestNoAWSInCoreBinary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/aws/") {
			t.Fatalf("rexdash links %s; AWS panes belong in rexdash-aws", dep)
		}
	}
}

// `rexdash logs …` (any AWS pane) hands off to rexdash-aws in the same process, so existing
// commands, session records and Rex's view of the pane don't change.
func TestAWSPaneHandsOffInPlace(t *testing.T) {
	if testing.Short() {
		t.Skip("builds both binaries and runs them in a pty")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	run(t, ".", "go", "build", "-o", filepath.Join(bin, "rexdash"), ".")
	run(t, ".", "go", "build", "-o", filepath.Join(bin, "rexdash-aws"), "../rexdash-aws")

	master, slave := openPty(t)
	go io.Copy(io.Discard, master)
	trace := filepath.Join(dir, "trace.log")
	cmd := exec.Command(filepath.Join(bin, "rexdash"), "logs", "some-group")
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// Dummy credentials and a dead local endpoint: the pane starts, nothing reaches AWS.
	cmd.Env = append(os.Environ(), "HOME="+dir, "REXDASH_TRACE="+trace, "REX_SESSION=", "REX_BLOCK=",
		"AWS_PROFILE=", "AWS_ACCESS_KEY_ID=x", "AWS_SECRET_ACCESS_KEY=y", "AWS_REGION=us-east-1",
		"AWS_ENDPOINT_URL=http://127.0.0.1:9")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	line := traceLines(t, trace, "init done", 1, 10*time.Second)[0]
	pid := cmd.Process.Pid
	if !strings.HasPrefix(line, strconv.Itoa(pid)+" ") {
		t.Fatalf("logs pane ran as another process: %q (pane pid %d)", line, pid)
	}
	out, _ := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if got := filepath.Base(strings.TrimSpace(string(out))); got != "rexdash-aws" {
		t.Fatalf("pane runs %q, want rexdash-aws", got)
	}
}
