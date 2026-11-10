// Package ci is the CI pane: GitHub Actions runs for the repo in the current directory,
// with the latest result per workflow on top. Uses the same token as the PRs pane.
package ci

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/tidwall/gjson"

	"rexdash/internal/ghauth"
	"rexdash/internal/netx"
	"rexdash/internal/ui"
)

var gear = ui.Ic{Glyph: "", Plain: "⚙"}

// Run is one workflow run.
type Run struct {
	Workflow, Title, Branch, Event, State string
	Started, Secs                         int64
	URL                                   string
}

// runState folds GitHub's status + conclusion into one state name the shared tones understand.
func runState(status string, conclusion *string) string {
	if status == "completed" {
		if conclusion != nil {
			return strings.ToUpper(*conclusion)
		}
		return "COMPLETED"
	}
	return strings.ToUpper(status)
}

// parseRuns reads GET /repos/{repo}/actions/runs; a running job's duration counts up to now.
func parseRuns(body []byte, now int64) []Run {
	var out []Run
	for _, r := range gjson.GetBytes(body, "workflow_runs").Array() {
		started := ui.ParseTime(r.Get("run_started_at").String())
		var concl *string
		if c := r.Get("conclusion"); c.Type == gjson.String {
			s := c.String()
			concl = &s
		}
		state := runState(r.Get("status").String(), concl)
		end := ui.ParseTime(r.Get("updated_at").String())
		if ui.ToneOf(state) == ui.ToneBusy {
			end = now
		}
		out = append(out, Run{
			Workflow: r.Get("name").String(),
			Title:    r.Get("display_title").String(),
			Branch:   r.Get("head_branch").String(),
			Event:    strings.ReplaceAll(r.Get("event").String(), "_", " "),
			State:    state,
			Started:  started,
			Secs:     max(end-started, 0),
			URL:      r.Get("html_url").String(),
		})
	}
	return out
}

type pane struct {
	ui.Base
	every  time.Duration
	repo   string
	token  string
	runs   []Run
	st     ui.TableState
	loaded bool
}

// New makes the CI pane for OWNER/REPO ("" = the origin of the current directory).
func New(repo string, every time.Duration) (ui.Pane, error) {
	if repo == "" {
		r, err := ghauth.RepoSlug()
		if err != nil {
			return nil, err
		}
		repo = r
	}
	tok, err := ghauth.Token()
	if err != nil {
		return nil, err
	}
	return &pane{every: every, repo: repo, token: tok, st: ui.NewTableState()}, nil
}

func (p *pane) Name() string         { return "CI" }
func (p *pane) Title() ui.Line       { return ui.LT(p.repo+" · actions", ui.Fg(ui.Muted())) }
func (p *pane) Every() time.Duration { return p.every }
func (p *pane) Hints() []ui.Hint {
	return []ui.Hint{{Key: "↑↓", Desc: "runs"}, {Key: "⏎", Desc: "open"}}
}

func (p *pane) Status() ui.Status {
	if !p.loaded {
		return ui.Status{State: "working", Msg: "loading"}
	}
	if len(p.runs) == 0 {
		return ui.Status{State: "idle", Msg: "no runs"}
	}
	r := p.runs[0]
	state := "idle"
	switch ui.ToneOf(r.State) {
	case ui.ToneBad:
		state = "error"
	case ui.ToneBusy:
		state = "working"
	}
	return ui.Status{State: state, Msg: fmt.Sprintf("%s · %s · %s", r.Workflow, r.Branch, strings.ToLower(r.State))}
}

func (p *pane) Refresh() error {
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+p.repo+"/actions/runs?per_page=30", nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rexdash")
	resp, err := netx.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("github: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	p.runs = parseRuns(body, time.Now().Unix())
	p.loaded = true
	ui.Clamp(&p.st.Sel, len(p.runs))
	return nil
}

func (p *pane) Key(ev *tcell.EventKey) error {
	if ui.IsEnter(ev) || ui.IsRune(ev, 'o') {
		if p.st.Sel >= 0 && p.st.Sel < len(p.runs) {
			ui.OpenURL(p.runs[p.st.Sel].URL)
		}
		return nil
	}
	ui.Nav(&p.st.Sel, len(p.runs), ev, 10)
	return nil
}

func (p *pane) Draw(b *ui.Buffer, area ui.Rect) {
	if !p.loaded {
		ui.EmptyState(b, area, gear.S(), ui.Muted(), "Loading workflow runs…", p.repo)
		return
	}
	if len(p.runs) == 0 {
		ui.EmptyState(b, area, gear.S(), ui.Muted(), "No GitHub Actions runs", p.repo)
		return
	}
	// Latest result per workflow, as chips.
	seen := map[string]bool{}
	chips := ui.L(ui.T(" "))
	for _, r := range p.runs {
		if seen[r.Workflow] {
			continue
		}
		seen[r.Workflow] = true
		chips.Spans = append(chips.Spans, ui.StatusIcon(r.State), ui.C(" "+r.Workflow+"   ", ui.StatusColor(r.State)))
	}
	parts := ui.Vertical(area, 0, ui.Len(1), ui.Min(3))
	b.SetLine(parts[0], chips)

	muted := ui.Fg(ui.Muted())
	rows := make([]ui.Row, len(p.runs))
	for i, r := range p.runs {
		rows[i] = ui.R(
			ui.L(ui.StatusIcon(r.State)),
			ui.LT(r.Workflow, ui.Style{}),
			ui.LT(r.Branch, muted),
			ui.LT(ui.Ago(r.Started), muted).RightAligned(),
			ui.LT(ui.Dur(r.Secs), muted).RightAligned(),
			ui.LT(r.Event, muted),
			ui.LT(r.Title, ui.Style{}),
		)
	}
	hdr := func(s ...string) []ui.Line {
		out := make([]ui.Line, len(s))
		for i, x := range s {
			out[i] = ui.LT(x, ui.Style{})
		}
		return out
	}
	ta := parts[1]
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Len(16), ui.Len(14), ui.Len(4), ui.Len(6), ui.Len(12), ui.Min(10)},
		Header:    hdr("", "Workflow", "Branch", "Age", "Took", "Event", ""),
		Rows:      rows,
		Spacing:   2,
		Highlight: true,
		Block:     &ui.Panel{Title: ui.LT(" "+ui.ROCKET.S()+" Runs ", ui.Style{})},
	}.Draw(b, ta, &p.st)
	ui.Scrollbar(b, ta, len(p.runs), max(p.st.Sel, 0), max(ta.H-3, 0))
}
