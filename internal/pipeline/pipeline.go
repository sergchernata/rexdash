// Package pipeline is the CodePipeline pane: stages, runs, per-run actions, CodeBuild logs.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cp "github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/awsx"
	"rexdash/internal/ui"
)

// short cuts s to 60 characters.
func short(s string) string {
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return s
}

// commitMsg: CodePipeline summaries are JSON with a CommitMessage for GitHub sources, plain text otherwise.
func commitMsg(raw string) string {
	var v struct{ CommitMessage *string }
	if json.Unmarshal([]byte(raw), &v) == nil && v.CommitMessage != nil {
		return *v.CommitMessage
	}
	return raw
}

// awsErr keeps just the service's code and message.
func awsErr(err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return fmt.Errorf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	return err
}

func secs(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

type stage struct {
	name, status, action string
	changed              int64 // 0 = never
}

type run struct {
	id, status, msg, trigger string
	start, end               int64
}

// took is how long a run or action has taken, up to now while it's running.
func took(status string, start, end int64) int64 {
	if status == "InProgress" {
		end = ui.Now()
	}
	return end - start
}

type action struct {
	stage, name, status, summary, url string
	start, end                        int64
	buildID                           string // CodeBuild "project:uuid", "" otherwise
}

type runView struct {
	run     int    // index into runs
	id      string // the run's execution id; runs shift down as new ones start
	actions []action
	st      ui.TableState
	log     *ui.TextView
}

// cpAPI is the part of the CodePipeline client the pane uses.
type cpAPI interface {
	GetPipelineState(context.Context, *cp.GetPipelineStateInput, ...func(*cp.Options)) (*cp.GetPipelineStateOutput, error)
	ListPipelineExecutions(context.Context, *cp.ListPipelineExecutionsInput, ...func(*cp.Options)) (*cp.ListPipelineExecutionsOutput, error)
	ListActionExecutions(context.Context, *cp.ListActionExecutionsInput, ...func(*cp.Options)) (*cp.ListActionExecutionsOutput, error)
}

// logsAPI is the part of the CloudWatch Logs client the pane uses.
type logsAPI interface {
	GetLogEvents(context.Context, *cloudwatchlogs.GetLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error)
}

type Pipeline struct {
	ui.Base
	every  time.Duration
	name   string
	region string
	cp     cpAPI
	logs   logsAPI
	stages []stage
	runs   []run
	st     ui.TableState
	view   *runView
}

func New(name string, every time.Duration) (ui.Pane, error) {
	cfg, err := awsx.Config(context.Background())
	if err != nil {
		return nil, err
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	return &Pipeline{
		every:  every,
		name:   name,
		region: region,
		cp:     cp.NewFromConfig(cfg),
		logs:   cloudwatchlogs.NewFromConfig(cfg),
		st:     ui.NewTableState(),
	}, nil
}

func (p *Pipeline) consoleURL(runID string) string {
	r := p.region
	return fmt.Sprintf("https://%s.console.aws.amazon.com/codesuite/codepipeline/pipelines/%s/executions/%s/timeline?region=%s", r, p.name, runID, r)
}

func (p *Pipeline) loadActions(runID string) ([]action, error) {
	resp, err := p.cp.ListActionExecutions(context.Background(), &cp.ListActionExecutionsInput{
		PipelineName: &p.name,
		Filter:       &types.ActionExecutionFilter{PipelineExecutionId: &runID},
	})
	if err != nil {
		return nil, awsErr(err)
	}
	out := make([]action, 0, len(resp.ActionExecutionDetails))
	for _, a := range resp.ActionExecutionDetails {
		var provider, ext, summary, url string
		if a.Input != nil && a.Input.ActionTypeId != nil {
			provider = aws.ToString(a.Input.ActionTypeId.Provider)
		}
		if a.Output != nil && a.Output.ExecutionResult != nil {
			r := a.Output.ExecutionResult
			ext = aws.ToString(r.ExternalExecutionId)
			summary = commitMsg(aws.ToString(r.ExternalExecutionSummary))
			url = aws.ToString(r.ExternalExecutionUrl)
		}
		act := action{
			stage:   aws.ToString(a.StageName),
			name:    aws.ToString(a.ActionName),
			status:  string(a.Status),
			start:   secs(a.StartTime),
			end:     secs(a.LastUpdateTime),
			summary: summary,
			url:     url,
		}
		if provider == "CodeBuild" && strings.Contains(ext, ":") {
			act.buildID = ext
		}
		out = append(out, act)
	}
	slices.SortStableFunc(out, func(a, b action) int { return int(a.start - b.start) })
	return out, nil
}

// buildLog reads a CodeBuild log: /aws/codebuild/<project>, stream = the build's UUID.
func (p *Pipeline) buildLog(buildID string) (*ui.TextView, error) {
	project, uuid, ok := strings.Cut(buildID, ":")
	if !ok {
		return nil, errors.New("unexpected build id")
	}
	// Read backwards from the end; GetLogEvents may return empty pages before the data,
	// so keep going until the token stops changing.
	var msgs []string
	var token *string
	for range 20 {
		resp, err := p.logs.GetLogEvents(context.Background(), &cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  aws.String("/aws/codebuild/" + project),
			LogStreamName: &uuid,
			StartFromHead: aws.Bool(false),
			NextToken:     token,
		})
		if err != nil {
			return nil, awsErr(err)
		}
		page := make([]string, 0, len(resp.Events))
		for _, e := range resp.Events {
			page = append(page, aws.ToString(e.Message))
		}
		msgs = append(page, msgs...)
		next := resp.NextBackwardToken
		if len(msgs) >= 3000 || next == nil || (token != nil && *next == *token) {
			break
		}
		token = next
	}
	var lines []ui.Line
	for _, m := range msgs {
		if m == "" {
			continue
		}
		for _, l := range strings.Split(strings.TrimSuffix(m, "\n"), "\n") {
			l = strings.TrimSuffix(l, "\r")
			low := strings.ToLower(l)
			var st ui.Style
			switch {
			case strings.Contains(low, "error") || strings.Contains(low, "failed") || strings.Contains(low, "** ("):
				st = ui.Fg(ui.Red())
			case strings.Contains(low, "warning"):
				st = ui.Fg(ui.Yellow())
			case strings.HasPrefix(l, "[Container]"):
				st = ui.Fg(ui.Cyan())
			}
			lines = append(lines, ui.LT(strings.ReplaceAll(l, "\t", "    "), st))
		}
	}
	return ui.NewTextView("build log "+project, lines).WithIcon(ui.TERMINAL).AtEnd(), nil
}

func (p *Pipeline) Name() string { return "Pipeline" }

func (p *Pipeline) Title() ui.Line { return ui.L(ui.C(p.name, ui.Muted())) }

func hint(key, desc string) ui.Hint { return ui.Hint{Key: key, Desc: desc} }

func (p *Pipeline) Hints() []ui.Hint {
	switch {
	case p.view != nil && p.view.log != nil:
		return []ui.Hint{hint("↑↓", "scroll"), hint("esc", "back")}
	case p.view != nil:
		return []ui.Hint{hint("↑↓", "move"), hint("⏎", "build log"), hint("o", "open"), hint("esc", "back")}
	}
	return []ui.Hint{hint("↑↓", "runs"), hint("⏎", "actions"), hint("o", "console")}
}

func (p *Pipeline) Every() time.Duration { return p.every }

func (p *Pipeline) Status() ui.Status {
	if len(p.runs) == 0 {
		return ui.Status{State: "idle", Msg: "no runs"}
	}
	r := p.runs[0]
	switch r.status {
	case "InProgress":
		name := ""
		for _, s := range p.stages {
			if s.status == "InProgress" {
				name = s.name
				break
			}
		}
		// Between stages no stage is InProgress yet.
		return ui.Status{State: "working", Msg: strings.TrimSpace(fmt.Sprintf("%s running %s", name, ui.Dur(ui.Now()-r.start)))}
	case "Failed", "Stopped", "Cancelled":
		return ui.Status{State: "error", Msg: fmt.Sprintf("%s %s ago · %s", r.status, ui.Ago(r.end), short(r.msg))}
	}
	return ui.Status{State: "idle", Msg: fmt.Sprintf("%s %s ago · %s", r.status, ui.Ago(r.end), short(r.msg))}
}

func (p *Pipeline) Refresh() error {
	ctx := context.Background()
	state, err := p.cp.GetPipelineState(ctx, &cp.GetPipelineStateInput{Name: &p.name})
	if err != nil {
		return awsErr(err)
	}
	p.stages = p.stages[:0]
	for _, s := range state.StageStates {
		st := stage{name: aws.ToString(s.StageName)}
		if s.LatestExecution != nil {
			st.status = string(s.LatestExecution.Status)
		}
		for _, a := range s.ActionStates {
			e := a.LatestExecution
			if e == nil {
				continue
			}
			if e.Status == types.ActionExecutionStatusInProgress && st.action == "" {
				st.action = aws.ToString(a.ActionName)
			}
			st.changed = max(st.changed, secs(e.LastStatusChange))
		}
		p.stages = append(p.stages, st)
	}

	runs, err := p.cp.ListPipelineExecutions(ctx, &cp.ListPipelineExecutionsInput{PipelineName: &p.name, MaxResults: aws.Int32(25)})
	if err != nil {
		return awsErr(err)
	}
	p.runs = p.runs[:0]
	for _, r := range runs.PipelineExecutionSummaries {
		raw := ""
		if len(r.SourceRevisions) > 0 {
			raw = aws.ToString(r.SourceRevisions[0].RevisionSummary)
		}
		msg, _, _ := strings.Cut(commitMsg(raw), "\n")
		trigger := ""
		if r.Trigger != nil {
			trigger = string(r.Trigger.TriggerType)
		}
		p.runs = append(p.runs, run{
			id:      aws.ToString(r.PipelineExecutionId),
			status:  string(r.Status),
			start:   secs(r.StartTime),
			end:     secs(r.LastUpdateTime),
			msg:     strings.TrimSuffix(msg, "\r"),
			trigger: trigger,
		})
	}
	ui.Clamp(&p.st.Sel, len(p.runs))

	// Keep an open run view on its run (new runs push it down the list), and live while it runs.
	if v := p.view; v != nil {
		v.run = slices.IndexFunc(p.runs, func(r run) bool { return r.id == v.id })
		if v.run < 0 {
			v.run = len(p.runs) // dropped off the list: keep what's shown
		}
	}
	if v := p.view; v != nil && v.log == nil && v.run < len(p.runs) {
		actions, err := p.loadActions(p.runs[v.run].id)
		if err != nil {
			return err
		}
		v.actions = actions
		ui.Clamp(&v.st.Sel, len(v.actions))
	}
	return nil
}

func (p *Pipeline) Key(ev *tcell.EventKey) error {
	if v := p.view; v != nil {
		if v.log != nil {
			if ui.IsBack(ev) {
				v.log = nil
			} else {
				v.log.Key(ev)
			}
			return nil
		}
		var sel *action
		if v.st.Sel >= 0 && v.st.Sel < len(v.actions) {
			sel = &v.actions[v.st.Sel]
		}
		switch {
		case ui.IsRune(ev, 'o'):
			if sel != nil && sel.url != "" {
				ui.OpenURL(sel.url)
			} else if v.run < len(p.runs) {
				ui.OpenURL(p.consoleURL(p.runs[v.run].id))
			}
		case ui.IsBack(ev):
			p.view = nil
			ui.Zoom(false)
		case ui.IsEnter(ev):
			if sel != nil && sel.buildID != "" {
				log, err := p.buildLog(sel.buildID)
				if err != nil {
					return err
				}
				v.log = log
			}
		default:
			ui.Nav(&v.st.Sel, len(v.actions), ev, 10)
		}
		return nil
	}
	i := p.st.Sel
	switch {
	case ui.IsRune(ev, 'o'):
		if i >= 0 && i < len(p.runs) {
			ui.OpenURL(p.consoleURL(p.runs[i].id))
		}
	case ui.IsEnter(ev):
		if i >= 0 && i < len(p.runs) {
			actions, err := p.loadActions(p.runs[i].id)
			if err != nil {
				return err
			}
			// Start on the first failed action, if any.
			st := ui.NewTableState()
			if len(actions) > 0 {
				st.Sel = 0
			}
			for j, a := range actions {
				if ui.ToneOf(a.status) == ui.ToneBad {
					st.Sel = j
					break
				}
			}
			p.view = &runView{run: i, id: p.runs[i].id, actions: actions, st: st}
			ui.Zoom(true)
		}
	default:
		ui.Nav(&p.st.Sel, len(p.runs), ev, 10)
	}
	return nil
}

func (p *Pipeline) Draw(b *ui.Buffer, area ui.Rect) {
	if p.view != nil {
		p.drawRun(b, area)
		return
	}
	parts := ui.Vertical(area, 0, ui.Len(4), ui.Min(5))
	sa, ra := parts[0], parts[1]

	// Stage strip as powerline segments coloured by status.
	var flow []ui.Span
	var prev ui.Color
	for i, s := range p.stages {
		segBg, segFg := ui.Surface(), ui.FgC()
		if t := ui.ToneOf(s.status); t != ui.ToneIdle {
			segBg, segFg = ui.ToneColor(t), ui.Bg()
		}
		switch {
		case i == 0:
			flow = append(flow, ui.C(ui.CAP_L.S(), segBg))
		case prev == segBg:
			flow = append(flow, ui.S(ui.ARROW_THIN.S(), ui.Style{Fg: ui.Bg(), Bg: segBg}))
		default:
			flow = append(flow, ui.S(ui.ARROW.S(), ui.Style{Fg: prev, Bg: segBg}))
		}
		label := fmt.Sprintf(" %s %s", ui.StageIcon(s.name).S(), s.name)
		if s.action != "" && s.action != s.name {
			label += " ▸ " + s.action
		}
		flow = append(flow, ui.S(label+" ", ui.Style{Fg: segFg, Bg: segBg, Bold: true}))
		prev = segBg
	}
	if len(p.stages) > 0 {
		flow = append(flow, ui.C(ui.ARROW.S(), prev))
	}
	var last int64
	for _, s := range p.stages {
		last = max(last, s.changed)
	}
	sub := ""
	if last > 0 {
		sub = fmt.Sprintf("%s last change %s · %s ago", ui.CALENDAR.S(), ui.Local(last, "Jan 02 15:04"), ui.Ago(last))
	}
	border := ui.Border()
	if len(p.runs) > 0 {
		switch ui.ToneOf(p.runs[0].status) {
		case ui.ToneBad:
			border = ui.Red()
		case ui.ToneBusy:
			border = ui.Yellow()
		}
	}
	in := ui.Panel{Title: ui.L(ui.T(" " + ui.STAGE.S() + " Stages ")), BorderColor: border}.Draw(b, sa)
	ui.Paragraph(b, ui.Middle(in, 2), []ui.Line{ui.L(flow...).Centered(), ui.LT(sub, ui.Fg(ui.Muted())).Centered()}, false)

	// Runs, with the average duration in the title.
	var sum int64
	for _, r := range p.runs {
		sum += max(0, took(r.status, r.start, r.end))
	}
	panel := ui.Panel{Title: ui.L(ui.T(" "+ui.ROCKET.S()+" Runs "), ui.CountBadge(len(p.runs)), ui.T(" "))}
	if len(p.runs) > 0 {
		avg := sum / int64(len(p.runs))
		panel.Right = ui.LT(fmt.Sprintf(" %s avg %s ", ui.HOURGLASS.S(), ui.Dur(avg)), ui.Fg(ui.Muted()))
	}
	in = panel.Draw(b, ra)
	rows := make([]ui.Row, 0, len(p.runs))
	for _, r := range p.runs {
		what := r.msg
		if what == "" {
			what = r.trigger
		}
		rows = append(rows, ui.R(
			ui.L(ui.StatusIcon(r.status)),
			ui.LT(ui.Local(r.start, "Jan 02 15:04"), ui.Fg(ui.Muted())),
			ui.LT(ui.Dur(took(r.status, r.start, r.end)), ui.Fg(ui.Muted())).RightAligned(),
			ui.LT(what, ui.Style{}),
		))
	}
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Len(12), ui.Len(7), ui.Min(10)},
		Header:    []ui.Line{{}, ui.LT("Started", ui.Style{}), ui.LT("Took", ui.Style{}), ui.LT("Commit", ui.Style{})},
		Rows:      rows,
		Spacing:   1,
		Highlight: true,
	}.Draw(b, in, &p.st)
	ui.Scrollbar(b, ra, len(p.runs), max(p.st.Sel, 0), in.H-1)
}

// drawRun is one run: its header, actions, and the selected action's summary or error.
func (p *Pipeline) drawRun(b *ui.Buffer, area ui.Rect) {
	v := p.view
	if v.log != nil {
		v.log.Draw(b, area)
		return
	}
	parts := ui.Vertical(area, 0, ui.Len(4), ui.Min(4), ui.Len(5))
	h, t, d := parts[0], parts[1], parts[2]
	if v.run < len(p.runs) {
		r := p.runs[v.run]
		head := []ui.Line{
			ui.L(ui.StatusIcon(r.status), ui.S(" "+r.msg, ui.Style{Bold: true})),
			ui.L(ui.C(r.status, ui.StatusColor(r.status)),
				ui.C(fmt.Sprintf("  %s %s  %s %s", ui.CALENDAR.S(), ui.Local(r.start, "Jan 02 15:04"), ui.HOURGLASS.S(), ui.Dur(took(r.status, r.start, r.end))), ui.Muted())),
		}
		in := ui.Panel{
			Title: ui.L(ui.T(" " + ui.ROCKET.S() + " Run ")),
			Right: ui.LT(" "+r.id[:min(8, len(r.id))]+" ", ui.Fg(ui.Muted())),
		}.Draw(b, h)
		ui.Paragraph(b, in, head, false)
	}
	rows := make([]ui.Row, 0, len(v.actions))
	for _, a := range v.actions {
		logCell := ui.Line{}
		if a.buildID != "" {
			logCell = ui.LT(ui.TERMINAL.S()+" log", ui.Fg(ui.Accent()))
		}
		rows = append(rows, ui.R(
			ui.L(ui.StatusIcon(a.status)),
			ui.LT(ui.StageIcon(a.stage).S()+" "+a.stage, ui.Style{Bold: true}),
			ui.LT(a.name, ui.Style{}),
			ui.LT(a.status, ui.Fg(ui.StatusColor(a.status))),
			ui.LT(ui.Dur(took(a.status, a.start, a.end)), ui.Fg(ui.Muted())).RightAligned(),
			logCell,
		))
	}
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Len(10), ui.Min(8), ui.Len(11), ui.Len(7), ui.Len(5)},
		Header:    []ui.Line{{}, ui.LT("Stage", ui.Style{}), ui.LT("Action", ui.Style{}), ui.LT("Status", ui.Style{}), ui.LT("Took", ui.Style{}), {}},
		Rows:      rows,
		Spacing:   1,
		Highlight: true,
		Block:     &ui.Panel{Title: ui.L(ui.T(" " + ui.CHECKLIST.S() + " Actions "))},
	}.Draw(b, t, &v.st)

	if v.st.Sel < 0 || v.st.Sel >= len(v.actions) {
		return
	}
	a := v.actions[v.st.Sel]
	failed := ui.ToneOf(a.status) == ui.ToneBad
	panel := ui.Panel{Title: ui.L(ui.T(" Summary "))}
	style := ui.Fg(ui.Muted())
	if failed {
		panel = ui.Panel{Title: ui.L(ui.T(" " + ui.BUG.S() + " Error ")), BorderColor: ui.Red()}
		style = ui.Fg(ui.Red())
	}
	// One paragraph: commit messages carry newlines.
	text := strings.Join(strings.Fields(a.summary), " ")
	if text == "" {
		text = "—"
	}
	in := panel.Draw(b, d)
	n := ui.WrappedRows(text, in.W)
	line := ui.LT(text, style)
	// Short text sits in the middle of the box; long text fills it from the top.
	if n == 1 {
		line = line.Centered()
	}
	ui.Paragraph(b, ui.Middle(in, n), []ui.Line{line}, true)
}
