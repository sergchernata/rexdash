// Package eas is the Expo (EAS) panes for a React Native app:
//
//	Runs    release lanes (latest run per workflow) and workflow runs with their build → submit jobs
//	Builds  every EAS build: platform, profile, version, duration, who started it
//
// Reads the project id from ./app.json (expo.extra.eas.projectId). Auth: EXPO_TOKEN, else the
// session `eas login` / `expo login` keeps in ~/.expo/state.json.
package eas

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/tidwall/gjson"

	"rexdash/internal/netx"
	"rexdash/internal/ui"
)

var (
	expo    = ui.Ic{Glyph: "", Plain: "▯"}
	apple   = ui.Ic{Glyph: "", Plain: "iOS"}
	android = ui.Ic{Glyph: "", Plain: "and"}
	tag     = ui.Ic{Glyph: "", Plain: ""}
)

const query = `query($id: String!) { app { byId(appId: $id) {
  slug ownerAccount { name }
  workflowRunsPaginated(first: 25) { edges { node {
    id name status triggerEventType requestedGitRef gitCommitMessage createdAt durationSeconds
    jobs { key name type status createdAt updatedAt
      turtleBuild { id appVersion appBuildVersion platform buildProfile }
      turtleSubmission { status } }
  } } }
  builds(offset: 0, limit: 40) {
    id platform status buildProfile appVersion appBuildVersion createdAt workerStartedAt completedAt
    gitCommitMessage gitRef developmentClient isForIosSimulator distribution
    initiatingActor { displayName } error { message }
  }
} } }`

// Mode picks which pane to show.
type Mode int

const (
	Runs Mode = iota
	Builds
)

// Job is one step of a workflow run (build, submission…); Version is set for builds.
type Job struct {
	Name, Kind, Status string
	Secs               int64
	Version            *string
}

// Run is one EAS workflow run.
type Run struct {
	ID, Name, Status, Ref, Msg string
	Created, Secs              int64
	Jobs                       []Job
}

// Build is one EAS build.
type Build struct {
	ID, Platform, Status, Profile, Version string
	Created, Secs                          int64
	Ref, Msg, Who, Error                   string
	Dev                                    bool
}

func ts(v gjson.Result) int64 { return ui.ParseTime(v.String()) }

// version is "1.2.9 (94)".
func version(v gjson.Result) string {
	a, b := v.Get("appVersion"), v.Get("appBuildVersion")
	switch {
	case a.Type == gjson.String && b.Type == gjson.String:
		return a.String() + " (" + b.String() + ")"
	case a.Type == gjson.String:
		return a.String()
	}
	return ""
}

// easTone maps EAS statuses (FINISHED, ERRORED, IN_PROGRESS, NEW…) to the shared tones.
func easTone(status string) ui.Tone {
	switch status {
	case "SUCCESS", "FINISHED":
		return ui.ToneOk
	case "FAILURE", "ERRORED", "CANCELED", "CANCELLED":
		return ui.ToneBad
	case "IN_PROGRESS", "NEW", "IN_QUEUE", "PENDING_CANCEL", "ACTION_REQUIRED":
		return ui.ToneBusy
	}
	return ui.ToneIdle
}

func toneIcon(t ui.Tone) ui.Span { return ui.C(ui.ToneIcon(t), ui.ToneColor(t)) }

func platformIcon(p string) ui.Span {
	switch p {
	case "IOS":
		return ui.C(apple.S(), ui.FgC())
	case "ANDROID":
		return ui.C(android.S(), ui.Green())
	}
	return ui.C("?", ui.Muted())
}

// projectID reads app.json's expo.extra.eas.projectId (or a top-level extra).
func projectID(app gjson.Result) (string, bool) {
	root := app
	if e := app.Get("expo"); e.IsObject() {
		root = e
	}
	id := root.Get("extra.eas.projectId")
	return id.String(), id.Type == gjson.String
}

// laneName keeps lane names short: "Staging iOS Build & Submit (TestFlight)" → "Staging iOS".
func laneName(n string) string {
	before, _, _ := strings.Cut(n, " Build")
	return before
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return strings.TrimRight(l, "\r")
}

// parse reads the project URL, workflow runs and builds from the GraphQL answer for app.byId.
func parse(app gjson.Result) (string, []Run, []Build) {
	url := "https://expo.dev/accounts/" + app.Get("ownerAccount.name").String() + "/projects/" + app.Get("slug").String()
	var runs []Run
	for _, e := range app.Get("workflowRunsPaginated.edges").Array() {
		n := e.Get("node")
		r := Run{
			ID:      n.Get("id").String(),
			Name:    n.Get("name").String(),
			Status:  n.Get("status").String(),
			Ref:     strings.TrimPrefix(n.Get("requestedGitRef").String(), "refs/heads/"),
			Msg:     n.Get("gitCommitMessage").String(),
			Created: ts(n.Get("createdAt")),
			Secs:    n.Get("durationSeconds").Int(),
			Jobs:    []Job{},
		}
		for _, j := range n.Get("jobs").Array() {
			job := Job{
				Name:   j.Get("name").String(),
				Kind:   j.Get("type").String(),
				Status: j.Get("status").String(),
				Secs:   max(ts(j.Get("updatedAt"))-ts(j.Get("createdAt")), 0),
			}
			if tb := j.Get("turtleBuild"); tb.IsObject() {
				v := version(tb)
				job.Version = &v
			}
			r.Jobs = append(r.Jobs, job)
		}
		runs = append(runs, r)
	}
	var builds []Build
	for _, b := range app.Get("builds").Array() {
		start := max(ts(b.Get("workerStartedAt")), ts(b.Get("createdAt")))
		end := ts(b.Get("completedAt"))
		bd := Build{
			ID:       b.Get("id").String(),
			Platform: b.Get("platform").String(),
			Status:   b.Get("status").String(),
			Profile:  b.Get("buildProfile").String(),
			Version:  version(b),
			Created:  ts(b.Get("createdAt")),
			Ref:      strings.TrimPrefix(b.Get("gitRef").String(), "refs/heads/"),
			Msg:      b.Get("gitCommitMessage").String(),
			Who:      b.Get("initiatingActor.displayName").String(),
			Error:    b.Get("error.message").String(),
			Dev:      b.Get("developmentClient").Bool(),
		}
		if end > 0 {
			bd.Secs = end - start
		}
		// Who started it isn't shown in the table, but local builds are worth telling apart.
		if !strings.HasPrefix(bd.Who, "GitHub") && bd.Who != "" && bd.Ref == "" {
			bd.Ref = "local · " + bd.Who
		}
		builds = append(builds, bd)
	}
	return url, runs, builds
}

// version of the first job that built something.
func (r Run) version() string {
	for _, j := range r.Jobs {
		if j.Version != nil {
			return *j.Version
		}
	}
	return ""
}

type pane struct {
	ui.Base
	mode    Mode
	every   time.Duration
	project string
	authK   string
	authV   string
	url     string
	runs    []Run
	builds  []Build
	st      ui.TableState
	view    *ui.TextView
	loaded  bool
}

const notLoggedIn = "not logged in to Expo; run `eas login` or set EXPO_TOKEN"

// New makes an EAS pane for the Expo project in the current directory.
func New(mode Mode, every time.Duration) (ui.Pane, error) {
	data, err := os.ReadFile("app.json")
	if err != nil {
		return nil, errors.New("no ./app.json; run from the Expo project")
	}
	project, ok := projectID(gjson.ParseBytes(data))
	if !ok {
		return nil, errors.New("no expo.extra.eas.projectId in app.json")
	}
	p := &pane{mode: mode, every: every, project: project, st: ui.NewTableState()}
	if t := os.Getenv("EXPO_TOKEN"); t != "" {
		p.authK, p.authV = "Authorization", "Bearer "+t
	} else {
		home, _ := os.UserHomeDir()
		st, err := os.ReadFile(filepath.Join(home, ".expo", "state.json"))
		if err != nil {
			return nil, errors.New(notLoggedIn)
		}
		secret := gjson.GetBytes(st, "auth.sessionSecret")
		if secret.Type != gjson.String {
			return nil, errors.New(notLoggedIn)
		}
		p.authK, p.authV = "expo-session", secret.String()
	}
	return p, nil
}

func (p *pane) rows() int {
	if p.mode == Builds {
		return len(p.builds)
	}
	return len(p.runs)
}

func (p *pane) Name() string {
	if p.mode == Builds {
		return "Builds"
	}
	return "EAS"
}

func (p *pane) Title() ui.Line {
	s := strings.Replace(strings.TrimPrefix(p.url, "https://expo.dev/accounts/"), "/projects/", "/", 1)
	return ui.LT(s, ui.Fg(ui.Muted()))
}

func (p *pane) Every() time.Duration { return p.every }

func (p *pane) Hints() []ui.Hint {
	switch {
	case p.view != nil:
		return []ui.Hint{{Key: "↑↓", Desc: "scroll"}, {Key: "esc", Desc: "back"}}
	case p.mode == Runs:
		return []ui.Hint{{Key: "↑↓", Desc: "runs"}, {Key: "⏎", Desc: "details"}, {Key: "o", Desc: "expo.dev"}}
	}
	return []ui.Hint{{Key: "↑↓", Desc: "builds"}, {Key: "⏎", Desc: "expo.dev"}}
}

func (p *pane) Status() ui.Status {
	if !p.loaded {
		return ui.Status{State: "working", Msg: "loading"}
	}
	var status, msg string
	switch {
	case p.mode == Runs && len(p.runs) > 0:
		r := p.runs[0]
		status = r.Status
		msg = fmt.Sprintf("%s · %s · %s ago", laneName(r.Name), strings.ToLower(r.Status), ui.Ago(r.Created))
	case p.mode == Runs:
		msg = "no workflow runs"
	case len(p.builds) > 0:
		b := p.builds[0]
		status = b.Status
		msg = fmt.Sprintf("%s %s %s · %s", strings.ToLower(b.Platform), b.Profile, b.Version, strings.ToLower(b.Status))
	default:
		msg = "no builds"
	}
	state := "idle"
	switch easTone(status) {
	case ui.ToneBad:
		state = "error"
	case ui.ToneBusy:
		state = "working"
	}
	return ui.Status{State: state, Msg: msg}
}

func (p *pane) Refresh() error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"id": p.project}})
	req, _ := http.NewRequest("POST", "https://api.expo.dev/graphql", bytes.NewReader(body))
	req.Header.Set(p.authK, p.authV)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rexdash")
	resp, err := netx.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("expo: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	v := gjson.ParseBytes(data)
	if e := v.Get("errors.0"); e.Exists() {
		msg := e.Get("message").String()
		if msg == "" {
			msg = "error"
		}
		return errors.New("expo: " + msg)
	}
	p.url, p.runs, p.builds = parse(v.Get("data.app.byId"))
	p.loaded = true
	ui.Clamp(&p.st.Sel, p.rows())
	return nil
}

func (p *pane) Key(ev *tcell.EventKey) error {
	if p.view != nil {
		if ui.IsBack(ev) {
			p.view = nil
			ui.Zoom(false)
		} else {
			p.view.Key(ev)
		}
		return nil
	}
	i := max(p.st.Sel, 0)
	switch {
	case ui.IsRune(ev, 'o') && p.mode == Runs:
		if i < len(p.runs) {
			ui.OpenURL(p.url + "/workflows/" + p.runs[i].ID)
		} else {
			ui.OpenURL(p.url)
		}
	case ui.IsRune(ev, 'o') || ui.IsEnter(ev):
		p.openSelected()
	default:
		ui.Nav(&p.st.Sel, p.rows(), ev, 10)
	}
	return nil
}

// openSelected opens a build on expo.dev, or a run's details.
func (p *pane) openSelected() {
	i := max(p.st.Sel, 0)
	if p.mode == Builds {
		if i < len(p.builds) {
			ui.OpenURL(p.url + "/builds/" + p.builds[i].ID)
		}
		return
	}
	if i >= len(p.runs) {
		return
	}
	r := p.runs[i]
	t := easTone(r.Status)
	muted := ui.Fg(ui.Muted())
	lines := []ui.Line{
		ui.L(toneIcon(t), ui.S(" "+r.Name+" ", ui.Style{Bold: true}), ui.C(strings.ToLower(r.Status), ui.ToneColor(t))),
		ui.LT(fmt.Sprintf("%s · %s · %s", r.Ref, ui.Local(r.Created, "Jan 02 15:04"), ui.Dur(r.Secs)), muted),
		{},
	}
	for _, j := range r.Jobs {
		v := ""
		if j.Version != nil {
			v = *j.Version
		}
		lines = append(lines, ui.L(
			toneIcon(easTone(j.Status)),
			ui.T(fmt.Sprintf(" %-12s", j.Name)),
			ui.S(fmt.Sprintf("%-11s", strings.ToLower(j.Kind)), muted),
			ui.S(fmt.Sprintf("%7s  ", ui.Dur(j.Secs)), muted),
			ui.C(v, ui.Accent()),
		))
	}
	lines = append(lines, ui.Line{})
	if msg := strings.TrimRight(r.Msg, "\n"); msg != "" {
		for _, l := range strings.Split(msg, "\n") {
			lines = append(lines, ui.LT(strings.TrimRight(l, "\r"), ui.Style{}))
		}
	}
	p.view = ui.NewTextView(laneName(r.Name)+" · "+r.Ref, lines).WithIcon(expo)
	ui.Zoom(true)
}

func (p *pane) Draw(b *ui.Buffer, area ui.Rect) {
	switch {
	case p.view != nil:
		p.view.Draw(b, area)
	case !p.loaded:
		ui.EmptyState(b, area, expo.S(), ui.Muted(), "Loading from Expo…", "")
	case p.mode == Builds:
		p.drawBuilds(b, area)
	default:
		p.drawRuns(b, area)
	}
}

func (p *pane) drawRuns(b *ui.Buffer, area ui.Rect) {
	// One tile per workflow: its latest run.
	var lanes []Run
	seen := map[string]bool{}
	for _, r := range p.runs {
		if !seen[r.Name] && len(lanes) < 3 {
			seen[r.Name] = true
			lanes = append(lanes, r)
		}
	}
	tileH := 6
	if len(lanes) == 0 {
		tileH = 0
	}
	parts := ui.Vertical(area, 0, ui.Len(tileH), ui.Min(3))
	cs := make([]ui.Constraint, max(len(lanes), 1))
	for i := range cs {
		cs[i] = ui.Fill(1)
	}
	cols := ui.Horizontal(parts[0], 1, cs...)
	muted := ui.Fg(ui.Muted())
	for i, r := range lanes {
		t := easTone(r.Status)
		bc := ui.Border()
		if t == ui.ToneBad {
			bc = ui.Red()
		}
		inner := ui.Panel{Title: ui.L(ui.T(" "), toneIcon(t), ui.T(" "+laneName(r.Name)+" ")), BorderColor: bc}.Draw(b, cols[i])
		var steps []ui.Span
		for k, j := range r.Jobs {
			jt := easTone(j.Status)
			if k > 0 {
				steps = append(steps, ui.C(" "+ui.ARROW_THIN.S()+" ", ui.Muted()))
			}
			steps = append(steps, toneIcon(jt), ui.C(" "+strings.ToLower(j.Kind), ui.ToneColor(jt)))
		}
		lines := []ui.Line{
			ui.L(ui.C(tag.S()+" ", ui.Accent()), ui.S(r.version(), ui.Style{Fg: ui.Accent(), Bold: true}), ui.S("  "+ui.Ago(r.Created)+" ago", muted)),
			ui.L(steps...),
			ui.LT(r.Ref+" · "+firstLine(r.Msg), muted),
		}
		ui.Paragraph(b, ui.Middle(inner, 3), lines, false)
	}

	rows := make([]ui.Row, len(p.runs))
	for i, r := range p.runs {
		var jobs []ui.Span
		for _, j := range r.Jobs {
			jobs = append(jobs, toneIcon(easTone(j.Status)), ui.T(" "))
		}
		rows[i] = ui.R(
			ui.L(toneIcon(easTone(r.Status))),
			ui.LT(ui.Local(r.Created, "Jan 02 15:04"), muted),
			ui.LT(laneName(r.Name), ui.Style{}),
			ui.LT(r.Ref, muted),
			ui.LT(r.version(), ui.Fg(ui.Accent())),
			ui.L(jobs...),
			ui.LT(ui.Dur(r.Secs), muted).RightAligned(),
			ui.LT(firstLine(r.Msg), ui.Style{}),
		)
	}
	ra := parts[1]
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Len(12), ui.Len(16), ui.Len(8), ui.Len(11), ui.Len(5), ui.Len(6), ui.Min(10)},
		Header:    header("", "Started", "Workflow", "Branch", "Version", "Jobs", "Took", "Commit"),
		Rows:      rows,
		Spacing:   2,
		Highlight: true,
		Block:     &ui.Panel{Title: ui.LT(" "+ui.ROCKET.S()+" Workflow runs ", ui.Style{})},
	}.Draw(b, ra, &p.st)
	ui.Scrollbar(b, ra, len(p.runs), max(p.st.Sel, 0), max(ra.H-3, 0))
}

func (p *pane) drawBuilds(b *ui.Buffer, area ui.Rect) {
	// Average build time of finished store builds, per platform.
	var right ui.Line
	for _, pl := range []string{"IOS", "ANDROID"} {
		var sum, n int64
		for _, bd := range p.builds {
			if bd.Platform == pl && bd.Status == "FINISHED" && !bd.Dev {
				sum += max(bd.Secs, 0)
				n++
			}
		}
		if n == 0 {
			continue
		}
		right.Spans = append(right.Spans, platformIcon(pl), ui.C(" avg "+ui.Dur(sum/n)+" ", ui.Muted()))
	}
	muted := ui.Fg(ui.Muted())
	rows := make([]ui.Row, len(p.builds))
	for i, bd := range p.builds {
		profile := bd.Profile
		if bd.Dev {
			profile += " (dev)"
		}
		detail, dc := firstLine(bd.Msg), ui.FgC()
		if bd.Error != "" {
			detail, dc = bd.Error, ui.Red()
		}
		took := ""
		if bd.Secs > 0 {
			took = ui.Dur(bd.Secs)
		}
		rows[i] = ui.R(
			ui.L(toneIcon(easTone(bd.Status))),
			ui.L(platformIcon(bd.Platform)),
			ui.LT(profile, ui.Style{}),
			ui.LT(bd.Version, ui.Fg(ui.Accent())),
			ui.LT(ui.Ago(bd.Created), muted).RightAligned(),
			ui.LT(took, muted).RightAligned(),
			ui.LT(bd.Ref, muted),
			ui.LT(detail, ui.Fg(dc)),
		)
	}
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Len(1), ui.Len(12), ui.Len(11), ui.Len(4), ui.Len(6), ui.Len(8), ui.Min(8)},
		Header:    header("", "", "Profile", "Version", "Age", "Took", "Branch", ""),
		Rows:      rows,
		Spacing:   2,
		Highlight: true,
		Block:     &ui.Panel{Title: ui.LT(" "+ui.BUILD.S()+" Builds ", ui.Style{}), Right: right},
	}.Draw(b, area, &p.st)
	ui.Scrollbar(b, area, len(p.builds), max(p.st.Sel, 0), max(area.H-3, 0))
}

func header(s ...string) []ui.Line {
	out := make([]ui.Line, len(s))
	for i, x := range s {
		out[i] = ui.LT(x, ui.Style{})
	}
	return out
}
