// Package fleet is the Fleet pane: every ECS service and RDS instance in the account on one
// screen, with current load and a one-hour trend. Metrics are 5-minute, so it refreshes every 5 minutes.
package fleet

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/awsx"
	"rexdash/internal/infra"
	"rexdash/internal/ui"
)

var (
	CUBES = ui.Ic{Glyph: "", Plain: "▦"}
	BOX   = ui.Ic{Glyph: "", Plain: ""}
	DB    = ui.Ic{Glyph: "", Plain: ""}
)

const gib = 1 << 30

type svc struct {
	cluster, name             string
	running, desired, pending int32
	rollout                   string
	deployed                  int64
	rev                       string
}

type db struct {
	id, class, status string
	allocated         float64
}

// Spark is "▁▃▅█" for a series. Percentages draw against 0–top; counts (top nil) against their
// own low–high range, so a steady count reads as a flat mid line rather than a full bar.
func Spark(values []float64, top *float64) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	lo, hi := math.MaxFloat64, -math.MaxFloat64
	if top != nil {
		lo, hi = 0, *top
	} else {
		for _, v := range values {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	var b strings.Builder
	for _, v := range values {
		if hi-lo < 0x1p-52 { // f64 epsilon
			b.WriteRune(bars[3])
			continue
		}
		f := math.Max(0, math.Min(1, (v-lo)/(hi-lo)))
		b.WriteRune(bars[int(math.Round(f*7))])
	}
	return b.String()
}

type Fleet struct {
	ui.Base
	every  time.Duration
	region string
	ecs    *ecs.Client
	rds    *rds.Client
	cw     *cloudwatch.Client
	svcs   []svc
	dbs    []db
	m      map[string][]float64
	sel    int
	loaded bool
}

func New(every time.Duration) (ui.Pane, error) {
	cfg, err := awsx.Config(context.Background())
	if err != nil {
		return nil, err
	}
	return &Fleet{
		every:  every,
		region: infra.Region(cfg),
		ecs:    ecs.NewFromConfig(cfg),
		rds:    rds.NewFromConfig(cfg),
		cw:     cloudwatch.NewFromConfig(cfg),
		m:      map[string][]float64{},
		sel:    -1,
	}, nil
}

func (p *Fleet) last(k string) (float64, bool) {
	v := p.m[k]
	if len(v) == 0 {
		return 0, false
	}
	return v[len(v)-1], true
}

func (p *Fleet) dbStorage(i int) (float64, bool) {
	d := p.dbs[i]
	free, ok := p.last(fmt.Sprintf("d%df", i))
	if !ok || d.allocated <= 0 {
		return 0, false
	}
	return (d.allocated - free/gib) / d.allocated * 100, true
}

func (p *Fleet) dbMem(i int) []float64 {
	t, ok := infra.ClassGiB(p.dbs[i].class)
	if !ok {
		return nil
	}
	var out []float64
	for _, b := range p.m[fmt.Sprintf("d%dr", i)] {
		out = append(out, math.Max(0, math.Min(100, (t-b/gib)/t*100)))
	}
	return out
}

func (p *Fleet) issues() []string {
	var out []string
	for i, s := range p.svcs {
		if s.running < s.desired {
			out = append(out, fmt.Sprintf("%s %d/%d", s.name, s.running, s.desired))
		}
		for _, k := range [][2]string{{"c", "CPU"}, {"m", "MEM"}} {
			if v, _ := p.last(fmt.Sprintf("s%d%s", i, k[0])); v > 90 {
				out = append(out, s.name+" "+k[1])
			}
		}
	}
	for i, d := range p.dbs {
		if d.status != "available" && d.status != "stopped" {
			out = append(out, d.id+" "+d.status)
		}
		if v, _ := p.dbStorage(i); v > 90 {
			out = append(out, d.id+" storage")
		}
	}
	return out
}

func (p *Fleet) loadECS(ctx context.Context) error {
	var clusters []string
	cp := ecs.NewListClustersPaginator(p.ecs, &ecs.ListClustersInput{})
	for cp.HasMorePages() {
		r, err := cp.NextPage(ctx)
		if err != nil {
			return infra.Err(err)
		}
		for _, a := range r.ClusterArns {
			clusters = append(clusters, a[strings.LastIndex(a, "/")+1:])
		}
	}
	var svcs []svc
	for _, c := range clusters {
		var arns []string
		sp := ecs.NewListServicesPaginator(p.ecs, &ecs.ListServicesInput{Cluster: aws.String(c)})
		for sp.HasMorePages() {
			r, err := sp.NextPage(ctx)
			if err != nil {
				return infra.Err(err)
			}
			arns = append(arns, r.ServiceArns...)
		}
		// DescribeServices takes at most 10.
		for i := 0; i < len(arns); i += 10 {
			r, err := p.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(c), Services: arns[i:min(i+10, len(arns))]})
			if err != nil {
				return infra.Err(err)
			}
			for _, s := range r.Services {
				v := svc{cluster: c, name: aws.ToString(s.ServiceName), running: s.RunningCount, desired: s.DesiredCount, pending: s.PendingCount}
				for _, d := range s.Deployments {
					if aws.ToString(d.Status) == "PRIMARY" {
						v.rollout = string(d.RolloutState)
						v.deployed = infra.Secs(d.CreatedAt)
						break
					}
				}
				td := aws.ToString(s.TaskDefinition)
				v.rev = td[strings.LastIndex(td, ":")+1:]
				svcs = append(svcs, v)
			}
		}
	}
	slices.SortStableFunc(svcs, func(a, b svc) int { return cmp.Or(cmp.Compare(a.cluster, b.cluster), cmp.Compare(a.name, b.name)) })
	p.svcs = svcs
	return nil
}

func (p *Fleet) loadRDS(ctx context.Context) error {
	var dbs []db
	pg := rds.NewDescribeDBInstancesPaginator(p.rds, &rds.DescribeDBInstancesInput{})
	for pg.HasMorePages() {
		r, err := pg.NextPage(ctx)
		if err != nil {
			return infra.Err(err)
		}
		for _, d := range r.DBInstances {
			dbs = append(dbs, db{
				id:        aws.ToString(d.DBInstanceIdentifier),
				class:     aws.ToString(d.DBInstanceClass),
				status:    aws.ToString(d.DBInstanceStatus),
				allocated: float64(aws.ToInt32(d.AllocatedStorage)),
			})
		}
	}
	slices.SortStableFunc(dbs, func(a, b db) int { return cmp.Compare(a.id, b.id) })
	p.dbs = dbs
	return nil
}

func (p *Fleet) loadMetrics(ctx context.Context) error {
	var queries []cwtypes.MetricDataQuery
	q := func(id, ns, name string, dims []infra.Dim) {
		queries = append(queries, infra.Query(id, ns, name, dims, "Average", 300, true))
	}
	for i, s := range p.svcs {
		dims := []infra.Dim{{Name: "ClusterName", Value: s.cluster}, {Name: "ServiceName", Value: s.name}}
		q(fmt.Sprintf("s%dc", i), "AWS/ECS", "CPUUtilization", dims)
		q(fmt.Sprintf("s%dm", i), "AWS/ECS", "MemoryUtilization", dims)
	}
	for i, d := range p.dbs {
		dims := []infra.Dim{{Name: "DBInstanceIdentifier", Value: d.id}}
		q(fmt.Sprintf("d%dc", i), "AWS/RDS", "CPUUtilization", dims)
		q(fmt.Sprintf("d%df", i), "AWS/RDS", "FreeStorageSpace", dims)
		q(fmt.Sprintf("d%dn", i), "AWS/RDS", "DatabaseConnections", dims)
		q(fmt.Sprintf("d%dr", i), "AWS/RDS", "FreeableMemory", dims)
	}
	end := time.Now()
	m := map[string][]float64{}
	// GetMetricData takes at most 500 queries per call.
	for i := 0; i < len(queries); i += 500 {
		pg := cloudwatch.NewGetMetricDataPaginator(p.cw, &cloudwatch.GetMetricDataInput{
			MetricDataQueries: queries[i:min(i+500, len(queries))],
			StartTime:         aws.Time(end.Add(-time.Hour)),
			EndTime:           aws.Time(end),
			ScanBy:            cwtypes.ScanByTimestampAscending,
		})
		for pg.HasMorePages() {
			r, err := pg.NextPage(ctx)
			if err != nil {
				return infra.Err(err)
			}
			for _, res := range r.MetricDataResults {
				id := aws.ToString(res.Id)
				m[id] = append(m[id], res.Values...)
			}
		}
	}
	p.m = m
	return nil
}

// pctCell is "42% ▂▃▅▆" coloured by load; "—" when there's no data.
func pctCell(last float64, ok bool, series []float64) ui.Line {
	if !ok {
		return ui.LT("  —", ui.Fg(ui.Muted()))
	}
	c := infra.LoadColor(last)
	hundred := 100.0
	return ui.L(ui.C(fmt.Sprintf("%3.0f%% ", last), c), ui.C(Spark(series, &hundred), c))
}

func (p *Fleet) Name() string { return "Fleet" }

func (p *Fleet) Title() ui.Line {
	return ui.LT(fmt.Sprintf("%d services · %d databases · %s", len(p.svcs), len(p.dbs), p.region), ui.Fg(ui.Muted()))
}

func (p *Fleet) Hints() []ui.Hint {
	return []ui.Hint{{Key: "↑↓", Desc: "select"}, {Key: "o", Desc: "console"}}
}

func (p *Fleet) Every() time.Duration { return p.every }

func (p *Fleet) Status() ui.Status {
	if !p.loaded {
		return ui.Status{State: "working", Msg: "loading"}
	}
	if issues := p.issues(); len(issues) > 0 {
		return ui.Status{State: "error", Msg: strings.Join(issues, " · ")}
	}
	state := "idle"
	for _, s := range p.svcs {
		if s.pending > 0 || s.rollout == "IN_PROGRESS" {
			state = "working"
		}
	}
	return ui.Status{State: state, Msg: fmt.Sprintf("%d services · %d DBs · healthy", len(p.svcs), len(p.dbs))}
}

func (p *Fleet) Refresh() error {
	ctx := context.Background()
	if err := p.loadECS(ctx); err != nil {
		return err
	}
	if err := p.loadRDS(ctx); err != nil {
		return err
	}
	if err := p.loadMetrics(ctx); err != nil {
		return err
	}
	p.loaded = true
	ui.Clamp(&p.sel, len(p.svcs)+len(p.dbs))
	return nil
}

func (p *Fleet) Key(ev *tcell.EventKey) error {
	r := p.region
	if !ui.IsRune(ev, 'o') {
		ui.Nav(&p.sel, len(p.svcs)+len(p.dbs), ev, 10)
		return nil
	}
	i := max(p.sel, 0)
	if i < len(p.svcs) {
		s := p.svcs[i]
		ui.OpenURL(fmt.Sprintf("https://%s.console.aws.amazon.com/ecs/v2/clusters/%s/services/%s/health?region=%s", r, s.cluster, s.name, r))
	} else if i -= len(p.svcs); i < len(p.dbs) {
		ui.OpenURL(fmt.Sprintf("https://%s.console.aws.amazon.com/rds/home?region=%s#database:id=%s;is-cluster=false", r, r, p.dbs[i].id))
	}
	return nil
}

func (p *Fleet) Draw(b *ui.Buffer, area ui.Rect) {
	if !p.loaded {
		ui.EmptyState(b, area, CUBES.S(), ui.Muted(), "Loading services and databases…", "")
		return
	}
	ecsH := min(len(p.svcs)+3, max(area.H-5, 0))
	parts := ui.Vertical(area, 0, ui.Len(ecsH), ui.Min(4))
	muted := ui.Fg(ui.Muted())
	sel := func(i int) ui.Style {
		if i == p.sel {
			return ui.SelectedStyle()
		}
		return ui.Style{}
	}
	h := func(s ...string) []ui.Line {
		out := make([]ui.Line, len(s))
		for i, t := range s {
			out[i] = ui.LT(t, ui.Style{})
		}
		return out
	}

	// ECS services
	var rows []ui.Row
	for i, s := range p.svcs {
		icon, color := ui.OK.S(), ui.Green()
		switch {
		case s.running < s.desired:
			icon, color = ui.FAIL.S(), ui.Red()
		case s.pending > 0 || s.rollout == "IN_PROGRESS":
			icon, color = ui.RUNNING.S(), ui.Yellow()
		case s.desired == 0:
			icon, color = ui.PENDING.S(), ui.Muted()
		}
		name := ui.L(ui.T(s.name))
		if s.cluster != s.name {
			name = ui.L(ui.C(s.cluster+"/", ui.Muted()), ui.T(s.name))
		}
		deployed := ""
		if s.deployed > 0 {
			deployed = ui.Ago(s.deployed)
		}
		cpu, cok := p.last(fmt.Sprintf("s%dc", i))
		mem, mok := p.last(fmt.Sprintf("s%dm", i))
		rows = append(rows, ui.Row{Style: sel(i), Cells: []ui.Line{
			ui.LT(icon, ui.Fg(color)),
			name,
			ui.LT(fmt.Sprintf("%d/%d", s.running, s.desired), ui.Fg(color)),
			pctCell(cpu, cok, p.m[fmt.Sprintf("s%dc", i)]),
			pctCell(mem, mok, p.m[fmt.Sprintf("s%dm", i)]),
			ui.LT("r"+s.rev, muted),
			ui.LT(deployed, muted),
		}})
	}
	ui.Table{
		Cols:    []ui.Constraint{ui.Len(1), ui.Fill(1), ui.Len(5), ui.Len(18), ui.Len(18), ui.Len(4), ui.Len(6)},
		Header:  h("", "Service", "Tasks", "CPU · 1h", "Memory · 1h", "Rev", "Deploy"),
		Rows:    rows,
		Spacing: 2,
		Block:   &ui.Panel{Title: ui.LT(fmt.Sprintf(" %s ECS services ", BOX.S()), ui.Style{})},
	}.Draw(b, parts[0], nil)

	// RDS instances
	rows = nil
	for i, d := range p.dbs {
		ok := d.status == "available"
		icon, color := ui.RUNNING.S(), ui.Yellow()
		if ok {
			icon, color = ui.OK.S(), ui.Green()
		} else if d.status == "stopped" {
			color = ui.Muted()
		}
		storage := ui.LT("  —", muted)
		if pct, ok := p.dbStorage(i); ok {
			storage = ui.L(ui.C(fmt.Sprintf("%3.0f%% ", pct), infra.LoadColor(pct)), ui.C(fmt.Sprintf("of %.0fG", d.allocated), ui.Muted()))
		}
		mem := p.dbMem(i)
		var memLast float64
		if len(mem) > 0 {
			memLast = mem[len(mem)-1]
		}
		conns := p.m[fmt.Sprintf("d%dn", i)]
		var connLast float64
		if len(conns) > 0 {
			connLast = conns[len(conns)-1]
		}
		cpu, cok := p.last(fmt.Sprintf("d%dc", i))
		rows = append(rows, ui.Row{Style: sel(len(p.svcs) + i), Cells: []ui.Line{
			ui.LT(icon, ui.Fg(color)),
			ui.LT(d.id, ui.Style{}),
			ui.LT(strings.TrimPrefix(d.class, "db."), muted),
			pctCell(cpu, cok, p.m[fmt.Sprintf("d%dc", i)]),
			pctCell(memLast, len(mem) > 0, mem),
			storage,
			ui.L(ui.T(fmt.Sprintf("%4.0f ", connLast)), ui.C(Spark(conns, nil), ui.Blue())),
		}})
	}
	ui.Table{
		Cols:    []ui.Constraint{ui.Len(1), ui.Fill(1), ui.Len(10), ui.Len(18), ui.Len(18), ui.Len(12), ui.Len(17)},
		Header:  h("", "Database", "Class", "CPU · 1h", "Memory · 1h", "Storage", "Connections"),
		Rows:    rows,
		Spacing: 2,
		Block:   &ui.Panel{Title: ui.LT(fmt.Sprintf(" %s RDS ", DB.S()), ui.Style{})},
	}.Draw(b, parts[1], nil)
}
