package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	lt "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	cp "github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

// fakeCP serves canned CodePipeline responses and records what was asked.
type fakeCP struct {
	state      *cp.GetPipelineStateOutput
	execs      *cp.ListPipelineExecutionsOutput
	actions    map[string][]types.ActionExecutionDetail // by execution id
	stateErr   error
	execsErr   error
	actionsErr error
	actionRuns []string // execution ids ListActionExecutions was asked for
}

func (f *fakeCP) GetPipelineState(_ context.Context, in *cp.GetPipelineStateInput, _ ...func(*cp.Options)) (*cp.GetPipelineStateOutput, error) {
	if aws.ToString(in.Name) != "app" {
		return nil, &smithy.GenericAPIError{Code: "PipelineNotFoundException", Message: "wrong name"}
	}
	return f.state, f.stateErr
}

func (f *fakeCP) ListPipelineExecutions(_ context.Context, in *cp.ListPipelineExecutionsInput, _ ...func(*cp.Options)) (*cp.ListPipelineExecutionsOutput, error) {
	return f.execs, f.execsErr
}

func (f *fakeCP) ListActionExecutions(_ context.Context, in *cp.ListActionExecutionsInput, _ ...func(*cp.Options)) (*cp.ListActionExecutionsOutput, error) {
	id := aws.ToString(in.Filter.PipelineExecutionId)
	f.actionRuns = append(f.actionRuns, id)
	if f.actionsErr != nil {
		return nil, f.actionsErr
	}
	return &cp.ListActionExecutionsOutput{ActionExecutionDetails: f.actions[id]}, nil
}

// fakeLogs pages GetLogEvents backward: pages[token] is the response for that NextToken ("" = first call).
type fakeLogs struct {
	pages  map[string]*cloudwatchlogs.GetLogEventsOutput
	next   func(call int) *cloudwatchlogs.GetLogEventsOutput // used when pages is nil
	err    error
	inputs []*cloudwatchlogs.GetLogEventsInput
}

func (f *fakeLogs) GetLogEvents(_ context.Context, in *cloudwatchlogs.GetLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return nil, f.err
	}
	if f.pages == nil {
		return f.next(len(f.inputs)), nil
	}
	out, ok := f.pages[aws.ToString(in.NextToken)]
	if !ok {
		panic("unexpected token " + aws.ToString(in.NextToken))
	}
	return out, nil
}

func ago(s int64) *time.Time { t := time.Unix(ui.Now()-s, 0); return &t }

func key(k tcell.Key) *tcell.EventKey { return tcell.NewEventKey(k, 0, tcell.ModNone) }

func draw(p *Pipeline, w, h int) *ui.Buffer {
	b := ui.NewBuffer(w, h)
	p.Draw(b, b.Area())
	return b
}

func find(t *testing.T, b *ui.Buffer, s string) (int, int) {
	t.Helper()
	x, y, ok := b.Find(s)
	if !ok {
		t.Fatalf("%q not on screen:\n%s", s, b.Text())
	}
	return x, y
}

func execSummary(id string, st types.PipelineExecutionStatus, start, end int64, summary string) types.PipelineExecutionSummary {
	s := types.PipelineExecutionSummary{
		PipelineExecutionId: aws.String(id),
		Status:              st,
		StartTime:           ago(start),
		LastUpdateTime:      ago(end),
	}
	if summary != "" {
		s.SourceRevisions = []types.SourceRevision{{RevisionSummary: aws.String(summary)}}
	}
	return s
}

func stageState(name string, st types.StageExecutionStatus, actions ...types.ActionState) types.StageState {
	s := types.StageState{StageName: aws.String(name), ActionStates: actions}
	if st != "" {
		s.LatestExecution = &types.StageExecution{Status: st}
	}
	return s
}

func actionState(name string, st types.ActionExecutionStatus, changed int64) types.ActionState {
	return types.ActionState{ActionName: aws.String(name), LatestExecution: &types.ActionExecution{Status: st, LastStatusChange: ago(changed)}}
}

func actionDetail(stage, name, provider string, st types.ActionExecutionStatus, start int64, ext, summary string) types.ActionExecutionDetail {
	return types.ActionExecutionDetail{
		StageName:      aws.String(stage),
		ActionName:     aws.String(name),
		Status:         st,
		StartTime:      ago(start),
		LastUpdateTime: ago(start - 30),
		Input:          &types.ActionExecutionInput{ActionTypeId: &types.ActionTypeId{Provider: aws.String(provider)}},
		Output: &types.ActionExecutionOutput{ExecutionResult: &types.ActionExecutionResult{
			ExternalExecutionId:      aws.String(ext),
			ExternalExecutionSummary: aws.String(summary),
			ExternalExecutionUrl:     aws.String("https://example.test/" + name),
		}},
	}
}

// A pipeline mid-run: Source done, Build running, Deploy not started; three runs.
func running() *fakeCP {
	return &fakeCP{
		state: &cp.GetPipelineStateOutput{StageStates: []types.StageState{
			stageState("Source", types.StageExecutionStatusSucceeded, actionState("Source", types.ActionExecutionStatusSucceeded, 400)),
			stageState("Build", types.StageExecutionStatusInProgress,
				actionState("Lint", types.ActionExecutionStatusSucceeded, 200),
				actionState("Compile", types.ActionExecutionStatusInProgress, 100)),
			stageState("Deploy", ""),
		}},
		execs: &cp.ListPipelineExecutionsOutput{PipelineExecutionSummaries: []types.PipelineExecutionSummary{
			execSummary("run-new-0001", types.PipelineExecutionStatusInProgress, 300, 0,
				`{"ProviderType":"GitHub","CommitMessage":"Fix login\r\n\r\nLonger body"}`),
			execSummary("run-old-0002", types.PipelineExecutionStatusSucceeded, 3600, 3480, "Plain summary"),
			{PipelineExecutionId: aws.String("run-man-0003"), Status: types.PipelineExecutionStatusFailed,
				StartTime: ago(7200), LastUpdateTime: ago(7140),
				Trigger: &types.ExecutionTrigger{TriggerType: types.TriggerTypeStartPipelineExecution}},
		}},
		actions: map[string][]types.ActionExecutionDetail{
			"run-old-0002": {
				// Out of order: the pane sorts by start.
				actionDetail("Build", "Compile", "CodeBuild", types.ActionExecutionStatusFailed, 3500, "proj:uuid-1", "Tests failed\n  in   module"),
				actionDetail("Source", "Source", "CodeStarSourceConnection", types.ActionExecutionStatusSucceeded, 3590, "abc123",
					`{"ProviderType":"GitHub","CommitMessage":"Plain summary"}`),
				actionDetail("Deploy", "Ship", "ECS", types.ActionExecutionStatusSucceeded, 3400, "x:y", "deployed"),
			},
		},
	}
}

func newPane(c *fakeCP, l *fakeLogs) *Pipeline {
	return &Pipeline{name: "app", region: "eu-west-1", cp: c, logs: l, st: ui.NewTableState()}
}

func TestRefreshStagesAndRuns(t *testing.T) {
	p := newPane(running(), nil)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if len(p.stages) != 3 || p.stages[1].action != "Compile" {
		t.Fatalf("stages %+v", p.stages)
	}
	if want := ui.Now() - 100; p.stages[1].changed < want-1 || p.stages[1].changed > want {
		t.Errorf("stage changed = latest action change, got %d want %d", p.stages[1].changed, want)
	}
	if p.stages[2].status != "" || p.stages[2].changed != 0 {
		t.Errorf("unstarted stage %+v", p.stages[2])
	}
	// Commit message: first line of the JSON CommitMessage, \r trimmed; plain summaries as-is.
	if p.runs[0].msg != "Fix login" || p.runs[1].msg != "Plain summary" || p.runs[2].msg != "" || p.runs[2].trigger != "StartPipelineExecution" {
		t.Errorf("runs %+v", p.runs)
	}

	b := draw(p, 120, 20)
	// Segments in pipeline order, each coloured by its stage's status.
	sx, sy := find(t, b, "Source")
	bx, by := find(t, b, "Build ▸ Compile")
	dx, dy := find(t, b, "Deploy")
	if sy != by || by != dy || !(sx < bx && bx < dx) {
		t.Errorf("stage order: Source@%d,%d Build@%d,%d Deploy@%d,%d", sx, sy, bx, by, dx, dy)
	}
	for _, c := range []struct {
		x  int
		bg ui.Color
	}{{sx, ui.Green()}, {bx, ui.Yellow()}, {dx, ui.Surface()}} {
		if st := b.StyleAt(c.x, sy); st.Bg != c.bg || !st.Bold {
			t.Errorf("segment at %d: style %+v, want bg %v", c.x, st, c.bg)
		}
	}
	// Green → yellow is a solid arrow drawn in the previous segment's colour.
	// It's the first cell left of the Build label that isn't bold label text.
	ax := bx - 1
	for ax > sx && b.StyleAt(ax, sy).Bold {
		ax--
	}
	if arrow := b.StyleAt(ax, sy); arrow.Fg != ui.Green() || arrow.Bg != ui.Yellow() {
		t.Errorf("arrow style %+v", arrow)
	}
	if !strings.Contains(b.Text(), "last change") {
		t.Errorf("no last-change subtitle:\n%s", b.Text())
	}
	// The newest run is in progress: the stages border is yellow.
	if st := b.StyleAt(0, 0); st.Fg != ui.Yellow() {
		t.Errorf("border %+v", st)
	}

	// Runs: count, avg of (300 + 120 + 60)/3 = 160s, message or trigger per row.
	text := b.Text()
	for _, want := range []string{"Runs", "avg 2m40s", "Fix login", "Plain summary", "StartPipelineExecution", "2m00s", "1m00s"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Longer body") || strings.Contains(text, "ProviderType") {
		t.Errorf("raw summary leaked:\n%s", text)
	}
	_, fy := find(t, b, "Fix login")
	_, py := find(t, b, "Plain summary")
	_, my := find(t, b, "StartPipelineExecution")
	if !(fy < py && py < my) {
		t.Errorf("runs not in API order (newest first)")
	}
}

func TestStatus(t *testing.T) {
	p := newPane(running(), nil)
	if s := p.Status(); s.State != "idle" || s.Msg != "no runs" {
		t.Errorf("before load %+v", s)
	}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "working" || !strings.HasPrefix(s.Msg, "Build running 5m0") {
		t.Errorf("running %+v", s)
	}

	c := running()
	c.execs.PipelineExecutionSummaries = c.execs.PipelineExecutionSummaries[1:] // newest = Succeeded
	p = newPane(c, nil)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "idle" || s.Msg != "Succeeded 58m ago · Plain summary" {
		t.Errorf("succeeded %+v", s)
	}

	c.execs.PipelineExecutionSummaries = c.execs.PipelineExecutionSummaries[1:] // newest = Failed
	c.execs.PipelineExecutionSummaries[0].SourceRevisions = []types.SourceRevision{{RevisionSummary: aws.String(strings.Repeat("x", 80))}}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "error" || s.Msg != "Failed 1h ago · "+strings.Repeat("x", 59)+"…" {
		t.Errorf("failed %+v", s)
	}
	if st := draw(p, 120, 20).StyleAt(0, 0); st.Fg != ui.Red() {
		t.Errorf("failed run should turn the stages border red, got %+v", st)
	}
}

func TestRefreshErrors(t *testing.T) {
	c := running()
	c.stateErr = &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not allowed"}
	if err := newPane(c, nil).Refresh(); err == nil || err.Error() != "AccessDeniedException: not allowed" {
		t.Errorf("state error = %v", err)
	}
	c = running()
	c.execsErr = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}
	if err := newPane(c, nil).Refresh(); err == nil || err.Error() != "ThrottlingException: slow down" {
		t.Errorf("executions error = %v", err)
	}
}

func TestActionsAndBuildLog(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	c := running()
	// Backward pages: data, an empty page whose token still moves, more data, then the token repeats.
	logs := &fakeLogs{pages: map[string]*cloudwatchlogs.GetLogEventsOutput{
		"": {Events: []lt.OutputLogEvent{{Message: aws.String("ERROR: boom\n")}, {Message: aws.String("[Container] Phase complete")}},
			NextBackwardToken: aws.String("b1")},
		"b1": {NextBackwardToken: aws.String("b2")},
		"b2": {Events: []lt.OutputLogEvent{{Message: aws.String("warning: old dep\r\nline\ttwo")}, {Message: aws.String("")}},
			NextBackwardToken: aws.String("b3")},
		"b3": {NextBackwardToken: aws.String("b3")},
	}}
	p := newPane(c, logs)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	p.Key(key(tcell.KeyDown)) // select the second run
	if err := p.Key(key(tcell.KeyEnter)); err != nil {
		t.Fatal(err)
	}
	if p.view == nil || p.view.run != 1 || c.actionRuns[0] != "run-old-0002" {
		t.Fatalf("view %+v, asked for %v", p.view, c.actionRuns)
	}
	v := p.view
	if v.actions[0].name != "Source" || v.actions[1].name != "Compile" || v.actions[2].name != "Ship" {
		t.Errorf("actions not sorted by start: %+v", v.actions)
	}
	// Starts on the first failed action; only CodeBuild actions with project:uuid get a build id.
	if v.st.Sel != 1 || v.actions[1].buildID != "proj:uuid-1" || v.actions[0].buildID != "" || v.actions[2].buildID != "" {
		t.Errorf("sel %d, actions %+v", v.st.Sel, v.actions)
	}
	if v.actions[0].summary != "Plain summary" {
		t.Errorf("source summary %q", v.actions[0].summary)
	}
	b := draw(p, 110, 24)
	text := b.Text()
	for _, want := range []string{"Run", "run-old-", "Plain summary", "Actions", "Compile", "Failed", "log", "Error", "Tests failed in module"} {
		if !strings.Contains(text, want) {
			t.Errorf("run view missing %q:\n%s", want, text)
		}
	}
	if x, y := find(t, b, "Tests failed"); b.StyleAt(x, y).Fg != ui.Red() {
		t.Error("failed action's summary should be red")
	}
	if strings.Count(text, "log") != 1 {
		t.Errorf("only the CodeBuild action has a log cell:\n%s", text)
	}
	if hs := p.Hints(); hs[1].Desc != "build log" {
		t.Errorf("hints %+v", hs)
	}

	// Enter on a non-CodeBuild action (Source) does nothing.
	p.Key(key(tcell.KeyUp))
	if err := p.Key(key(tcell.KeyEnter)); err != nil || v.log != nil || len(logs.inputs) != 0 {
		t.Fatalf("enter on ECS action: err %v log %v calls %d", err, v.log, len(logs.inputs))
	}
	p.Key(key(tcell.KeyDown))
	if err := p.Key(key(tcell.KeyEnter)); err != nil {
		t.Fatal(err)
	}
	if v.log == nil {
		t.Fatal("no log view")
	}
	var tokens []string
	for _, in := range logs.inputs {
		if aws.ToString(in.LogGroupName) != "/aws/codebuild/proj" || aws.ToString(in.LogStreamName) != "uuid-1" || aws.ToBool(in.StartFromHead) {
			t.Errorf("input %+v", in)
		}
		tokens = append(tokens, aws.ToString(in.NextToken))
	}
	if strings.Join(tokens, ",") != ",b1,b2,b3" {
		t.Errorf("tokens %q: should page through the empty b1 page and stop when b3 repeats", tokens)
	}
	// Oldest page first; multi-line messages split, \r and \t cleaned, empty messages dropped.
	var got []string
	for _, l := range v.log.Lines {
		got = append(got, l.String())
	}
	if strings.Join(got, "|") != "warning: old dep|line    two|ERROR: boom|[Container] Phase complete" {
		t.Errorf("log lines %q", got)
	}
	for i, want := range []ui.Color{ui.Yellow(), 0, ui.Red(), ui.Cyan()} {
		if c := v.log.Lines[i].Spans[0].Style.Fg; c != want {
			t.Errorf("line %d colour %v want %v", i, c, want)
		}
	}
	b = draw(p, 80, 10)
	if !strings.Contains(b.Text(), "build log proj") || !strings.Contains(b.Text(), "[Container] Phase complete") {
		t.Errorf("log view:\n%s", b.Text())
	}
	if hs := p.Hints(); hs[0].Desc != "scroll" {
		t.Errorf("log hints %+v", hs)
	}

	// Refresh keeps the log open and doesn't reload actions under it.
	n := len(c.actionRuns)
	if err := p.Refresh(); err != nil || len(c.actionRuns) != n || v.log == nil {
		t.Errorf("refresh with log open: err %v, reloads %d", err, len(c.actionRuns)-n)
	}

	// Esc: log → actions → runs.
	p.Key(key(tcell.KeyEscape))
	if p.view == nil || p.view.log != nil {
		t.Fatal("esc should close only the log")
	}
	p.Key(key(tcell.KeyEscape))
	if p.view != nil {
		t.Fatal("esc should close the run view")
	}
	if !strings.Contains(draw(p, 110, 20).Text(), "Runs") {
		t.Error("not back on the runs table")
	}
}

func TestBuildLogStopsWhenTokenEnds(t *testing.T) {
	// No backward token at all: one call.
	l := &fakeLogs{next: func(int) *cloudwatchlogs.GetLogEventsOutput {
		return &cloudwatchlogs.GetLogEventsOutput{Events: []lt.OutputLogEvent{{Message: aws.String("only")}}}
	}}
	v, err := newPane(nil, l).buildLog("proj:id")
	if err != nil || len(l.inputs) != 1 || len(v.Lines) != 1 {
		t.Errorf("err %v calls %d lines %d", err, len(l.inputs), len(v.Lines))
	}
	// A token that changes forever with empty pages is bounded.
	l = &fakeLogs{next: func(i int) *cloudwatchlogs.GetLogEventsOutput {
		return &cloudwatchlogs.GetLogEventsOutput{NextBackwardToken: aws.String(strings.Repeat("t", i))}
	}}
	if _, err := newPane(nil, l).buildLog("proj:id"); err != nil || len(l.inputs) != 20 {
		t.Errorf("err %v calls %d, want 20", err, len(l.inputs))
	}
	// Enough lines stops early.
	l = &fakeLogs{next: func(i int) *cloudwatchlogs.GetLogEventsOutput {
		ev := make([]lt.OutputLogEvent, 1000)
		for j := range ev {
			ev[j].Message = aws.String("x")
		}
		return &cloudwatchlogs.GetLogEventsOutput{Events: ev, NextBackwardToken: aws.String(strings.Repeat("t", i))}
	}}
	if _, err := newPane(nil, l).buildLog("proj:id"); err != nil || len(l.inputs) != 3 {
		t.Errorf("err %v calls %d, want 3", err, len(l.inputs))
	}
	if _, err := newPane(nil, l).buildLog("no-colon"); err == nil {
		t.Error("bad build id accepted")
	}
}

func TestActionErrors(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	c := running()
	c.actionsErr = &smithy.GenericAPIError{Code: "PipelineExecutionNotFoundException", Message: "gone"}
	p := newPane(c, &fakeLogs{err: &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "no stream"}})
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	p.Key(key(tcell.KeyDown))
	if err := p.Key(key(tcell.KeyEnter)); err == nil || err.Error() != "PipelineExecutionNotFoundException: gone" || p.view != nil {
		t.Fatalf("enter: err %v view %v", err, p.view)
	}
	c.actionsErr = nil
	if err := p.Key(key(tcell.KeyEnter)); err != nil {
		t.Fatal(err)
	}
	if err := p.Key(key(tcell.KeyEnter)); err == nil || err.Error() != "ResourceNotFoundException: no stream" || p.view.log != nil {
		t.Errorf("log: err %v", err)
	}
	// A failing reload while the run view is open is surfaced by Refresh.
	c.actionsErr = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow"}
	if err := p.Refresh(); err == nil || err.Error() != "ThrottlingException: slow" {
		t.Errorf("refresh: %v", err)
	}
}

func TestOpenRunFollowsItsExecution(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	c := running()
	p := newPane(c, nil)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	p.Key(key(tcell.KeyDown))
	if err := p.Key(key(tcell.KeyEnter)); err != nil {
		t.Fatal(err)
	}
	// A new execution starts while the run view is open: it lands at the top of the list.
	c.execs.PipelineExecutionSummaries = append([]types.PipelineExecutionSummary{
		execSummary("run-newest-0", types.PipelineExecutionStatusInProgress, 10, 0, "Another commit"),
	}, c.execs.PipelineExecutionSummaries...)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := c.actionRuns[len(c.actionRuns)-1]; got != "run-old-0002" {
		t.Errorf("reloaded actions of %s, want the open run run-old-0002", got)
	}
	if text := draw(p, 110, 24).Text(); !strings.Contains(text, "run-old-") || strings.Contains(text, "Another commit") {
		t.Errorf("run view switched runs:\n%s", text)
	}
}

// A run can be in progress between stages (no stage InProgress yet): the status still reads
// sensibly instead of " running 5m" with a blank stage name.
func TestStatusRunningBetweenStages(t *testing.T) {
	c := running()
	c.state.StageStates[1].LatestExecution.Status = types.StageExecutionStatusSucceeded
	p := newPane(c, nil)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	st := p.Status()
	if st.State != "working" || !strings.HasPrefix(st.Msg, "running ") {
		t.Fatalf("status %+v, want working \"running …\"", st)
	}
}
