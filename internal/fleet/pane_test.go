package fleet

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

// page splits items into pages of n, addressed by token "" (first), "1", "2", …
func page[T any](items []T, n int, token *string) ([]T, *string) {
	i := 0
	if token != nil {
		fmt.Sscan(*token, &i)
	}
	lo, hi := i*n, min((i+1)*n, len(items))
	if hi >= len(items) {
		return items[lo:], nil
	}
	return items[lo:hi], aws.String(fmt.Sprint(i + 1))
}

type fakeECS struct {
	clusters  []string                      // cluster names, served one per page
	services  map[string][]ecstypes.Service // by cluster
	svcPage   int                           // ListServices page size
	err       map[string]error              // by method
	listPages int
	chunks    []int // DescribeServices batch sizes
}

func (f *fakeECS) ListClusters(_ context.Context, in *ecs.ListClustersInput, _ ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	if err := f.err["ListClusters"]; err != nil {
		return nil, err
	}
	var arns []string
	for _, c := range f.clusters {
		arns = append(arns, "arn:aws:ecs:eu-west-1:123:cluster/"+c)
	}
	items, next := page(arns, 1, in.NextToken)
	return &ecs.ListClustersOutput{ClusterArns: items, NextToken: next}, nil
}

func (f *fakeECS) ListServices(_ context.Context, in *ecs.ListServicesInput, _ ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	if err := f.err["ListServices"]; err != nil {
		return nil, err
	}
	f.listPages++
	var arns []string
	for _, s := range f.services[aws.ToString(in.Cluster)] {
		arns = append(arns, "arn:aws:ecs:eu-west-1:123:service/"+aws.ToString(in.Cluster)+"/"+aws.ToString(s.ServiceName))
	}
	items, next := page(arns, max(f.svcPage, 1), in.NextToken)
	return &ecs.ListServicesOutput{ServiceArns: items, NextToken: next}, nil
}

func (f *fakeECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if err := f.err["DescribeServices"]; err != nil {
		return nil, err
	}
	f.chunks = append(f.chunks, len(in.Services))
	out := &ecs.DescribeServicesOutput{}
	for _, arn := range in.Services {
		for _, s := range f.services[aws.ToString(in.Cluster)] {
			if strings.HasSuffix(arn, "/"+aws.ToString(s.ServiceName)) {
				out.Services = append(out.Services, s)
			}
		}
	}
	return out, nil
}

type fakeRDS struct {
	dbs   []rdstypes.DBInstance
	err   error
	pages int
}

func (f *fakeRDS) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.pages++
	items, next := page(f.dbs, 2, in.Marker)
	return &rds.DescribeDBInstancesOutput{DBInstances: items, Marker: next}, nil
}

// fakeCW serves m[id], split over two NextToken pages per batch to check the values are joined.
type fakeCW struct {
	m       map[string][]float64
	err     error
	batches []int // query counts of first-page calls
	calls   int
}

func (f *fakeCW) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls++
	first := in.NextToken == nil
	if first {
		f.batches = append(f.batches, len(in.MetricDataQueries))
	}
	out := &cloudwatch.GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		v, ok := f.m[aws.ToString(q.Id)]
		if !ok {
			continue
		}
		half := len(v) / 2
		if first {
			v = v[:half]
		} else {
			v = v[half:]
		}
		out.MetricDataResults = append(out.MetricDataResults, cwtypes.MetricDataResult{Id: q.Id, Values: v})
	}
	if first {
		out.NextToken = aws.String("more")
	}
	return out, nil
}

func ago(s int64) *time.Time { t := time.Unix(ui.Now()-s, 0); return &t }

func service(name string, running, desired, pending int32, rollout ecstypes.DeploymentRolloutState, rev int) ecstypes.Service {
	return ecstypes.Service{
		ServiceName: aws.String(name), RunningCount: running, DesiredCount: desired, PendingCount: pending,
		TaskDefinition: aws.String(fmt.Sprintf("arn:aws:ecs:eu-west-1:123:task-definition/%s:%d", name, rev)),
		Deployments:    []ecstypes.Deployment{{Status: aws.String("PRIMARY"), RolloutState: rollout, CreatedAt: ago(3 * 3600)}},
	}
}

func instance(id, class, status string, gb int32) rdstypes.DBInstance {
	return rdstypes.DBInstance{DBInstanceIdentifier: aws.String(id), DBInstanceClass: aws.String(class),
		DBInstanceStatus: aws.String(status), AllocatedStorage: aws.Int32(gb)}
}

const gb = float64(gib)

// fleet: after sorting, s0 prod/api, s1 prod/jobs, s2 prod/worker, s3 web/web; d0 alpha, d1 beta, d2 gamma.
func sample() (*fakeECS, *fakeRDS, *fakeCW) {
	done := ecstypes.DeploymentRolloutStateCompleted
	e := &fakeECS{
		clusters: []string{"web", "prod"},
		services: map[string][]ecstypes.Service{
			"prod": {
				service("worker", 1, 2, 0, done, 3),
				service("api", 2, 2, 0, done, 7),
				service("jobs", 0, 0, 0, done, 1),
			},
			"web": {service("web", 1, 1, 0, ecstypes.DeploymentRolloutStateInProgress, 12)},
		},
		svcPage: 2,
	}
	r := &fakeRDS{dbs: []rdstypes.DBInstance{
		instance("gamma", "db.serverless", "stopped", 20),
		instance("beta", "db.r6g.large", "modifying", 50),
		instance("alpha", "db.t4g.medium", "available", 100),
	}}
	c := &fakeCW{m: map[string][]float64{
		"s0c": {10, 30, 40, 50}, "s0m": {30, 40},
		"s3c": {5, 6}, "s3m": {7, 8},
		"d0c": {20, 22}, "d0f": {6 * gb, 5 * gb}, "d0n": {3, 5, 4, 4}, "d0r": {2 * gb, 1 * gb}, // 95% storage, 75% memory
		"d1c": {1, 2}, "d1f": {40 * gb, 40 * gb}, "d1n": {1, 1}, "d1r": {8 * gb, 8 * gb},
	}}
	return e, r, c
}

func newPane(e *fakeECS, r *fakeRDS, c *fakeCW) *Fleet {
	return &Fleet{region: "eu-west-1", ecs: e, rds: r, cw: c, m: map[string][]float64{}, sel: -1}
}

func draw(p *Fleet) *ui.Buffer {
	b := ui.NewBuffer(130, 20)
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

func TestRefreshAndDraw(t *testing.T) {
	e, r, c := sample()
	p := newPane(e, r, c)
	if s := p.Status(); s.State != "working" || s.Msg != "loading" {
		t.Errorf("before load %+v", s)
	}
	find(t, draw(p), "Loading services and databases…")

	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range p.svcs {
		names = append(names, s.cluster+"/"+s.name+"@"+s.rev)
	}
	if got := strings.Join(names, " "); got != "prod/api@7 prod/jobs@1 prod/worker@3 web/web@12" {
		t.Errorf("services %s", got)
	}
	if p.dbs[0].id != "alpha" || p.dbs[2].id != "gamma" || p.dbs[0].allocated != 100 {
		t.Errorf("dbs %+v", p.dbs)
	}
	// NextToken pages of one batch are joined per id.
	if got := p.m["s0c"]; !slices.Equal(got, []float64{10, 30, 40, 50}) {
		t.Errorf("s0c %v", got)
	}
	if p.Title().String() != "4 services · 3 databases · eu-west-1" {
		t.Errorf("title %q", p.Title().String())
	}

	b := draw(p)
	text := b.Text()
	for _, want := range []string{"prod/api", "prod/worker", "r7", "r12", "3h", "t4g.medium", "serverless", " 95% of 100G", " 50% ▂▃▄▅"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "web/web") {
		t.Error("a service named like its cluster should drop the cluster prefix")
	}
	// Missing metrics read "—": worker and jobs CPU/memory, gamma CPU/memory/storage.
	for _, name := range []string{"worker", "jobs", "gamma"} {
		_, y := find(t, b, name)
		if row := strings.Split(text, "\n")[y]; strings.Count(row, "—") < 2 {
			t.Errorf("%s: want — for missing metrics in %q", name, row)
		}
	}
	// Colours: a service below desired is red, alpha's 95% storage red, its 75% memory yellow, api's CPU green.
	for _, c := range []struct {
		s    string
		want ui.Color
	}{{"1/2", ui.Red()}, {"2/2", ui.Green()}, {"95%", ui.Red()}, {"75%", ui.Yellow()}, {"50%", ui.Green()}} {
		x, y := find(t, b, c.s)
		if st := b.StyleAt(x, y); st.Fg != c.want {
			t.Errorf("%s: %v want %v", c.s, st.Fg, c.want)
		}
	}
}

func TestIssuesAndStatus(t *testing.T) {
	e, r, c := sample()
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "error" || s.Msg != "worker 1/2 · alpha storage · beta modifying" {
		t.Errorf("status %+v", s)
	}

	// Hot CPU and memory are issues too; a stopped DB is not.
	c.m["s0c"] = []float64{91}
	c.m["s3m"] = []float64{99}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.issues(), ","); got != "api CPU,worker 1/2,web MEM,alpha storage,beta modifying" {
		t.Errorf("issues %s", got)
	}

	// Fix everything: a rollout in progress is "working", then idle once it completes.
	e, r, c = sample()
	e.services["prod"][0].RunningCount = 2
	r.dbs[1].DBInstanceStatus = aws.String("available")
	c.m["d0f"] = []float64{50 * gb}
	p = newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "working" || s.Msg != "4 services · 3 DBs · healthy" {
		t.Errorf("rolling out %+v", s)
	}
	e.services["web"][0].Deployments[0].RolloutState = ecstypes.DeploymentRolloutStateCompleted
	e.services["web"][0].PendingCount = 1
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "working" {
		t.Errorf("pending task %+v", s)
	}
	e.services["web"][0].PendingCount = 0
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if s := p.Status(); s.State != "idle" {
		t.Errorf("healthy %+v", s)
	}
}

func TestSelectionSpansBothTables(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	e, r, c := sample()
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if p.sel != 0 {
		t.Fatalf("refresh should select the first row, sel %d", p.sel)
	}
	selected := func(name string) bool {
		b := draw(p)
		x, y := find(t, b, name)
		return b.StyleAt(x, y).Bg == ui.SelBg()
	}
	if !selected("api") || selected("alpha") {
		t.Error("first service should be selected")
	}
	for range 4 {
		p.Key(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	}
	if p.sel != 4 || !selected("alpha") || selected("web") {
		t.Errorf("down past the services lands on the first database, sel %d", p.sel)
	}
	p.Key(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	if p.sel != 6 || !selected("gamma") {
		t.Errorf("end: sel %d", p.sel)
	}
	if err := p.Key(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone)); err != nil {
		t.Error(err)
	}
	// The list shrinks: the selection is clamped.
	r.dbs = r.dbs[:1]
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if p.sel != 4 {
		t.Errorf("clamped sel %d, want 4", p.sel)
	}
}

func TestPaginationAndBatching(t *testing.T) {
	// 23 services in one cluster, listed 4 per page; 251 services overall → 502 queries.
	e := &fakeECS{clusters: []string{"a", "b"}, services: map[string][]ecstypes.Service{}, svcPage: 4}
	for i := range 23 {
		e.services["a"] = append(e.services["a"], service(fmt.Sprintf("a%02d", i), 1, 1, 0, "", 1))
	}
	for i := range 228 {
		e.services["b"] = append(e.services["b"], service(fmt.Sprintf("b%03d", i), 1, 1, 0, "", 1))
	}
	r := &fakeRDS{}
	for i := range 5 {
		r.dbs = append(r.dbs, instance(fmt.Sprintf("db%d", i), "db.t4g.micro", "available", 20))
	}
	c := &fakeCW{m: map[string][]float64{"s250m": {1, 2}, "d4f": {3, 4}}}
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if len(p.svcs) != 251 || len(p.dbs) != 5 {
		t.Fatalf("got %d services, %d dbs", len(p.svcs), len(p.dbs))
	}
	if want := 6 + 57; e.listPages != want {
		t.Errorf("ListServices pages %d, want %d", e.listPages, want)
	}
	// DescribeServices takes at most 10.
	if want := []int{10, 10, 3}; !slices.Equal(e.chunks[:3], want) || len(e.chunks) != 3+23 || slices.Max(e.chunks) > 10 {
		t.Errorf("DescribeServices chunks %v", e.chunks)
	}
	if r.pages != 3 {
		t.Errorf("DescribeDBInstances pages %d, want 3", r.pages)
	}
	// 251*2 + 5*4 = 522 queries: 500, then 22, each paged twice.
	if !slices.Equal(c.batches, []int{500, 22}) || c.calls != 4 {
		t.Errorf("GetMetricData batches %v in %d calls", c.batches, c.calls)
	}
	if !slices.Equal(p.m["s250m"], []float64{1, 2}) || !slices.Equal(p.m["d4f"], []float64{3, 4}) {
		t.Errorf("values across batches: %v %v", p.m["s250m"], p.m["d4f"])
	}
}

func TestRefreshErrors(t *testing.T) {
	apiErr := &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "nope"}
	for _, name := range []string{"ListClusters", "ListServices", "DescribeServices", "rds", "cw"} {
		t.Run(name, func(t *testing.T) {
			e, r, c := sample()
			switch name {
			case "rds":
				r.err = apiErr
			case "cw":
				c.err = apiErr
			default:
				e.err = map[string]error{name: apiErr}
			}
			p := newPane(e, r, c)
			if err := p.Refresh(); err == nil || err.Error() != "AccessDeniedException: nope" {
				t.Errorf("err %v", err)
			}
			if p.loaded {
				t.Error("loaded after a failed refresh")
			}
		})
	}
}

// Metrics are keyed by row position. If loading them fails after the service and DB lists
// were replaced, the old values must not be shown against the new rows.
func TestFailedMetricsDontMisalignRows(t *testing.T) {
	e, r, c := sample()
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if len(p.m) == 0 {
		t.Fatal("no metrics after a good refresh")
	}
	c.err = errors.New("Throttling: slow down")
	if err := p.Refresh(); err == nil {
		t.Fatal("refresh with failing metrics succeeded")
	}
	if len(p.m) != 0 {
		t.Errorf("kept %d stale metric series after a failed metrics load", len(p.m))
	}
}
