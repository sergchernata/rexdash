// rexdash: small, always-running, interactive display panes for Rex.
//
//	rexdash git      [-i SECS]                         changed files, commits, diffs
//	rexdash prs      [-i SECS] [-r OWNER/REPO]         open/merged PRs, CI checks
//	rexdash ci       [-i SECS] [-r OWNER/REPO]         GitHub Actions runs
//	rexdash eas      [-i SECS]                         Expo release lanes and workflow runs
//	rexdash builds   [-i SECS]                         Expo EAS builds
//	rexdash session  <agent|save|relaunch|install|uninstall>  bring panes back after a Rex restart
//
// AWS panes (pipeline, infra, fleet, billing, terraform, logs) run as rexdash-aws: this
// binary hands them off in place, so it never loads the AWS SDK itself.
//
// GitHub uses GH_TOKEN / GITHUB_TOKEN, else `gh auth token`.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"rexdash/internal/app"
	"rexdash/internal/ci"
	"rexdash/internal/cli"
	"rexdash/internal/eas"
	"rexdash/internal/git"
	"rexdash/internal/prs"
	"rexdash/internal/session"
	"rexdash/internal/ui"
)

const usage = "usage: rexdash <git|prs|ci|eas|builds|session CMD|pipeline NAME|infra CLUSTER [SERVICE] [DB|-]|fleet|billing|terraform [BUCKET [KEY]]|logs GROUP> [-i SECS] [-r OWNER/REPO] [--stream PREFIX] [--since 15m] [--hide TEXT]"

// awsBinary is rexdash-aws next to this binary, else on PATH.
func awsBinary() (string, error) {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "rexdash-aws")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("rexdash-aws"); err == nil {
		return p, nil
	}
	return "", errors.New("rexdash-aws not found next to rexdash or on PATH; run `make install`")
}

func pane(a cli.Args) (ui.Pane, error) {
	every := func(d time.Duration) time.Duration { return app.Every(a, d) }
	switch a.Cmd {
	case "git":
		return git.New(every(30 * time.Second)), nil
	case "prs":
		return prs.New(a.Repo, every(60*time.Second))
	case "ci":
		return ci.New(a.Repo, every(60*time.Second))
	case "eas":
		return eas.New(eas.Runs, every(60*time.Second))
	case "builds":
		return eas.New(eas.Builds, every(60*time.Second))
	}
	return nil, errors.New(usage)
}

func main() {
	app.TuneMemory()
	a := cli.Parse(os.Args[1:])
	switch {
	case a.Cmd == "session":
		app.Fail(session.Main(app.Pos(a, 0, "")))
	case cli.IsAWS(a.Cmd):
		bin, err := awsBinary()
		app.Fail(err)
		app.Fail(ui.Reexec(bin, append([]string{bin}, os.Args[1:]...)))
	default:
		app.Show(pane(a))
	}
}
