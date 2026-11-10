// Package session keeps Rex panes running across a Rex restart.
//
// Rex brings back windows, layouts, pane labels and folders after a restart (with the same
// block IDs), but each pane comes back as a plain shell. This records what every pane runs and,
// once per new Rex server, starts it again:
//
//	rexdash panes → the same command and AWS profile
//	Claude Code   → `claude --resume <id>`, the same conversation
//	anything else → the command is typed in but not run
//
// Subcommands: agent (launchd runs it), save, relaunch, install, uninstall.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func home() string { return os.Getenv("HOME") }

func cacheDir() string     { return filepath.Join(home(), "Library", "Caches", "rexdash") }
func snapshotPath() string { return filepath.Join(cacheDir(), "session.json") }

// paneDir is where each running rexdash pane records how it was started, by pid.
func paneDir() string { return filepath.Join(cacheDir(), "panes") }

// RecordPane is called by every rexdash pane at start: its arguments and AWS settings.
func RecordPane() {
	_ = os.MkdirAll(paneDir(), 0o755)
	env := map[string]string{}
	for _, k := range []string{"AWS_PROFILE", "AWS_REGION"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	cwd, _ := os.Getwd()
	b, _ := json.Marshal(map[string]any{"argv": os.Args[1:], "env": env, "cwd": cwd})
	_ = os.WriteFile(filepath.Join(paneDir(), fmt.Sprintf("%d.json", os.Getpid())), b, 0o644)
}

// ForgetPane removes this pane's record on a normal exit.
func ForgetPane() { _ = os.Remove(filepath.Join(paneDir(), fmt.Sprintf("%d.json", os.Getpid()))) }

func rexBin() string {
	if b := os.Getenv("REX_BIN"); b != "" {
		return b
	}
	return "rex"
}

// rex runs a rex command without starting a server that isn't running.
// Outside access, swappable in tests: Rex's CLI, the process table and working directories.
var (
	rexCall   = rex
	listProcs = processes
	procCwds  = cwds
	logf      = func(format string, a ...any) { fmt.Fprintf(os.Stderr, format, a...) }
)

func rex(args ...string) (map[string]any, error) {
	out, err := exec.Command(rexBin(), append([]string{"--autostart=false", "--timeout", "5s"}, args...)...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	var v map[string]any
	_ = json.Unmarshal(out, &v)
	return v, nil
}

var safe = regexp.MustCompile(`^[A-Za-z0-9\-_./=:,@%+~]+$`)

// shQuote quotes for a POSIX shell when needed.
func shQuote(s string) string {
	if safe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type proc struct {
	pgid, tpgid int
	args        string
}

func processes() map[int]proc {
	out, _ := exec.Command("ps", "-axo", "pid=,pgid=,tpgid=,args=").Output()
	m := map[int]proc{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		pgid, e2 := strconv.Atoi(f[1])
		tpgid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		m[pid] = proc{pgid, tpgid, strings.Join(f[3:], " ")}
	}
	return m
}

func cwds(pids []int) map[int]string {
	m := map[int]string{}
	if len(pids) == 0 {
		return m
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	out, _ := exec.Command("lsof", "-a", "-p", strings.Join(list, ","), "-d", "cwd", "-Fpn").Output()
	pid := 0
	for _, l := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(l, "p"):
			pid, _ = strconv.Atoi(l[1:])
		case strings.HasPrefix(l, "n"):
			m[pid] = l[1:]
		}
	}
	return m
}

func isShell(args string) bool {
	f := strings.Fields(args)
	if len(f) == 0 {
		return false
	}
	a := strings.TrimLeft(filepath.Base(f[0]), "-")
	switch a {
	case "zsh", "bash", "fish", "sh", "login":
		return true
	}
	return false
}

func readJSON(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

// classify says what a pane is running, from its foreground process.
func classify(fg int, args, cwd string) map[string]any {
	if c := readJSON(filepath.Join(home(), ".claude", "sessions", fmt.Sprintf("%d.json", fg))); c != nil {
		if id := str(c["sessionId"]); id != "" {
			d := str(c["cwd"])
			if d == "" {
				d = cwd
			}
			return map[string]any{"kind": "claude", "session": id, "cwd": d}
		}
	}
	if p := readJSON(filepath.Join(paneDir(), fmt.Sprintf("%d.json", fg))); p != nil {
		d := str(p["cwd"])
		if d == "" {
			d = cwd
		}
		return map[string]any{"kind": "rexdash", "argv": p["argv"], "env": p["env"], "cwd": d}
	}
	if isShell(args) {
		return map[string]any{"kind": "shell", "cwd": cwd}
	}
	return map[string]any{"kind": "other", "command": args, "cwd": cwd}
}

// relaunchCommand is the shell command that starts a recorded pane again, and whether to press Enter.
func relaunchCommand(v map[string]any) (string, bool, bool) {
	cd := ""
	if c, ok := v["cwd"].(string); ok {
		cd = "cd " + shQuote(c) + " && "
	}
	switch v["kind"] {
	case "claude":
		id, ok := v["session"].(string)
		if !ok {
			return "", false, false
		}
		return cd + "claude --resume " + shQuote(id), true, true
	case "rexdash":
		argv, ok := v["argv"].([]any)
		if !ok {
			return "", false, false
		}
		env, _ := v["env"].(map[string]any)
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(cd)
		for _, k := range keys {
			if s, ok := env[k].(string); ok {
				b.WriteString(k + "=" + shQuote(s) + " ")
			}
		}
		b.WriteString("rexdash")
		for _, a := range argv {
			if s, ok := a.(string); ok {
				b.WriteString(" " + shQuote(s))
			}
		}
		return b.String(), true, true
	case "other":
		// Typed, not run: the user decides whether to start a dev server or editor again.
		cmd, ok := v["command"].(string)
		if !ok {
			return "", false, false
		}
		return cd + cmd, false, true
	}
	return "", false, false
}

// children maps block id → the pid Rex started in it; stable while the block lives,
// so Rex is asked only about blocks not seen before (or whose process is gone).
type children map[string]int

// keep drops panes that no longer exist, so a long-running agent doesn't accumulate them.
func (k children) keep(live map[string]bool) {
	for id := range k {
		if !live[id] {
			delete(k, id)
		}
	}
}

func num(v any) int { f, _ := v.(float64); return int(f) }

func childPid(p map[string]any) int {
	c, _ := p["child"].(map[string]any)
	return num(c["pid"])
}

// capture records every pane in every session: block id → what it runs.
func capture(kids children) (map[string]any, error) {
	status, err := rexCall("server", "status", "--json")
	if err != nil {
		return nil, err
	}
	procs := listProcs()
	blocks := map[string]any{}
	type fgOf struct {
		block string
		fg    int
	}
	var fgs []fgOf
	sessions, err := rexCall("session", "ls", "--json")
	if err != nil {
		return nil, err
	}
	list, _ := sessions["sessions"].([]any)
	for _, s := range list {
		sid := str(s.(map[string]any)["session_id"])
		bl, err := rexCall("block", "ls", "-s", sid, "--json")
		if err != nil {
			return nil, err
		}
		bs, _ := bl["blocks"].([]any)
		for _, x := range bs {
			b := x.(map[string]any)
			bid := str(b["block_id"])
			child, ok := kids[bid]
			if _, alive := procs[child]; !ok || !alive {
				p, err := rexCall("block", "call", "-s", sid, "-b", bid, "process")
				if err != nil {
					continue
				}
				if child = childPid(p); child == 0 {
					continue
				}
				kids[bid] = child
			}
			// The terminal's foreground process group, from the block's own process.
			fg := child
			if c, ok := procs[child]; ok && c.tpgid > 0 {
				fg = c.tpgid
			}
			fgs = append(fgs, fgOf{bid, fg})
			blocks[bid] = map[string]any{"session": sid, "label": b["label"]}
		}
	}
	live := make(map[string]bool, len(blocks))
	for id := range blocks {
		live[id] = true
	}
	kids.keep(live)
	pids := make([]int, len(fgs))
	for i, f := range fgs {
		pids[i] = f.fg
	}
	dirs := procCwds(pids)
	for _, f := range fgs {
		what := classify(f.fg, procs[f.fg].args, dirs[f.fg])
		m := blocks[f.block].(map[string]any)
		for k, v := range what {
			m[k] = v
		}
	}
	return map[string]any{"instance": status["instance_id"], "taken": time.Now().Unix(), "blocks": blocks}, nil
}

func save(snap map[string]any) error {
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := snapshotPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, snapshotPath())
}

func load() map[string]any { return readJSON(snapshotPath()) }

// relaunch starts each recorded program in its pane, if that pane is now an idle shell.
func relaunch(snap map[string]any) int {
	procs := listProcs()
	n := 0
	blocks, _ := snap["blocks"].(map[string]any)
	for bid, x := range blocks {
		v, _ := x.(map[string]any)
		cmd, enter, ok := relaunchCommand(v)
		if !ok {
			continue
		}
		sid := str(v["session"])
		p, err := rexCall("block", "call", "-s", sid, "-b", bid, "process")
		if err != nil {
			continue
		}
		child := childPid(p)
		if child == 0 {
			continue
		}
		fg := child
		if c, ok := procs[child]; ok && c.tpgid > 0 {
			fg = c.tpgid
		}
		f, ok := procs[fg]
		if !ok || !isShell(f.args) || f.pgid != fg {
			continue
		}
		if enter {
			cmd += "\n"
		}
		if _, err := rexCall("send", "-s", sid, "-b", bid, cmd); err == nil {
			n++
		}
	}
	return n
}

// tidy removes pane records whose process has exited.
func tidy() {
	procs := listProcs()
	entries, _ := os.ReadDir(paneDir())
	for _, e := range entries {
		pid, _ := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if _, ok := procs[pid]; !ok {
			_ = os.Remove(filepath.Join(paneDir(), e.Name()))
		}
	}
}

func exeStamp() string {
	exe, _ := os.Executable()
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d %d", fi.Size(), fi.ModTime().UnixNano())
}

// Waits around a relaunch, swappable in tests.
var (
	settle  = 4 * time.Second  // after a restart, for Rex's shells to start
	startup = 10 * time.Second // after relaunching, before recording again
	every   = 30 * time.Second
)

// step is one agent pass: if Rex restarted since the last record, relaunch from it first;
// then record what every pane runs and tidy stale pane records. It does nothing while Rex is down.
func step(kids children) children {
	status, err := rexCall("server", "status", "--json")
	if err != nil {
		return kids
	}
	instance := str(status["instance_id"])
	prev := load()
	recorded := ""
	if prev != nil {
		recorded = str(prev["instance"])
	}
	if recorded != "" && recorded != instance {
		// Rex restarted. Give its shells a moment to start, then bring the programs back.
		time.Sleep(settle)
		logf("rexdash session: relaunched %d panes after restart\n", relaunch(prev))
		// Let them start before recording, so the next record isn't a page of idle shells.
		time.Sleep(startup)
		kids = children{}
	}
	if snap, err := capture(kids); err == nil {
		_ = save(snap)
	} else {
		logf("rexdash session: %v\n", err)
	}
	tidy()
	return kids
}

// agent records every 30s; after a new Rex server starts, it relaunches from the last record first.
func agent() error {
	start := exeStamp()
	kids := children{}
	for {
		// A new build was installed: exit and let launchd start it.
		if exeStamp() != start {
			return nil
		}
		kids = step(kids)
		time.Sleep(every)
	}
}

const label = "com.rexdash.session"

func plistPath() string { return filepath.Join(home(), "Library", "LaunchAgents", label+".plist") }

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// launchd's PATH doesn't include Rex, so remember where it is.
	out, _ := exec.Command("/bin/zsh", "-lc", "command -v rex").Output()
	rexPath := strings.TrimSpace(string(out))
	if rexPath == "" {
		return errors.New("rex not found on PATH")
	}
	plist := plistXML(exe, rexPath, home())
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	unload()
	if out, err := exec.Command("launchctl", "bootstrap", domain(), plistPath()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl: %s", strings.TrimSpace(string(out)))
	}
	fmt.Printf("installed %s (log: ~/Library/Logs/rexdash-session.log)\n", plistPath())
	return nil
}

// plistXML is the launchd agent definition: run `exe session agent`, keep it alive, log to ~/Library/Logs.
func plistXML(exe, rexPath, homeDir string) string {
	esc := func(s string) string { return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s) }
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>session</string><string>agent</string></array>
  <key>EnvironmentVariables</key><dict><key>REX_BIN</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Background</string>
  <key>StandardErrorPath</key><string>%s/Library/Logs/rexdash-session.log</string>
</dict>
</plist>
`, label, esc(exe), esc(rexPath), esc(homeDir))
}

// unload stops a loaded agent and waits until launchd has let go of it: bootstrapping
// again before that fails with "Bootstrap failed: 5: Input/output error".
func unload() {
	_ = exec.Command("launchctl", "bootout", domain()+"/"+label).Run()
	for i := 0; i < 50 && exec.Command("launchctl", "print", domain()+"/"+label).Run() == nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

func uninstall() error {
	unload()
	_ = os.Remove(plistPath())
	fmt.Println("removed", label)
	return nil
}

// Main runs a `rexdash session` subcommand.
func Main(sub string) error {
	switch sub {
	case "agent":
		return agent()
	case "save":
		s, err := capture(children{})
		if err != nil {
			return err
		}
		if err := save(s); err != nil {
			return err
		}
		fmt.Printf("recorded %d panes to %s\n", len(s["blocks"].(map[string]any)), snapshotPath())
		return nil
	case "relaunch":
		s := load()
		if s == nil {
			return errors.New("nothing recorded yet")
		}
		fmt.Printf("relaunched %d panes\n", relaunch(s))
		return nil
	case "install":
		return install()
	case "uninstall":
		return uninstall()
	}
	return errors.New("usage: rexdash session <agent|save|relaunch|install|uninstall>")
}
