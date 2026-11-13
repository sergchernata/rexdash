package infra

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"

	"rexdash/internal/ui"
)

type fakeECS struct {
	svc               *ecstypes.Service
	running, stopped  []string
	tasks             []ecstypes.Task
	descErr, listErr  error
	tasksErr          error
	listed            []ecstypes.DesiredStatus
	describedTasks    []string
	describedServices []string
}

func (f *fakeECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.describedServices = append(f.describedServices, in.Services...)
	if f.descErr != nil {
		return nil, f.descErr
	}
	out := &ecs.DescribeServicesOutput{}
	if f.svc != nil {
		out.Services = []ecstypes.Service{*f.svc}
	}
	return out, nil
}

func (f *fakeECS) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.listed = append(f.listed, in.DesiredStatus)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if in.DesiredStatus == ecstypes.DesiredStatusRunning {
		return &ecs.ListTasksOutput{TaskArns: f.running}, nil
	}
	return &ecs.ListTasksOutput{TaskArns: f.stopped}, nil
}

func (f *fakeECS) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.describedTasks = in.Tasks
	if f.tasksErr != nil {
		return nil, f.tasksErr
	}
	return &ecs.DescribeTasksOutput{Tasks: f.tasks}, nil
}

type fakeRDS struct {
	db         *rdstypes.DBInstance
	err        error
	params     map[string]string // max_connections by parameter group
	paramErr   error
	paramCalls map[string]int
}

func (f *fakeRDS) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	if aws.ToString(in.DBInstanceIdentifier) != "appdb" {
		return nil, &smithy.GenericAPIError{Code: "DBInstanceNotFound", Message: "wrong id"}
	}
	return &rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{*f.db}}, nil
}

// fakeCW answers the 3-hour query with m and the weekly free-storage query with free7d.
type fakeCW struct {
	m        map[string][]float64
	free7d   []float64
	err      error
	err7d    error
	inputs   []*cloudwatch.GetMetricDataInput
	returned []string // ids the 3h call asked to return
}

func (f *fakeCW) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.inputs = append(f.inputs, in)
	if aws.ToString(in.MetricDataQueries[0].Id) == "free7d" {
		if f.err7d != nil {
			return nil, f.err7d
		}
		return &cloudwatch.GetMetricDataOutput{MetricDataResults: []cwtypes.MetricDataResult{{Id: aws.String("free7d"), Values: f.free7d}}}, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	out := &cloudwatch.GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		id := aws.ToString(q.Id)
		if !aws.ToBool(q.ReturnData) {
			continue
		}
		f.returned = append(f.returned, id)
		if v, ok := f.m[id]; ok {
			out.MetricDataResults = append(out.MetricDataResults, cwtypes.MetricDataResult{Id: aws.String(id), Values: v})
		}
	}
	return out, nil
}

func ago(s int64) *time.Time { t := time.Unix(ui.Now()-s, 0); return &t }

const taskPrefix = "arn:aws:ecs:eu-west-1:123:task/main/"

// A healthy service with three tasks, a 100 GB r6g.large database, and no Container Insights.
func healthy() (*fakeECS, *fakeRDS, *fakeCW) {
	e := &fakeECS{
		svc: &ecstypes.Service{
			RunningCount: 2, DesiredCount: 2,
			TaskDefinition: aws.String("arn:aws:ecs:eu-west-1:123:task-definition/app:42"),
			Deployments: []ecstypes.Deployment{
				{Status: aws.String("ACTIVE"), RolloutState: ecstypes.DeploymentRolloutStateInProgress, CreatedAt: ago(60)},
				{Status: aws.String("PRIMARY"), RolloutState: ecstypes.DeploymentRolloutStateCompleted, CreatedAt: ago(7200)},
			},
			Events: []ecstypes.ServiceEvent{
				{CreatedAt: ago(600), Message: aws.String("(service app) has reached a steady state.")},
				{CreatedAt: ago(1800), Message: aws.String("(service app) has started 1 tasks: (task aaaa).")},
				{CreatedAt: ago(1900), Message: aws.String("(service app) has stopped 1 running tasks: (task cccc).")},
				{CreatedAt: ago(30 * 3600), Message: aws.String("(service app) has started 2 tasks: (task dddd).")},
			},
		},
		running: []string{taskPrefix + "aaaaaaaaaaaa", taskPrefix + "bbbbbbbbbbbb"},
		stopped: []string{taskPrefix + "cccccccccccc"},
		tasks: []ecstypes.Task{
			{TaskArn: aws.String(taskPrefix + "cccccccccccc"), LastStatus: aws.String("STOPPED"), StartedAt: ago(7000), StoppedAt: ago(1900),
				StoppedReason: aws.String("Essential container in task exited"),
				Containers:    []ecstypes.Container{{ExitCode: aws.Int32(137), Reason: aws.String("OOM")}}},
			{TaskArn: aws.String(taskPrefix + "aaaaaaaaaaaa"), LastStatus: aws.String("RUNNING"), HealthStatus: ecstypes.HealthStatusHealthy, StartedAt: ago(3600)},
			{TaskArn: aws.String(taskPrefix + "bbbbbbbbbbbb"), LastStatus: aws.String("RUNNING"), HealthStatus: ecstypes.HealthStatusUnhealthy, StartedAt: ago(1800)},
		},
	}
	r := &fakeRDS{db: &rdstypes.DBInstance{
		DBInstanceStatus: aws.String("available"), DBInstanceClass: aws.String("db.r6g.large"),
		Engine: aws.String("postgres"), EngineVersion: aws.String("16.3"),
		AllocatedStorage: aws.Int32(100), MaxAllocatedStorage: aws.Int32(200),
	}}
	// A week of hourly free storage: 30 GiB down to 25 GiB, i.e. 5 GB used over 7 days.
	free7d := make([]float64, 169)
	for h := range free7d {
		free7d[h] = (30 - 5*float64(h)/168) * gib
	}
	c := &fakeCW{
		m: map[string][]float64{
			"cpu":       {10, 20, 30},
			"mem":       {40, 50, 60},
			"dbcpu":     {80, 82},
			"dbconn":    {12, 14},
			"dbfreemem": {6 * gib, 4 * gib}, // of 16 GiB: 75% used
			"dbfree":    {26 * gib, 25 * gib},
		},
		free7d: free7d,
	}
	return e, r, c
}

func newPane(e *fakeECS, r *fakeRDS, c *fakeCW) *Infra {
	return &Infra{cluster: "main", service: "app", db: "appdb", region: "eu-west-1",
		ecs: e, rds: r, cw: c, m: map[string][]float64{}, st: ui.NewTableState()}
}

func draw(p *Infra) *ui.Buffer {
	b := ui.NewBuffer(150, 26)
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

func TestRefreshHealthy(t *testing.T) {
	e, r, c := healthy()
	p := newPane(e, r, c)
	if s := p.Status(); s.State != "working" || s.Msg != "loading" {
		t.Errorf("before load %+v", s)
	}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "idle" || s.Msg != "2/2 tasks · CPU 30% · MEM 60% · DB 75% full" {
		t.Errorf("status %+v", s)
	}
	if strings.Join(e.describedServices, ",") != "app" {
		t.Errorf("described %v", e.describedServices)
	}
	if len(e.listed) != 2 || e.listed[0] != ecstypes.DesiredStatusRunning || e.listed[1] != ecstypes.DesiredStatusStopped {
		t.Errorf("ListTasks statuses %v", e.listed)
	}
	if len(e.describedTasks) != 3 {
		t.Errorf("DescribeTasks got %d arns", len(e.describedTasks))
	}
	// The 3h query: the Container Insights inputs feed the disk expression but aren't returned.
	if got := strings.Join(c.returned, ","); got != "cpu,mem,dbcpu,dbconn,dbfreemem,dbfree,disk" {
		t.Errorf("returned ids %s", got)
	}
	for _, in := range c.inputs {
		if in.ScanBy != cwtypes.ScanByTimestampAscending {
			t.Error("metrics must be oldest first so the last value is current")
		}
	}

	// Tasks: running first (newest first), short ids, stopped with exit code and stop reason.
	var ids []string
	for _, tk := range p.tasks {
		ids = append(ids, tk.id)
	}
	if strings.Join(ids, ",") != "bbbbbbbb,aaaaaaaa,cccccccc" {
		t.Errorf("task order %v", ids)
	}
	if p.tasks[2].reason != "exit 137 Essential container in task exited" {
		t.Errorf("stopped reason %q", p.tasks[2].reason)
	}
	if p.events[1].msg != "has started 1 tasks: (task aaaa)." {
		t.Errorf("event prefix not trimmed: %q", p.events[1].msg)
	}

	b := draw(p)
	text := b.Text()
	for _, want := range []string{
		"2/2 running", "rev 42", "completed 2h ago", // PRIMARY deployment, not the ACTIVE one
		"available", "postgres 16.3", "db.r6g.large",
		"75/100 GB · +0.7 GB/day · max 200",
		"up 30m", "up 1h00m", "31m ago · exit 137 Essential co",
		"1 starts/24h", "has reached a steady state.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	// No Container Insights data: no app storage tile, CPU and memory share the row.
	if strings.Contains(text, "App storage") {
		t.Errorf("app storage tile shown without data:\n%s", text)
	}

	// Threshold colours: ECS CPU 30% green, RDS memory 75% yellow, RDS CPU 82% yellow-orange, conns accent.
	for _, c := range []struct {
		val  string
		want ui.Color
	}{{"30%", ui.Green()}, {"60%", ui.Heat(60)}, {"75%", ui.Heat(75)}, {"82%", ui.Heat(82)}} {
		x, y := find(t, b, c.val)
		if st := b.StyleAt(x, y); st.Fg != c.want || !st.Bold {
			t.Errorf("%s: style %+v, want %v", c.val, st, c.want)
		}
	}
	x, y := find(t, b, "14")
	if st := b.StyleAt(x, y); st.Fg != ui.Accent() {
		t.Errorf("connections have no threshold, want accent: %+v", st)
	}

	// Health hearts: green for HEALTHY, red for UNHEALTHY, none for the stopped task.
	heart := HEART.S()
	for _, c := range []struct {
		id   string
		want ui.Color
	}{{"aaaaaaaa", ui.Green()}, {"bbbbbbbb", ui.Red()}} {
		_, y := find(t, b, c.id)
		row := []rune(strings.Split(text, "\n")[y])
		i := strings.Index(string(row), "running "+heart)
		if i < 0 {
			t.Errorf("%s row has no heart: %q", c.id, string(row))
			continue
		}
		col := len([]rune(string(row)[:i])) + len("running ")
		if st := b.StyleAt(col, y); st.Fg != c.want {
			t.Errorf("%s heart %+v want %v", c.id, st, c.want)
		}
	}
	if _, y := find(t, b, "cccccccc"); strings.Contains(strings.Split(text, "\n")[y], heart) {
		t.Error("stopped task without health shows a heart")
	}

	// Event icons: start green, stop red, steady state muted text.
	for _, c := range []struct {
		msg  string
		icon string
		want ui.Color
	}{{"has started 1 tasks", PLAY.S(), ui.Green()}, {"has stopped 1 running", STOP.S(), ui.Red()}, {"has reached a steady", ui.OK.S(), ui.Muted()}} {
		mx, my := find(t, b, c.msg)
		ix := mx - 15 // icon, space, 12-wide time, space
		if got := b.StyleAt(ix, my); got.Fg != c.want {
			t.Errorf("%q icon colour %v want %v", c.msg, got.Fg, c.want)
		}
		var at int64
		for _, e := range p.events {
			if strings.HasPrefix(e.msg, c.msg) {
				at = e.at
			}
		}
		if row, want := strings.Split(text, "\n")[my], c.icon+" "+ui.Local(at, "Jan 02 15:04")+" "+c.msg; !strings.Contains(row, want) {
			t.Errorf("want %q in %q", want, row)
		}
	}
}

func TestAppStorageTileWithContainerInsights(t *testing.T) {
	e, r, c := healthy()
	c.m["disk"] = []float64{40, 91}
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	b := draw(p)
	find(t, b, "App storage")
	find(t, b, "ephemeral · 3h")
	x, y := find(t, b, "91%")
	if b.StyleAt(x, y).Fg != ui.Heat(91) {
		t.Error("91% app storage should be shaded near red")
	}
	// Before anything has loaded the tile keeps its place.
	if !strings.Contains(draw(newPane(e, r, c)).Text(), "App storage") {
		t.Error("tile hidden before first load")
	}
	if !strings.Contains(draw(newPane(e, r, c)).Text(), "loading…") {
		t.Error("no loading marker")
	}
}

func TestStatusStates(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(*fakeECS, *fakeRDS, *fakeCW)
		state string
		msg   string
	}{
		{"below desired", func(e *fakeECS, _ *fakeRDS, _ *fakeCW) { e.svc.RunningCount = 1 }, "error", "1/2 tasks"},
		{"pending", func(e *fakeECS, _ *fakeRDS, _ *fakeCW) { e.svc.PendingCount = 1 }, "working", "2/2 tasks"},
		{"rolling out", func(e *fakeECS, _ *fakeRDS, _ *fakeCW) {
			e.svc.Deployments[1].RolloutState = ecstypes.DeploymentRolloutStateInProgress
		}, "working", "2/2 tasks"},
		{"cpu hot", func(_ *fakeECS, _ *fakeRDS, c *fakeCW) { c.m["cpu"] = []float64{95} }, "error", "2/2 tasks · CPU 95%"},
		{"memory hot", func(_ *fakeECS, _ *fakeRDS, c *fakeCW) { c.m["mem"] = []float64{91} }, "error", "2/2 tasks · CPU 30% · MEM 91%"},
		{"db full", func(_ *fakeECS, _ *fakeRDS, c *fakeCW) { c.m["dbfree"] = []float64{5 * gib} }, "error", "2/2 tasks · CPU 30% · MEM 60% · DB 95% full"},
		{"no metrics", func(_ *fakeECS, _ *fakeRDS, c *fakeCW) { c.m = nil }, "idle", "2/2 tasks"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, r, cw := healthy()
			c.edit(e, r, cw)
			p := newPane(e, r, cw)
			if err := p.Refresh(); err != nil {
				t.Fatal(err)
			}
			if s := p.Status(); s.State != c.state || !strings.HasPrefix(s.Msg, c.msg) {
				t.Errorf("status %+v, want %s %q", s, c.state, c.msg)
			}
		})
	}
	// The service header goes red below desired.
	e, r, c := healthy()
	e.svc.RunningCount = 1
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	b := draw(p)
	x, y := find(t, b, "1/2 running")
	if b.StyleAt(x, y).Fg != ui.Red() {
		t.Error("1/2 running should be red")
	}
	// Missing metrics read as "—".
	e, r, c = healthy()
	c.m = nil
	p = newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(draw(p).Text(), "—"); n < 5 {
		t.Errorf("want — for every tile without data, got %d:\n%s", n, draw(p).Text())
	}
}

func TestDescribeTasksCappedAt100(t *testing.T) {
	e, r, c := healthy()
	e.running, e.stopped = nil, nil
	for i := range 80 {
		e.running = append(e.running, fmt.Sprintf("%sr%03d", taskPrefix, i))
	}
	for i := range 50 {
		e.stopped = append(e.stopped, fmt.Sprintf("%ss%03d", taskPrefix, i))
	}
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if len(e.describedTasks) != 100 || e.describedTasks[79] != taskPrefix+"r079" || e.describedTasks[80] != taskPrefix+"s000" {
		t.Errorf("DescribeTasks got %d arns; running ones must come first", len(e.describedTasks))
	}

	// No tasks at all: DescribeTasks isn't called and the table shows its empty state.
	e, r, c = healthy()
	e.running, e.stopped = nil, nil
	p = newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if e.describedTasks != nil || len(p.tasks) != 0 {
		t.Error("DescribeTasks called with no arns")
	}
	find(t, draw(p), "No tasks")
}

func TestStorageWithoutWeekOfData(t *testing.T) {
	e, r, c := healthy()
	c.free7d = c.free7d[:24] // a day isn't enough for a growth rate
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	text := draw(p).Text()
	if !strings.Contains(text, "75/100 GB · max 200") || strings.Contains(text, "GB/day") {
		t.Errorf("storage sub:\n%s", text)
	}
	// Unknown class (serverless): memory is unknown, not 0%.
	r.db.DBInstanceClass = aws.String("db.serverless")
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.dbMem(); ok {
		t.Error("memory % for a class with no fixed size")
	}
}

func TestRefreshErrors(t *testing.T) {
	apiErr := &smithy.GenericAPIError{Code: "AccessDenied", Message: "nope"}
	for _, c := range []struct {
		name string
		edit func(*fakeECS, *fakeRDS, *fakeCW)
		want string
	}{
		{"rds", func(_ *fakeECS, r *fakeRDS, _ *fakeCW) { r.err = apiErr }, "AccessDenied: nope"},
		{"describe services", func(e *fakeECS, _ *fakeRDS, _ *fakeCW) { e.descErr = apiErr }, "AccessDenied: nope"},
		{"service missing", func(e *fakeECS, _ *fakeRDS, _ *fakeCW) { e.svc = nil }, "service app not found"},
		{"list tasks", func(e *fakeECS, _ *fakeRDS, _ *fakeCW) { e.listErr = apiErr }, "AccessDenied: nope"},
		{"describe tasks", func(e *fakeECS, _ *fakeRDS, _ *fakeCW) { e.tasksErr = apiErr }, "AccessDenied: nope"},
		{"metrics", func(_ *fakeECS, _ *fakeRDS, c *fakeCW) { c.err = apiErr }, "AccessDenied: nope"},
		{"weekly metrics", func(_ *fakeECS, _ *fakeRDS, c *fakeCW) { c.err7d = apiErr }, "AccessDenied: nope"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, r, cw := healthy()
			c.edit(e, r, cw)
			if err := newPane(e, r, cw).Refresh(); err == nil || err.Error() != c.want {
				t.Errorf("err %v, want %q", err, c.want)
			}
		})
	}
}

// A database reporting 0 GB allocated (e.g. while it's created) shows no storage figure
// rather than NaN/Inf.
func TestZeroAllocatedStorage(t *testing.T) {
	e, r, c := healthy()
	r.db.AllocatedStorage = aws.Int32(0)
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := p.dbStorage(); ok {
		t.Error("storage percent computed for 0 GB allocated")
	}
	out := draw(p).Text() + p.Status().Msg
	if strings.Contains(out, "NaN") || strings.Contains(out, "Inf") {
		t.Errorf("NaN/Inf on screen:\n%s", out)
	}
}
