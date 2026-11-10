// Package prs is the pull requests pane: one GitHub GraphQL call per refresh, details on demand.
package prs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/tidwall/gjson"

	"rexdash/internal/ghauth"
	"rexdash/internal/netx"
	"rexdash/internal/ui"
)

const listQuery = `query($owner:String!,$name:String!){repository(owner:$owner,name:$name){
 open: pullRequests(states:OPEN,first:30,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{...pr}}
 merged: pullRequests(states:MERGED,first:15,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{...pr}}}}
fragment pr on PullRequest{number title url isDraft headRefName updatedAt author{login} reviewDecision additions deletions
 commits(last:1){nodes{commit{statusCheckRollup{state}}}}}`

const detailQuery = `query($owner:String!,$name:String!,$n:Int!){repository(owner:$owner,name:$name){pullRequest(number:$n){
 number title url body isDraft state headRefName baseRefName createdAt mergeable additions deletions changedFiles author{login}
 reviews(last:10){nodes{author{login} state submittedAt}}
 commits(last:1){nodes{commit{oid statusCheckRollup{state contexts(first:60){nodes{__typename
  ... on CheckRun{name status conclusion startedAt completedAt detailsUrl}
  ... on StatusContext{context state targetUrl createdAt}}}}}}}}}}`

// ts is an RFC 3339 time as Unix seconds; false when missing or invalid.
func ts(v gjson.Result) (int64, bool) {
	t, err := time.Parse(time.RFC3339Nano, v.String())
	if v.Type != gjson.String || err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// ago is "5m" since an RFC 3339 time, or "".
func ago(v gjson.Result) string {
	if t, ok := ts(v); ok {
		return ui.Ago(t)
	}
	return ""
}

// ciState is the PR's latest commit check rollup (SUCCESS, FAILURE, PENDING…).
func ciState(p gjson.Result) string {
	return p.Get("commits.nodes.0.commit.statusCheckRollup.state").String()
}

// pretty is "CHANGES_REQUESTED" → "changes requested".
func pretty(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", " ")) }

type check struct {
	name, state, took, url string
}

type detail struct {
	url    string
	info   []ui.Line
	checks []check
	st     ui.TableState
}

type pane struct {
	ui.Base
	every       time.Duration
	slug        string
	owner, name string
	token       string
	open        []gjson.Result
	merged      []gjson.Result
	showMerged  bool
	branch      string
	st          ui.TableState
	detail      *detail
}

// New makes the PRs pane for OWNER/REPO ("" = the origin of the current directory).
func New(repo string, every time.Duration) (ui.Pane, error) {
	if repo == "" {
		r, err := ghauth.RepoSlug()
		if err != nil {
			return nil, err
		}
		repo = r
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, errors.New("repo must be OWNER/NAME")
	}
	tok, err := ghauth.Token()
	if err != nil {
		return nil, err
	}
	return &pane{every: every, slug: repo, owner: owner, name: name, token: tok, st: ui.NewTableState()}, nil
}

// gql runs a query and returns data.repository.
func (p *pane) gql(query string, vars map[string]any) (gjson.Result, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, _ := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rexdash")
	resp, err := netx.HTTP.Do(req)
	if err != nil {
		return gjson.Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return gjson.Result{}, fmt.Errorf("github: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return gjson.Result{}, err
	}
	v := gjson.ParseBytes(data)
	if e := v.Get("errors"); e.Exists() {
		msg := e.Get("0.message").String()
		if msg == "" {
			msg = "GraphQL error"
		}
		return gjson.Result{}, errors.New(msg)
	}
	return v.Get("data.repository"), nil
}

func (p *pane) list() []gjson.Result {
	if p.showMerged {
		return p.merged
	}
	return p.open
}

func (p *pane) selected() (gjson.Result, bool) {
	l := p.list()
	if p.st.Sel < 0 || p.st.Sel >= len(l) {
		return gjson.Result{}, false
	}
	return l[p.st.Sel], true
}

func (p *pane) openDetail() error {
	sel, ok := p.selected()
	if !ok {
		return nil
	}
	n := sel.Get("number").Int()
	r, err := p.gql(detailQuery, map[string]any{"owner": p.owner, "name": p.name, "n": n})
	if err != nil {
		return err
	}
	pr := r.Get("pullRequest")
	author := pr.Get("author.login").String()
	if author == "" {
		author = "?"
	}
	merge, mergeC := "", ui.Green()
	switch pr.Get("mergeable").String() {
	case "CONFLICTING":
		merge, mergeC = "  merge conflicts", ui.Red()
	case "MERGEABLE":
		merge = "  mergeable"
	}
	draft := ""
	if pr.Get("isDraft").Bool() {
		draft = "  draft"
	}
	dim := ui.Style{Dim: true}
	info := []ui.Line{
		ui.L(ui.S(fmt.Sprintf("#%d ", n), ui.Style{Fg: ui.Magenta(), Bold: true}), ui.S(pr.Get("title").String(), ui.Style{Bold: true})),
		ui.L(
			ui.C(pr.Get("headRefName").String()+" → "+pr.Get("baseRefName").String(), ui.Cyan()),
			ui.S(fmt.Sprintf("  %s · opened %s ago", author, ago(pr.Get("createdAt"))), dim),
		),
		ui.L(
			ui.C(fmt.Sprintf("+%d", pr.Get("additions").Int()), ui.Green()),
			ui.C(fmt.Sprintf(" -%d", pr.Get("deletions").Int()), ui.Red()),
			ui.S(fmt.Sprintf(" in %d files", pr.Get("changedFiles").Int()), dim),
			ui.C(merge, mergeC),
			ui.S(draft, dim),
		),
	}
	var reviews []ui.Span
	for _, rv := range pr.Get("reviews.nodes").Array() {
		s := rv.Get("state").String()
		if s == "COMMENTED" {
			continue
		}
		who := rv.Get("author.login").String()
		if who == "" {
			who = "?"
		}
		reviews = append(reviews, ui.C(who+" "+pretty(s)+"  ", ui.StatusColor(s)))
	}
	if len(reviews) > 0 {
		info = append(info, ui.L(append([]ui.Span{ui.S("reviews ", dim)}, reviews...)...))
	}
	if body := strings.TrimSpace(pr.Get("body").String()); body != "" {
		info = append(info, ui.Line{})
		n := 0
		for _, l := range strings.Split(body, "\n") {
			l = strings.TrimRight(l, "\r")
			if strings.TrimSpace(l) == "" {
				continue
			}
			if n++; n > 6 {
				break
			}
			info = append(info, ui.LT(l, dim))
		}
	}

	var checks []check
	now := time.Now().Unix()
	for _, c := range pr.Get("commits.nodes.0.commit.statusCheckRollup.contexts.nodes").Array() {
		if c.Get("__typename").String() != "CheckRun" {
			checks = append(checks, check{name: c.Get("context").String(), state: c.Get("state").String(), url: c.Get("targetUrl").String()})
			continue
		}
		state := c.Get("conclusion").String()
		if state == "" {
			state = c.Get("status").String()
		}
		took := ""
		a, okA := ts(c.Get("startedAt"))
		z, okZ := ts(c.Get("completedAt"))
		switch {
		case okA && okZ:
			took = ui.Dur(z - a)
		case okA:
			took = ui.Dur(now-a) + "…"
		}
		checks = append(checks, check{name: c.Get("name").String(), state: state, took: took, url: c.Get("detailsUrl").String()})
	}
	// Failures first, then running, then the rest.
	rank := func(s string) int {
		switch ui.ToneOf(s) {
		case ui.ToneBad:
			return 0
		case ui.ToneBusy:
			return 1
		}
		return 2
	}
	sort.SliceStable(checks, func(i, j int) bool { return rank(checks[i].state) < rank(checks[j].state) })
	st := ui.NewTableState()
	if len(checks) > 0 {
		st.Sel = 0
	}
	p.detail = &detail{url: pr.Get("url").String(), info: info, checks: checks, st: st}
	ui.Zoom(true)
	return nil
}

func (p *pane) Name() string         { return "PRs" }
func (p *pane) Title() ui.Line       { return ui.LT(p.slug, ui.Fg(ui.Muted())) }
func (p *pane) Every() time.Duration { return p.every }

func (p *pane) Hints() []ui.Hint {
	if p.detail != nil {
		return []ui.Hint{{Key: "↑↓", Desc: "checks"}, {Key: "⏎", Desc: "open check"}, {Key: "o", Desc: "open PR"}, {Key: "esc", Desc: "back"}}
	}
	return []ui.Hint{{Key: "↑↓", Desc: "move"}, {Key: "⏎", Desc: "details"}, {Key: "o", Desc: "open"}, {Key: "tab", Desc: "open/merged"}}
}

func (p *pane) Status() ui.Status {
	failing, running := 0, 0
	for _, pr := range p.open {
		switch ui.ToneOf(ciState(pr)) {
		case ui.ToneBad:
			failing++
		case ui.ToneBusy:
			running++
		}
	}
	msg := fmt.Sprintf("%d open", len(p.open))
	if failing > 0 {
		msg += fmt.Sprintf(" · %d failing CI", failing)
	}
	if running > 0 {
		msg += fmt.Sprintf(" · %d running", running)
		return ui.Status{State: "working", Msg: msg}
	}
	return ui.Status{State: "idle", Msg: msg}
}

func (p *pane) Refresh() error {
	r, err := p.gql(listQuery, map[string]any{"owner": p.owner, "name": p.name})
	if err != nil {
		return err
	}
	p.open = r.Get("open.nodes").Array()
	p.merged = r.Get("merged.nodes").Array()
	p.branch = ghauth.Branch()
	ui.Clamp(&p.st.Sel, len(p.list()))
	return nil
}

func (p *pane) Key(ev *tcell.EventKey) error {
	if d := p.detail; d != nil {
		switch {
		case ui.IsRune(ev, 'o'):
			ui.OpenURL(d.url)
		case ui.IsBack(ev):
			p.detail = nil
			ui.Zoom(false)
		case ui.IsEnter(ev):
			if d.st.Sel >= 0 && d.st.Sel < len(d.checks) {
				ui.OpenURL(d.checks[d.st.Sel].url)
			}
		default:
			ui.Nav(&d.st.Sel, len(d.checks), ev, 10)
		}
		return nil
	}
	switch {
	case ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab:
		p.showMerged = !p.showMerged
		p.st = ui.TableState{Sel: 0}
		ui.Clamp(&p.st.Sel, len(p.list()))
	case ui.IsRune(ev, 'o'):
		if pr, ok := p.selected(); ok {
			ui.OpenURL(pr.Get("url").String())
		}
	case ui.IsEnter(ev):
		return p.openDetail()
	default:
		ui.Nav(&p.st.Sel, len(p.list()), ev, 10)
	}
	return nil
}

func (p *pane) Draw(b *ui.Buffer, area ui.Rect) {
	if p.detail != nil {
		p.drawDetail(b, area)
		return
	}
	parts := ui.Vertical(area, 0, ui.Len(1), ui.Min(3))
	ta, la := parts[0], parts[1]
	mineMissing := ""
	if p.branch != "" && !p.hasPR(p.branch) {
		mineMissing = fmt.Sprintf("%s %s has no PR ", ui.BRANCH.S(), p.branch)
	}
	top := ui.Horizontal(ta, 0, ui.Min(10), ui.Len(ui.StrWidth(mineMissing)))
	b.SetLine(top[0], p.tabs())
	b.SetLine(top[1], ui.LT(mineMissing, ui.Fg(ui.Muted())))

	list := p.list()
	if len(list) == 0 {
		inner := ui.Panel{}.Draw(b, la)
		if p.showMerged {
			ui.EmptyState(b, inner, ui.MERGED.S(), ui.Magenta(), "No merged pull requests", "")
		} else {
			hint := fmt.Sprintf("%d merged recently · tab to view", len(p.merged))
			ui.EmptyState(b, inner, ui.OK.S(), ui.Green(), "No open pull requests", hint)
		}
		return
	}
	muted := ui.Fg(ui.Muted())
	rows := make([]ui.Row, len(list))
	for i, pr := range list {
		ci := ciState(pr)
		rd := pr.Get("reviewDecision").String()
		var title []ui.Span
		if p.branch != "" && pr.Get("headRefName").String() == p.branch {
			title = append(title, ui.C("▶ ", ui.Accent()))
		}
		if pr.Get("isDraft").Bool() {
			title = append(title, ui.C(ui.DRAFT.S()+" ", ui.Muted()))
		}
		title = append(title, ui.T(pr.Get("title").String()))
		review := ""
		switch rd {
		case "APPROVED":
			review = ui.OK.S() + " ok"
		case "CHANGES_REQUESTED":
			review = ui.EXCL.S() + " changes"
		case "REVIEW_REQUIRED":
			review = ui.EYE.S() + " review"
		}
		icon := ui.StatusIcon(ci)
		if p.showMerged {
			icon = ui.C(ui.MERGED.S(), ui.Magenta())
		}
		rows[i] = ui.R(
			ui.L(icon),
			ui.LT(fmt.Sprintf("#%d", pr.Get("number").Int()), ui.Fg(ui.Magenta())),
			ui.L(title...),
			ui.L(ui.C(fmt.Sprintf("+%d", pr.Get("additions").Int()), ui.Green()), ui.C(fmt.Sprintf(" -%d", pr.Get("deletions").Int()), ui.Red())).RightAligned(),
			ui.LT(review, ui.Fg(ui.StatusColor(rd))),
			ui.LT(pr.Get("author.login").String(), muted),
			ui.LT(ago(pr.Get("updatedAt")), muted).RightAligned(),
		)
	}
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Len(5), ui.Min(12), ui.Len(11), ui.Len(9), ui.Len(12), ui.Len(4)},
		Header:    header("", "#", "Title", "+/-", "Review", "Author", "Age"),
		Rows:      rows,
		Spacing:   1,
		Highlight: true,
		Block:     &ui.Panel{},
	}.Draw(b, la, &p.st)
	ui.Scrollbar(b, la, len(list), max(p.st.Sel, 0), max(la.H-3, 0))
}

func (p *pane) hasPR(branch string) bool {
	for _, pr := range p.open {
		if pr.Get("headRefName").String() == branch {
			return true
		}
	}
	return false
}

// tabs is " Open n │ Merged m ", the selected one bold and underlined.
func (p *pane) tabs() ui.Line {
	plain := ui.Fg(ui.Muted())
	sel := ui.Style{Fg: ui.FgC(), Bold: true, Ulin: true}
	open, merged := plain, plain
	if p.showMerged {
		merged = sel
	} else {
		open = sel
	}
	return ui.L(
		ui.T(" "),
		ui.S(ui.PR.S()+" Open ", open), ui.S(fmt.Sprint(len(p.open)), open.WithFg(ui.Accent())),
		ui.T(" "), ui.C("│", ui.Border()), ui.T(" "),
		ui.S(ui.MERGED.S()+" Merged ", merged), ui.S(fmt.Sprint(len(p.merged)), merged.WithFg(ui.Magenta())),
		ui.T(" "),
	)
}

func (p *pane) drawDetail(b *ui.Buffer, area ui.Rect) {
	d := p.detail
	infoH := max(min(len(d.info)+2, area.H/2), 4)
	parts := ui.Vertical(area, 0, ui.Len(infoH), ui.Min(4))
	inner := ui.Panel{
		Title: ui.LT(" "+ui.PR.S()+" Pull request ", ui.Style{}),
		Right: ui.LT(" o open in browser ", ui.Fg(ui.Muted())),
	}.Draw(b, parts[0])
	ui.Paragraph(b, inner, d.info, true)

	ok, failed := 0, 0
	for _, c := range d.checks {
		switch ui.ToneOf(c.state) {
		case ui.ToneOk:
			ok++
		case ui.ToneBad:
			failed++
		}
	}
	title := ui.L(ui.T(" "+ui.CHECKLIST.S()+" Checks "), ui.CountBadge(len(d.checks)), ui.T(" "))
	if failed > 0 {
		title.Spans = append(title.Spans, ui.S(fmt.Sprintf("%d failing ", failed), ui.Style{Fg: ui.Red(), Bold: true}))
	}
	inner = ui.Panel{Title: title}.Draw(b, parts[1])
	if len(d.checks) == 0 {
		ui.EmptyState(b, inner, ui.PENDING.S(), ui.Muted(), "No checks reported", "")
		return
	}
	rows := ui.Vertical(inner, 0, ui.Len(2), ui.Min(1))
	ga := rows[0].Row(0)
	label := ui.LT(fmt.Sprintf("%d/%d passing ", ok, len(d.checks)), ui.Fg(ui.Muted()))
	lw := min(label.Width(), ga.W)
	b.SetLine(ga, label)
	fill := ui.Green()
	if failed > 0 {
		fill = ui.Red()
	}
	gx := ga.X + lw + 1
	ui.LineGauge(b, ui.Rect{X: gx, Y: ga.Y, W: max(ga.Right()-gx, 0), H: 1}, float64(ok)/float64(len(d.checks)), ui.Fg(fill), ui.Fg(ui.Border()))

	cr := make([]ui.Row, len(d.checks))
	for i, c := range d.checks {
		cr[i] = ui.R(
			ui.L(ui.StatusIcon(c.state)),
			ui.LT(c.name, ui.Style{}),
			ui.LT(pretty(c.state), ui.Fg(ui.StatusColor(c.state))),
			ui.LT(c.took, ui.Fg(ui.Muted())).RightAligned(),
		)
	}
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Min(10), ui.Len(10), ui.Len(7)},
		Rows:      cr,
		Spacing:   1,
		Highlight: true,
	}.Draw(b, rows[1], &d.st)
}

func header(s ...string) []ui.Line {
	out := make([]ui.Line, len(s))
	for i, x := range s {
		out[i] = ui.LT(x, ui.Style{})
	}
	return out
}
