package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRex stands in for Rex's CLI, the process table and lsof: one session with blocks whose
// programs are described by pane records and Claude Code session files under a temp HOME.
type fakeRex struct {
	instance string
	down     bool
	blocks   []fakeBlock
	procs    map[int]proc
	cwd      map[int]string
	calls    []string // every rex invocation, joined by spaces
	sent     map[string]string
}

type fakeBlock struct {
	id, label string
	child     int
}

func (f *fakeRex) rex(args ...string) (map[string]any, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.down {
		return nil, errors.New("rex server not running")
	}
	j := func(v any) map[string]any {
		b, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	switch {
	case args[0] == "server":
		return j(map[string]any{"instance_id": f.instance}), nil
	case args[0] == "session":
		return j(map[string]any{"sessions": []any{map[string]any{"session_id": "S1", "label": "work"}}}), nil
	case args[0] == "block" && args[1] == "ls":
		var bs []any
		for _, b := range f.blocks {
			bs = append(bs, map[string]any{"block_id": b.id, "label": b.label})
		}
		return j(map[string]any{"blocks": bs}), nil
	case args[0] == "block" && args[1] == "call":
		for _, b := range f.blocks {
			if b.id == args[5] {
				return j(map[string]any{"child": map[string]any{"pid": b.child}}), nil
			}
		}
		return nil, errors.New("no such block")
	case args[0] == "send":
		f.sent[args[4]] = args[5]
		return map[string]any{}, nil
	}
	return nil, errors.New("unexpected rex call: " + strings.Join(args, " "))
}

func (f *fakeRex) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// world sets HOME to a temp dir and installs a fake Rex with four panes:
//
//	b-claude  Claude Code (session file for its foreground pid)
//	b-dash    a rexdash pane (pane record for its foreground pid)
//	b-shell   an idle shell
//	b-dev     a dev server started from a shell
func world(t *testing.T) *fakeRex {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &fakeRex{
		instance: "inst-1",
		blocks: []fakeBlock{
			{"b-claude", "shell", 100}, {"b-dash", "Logs", 200}, {"b-shell", "shell", 300}, {"b-dev", "web", 400},
		},
		procs: map[int]proc{
			100: {100, 101, "login -pflq me /bin/zsh"}, 101: {101, 101, "2.1.322"},
			200: {200, 201, "login -pflq me /bin/zsh"}, 201: {201, 201, "rexdash logs app --stream web/"},
			300: {300, 301, "login -pflq me /bin/zsh"}, 301: {301, 301, "-zsh"},
			400: {400, 401, "login -pflq me /bin/zsh"}, 401: {401, 401, "bun run dev"},
		},
		cwd:  map[int]string{101: "/w/app", 201: "/w/infra", 301: "/w/sh", 401: "/w/web"},
		sent: map[string]string{},
	}
	write := func(path string, v any) {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude", "sessions", "101.json"), map[string]any{"pid": 101, "sessionId": "sess-1", "cwd": "/w/app"})
	write(filepath.Join(paneDir(), "201.json"), map[string]any{"argv": []string{"logs", "app", "--stream", "web/"}, "env": map[string]string{"AWS_PROFILE": "prod"}, "cwd": "/w/infra"})

	oldRex, oldProcs, oldCwds, oldLog := rexCall, listProcs, procCwds, logf
	oldSettle, oldStartup := settle, startup
	rexCall = f.rex
	listProcs = func() map[int]proc { return f.procs }
	procCwds = func(pids []int) map[int]string {
		m := map[int]string{}
		for _, p := range pids {
			if d, ok := f.cwd[p]; ok {
				m[p] = d
			}
		}
		return m
	}
	logf = func(string, ...any) {}
	settle, startup = 0, 0
	t.Cleanup(func() {
		rexCall, listProcs, procCwds, logf = oldRex, oldProcs, oldCwds, oldLog
		settle, startup = oldSettle, oldStartup
	})
	return f
}

// idle turns a pane back into a plain shell, as Rex does after a restart.
func (f *fakeRex) idle(child int) {
	shell := child + 50
	f.procs[child] = proc{child, shell, "login -pflq me /bin/zsh"}
	f.procs[shell] = proc{shell, shell, "-zsh"}
}
