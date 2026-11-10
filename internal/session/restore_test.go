package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func blockOf(t *testing.T, snap map[string]any, id string) map[string]any {
	t.Helper()
	b, ok := snap["blocks"].(map[string]any)[id].(map[string]any)
	if !ok {
		t.Fatalf("block %s not recorded: %v", id, snap["blocks"])
	}
	return b
}

func TestCaptureRecordsWhatEachPaneRuns(t *testing.T) {
	f := world(t)
	snap, err := capture(children{})
	if err != nil {
		t.Fatal(err)
	}
	if snap["instance"] != "inst-1" {
		t.Errorf("instance %v", snap["instance"])
	}
	c := blockOf(t, snap, "b-claude")
	if c["kind"] != "claude" || c["session"] != "sess-1" || c["cwd"] != "/w/app" || c["label"] != "shell" {
		t.Errorf("claude pane recorded as %v", c)
	}
	d := blockOf(t, snap, "b-dash")
	if d["kind"] != "rexdash" || d["cwd"] != "/w/infra" || d["label"] != "Logs" {
		t.Errorf("rexdash pane recorded as %v", d)
	}
	if argv, _ := json.Marshal(d["argv"]); string(argv) != `["logs","app","--stream","web/"]` {
		t.Errorf("rexdash argv %s", argv)
	}
	if s := blockOf(t, snap, "b-shell"); s["kind"] != "shell" || s["cwd"] != "/w/sh" {
		t.Errorf("idle shell recorded as %v", s)
	}
	if o := blockOf(t, snap, "b-dev"); o["kind"] != "other" || o["command"] != "bun run dev" || o["cwd"] != "/w/web" {
		t.Errorf("dev server recorded as %v", o)
	}
	if n := f.count("block call"); n != 4 {
		t.Errorf("asked Rex about %d panes, want 4", n)
	}
}

// The agent asks Rex for a pane's process once and reuses it while that process lives;
// it asks again when the pane's program is replaced and forgets panes that close.
func TestCaptureReusesKnownPanesAndForgetsClosedOnes(t *testing.T) {
	f := world(t)
	kids := children{}
	if _, err := capture(kids); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if _, err := capture(kids); err != nil {
		t.Fatal(err)
	}
	if n := f.count("block call"); n != 0 {
		t.Errorf("second pass asked Rex about %d panes, want 0", n)
	}

	// b-dev's terminal restarted with a new process; b-shell was closed.
	f.blocks[3].child = 500
	f.procs[500] = proc{500, 501, "login -pflq me /bin/zsh"}
	f.procs[501] = proc{501, 501, "-zsh"}
	delete(f.procs, 400)
	f.blocks = append(f.blocks[:2], f.blocks[3])
	f.calls = nil
	snap, err := capture(kids)
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count("block call"); n != 1 {
		t.Errorf("asked Rex about %d panes, want only the replaced one", n)
	}
	if _, ok := kids["b-shell"]; ok {
		t.Error("closed pane still remembered")
	}
	if blockOf(t, snap, "b-dev")["kind"] != "shell" {
		t.Error("replaced pane not re-read")
	}
}

func TestCaptureFailsWhenRexIsDown(t *testing.T) {
	f := world(t)
	f.down = true
	if _, err := capture(children{}); err == nil {
		t.Fatal("capture succeeded with Rex down")
	}
}

// After a restart every pane is an idle shell: Claude and rexdash panes are started again,
// other commands are typed but not run, plain shells are left alone.
func TestRelaunchStartsRecordedProgramsInIdlePanes(t *testing.T) {
	f := world(t)
	snap, err := capture(children{})
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range []int{100, 200, 300, 400} {
		f.idle(child)
	}
	if n := relaunch(snap); n != 3 {
		t.Errorf("relaunched %d panes, want 3", n)
	}
	want := map[string]string{
		"b-claude": "cd /w/app && claude --resume sess-1\n",
		"b-dash":   "cd /w/infra && AWS_PROFILE=prod rexdash logs app --stream web/\n",
		"b-dev":    "cd /w/web && bun run dev",
	}
	for id, w := range want {
		if f.sent[id] != w {
			t.Errorf("%s: sent %q, want %q", id, f.sent[id], w)
		}
	}
	if _, ok := f.sent["b-shell"]; ok {
		t.Error("typed into a plain shell")
	}
}

// A pane that is already running something is never typed into.
func TestRelaunchSkipsBusyPanes(t *testing.T) {
	f := world(t)
	snap, err := capture(children{})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(100) // only the Claude pane came back as a shell
	if n := relaunch(snap); n != 1 {
		t.Errorf("relaunched %d panes, want 1", n)
	}
	if _, ok := f.sent["b-dash"]; ok {
		t.Error("typed into a pane whose program is still running")
	}
}

// One agent pass: a new Rex server means relaunch from the previous record, then record anew.
func TestStepRelaunchesOnceAfterRestart(t *testing.T) {
	f := world(t)
	step(children{}) // first record, server inst-1
	prev := load()
	if prev == nil || prev["instance"] != "inst-1" {
		t.Fatalf("first record: %v", prev)
	}

	// Rex restarts: new server, every pane a shell.
	f.instance = "inst-2"
	for _, child := range []int{100, 200, 300, 400} {
		f.idle(child)
	}
	kids := step(children{})
	if len(f.sent) != 3 {
		t.Fatalf("after restart sent %d commands, want 3: %v", len(f.sent), f.sent)
	}
	if rec := load(); rec["instance"] != "inst-2" {
		t.Errorf("record after restart has instance %v", rec["instance"])
	}

	// Same server on the next pass: nothing relaunched again.
	f.sent = map[string]string{}
	step(kids)
	if len(f.sent) != 0 {
		t.Errorf("relaunched again without a restart: %v", f.sent)
	}
}

// While Rex is down the agent leaves the last record alone, so a restart can still use it.
func TestStepKeepsRecordWhileRexIsDown(t *testing.T) {
	f := world(t)
	step(children{})
	before, _ := os.ReadFile(snapshotPath())
	f.down = true
	step(children{})
	after, _ := os.ReadFile(snapshotPath())
	if string(before) != string(after) {
		t.Error("record changed while Rex was down")
	}
}

func TestTidyRemovesRecordsOfExitedPanes(t *testing.T) {
	world(t)
	dead := filepath.Join(paneDir(), "999.json")
	if err := os.WriteFile(dead, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Error("record of an exited pane kept")
	}
	if _, err := os.Stat(filepath.Join(paneDir(), "201.json")); err != nil {
		t.Error("record of a running pane removed")
	}
}

func TestRecordAndForgetPane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AWS_PROFILE", "prod")
	t.Setenv("AWS_REGION", "")
	RecordPane()
	path := filepath.Join(paneDir(), itoa(os.Getpid())+".json")
	var rec map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	env, _ := rec["env"].(map[string]any)
	if env["AWS_PROFILE"] != "prod" || len(env) != 1 {
		t.Errorf("env recorded as %v", env)
	}
	if cwd, _ := os.Getwd(); rec["cwd"] != cwd {
		t.Errorf("cwd recorded as %v", rec["cwd"])
	}
	ForgetPane()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("record kept after ForgetPane")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := save(map[string]any{"instance": "x", "blocks": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshotPath() + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
	if load()["instance"] != "x" {
		t.Error("saved record not loaded back")
	}
}

func TestPlistRunsAgentAndEscapes(t *testing.T) {
	x := plistXML("/Users/me/bin/rexdash", "/Apps/R&D <beta>/rex", "/Users/me")
	for _, want := range []string{
		"<string>/Users/me/bin/rexdash</string><string>session</string><string>agent</string>",
		"<key>REX_BIN</key><string>/Apps/R&amp;D &lt;beta&gt;/rex</string>",
		"<key>KeepAlive</key><true/>",
		"/Users/me/Library/Logs/rexdash-session.log",
	} {
		if !strings.Contains(x, want) {
			t.Errorf("plist missing %q:\n%s", want, x)
		}
	}
}

func TestMainUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Main("bogus"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("unknown subcommand: %v", err)
	}
	if err := Main("relaunch"); err == nil || !strings.Contains(err.Error(), "nothing recorded") {
		t.Errorf("relaunch with no record: %v", err)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The real process table and lsof readers, checked against this test process.
func TestProcessAndCwdReaders(t *testing.T) {
	me := os.Getpid()
	p, ok := processes()[me]
	if !ok {
		t.Fatal("own process missing from the process table")
	}
	if p.pgid <= 0 || !strings.Contains(p.args, "session.test") {
		t.Errorf("own process read as %+v", p)
	}
	wd, _ := os.Getwd()
	real, _ := filepath.EvalSymlinks(wd)
	if got := cwds([]int{me})[me]; got != wd && got != real {
		t.Errorf("cwd read as %q, want %q", got, wd)
	}
	if len(cwds(nil)) != 0 {
		t.Error("cwds of no pids not empty")
	}
}
