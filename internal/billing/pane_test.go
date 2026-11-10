package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

// fakeCE answers the pane's three Cost Explorer requests from canned pages, and records them.
// Nothing here reaches AWS.
type fakeCE struct {
	mu       sync.Mutex
	monthly  []*costexplorer.GetCostAndUsageOutput // pages, chained by NextPageToken
	daily    *costexplorer.GetCostAndUsageOutput
	forecast *costexplorer.GetCostForecastOutput
	usageErr error
	fcErr    error

	usage []*costexplorer.GetCostAndUsageInput
	fcs   []*costexplorer.GetCostForecastInput
}

func (f *fakeCE) GetCostAndUsage(_ context.Context, in *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *in
	f.usage = append(f.usage, &cp)
	if f.usageErr != nil {
		return nil, f.usageErr
	}
	if in.Granularity == cetypes.GranularityDaily {
		return f.daily, nil
	}
	page := 0
	if in.NextPageToken != nil {
		fmt.Sscanf(*in.NextPageToken, "page-%d", &page)
	}
	out := *f.monthly[page]
	if page+1 < len(f.monthly) {
		out.NextPageToken = aws.String(fmt.Sprintf("page-%d", page+1))
	}
	return &out, nil
}

func (f *fakeCE) GetCostForecast(_ context.Context, in *costexplorer.GetCostForecastInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostForecastOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fcs = append(f.fcs, in)
	if f.fcErr != nil {
		return nil, f.fcErr
	}
	return f.forecast, nil
}

func (f *fakeCE) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.usage) + len(f.fcs)
}

func cost(v string) map[string]cetypes.MetricValue {
	return map[string]cetypes.MetricValue{"UnblendedCost": {Amount: aws.String(v), Unit: aws.String("USD")}}
}

func period(start time.Time) *cetypes.DateInterval {
	return &cetypes.DateInterval{Start: aws.String(start.Format(time.DateOnly)), End: aws.String(start.AddDate(0, 1, 0).Format(time.DateOnly))}
}

func groups(kv ...string) []cetypes.Group {
	var out []cetypes.Group
	for i := 0; i < len(kv); i += 2 {
		out = append(out, cetypes.Group{Keys: []string{kv[i]}, Metrics: cost(kv[i+1])})
	}
	return out
}

// months relative to now: the current one, the one before, and two before.
func months() (this, prev, prev2 time.Time) {
	now := time.Now().UTC()
	this = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return this, this.AddDate(0, -1, 0), this.AddDate(0, -2, 0)
}

// newFake is a two-page monthly answer:
//
//	two months ago: EC2 100 + S3 50                                   = 150
//	last month:     RDS 300 + EC2 400 (page 1), S3 90 + CloudWatch 10 (page 2) = 800
//	this month:     RDS 197 + EC2 95 + Lambda 5 + CloudWatch 3 + Tax 0.004 ≈ 300
//
// and a forecast of 700 more (600–800 at 80%).
func newFake() *fakeCE {
	this, prev, prev2 := months()
	daily := &costexplorer.GetCostAndUsageOutput{}
	for i := range 30 {
		daily.ResultsByTime = append(daily.ResultsByTime, cetypes.ResultByTime{Total: cost(fmt.Sprint(10 + i))})
	}
	return &fakeCE{
		monthly: []*costexplorer.GetCostAndUsageOutput{
			{ResultsByTime: []cetypes.ResultByTime{
				{TimePeriod: period(prev2), Groups: groups("Amazon Elastic Compute Cloud - Compute", "100", "Amazon Simple Storage Service", "50")},
				{TimePeriod: period(prev), Groups: groups("Amazon Relational Database Service", "300", "Amazon Elastic Compute Cloud - Compute", "400")},
			}},
			{ResultsByTime: []cetypes.ResultByTime{
				{TimePeriod: period(prev), Groups: groups("Amazon Simple Storage Service", "90", "AmazonCloudWatch", "10")},
				{TimePeriod: period(this), Groups: groups(
					"Amazon Relational Database Service", "197",
					"Amazon Elastic Compute Cloud - Compute", "95",
					"AWS Lambda", "5",
					"AmazonCloudWatch", "3",
					"Tax", "0.004",
				)},
			}},
		},
		daily: daily,
		forecast: &costexplorer.GetCostForecastOutput{
			Total: &cetypes.MetricValue{Amount: aws.String("700")},
			ForecastResultsByTime: []cetypes.ForecastResult{
				{PredictionIntervalLowerBound: aws.String("600"), PredictionIntervalUpperBound: aws.String("800")},
			},
		},
	}
}

func newPane(t *testing.T, f *fakeCE) *Billing {
	t.Helper()
	return &Billing{
		every: time.Hour,
		ce:    f,
		cache: filepath.Join(t.TempDir(), "Caches", "rexdash", "billing-test.json"),
		first: true,
		st:    ui.NewTableState(),
	}
}

func near(a, b float64) bool { return a-b < 0.01 && b-a < 0.01 }

func TestFetchSumsPagesIntoMonthsAndServices(t *testing.T) {
	f := newFake()
	p := newPane(t, f)
	d, err := p.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	this, prev, prev2 := months()

	// Requests: two monthly pages (the second carrying the token), daily, forecast.
	if len(f.usage) != 3 || len(f.fcs) != 1 {
		t.Fatalf("calls: usage %d forecast %d", len(f.usage), len(f.fcs))
	}
	m1, m2, day := f.usage[0], f.usage[1], f.usage[2]
	if m1.Granularity != cetypes.GranularityMonthly || m1.NextPageToken != nil || aws.ToString(m2.NextPageToken) != "page-1" {
		t.Errorf("monthly pages: %v %v %v", m1.Granularity, m1.NextPageToken, m2.NextPageToken)
	}
	if got := aws.ToString(m1.TimePeriod.Start); got != this.AddDate(0, -6, 0).Format(time.DateOnly) {
		t.Errorf("monthly start %s", got)
	}
	if len(m1.GroupBy) != 1 || aws.ToString(m1.GroupBy[0].Key) != "SERVICE" {
		t.Errorf("group by %+v", m1.GroupBy)
	}
	if day.Granularity != cetypes.GranularityDaily || day.GroupBy != nil {
		t.Errorf("daily request %+v", day)
	}
	fc := f.fcs[0]
	if aws.ToInt32(fc.PredictionIntervalLevel) != 80 || fc.Metric != cetypes.MetricUnblendedCost ||
		aws.ToString(fc.TimePeriod.End) != this.AddDate(0, 1, 0).Format(time.DateOnly) {
		t.Errorf("forecast request %+v", fc)
	}

	// Months: oldest first, last month's groups summed across both pages.
	want := []month{{prev2.Format("2006-01"), 150}, {prev.Format("2006-01"), 800}, {this.Format("2006-01"), 300.004}}
	if len(d.Months) != len(want) {
		t.Fatalf("months %v", d.Months)
	}
	for i, m := range d.Months {
		if m.YM != want[i].YM || !near(m.Total, want[i].Total) {
			t.Errorf("month %d = %v, want %v", i, m, want[i])
		}
	}

	// Services: this and last month only, sub-cent ones dropped, most spent this month first.
	var names []string
	for _, s := range d.Services {
		names = append(names, serviceName(s.Name))
	}
	if !slices.Equal(names, []string{"RDS", "EC2", "Lambda", "CloudWatch", "S3"}) {
		t.Errorf("services %v", names)
	}
	if s := d.Services[0]; s.MTD != 197 || s.Last != 300 {
		t.Errorf("RDS %+v", s)
	}
	if s := d.Services[1]; s.MTD != 95 || s.Last != 400 {
		t.Errorf("EC2 (not counting two months ago) %+v", s)
	}
	if s := d.Services[4]; s.MTD != 0 || s.Last != 90 {
		t.Errorf("S3 (from page 2) %+v", s)
	}

	if len(d.Daily) != 30 || d.Daily[0] != 10 || d.Daily[29] != 39 {
		t.Errorf("daily %v", d.Daily)
	}
	if d.Forecast == nil || *d.Forecast != (forecast{700, 600, 800}) {
		t.Errorf("forecast %+v", d.Forecast)
	}
	if v, ok := d.projected(); !ok || !near(v, 1000) {
		t.Errorf("projected %v", v)
	}
	if ui.Now()-d.Fetched > 5 {
		t.Errorf("fetched %d", d.Fetched)
	}
}

func TestFailedForecastMeansNoProjection(t *testing.T) {
	f := newFake()
	f.fcErr = &smithy.GenericAPIError{Code: "DataUnavailableException", Message: "Insufficient amount of historical data"}
	p := newPane(t, f)
	if err := p.Refresh(); err != nil {
		t.Fatalf("a failed forecast must not fail the refresh: %v", err)
	}
	if p.data.Forecast != nil {
		t.Errorf("forecast %+v", p.data.Forecast)
	}
	if _, ok := p.data.projected(); ok {
		t.Error("projection without a forecast")
	}
	if st := p.Status(); st.State != "idle" || st.Msg != "MTD $300" {
		t.Errorf("status %+v", st)
	}
	raw := readCache(t, p.cache)
	if string(raw["forecast"]) != "null" {
		t.Errorf("cached forecast %s", raw["forecast"])
	}

	b := ui.NewBuffer(110, 30)
	p.Draw(b, b.Area())
	text := b.Text()
	for _, s := range []string{"not enough data yet", monthLabel(0) + "*"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in\n%s", s, text)
		}
	}
	// The current month's bar is its total so far, not a projection.
	if strings.Contains(text, "~") {
		t.Errorf("projected bar without a forecast:\n%s", text)
	}
}

func TestFetchErrorsSurfaceAndKeepData(t *testing.T) {
	f := newFake()
	f.usageErr = &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not allowed"}
	p := newPane(t, f)
	err := p.Refresh()
	if err == nil || err.Error() != "AccessDeniedException: not allowed" {
		t.Fatalf("err %v", err)
	}
	if p.data != nil {
		t.Error("data set after a failed fetch")
	}
	if _, statErr := os.Stat(p.cache); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("cache written after a failed fetch")
	}
	if st := p.Status(); st.State != "working" || st.Msg != "loading" {
		t.Errorf("status %+v", st)
	}
	b := ui.NewBuffer(80, 20)
	p.Draw(b, b.Area())
	if !strings.Contains(b.Text(), "Loading costs") {
		t.Errorf("empty state:\n%s", b.Text())
	}
	if p.Title().String() != "" {
		t.Error("title before data")
	}

	// The daily request failing is also an error.
	f = newFake()
	p = newPane(t, f)
	p.ce = &failDaily{f}
	if err := p.Refresh(); err == nil || !strings.Contains(err.Error(), "Throttling") {
		t.Errorf("daily err %v", err)
	}
}

type failDaily struct{ *fakeCE }

func (f *failDaily) GetCostAndUsage(ctx context.Context, in *costexplorer.GetCostAndUsageInput, o ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	if in.Granularity == cetypes.GranularityDaily {
		return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}
	}
	return f.fakeCE.GetCostAndUsage(ctx, in, o...)
}

func readCache(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeCache(t *testing.T, path string, d *data) {
	t.Helper()
	b, _ := json.Marshal(d)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFetchWritesCacheInRustFormat(t *testing.T) {
	p := newPane(t, newFake())
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	raw := readCache(t, p.cache)
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"daily", "fetched", "forecast", "months", "services"}) {
		t.Errorf("keys %v", keys)
	}
	var fetched int64
	var ms [][]any
	var ss [][]any
	var fc []float64
	var daily []float64
	if json.Unmarshal(raw["fetched"], &fetched) != nil || json.Unmarshal(raw["months"], &ms) != nil ||
		json.Unmarshal(raw["services"], &ss) != nil || json.Unmarshal(raw["forecast"], &fc) != nil ||
		json.Unmarshal(raw["daily"], &daily) != nil {
		t.Fatalf("shape: %v", raw)
	}
	_, prev, _ := months()
	if len(ms) != 3 || len(ms[1]) != 2 || ms[1][0] != prev.Format("2006-01") || ms[1][1] != 800.0 {
		t.Errorf("months %v", ms)
	}
	if len(ss) != 5 || len(ss[0]) != 3 || ss[0][0] != "Amazon Relational Database Service" || ss[0][1] != 197.0 || ss[0][2] != 300.0 {
		t.Errorf("services %v", ss)
	}
	if !slices.Equal(fc, []float64{700, 600, 800}) || len(daily) != 30 {
		t.Errorf("forecast %v daily %d", fc, len(daily))
	}
	// What was written reads back as what is shown.
	got, ok := p.loadCache()
	if !ok || got.mtd() != p.data.mtd() || len(got.Services) != 5 {
		t.Errorf("reload %+v", got)
	}
}

func cached() *data {
	this, prev, _ := months()
	return &data{
		Fetched:  ui.Now() - 60,
		Months:   []month{{prev.Format("2006-01"), 1646}, {this.Format("2006-01"), 518}},
		Services: []service{{"Amazon Relational Database Service", 200, 700}},
		Daily:    []float64{50, 60},
	}
}

func TestFreshCacheSkipsTheFirstFetchOnly(t *testing.T) {
	f := newFake()
	p := newPane(t, f)
	writeCache(t, p.cache, cached())

	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if n := f.calls(); n != 0 {
		t.Fatalf("fresh cache made %d Cost Explorer calls", n)
	}
	if p.data.mtd() != 518 {
		t.Errorf("not from cache: %v", p.data.Months)
	}
	if st := p.Status(); st.Msg != "MTD $518" {
		t.Errorf("status %+v", st)
	}
	if got := p.Title().String(); got != "cost explorer · fetched 1m ago" {
		t.Errorf("title %q", got)
	}

	// Later refreshes (timer or `r`) always fetch, even with the cache still fresh.
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if n := f.calls(); n != 4 {
		t.Errorf("second refresh made %d calls, want 4", n)
	}
	if !near(p.data.mtd(), 300) {
		t.Errorf("not refetched: %v", p.data.Months)
	}
	if got := p.Title().String(); got != "cost explorer · fetched just now" {
		t.Errorf("title %q", got)
	}
}

func TestStaleOrBadCacheFetches(t *testing.T) {
	for name, write := range map[string]func(t *testing.T, path string){
		"stale": func(t *testing.T, path string) {
			d := cached()
			d.Fetched = ui.Now() - 2*3600 // every is 1h
			writeCache(t, path, d)
		},
		"missing": func(*testing.T, string) {},
		"corrupt": func(t *testing.T, path string) {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, []byte(`{"fetched": 1}`), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			p := newPane(t, f)
			write(t, p.cache)
			if err := p.Refresh(); err != nil {
				t.Fatal(err)
			}
			if n := f.calls(); n != 4 {
				t.Errorf("%d calls, want 4", n)
			}
			// The cache now holds the new fetch.
			if d, ok := p.loadCache(); !ok || !near(d.mtd(), 300) || ui.Now()-d.Fetched > 5 {
				t.Errorf("cache not rewritten: %+v", d)
			}
		})
	}
}

// monthLabel is the bar label of the month n before the current one.
func monthLabel(n int) string {
	this, _, _ := months()
	return this.AddDate(0, -n, 0).Format("Jan")
}

// row is the buffer line holding text, or -1.
func row(b *ui.Buffer, text string) int {
	if _, y, ok := b.Find(text); ok {
		return y
	}
	return -1
}

func TestDrawShowsTilesBarsAndServices(t *testing.T) {
	p := newPane(t, newFake())
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	b := ui.NewBuffer(120, 30)
	p.Draw(b, b.Area())
	text := b.Text()
	lines := strings.Split(text, "\n")
	now := time.Now().UTC()

	// KPI tiles.
	for _, s := range []string{
		"This month", "$300", fmt.Sprintf("day %d of %d", now.Day(), daysInMonth(now)),
		"Projected", "$1,000", "25% vs last month · $900–$1,100", // 1000 vs 800
		"Last month", "$800", "433% vs month before", // 800 vs 150
		"Daily · 30d", "avg $25 · max $39",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in\n%s", s, text)
		}
	}

	// Month bars: past months plain, the current one starred and drawn as its projection.
	for _, s := range []string{monthLabel(2), monthLabel(1), monthLabel(0) + "*", "~1.0k"} {
		if !strings.Contains(text, s) {
			t.Errorf("bars: missing %q in\n%s", s, text)
		}
	}
	if strings.Contains(text, monthLabel(1)+"*") {
		t.Error("last month starred")
	}

	// Services: most spent this month first, with the change of the projection vs last month.
	order := []string{"RDS", "EC2", "Lambda", "CloudWatch", "S3"}
	ys := make([]int, len(order))
	for i, n := range order {
		ys[i] = row(b, n)
		if ys[i] < 0 {
			t.Fatalf("no %s row in\n%s", n, text)
		}
		if i > 0 && ys[i] <= ys[i-1] {
			t.Errorf("%s drawn above %s", n, order[i-1])
		}
	}
	for n, want := range map[string][]string{
		"RDS":        {"$197", "$657", "$300", "+119%"},
		"EC2":        {"$95", "$317", "$400", "−21%"},
		"Lambda":     {"$5.00", "$17", "$0.00", "new"},
		"CloudWatch": {"$3.00", "$10", "same"},
		"S3":         {"$0.00", "$90", "−100%"},
	} {
		line := lines[row(b, n)]
		for _, w := range want {
			if !strings.Contains(line, w) {
				t.Errorf("%s row %q lacks %q", n, line, w)
			}
		}
	}

	if st := p.Status(); st.State != "idle" || st.Msg != "MTD $300 · projected $1,000" {
		t.Errorf("status %+v", st)
	}
}

func TestProjectedTileColoursAndDownArrow(t *testing.T) {
	// Spend falling: projected 600 vs 800 last month.
	f := newFake()
	f.forecast.Total.Amount = aws.String("300")
	f.forecast.ForecastResultsByTime = nil // no interval: no range text
	p := newPane(t, f)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	b := ui.NewBuffer(110, 30)
	p.Draw(b, b.Area())
	text := b.Text()
	if !strings.Contains(text, DOWN.S()+" 25% vs last month") || strings.Contains(text, "–$") {
		t.Errorf("falling projection:\n%s", text)
	}
	x, y, _ := b.Find("$600")
	if st := b.StyleAt(x, y); st.Fg != ui.Green() {
		t.Errorf("falling projection colour %v", st.Fg)
	}
}

func TestKeysMoveSelection(t *testing.T) {
	p := newPane(t, newFake())
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if p.st.Sel != 0 {
		t.Fatalf("sel %d after refresh", p.st.Sel)
	}
	_ = p.Key(tcell.NewEventKey(tcell.KeyDown, 0, 0))
	_ = p.Key(tcell.NewEventKey(tcell.KeyRune, 'j', 0))
	if p.st.Sel != 2 {
		t.Errorf("sel %d", p.st.Sel)
	}
	for range 10 {
		_ = p.Key(tcell.NewEventKey(tcell.KeyDown, 0, 0))
	}
	if p.st.Sel != 4 {
		t.Errorf("sel %d past the end", p.st.Sel)
	}
	_ = p.Key(tcell.NewEventKey(tcell.KeyRune, 'o', 0)) // opens nothing under test
	if p.Name() != "Billing" || p.Every() != time.Hour || len(p.Hints()) != 2 {
		t.Error("pane basics")
	}
}

func TestNewUsesProfileCachePath(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, "config")
	if err := os.WriteFile(cfg, []byte("[profile work]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
	for profile, file := range map[string]string{"": "billing-default.json", "work": "billing-work.json"} {
		t.Setenv("AWS_PROFILE", profile)
		pane, err := New(3 * time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		p := pane.(*Billing)
		if want := filepath.Join(home, "Library", "Caches", "rexdash", file); p.cache != want {
			t.Errorf("cache %q, want %q", p.cache, want)
		}
		if _, ok := p.ce.(*costexplorer.Client); !ok || !p.first || p.Every() != 3*time.Hour {
			t.Errorf("pane %+v", p)
		}
	}
}
