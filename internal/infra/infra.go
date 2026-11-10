// Package infra is the Infra pane: ECS service health (CPU, memory, app storage, tasks, events)
// and RDS health (storage, CPU, connections, memory), via the AWS SDK.
package infra

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/awsx"
	"rexdash/internal/ui"
)

var (
	CPU     = ui.Ic{Glyph: "", Plain: ""}
	MEM     = ui.Ic{Glyph: "", Plain: ""}
	DISK    = ui.Ic{Glyph: "", Plain: ""}
	DB      = ui.Ic{Glyph: "", Plain: ""}
	PLUG    = ui.Ic{Glyph: "", Plain: ""}
	BOX     = ui.Ic{Glyph: "", Plain: ""}
	PLAY    = ui.Ic{Glyph: "", Plain: "▶"}
	STOP    = ui.Ic{Glyph: "", Plain: "■"}
	HISTORY = ui.Ic{Glyph: "", Plain: ""}
	HEART   = ui.Ic{Glyph: "", Plain: "♥"}
)

const gib = 1 << 30

// LoadColor shades from green through yellow and orange to red as something fills up.
func LoadColor(pct float64) ui.Color { return ui.Heat(pct) }

// LoadLevel is how full: ok below 70%, busy from 70%, bad from 85%.
func LoadLevel(pct float64) ui.Tone {
	switch {
	case pct >= 85:
		return ui.ToneBad
	case pct >= 70:
		return ui.ToneBusy
	}
	return ui.ToneOk
}

// PerDay is the average daily change of an hourly series; needs more than a day of points.
func PerDay(hourly []float64) (float64, bool) {
	n := len(hourly)
	if n <= 24 {
		return 0, false
	}
	// n hourly points span n-1 hours.
	return (hourly[n-1] - hourly[0]) / (float64(n-1) / 24), true
}

// ClassGiB is the RAM of an RDS instance class, for turning FreeableMemory into a percentage.
func ClassGiB(class string) (float64, bool) {
	parts := strings.Split(class, ".")
	sizes := map[string]float64{"micro": 1, "small": 2, "medium": 4, "large": 8, "xlarge": 16, "2xlarge": 32, "4xlarge": 64, "8xlarge": 128}
	g, ok := sizes[parts[len(parts)-1]]
	if !ok {
		return 0, false
	}
	// Memory-optimised families (r*, x*) have twice the RAM per size; check the family, not the size ("xlarge").
	if len(parts) > 1 && (strings.HasPrefix(parts[1], "r") || strings.HasPrefix(parts[1], "x")) {
		g *= 2
	}
	return g, true
}

// Dim is a CloudWatch dimension name and value.
type Dim struct{ Name, Value string }

// Query is a metric-data query for one statistic of one metric.
func Query(id, ns, name string, dims []Dim, stat string, period int32, ret bool) cwtypes.MetricDataQuery {
	var ds []cwtypes.Dimension
	for _, d := range dims {
		ds = append(ds, cwtypes.Dimension{Name: aws.String(d.Name), Value: aws.String(d.Value)})
	}
	return cwtypes.MetricDataQuery{
		Id: aws.String(id),
		MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String(name), Dimensions: ds},
			Period: aws.Int32(period),
			Stat:   aws.String(stat),
		},
		ReturnData: aws.Bool(ret),
	}
}

// Err shortens an SDK error to the service's code and message.
func Err(err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return fmt.Errorf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	return err
}

// Region is the configured region, us-east-1 when unset.
func Region(cfg aws.Config) string {
	if cfg.Region == "" {
		return "us-east-1"
	}
	return cfg.Region
}

// Secs is a time's Unix seconds, 0 when missing.
func Secs(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

// Opt is a pointer to v when ok, for optional values.
func Opt(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

type task struct {
	id, status, health string
	started, stopped   int64
	reason             string
}

type svc struct {
	running, desired, pending int32
	rollout                   string
	deployed                  int64
	revision                  string
}

type dbInfo struct {
	status, class, engine string
	allocated, max        float64
	conns                 ConnLimit // max_connections, when known
	hasConns              bool
}

type event struct {
	at  int64
	msg string
}

// ecsAPI, rdsAPI and cwAPI are the parts of the AWS clients the pane uses.
type ecsAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

type rdsAPI interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	ParamsAPI
}

type cwAPI interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

type Infra struct {
	ui.Base
	every                time.Duration
	cluster, service, db string
	region               string
	ecs                  ecsAPI
	rds                  rdsAPI
	cw                   cwAPI
	svc                  *svc
	tasks                []task
	events               []event
	m                    map[string][]float64
	used7d               []float64
	dbi                  *dbInfo
	params               ConnParams
	st                   ui.TableState
}

func New(cluster, service, db string, every time.Duration) (ui.Pane, error) {
	cfg, err := awsx.Config(context.Background())
	if err != nil {
		return nil, err
	}
	return &Infra{
		every:   every,
		cluster: cluster, service: service, db: db,
		region: Region(cfg),
		ecs:    ecs.NewFromConfig(cfg),
		rds:    rds.NewFromConfig(cfg),
		cw:     cloudwatch.NewFromConfig(cfg),
		m:      map[string][]float64{},
		st:     ui.NewTableState(),
	}, nil
}

func (p *Infra) last(k string) (float64, bool) {
	v := p.m[k]
	if len(v) == 0 {
		return 0, false
	}
	return v[len(v)-1], true
}

func (p *Infra) metrics(ctx context.Context) error {
	sd := []Dim{{"ClusterName", p.cluster}, {"ServiceName", p.service}}
	dd := []Dim{{"DBInstanceIdentifier", p.db}}
	queries := []cwtypes.MetricDataQuery{
		Query("cpu", "AWS/ECS", "CPUUtilization", sd, "Average", 60, true),
		Query("mem", "AWS/ECS", "MemoryUtilization", sd, "Average", 60, true),
		Query("eu", "ECS/ContainerInsights", "EphemeralStorageUtilized", sd, "Average", 60, false),
		Query("er", "ECS/ContainerInsights", "EphemeralStorageReserved", sd, "Average", 60, false),
		Query("dbcpu", "AWS/RDS", "CPUUtilization", dd, "Average", 60, true),
		Query("dbconn", "AWS/RDS", "DatabaseConnections", dd, "Average", 60, true),
		Query("dbfreemem", "AWS/RDS", "FreeableMemory", dd, "Average", 60, true),
		Query("dbfree", "AWS/RDS", "FreeStorageSpace", dd, "Average", 300, true),
		{Id: aws.String("disk"), Expression: aws.String("100*eu/er"), ReturnData: aws.Bool(true)},
	}
	end := time.Now()
	r, err := p.cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		MetricDataQueries: queries,
		StartTime:         aws.Time(end.Add(-3 * time.Hour)),
		EndTime:           aws.Time(end),
		ScanBy:            cwtypes.ScanByTimestampAscending,
	})
	if err != nil {
		return Err(err)
	}
	p.m = map[string][]float64{}
	for _, res := range r.MetricDataResults {
		p.m[aws.ToString(res.Id)] = res.Values
	}

	// A week of free storage, hourly, for the growth trend.
	t, err := p.cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		MetricDataQueries: []cwtypes.MetricDataQuery{Query("free7d", "AWS/RDS", "FreeStorageSpace", dd, "Average", 3600, true)},
		StartTime:         aws.Time(end.Add(-7 * 24 * time.Hour)),
		EndTime:           aws.Time(end),
		ScanBy:            cwtypes.ScanByTimestampAscending,
	})
	if err != nil {
		return Err(err)
	}
	alloc := 0.0
	if p.dbi != nil {
		alloc = p.dbi.allocated
	}
	p.used7d = nil
	if len(t.MetricDataResults) > 0 {
		for _, free := range t.MetricDataResults[0].Values {
			p.used7d = append(p.used7d, alloc-free/gib)
		}
	}
	return nil
}

func (p *Infra) ecsState(ctx context.Context) error {
	r, err := p.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &p.cluster, Services: []string{p.service}})
	if err != nil {
		return Err(err)
	}
	if len(r.Services) == 0 {
		return fmt.Errorf("service %s not found", p.service)
	}
	s := r.Services[0]
	v := &svc{running: s.RunningCount, desired: s.DesiredCount, pending: s.PendingCount}
	for _, d := range s.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" {
			v.rollout = string(d.RolloutState)
			v.deployed = Secs(d.CreatedAt)
			break
		}
	}
	if td := aws.ToString(s.TaskDefinition); td != "" {
		v.revision = "rev " + td[strings.LastIndex(td, ":")+1:]
	}
	p.svc = v
	p.events = p.events[:0]
	for _, e := range s.Events {
		msg := aws.ToString(e.Message)
		// "(service app-production) has started 1 tasks: ..." → "has started 1 tasks: ..."
		if _, rest, ok := strings.Cut(msg, ") "); ok {
			msg = rest
		}
		p.events = append(p.events, event{Secs(e.CreatedAt), msg})
	}

	var arns []string
	for _, status := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
		r, err := p.ecs.ListTasks(ctx, &ecs.ListTasksInput{Cluster: &p.cluster, ServiceName: &p.service, DesiredStatus: status})
		if err != nil {
			return Err(err)
		}
		arns = append(arns, r.TaskArns...)
	}
	// DescribeTasks takes at most 100.
	arns = arns[:min(len(arns), 100)]
	p.tasks = p.tasks[:0]
	if len(arns) > 0 {
		r, err := p.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: &p.cluster, Tasks: arns})
		if err != nil {
			return Err(err)
		}
		for _, t := range r.Tasks {
			var exit, reason string
			if len(t.Containers) > 0 {
				c := t.Containers[0]
				if c.ExitCode != nil {
					exit = fmt.Sprintf("exit %d ", *c.ExitCode)
				}
				reason = aws.ToString(c.Reason)
			}
			if t.StoppedReason != nil {
				reason = *t.StoppedReason
			}
			arn := aws.ToString(t.TaskArn)
			id := arn[strings.LastIndex(arn, "/")+1:]
			if r := []rune(id); len(r) > 8 {
				id = string(r[:8])
			}
			p.tasks = append(p.tasks, task{
				id:      id,
				status:  aws.ToString(t.LastStatus),
				health:  string(t.HealthStatus),
				started: Secs(t.StartedAt),
				stopped: Secs(t.StoppedAt),
				reason:  strings.TrimSpace(exit + reason),
			})
		}
		// Running first, newest first.
		slices.SortStableFunc(p.tasks, func(a, b task) int {
			ar, br := a.status == "RUNNING", b.status == "RUNNING"
			if ar != br {
				if ar {
					return -1
				}
				return 1
			}
			return int(b.started - a.started)
		})
	}
	ui.Clamp(&p.st.Sel, len(p.events))
	return nil
}

func (p *Infra) dbState(ctx context.Context) error {
	r, err := p.rds.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: &p.db})
	if err != nil {
		return Err(err)
	}
	if len(r.DBInstances) > 0 {
		d := r.DBInstances[0]
		p.dbi = &dbInfo{
			status:    aws.ToString(d.DBInstanceStatus),
			class:     aws.ToString(d.DBInstanceClass),
			engine:    aws.ToString(d.Engine) + " " + aws.ToString(d.EngineVersion),
			allocated: float64(aws.ToInt32(d.AllocatedStorage)),
			max:       float64(aws.ToInt32(d.MaxAllocatedStorage)),
		}
		if len(d.DBParameterGroups) > 0 {
			if p.params == nil {
				p.params = ConnParams{}
			}
			group := aws.ToString(d.DBParameterGroups[0].DBParameterGroupName)
			p.dbi.conns, p.dbi.hasConns = p.params.Limit(ctx, p.rds, group, p.dbi.class)
		}
	}
	return nil
}

// dbStorage is (percent used, used GB, allocated GB).
func (p *Infra) dbStorage() (pct, used, alloc float64, ok bool) {
	free, ok := p.last("dbfree")
	// No figure until the database reports its size (0 GB while it's being created).
	if p.dbi == nil || !ok || p.dbi.allocated <= 0 {
		return 0, 0, 0, false
	}
	alloc = p.dbi.allocated
	used = alloc - free/gib
	return used / alloc * 100, used, alloc, true
}

func (p *Infra) dbMem() (float64, bool) {
	if p.dbi == nil {
		return 0, false
	}
	total, ok := ClassGiB(p.dbi.class)
	free, ok2 := p.last("dbfreemem")
	if !ok || !ok2 {
		return 0, false
	}
	return clamp100((total - free/gib) / total * 100), true
}

func clamp100(v float64) float64 { return math.Max(0, math.Min(100, v)) }

func (p *Infra) starts24h() int {
	cutoff := ui.Now() - 86400
	n := 0
	for _, e := range p.events {
		if e.at >= cutoff && strings.Contains(e.msg, "has started") {
			n++
		}
	}
	return n
}

// Tile draws a metric tile: label + value, a level gauge, and a trend line.
// value and ratio are nil when unknown.
func Tile(b *ui.Buffer, area ui.Rect, icon ui.Ic, label string, value *float64, unit, sub string, series []float64, ratio *float64) {
	color := ui.Accent()
	if ratio != nil {
		color = LoadColor(*ratio * 100)
	}
	rows := ui.Vertical(area, 0, ui.Len(1), ui.Len(1), ui.Min(1), ui.Len(1))
	head, gauge, spark, foot := rows[0], rows[1], rows[2], rows[3]
	val := "—"
	if value != nil {
		val = fmt.Sprintf("%.0f%s", *value, unit)
	}
	hc := ui.Horizontal(head, 0, ui.Min(1), ui.Len(ui.StrWidth(val)))
	// Keep the icon and value; drop the label text when the tile is too narrow for both.
	if hc[0].W < ui.StrWidth(label)+3 {
		label = ""
	}
	b.SetLine(hc[0], ui.L(ui.C(icon.S()+" ", color), ui.S(label, ui.Style{Fg: ui.Muted(), Bold: true})))
	b.SetLine(hc[1], ui.LT(val, ui.Style{Fg: color, Bold: true}))
	if ratio != nil {
		ui.LineGauge(b, gauge, *ratio, ui.Fg(color), ui.Fg(ui.Border()))
	}
	if len(series) > 0 {
		// Percentages draw against 100 so the height means something; counts scale to their own max.
		data := make([]uint64, len(series))
		for i, v := range series {
			data[i] = uint64(math.Max(v*10, 0))
		}
		var maxV uint64
		if unit == "%" {
			maxV = 1000
		}
		ui.Sparkline(b, spark, ui.Stretch(data, spark.W), maxV, ui.Fg(color))
	}
	b.SetLine(foot, ui.LT(sub, ui.Fg(ui.Muted())))
}

func (p *Infra) Name() string { return "Infra" }

func (p *Infra) Title() ui.Line {
	return ui.LT(p.service+" · "+p.region, ui.Fg(ui.Muted()))
}

func (p *Infra) Hints() []ui.Hint {
	return []ui.Hint{{Key: "↑↓", Desc: "events"}, {Key: "o", Desc: "ECS console"}, {Key: "d", Desc: "RDS console"}}
}

func (p *Infra) Every() time.Duration { return p.every }

func (p *Infra) Status() ui.Status {
	s := p.svc
	if s == nil {
		return ui.Status{State: "working", Msg: "loading"}
	}
	msg := fmt.Sprintf("%d/%d tasks", s.running, s.desired)
	for _, k := range [][2]string{{"cpu", "CPU"}, {"mem", "MEM"}} {
		if v, ok := p.last(k[0]); ok {
			msg += fmt.Sprintf(" · %s %.0f%%", k[1], v)
		}
	}
	pct, _, _, full := p.dbStorage()
	if full {
		msg += fmt.Sprintf(" · DB %.0f%% full", pct)
	}
	cpu, _ := p.last("cpu")
	mem, _ := p.last("mem")
	state := "idle"
	switch {
	case s.running < s.desired || cpu > 90 || mem > 90 || (full && pct > 90):
		state = "error"
	case s.pending > 0 || s.rollout == "IN_PROGRESS":
		state = "working"
	}
	return ui.Status{State: state, Msg: msg}
}

func (p *Infra) Refresh() error {
	ctx := context.Background()
	if err := p.dbState(ctx); err != nil {
		return err
	}
	if err := p.ecsState(ctx); err != nil {
		return err
	}
	return p.metrics(ctx)
}

func (p *Infra) Key(ev *tcell.EventKey) error {
	r := p.region
	switch {
	case ui.IsRune(ev, 'o'):
		ui.OpenURL(fmt.Sprintf("https://%s.console.aws.amazon.com/ecs/v2/clusters/%s/services/%s/health?region=%s", r, p.cluster, p.service, r))
	case ui.IsRune(ev, 'd'):
		ui.OpenURL(fmt.Sprintf("https://%s.console.aws.amazon.com/rds/home?region=%s#database:id=%s;is-cluster=false", r, r, p.db))
	default:
		ui.Nav(&p.st.Sel, len(p.events), ev, 10)
	}
	return nil
}

func (p *Infra) Draw(b *ui.Buffer, area ui.Rect) {
	parts := ui.Vertical(area, 0, ui.Len(7), ui.Len(7), ui.Min(4))
	ea, da, la := parts[0], parts[1], parts[2]
	muted := ui.Fg(ui.Muted())

	// ECS service
	right := ui.LT(" loading… ", muted)
	if s := p.svc; s != nil {
		ok := s.running >= s.desired && s.pending == 0
		icon, color := ui.FAIL.S(), ui.Red()
		if ok {
			icon, color = ui.OK.S(), ui.Green()
		}
		rollout := strings.ReplaceAll(strings.ToLower(s.rollout), "_", " ")
		right = ui.L(
			ui.C(fmt.Sprintf(" %s %d/%d running ", icon, s.running, s.desired), color),
			ui.C(fmt.Sprintf("· %s · %s %s ago ", s.revision, rollout, ui.Ago(s.deployed)), ui.Muted()),
		)
	}
	inner := ui.Panel{Title: ui.LT(fmt.Sprintf(" %s ECS %s ", BOX.S(), p.service), ui.Style{}), Right: right}.Draw(b, ea)
	pct := func(k string) (*float64, *float64) {
		v, ok := p.last(k)
		return Opt(v, ok), Opt(v/100, ok)
	}
	// App storage comes from Container Insights; once data has loaded without it, give its room to CPU and memory.
	n := 3
	if p.svc != nil && len(p.m["disk"]) == 0 {
		n = 2
	}
	cs := make([]ui.Constraint, n)
	for i := range cs {
		cs[i] = ui.Fill(1)
	}
	cols := ui.Horizontal(inner, 3, cs...)
	v, r := pct("cpu")
	Tile(b, cols[0], CPU, "CPU", v, "%", "3h", p.m["cpu"], r)
	v, r = pct("mem")
	Tile(b, cols[1], MEM, "Memory", v, "%", "3h", p.m["mem"], r)
	if n == 3 {
		v, r = pct("disk")
		Tile(b, cols[2], DISK, "App storage", v, "%", "ephemeral · 3h", p.m["disk"], r)
	}

	// RDS
	right = ui.Line{}
	if d := p.dbi; d != nil {
		color := ui.Yellow()
		if d.status == "available" {
			color = ui.Green()
		}
		right = ui.L(ui.C(" "+d.status+" ", color), ui.C(fmt.Sprintf("· %s · %s ", d.engine, d.class), ui.Muted()))
	}
	inner = ui.Panel{Title: ui.LT(fmt.Sprintf(" %s RDS %s ", DB.S(), p.db), ui.Style{}), Right: right}.Draw(b, da)
	cols = ui.Horizontal(inner, 3, ui.Fill(2), ui.Fill(1), ui.Fill(1), ui.Fill(1))
	spct, used, alloc, ok := p.dbStorage()
	sub := "7d"
	if ok {
		growth := ""
		if d, ok := PerDay(p.used7d); ok {
			growth = fmt.Sprintf(" · %+.1f GB/day", d)
		}
		sub = fmt.Sprintf("%.0f/%.0f GB%s · max %.0f", used, alloc, growth, p.dbi.max)
	}
	// Storage trend: scale against the week's own low so growth is visible.
	floor := math.MaxFloat64
	for _, u := range p.used7d {
		floor = math.Min(floor, u)
	}
	floor *= 0.98
	trend := make([]float64, len(p.used7d))
	for i, u := range p.used7d {
		trend[i] = math.Max(u-floor, 0)
	}
	Tile(b, cols[0], DISK, "Storage", Opt(spct, ok), "%", sub, trend, Opt(spct/100, ok))
	v, r = pct("dbcpu")
	Tile(b, cols[1], CPU, "CPU", v, "%", "3h", p.m["dbcpu"], r)
	memp, ok := p.dbMem()
	var memSeries []float64
	if p.dbi != nil {
		if total, ok := ClassGiB(p.dbi.class); ok {
			for _, free := range p.m["dbfreemem"] {
				memSeries = append(memSeries, clamp100((total-free/gib)/total*100))
			}
		}
	}
	Tile(b, cols[2], MEM, "Memory", Opt(memp, ok), "%", "3h", memSeries, Opt(memp/100, ok))
	conns, ok := p.last("dbconn")
	unit, ratio := "", (*float64)(nil)
	if p.dbi != nil && p.dbi.hasConns && p.dbi.conns.N > 0 {
		unit, ratio = " of "+p.dbi.conns.String(), Opt(conns/p.dbi.conns.N, ok)
	}
	Tile(b, cols[3], PLUG, "Conns", Opt(conns, ok), unit, "3h", p.m["dbconn"], ratio)

	// Tasks and events
	halves := ui.Horizontal(la, 1, ui.Fill(1), ui.Fill(1))
	ta, va := halves[0], halves[1]
	block := ui.Panel{Title: ui.LT(fmt.Sprintf(" %s Tasks ", BOX.S()), ui.Style{})}
	if len(p.tasks) == 0 {
		ui.EmptyState(b, block.Draw(b, ta), BOX.S(), ui.Muted(), "No tasks", "")
	} else {
		now := ui.Now()
		var rows []ui.Row
		for _, t := range p.tasks {
			running := t.status == "RUNNING"
			icon, color := ui.RUNNING.S(), ui.Yellow()
			if running {
				icon, color = PLAY.S(), ui.Green()
			} else if t.status == "STOPPED" {
				icon, color = STOP.S(), ui.Red()
			}
			detail := t.reason
			if running {
				detail = "up " + ui.Dur(now-t.started)
			} else if t.stopped > 0 {
				detail = ui.Ago(t.stopped) + " ago · " + t.reason
			}
			status := ui.L(ui.C(strings.ToLower(t.status), color))
			switch t.health {
			case "HEALTHY":
				status.Spans = append(status.Spans, ui.C(" "+HEART.S(), ui.Green()))
			case "UNHEALTHY":
				status.Spans = append(status.Spans, ui.C(" "+HEART.S(), ui.Red()))
			}
			rows = append(rows, ui.R(
				ui.LT(icon, ui.Fg(color)),
				ui.LT(t.id, muted),
				status,
				ui.LT(ui.Local(t.started, "Jan 02 15:04"), muted),
				ui.LT(detail, ui.Style{}),
			))
		}
		ui.Table{
			Cols:    []ui.Constraint{ui.Len(1), ui.Len(8), ui.Len(10), ui.Len(12), ui.Min(8)},
			Header:  []ui.Line{{}, ui.LT("Task", ui.Style{}), ui.LT("Status", ui.Style{}), ui.LT("Started", ui.Style{}), {}},
			Rows:    rows,
			Spacing: 2,
			Block:   &block,
		}.Draw(b, ta, nil)
	}

	var rows []ui.Row
	for _, e := range p.events {
		icon, color := ui.PENDING.S(), ui.Muted()
		switch m := e.msg; {
		case strings.Contains(m, "has started"):
			icon, color = PLAY.S(), ui.Green()
		case strings.Contains(m, "has stopped") || strings.Contains(m, "unhealthy") || strings.Contains(m, "failed"):
			icon, color = STOP.S(), ui.Red()
		case strings.Contains(m, "steady state"):
			icon = ui.OK.S()
		}
		text := ui.FgC()
		if color == ui.Muted() {
			text = ui.Muted()
		}
		rows = append(rows, ui.R(ui.LT(icon, ui.Fg(color)), ui.LT(ui.Local(e.at, "Jan 02 15:04"), muted), ui.LT(e.msg, ui.Fg(text))))
	}
	block = ui.Panel{
		Title: ui.LT(fmt.Sprintf(" %s Events ", HISTORY.S()), ui.Style{}),
		Right: ui.LT(fmt.Sprintf(" %s %d starts/24h ", PLAY.S(), p.starts24h()), muted),
	}
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(1), ui.Len(12), ui.Min(10)},
		Rows:      rows,
		Spacing:   1,
		Highlight: true,
		Block:     &block,
	}.Draw(b, va, &p.st)
	ui.Scrollbar(b, va, len(p.events), max(p.st.Sel, 0), va.H-2)
}
