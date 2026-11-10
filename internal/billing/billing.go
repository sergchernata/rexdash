// Package billing is the Billing pane: monthly AWS spend, this month's projection, daily spend
// and top services (Cost Explorer).
//
// Cost Explorer charges $0.01 per request and its data only updates a few times a day,
// so a refresh makes three requests, runs every few hours, and is cached on disk so
// restarting the pane doesn't pay again.
package billing

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/awsx"
	"rexdash/internal/ui"
)

var (
	DOLLAR = ui.Ic{Glyph: "", Plain: "$"}
	CHART  = ui.Ic{Glyph: "", Plain: ""}
	UP     = ui.Ic{Glyph: "", Plain: "↑"}
	DOWN   = ui.Ic{Glyph: "", Plain: "↓"}
)

// month is (YYYY-MM, total), stored as a JSON pair.
type month struct {
	YM    string
	Total float64
}

// service is (name, this month so far, last month), stored as a JSON triple.
type service struct {
	Name      string
	MTD, Last float64
}

// forecast is the rest of this month: (mean, low, high) at 80% confidence.
type forecast struct{ Mean, Lo, Hi float64 }

func (m month) MarshalJSON() ([]byte, error)    { return json.Marshal([]any{m.YM, m.Total}) }
func (m *month) UnmarshalJSON(b []byte) error   { return tuple(b, &m.YM, &m.Total) }
func (s service) MarshalJSON() ([]byte, error)  { return json.Marshal([]any{s.Name, s.MTD, s.Last}) }
func (s *service) UnmarshalJSON(b []byte) error { return tuple(b, &s.Name, &s.MTD, &s.Last) }
func (f forecast) MarshalJSON() ([]byte, error) { return json.Marshal([]float64{f.Mean, f.Lo, f.Hi}) }
func (f *forecast) UnmarshalJSON(b []byte) error {
	return tuple(b, &f.Mean, &f.Lo, &f.Hi)
}

// tuple reads a JSON array of exactly len(dst) items.
func tuple(b []byte, dst ...any) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) != len(dst) {
		return fmt.Errorf("want %d items, got %d", len(dst), len(raw))
	}
	for i, r := range raw {
		if err := json.Unmarshal(r, dst[i]); err != nil {
			return err
		}
	}
	return nil
}

// data is one fetch; its JSON is the cache format shared with the Rust version.
type data struct {
	// Last 30 days, oldest first.
	Daily    []float64 `json:"daily"`
	Fetched  int64     `json:"fetched"`
	Forecast *forecast `json:"forecast"`
	// The last entry is the current month so far.
	Months   []month   `json:"months"`
	Services []service `json:"services"`
}

// parse reads a cache file; every field but forecast is required.
func parse(b []byte) (*data, bool) {
	var raw struct {
		Daily    *[]float64 `json:"daily"`
		Fetched  *int64     `json:"fetched"`
		Forecast *forecast  `json:"forecast"`
		Months   *[]month   `json:"months"`
		Services *[]service `json:"services"`
	}
	if json.Unmarshal(b, &raw) != nil || raw.Daily == nil || raw.Fetched == nil || raw.Months == nil || raw.Services == nil {
		return nil, false
	}
	return &data{Daily: *raw.Daily, Fetched: *raw.Fetched, Forecast: raw.Forecast, Months: *raw.Months, Services: *raw.Services}, true
}

func (d *data) mtd() float64 {
	if len(d.Months) == 0 {
		return 0
	}
	return d.Months[len(d.Months)-1].Total
}

// nthLast is the total of the month n before the current one.
func (d *data) nthLast(n int) (float64, bool) {
	if i := len(d.Months) - 1 - n; i >= 0 {
		return d.Months[i].Total, true
	}
	return 0, false
}

func (d *data) lastMonth() (float64, bool) { return d.nthLast(1) }

func (d *data) projected() (float64, bool) {
	if d.Forecast == nil {
		return 0, false
	}
	return d.mtd() + d.Forecast.Mean, true
}

// money is "$2,175" (whole dollars with thousands separators; cents below $10).
func money(v float64) string {
	if math.Abs(v) < 10 {
		return fmt.Sprintf("$%.2f", v)
	}
	n := int64(math.Round(v))
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	digits := strconv.FormatInt(n, 10)
	var out strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(c)
	}
	return sign + "$" + out.String()
}

// shortMoney is "2.2k", "640", for bar labels.
func shortMoney(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1fk", v/1000)
	}
	return fmt.Sprintf("%.0f", v)
}

// delta is the percent change from old to new; none without a base.
func delta(new, old float64) (float64, bool) {
	if math.Abs(old) <= 0.005 {
		return 0, false
	}
	return (new - old) / old * 100, true
}

var known = map[string]string{
	"Amazon Relational Database Service":     "RDS",
	"Amazon Elastic Compute Cloud - Compute": "EC2",
	"EC2 - Other":                            "EC2 other (EBS, NAT…)",
	"Amazon Elastic Container Service":       "ECS",
	"Amazon EC2 Container Registry (ECR)":    "ECR",
	"Amazon Simple Storage Service":          "S3",
	"Amazon Virtual Private Cloud":           "VPC",
	"Amazon Elastic Load Balancing":          "Load Balancing",
	"Elastic Load Balancing":                 "Load Balancing",
	"AmazonCloudWatch":                       "CloudWatch",
	"AWS Key Management Service":             "KMS",
	"Amazon Simple Email Service":            "SES",
	"Amazon Simple Notification Service":     "SNS",
	"Amazon Simple Queue Service":            "SQS",
	"Amazon Elastic File System":             "EFS",
	"Amazon ElastiCache":                     "ElastiCache",
}

// serviceName shortens Cost Explorer's long service names for a narrow column.
func serviceName(s string) string {
	if short, ok := known[s]; ok {
		return short
	}
	return strings.TrimPrefix(strings.TrimPrefix(s, "Amazon "), "AWS ")
}

func daysInMonth(d time.Time) int {
	return time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func amount(m map[string]cetypes.MetricValue) float64 {
	return num(m["UnblendedCost"].Amount)
}

func num(s *string) float64 {
	v, _ := strconv.ParseFloat(aws.ToString(s), 64)
	return v
}

func apiErr(err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return fmt.Errorf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	return err
}

type Billing struct {
	ui.Base
	every time.Duration
	ce    *costexplorer.Client
	cache string
	// The first refresh may use the disk cache; later ones (timer or `r`) always fetch.
	first bool
	data  *data
	st    ui.TableState
}

func New(every time.Duration) (ui.Pane, error) {
	cfg, err := awsx.Config(context.Background())
	if err != nil {
		return nil, err
	}
	profile := os.Getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	home, _ := os.UserHomeDir()
	return &Billing{
		every: every,
		ce:    costexplorer.NewFromConfig(cfg),
		cache: filepath.Join(home, "Library", "Caches", "rexdash", "billing-"+profile+".json"),
		first: true,
		st:    ui.NewTableState(),
	}, nil
}

func (p *Billing) loadCache() (*data, bool) {
	b, err := os.ReadFile(p.cache)
	if err != nil {
		return nil, false
	}
	return parse(b)
}

func (p *Billing) saveCache(d *data) {
	_ = os.MkdirAll(filepath.Dir(p.cache), 0o755)
	if b, err := json.Marshal(d); err == nil {
		_ = os.WriteFile(p.cache, b, 0o644)
	}
}

func (p *Billing) fetch(ctx context.Context) (*data, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)
	monthStart := today.AddDate(0, 0, 1-today.Day())
	iv := func(a, b time.Time) *cetypes.DateInterval {
		return &cetypes.DateInterval{Start: aws.String(a.Format(time.DateOnly)), End: aws.String(b.Format(time.DateOnly))}
	}

	// 1. Monthly totals by service, six full months plus this one.
	var months []month
	bySvc := map[string]*service{}
	this, prev := monthStart.Format("2006-01"), monthStart.AddDate(0, -1, 0).Format("2006-01")
	in := &costexplorer.GetCostAndUsageInput{
		TimePeriod:  iv(monthStart.AddDate(0, -6, 0), tomorrow),
		Granularity: cetypes.GranularityMonthly,
		Metrics:     []string{"UnblendedCost"},
		GroupBy:     []cetypes.GroupDefinition{{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("SERVICE")}},
	}
	for {
		r, err := p.ce.GetCostAndUsage(ctx, in)
		if err != nil {
			return nil, apiErr(err)
		}
		for _, t := range r.ResultsByTime {
			m := ""
			if t.TimePeriod != nil {
				m = aws.ToString(t.TimePeriod.Start)
				m = m[:min(len(m), 7)]
			}
			sum := 0.0
			for _, g := range t.Groups {
				v := amount(g.Metrics)
				sum += v
				if m != this && m != prev {
					continue
				}
				name := ""
				if len(g.Keys) > 0 {
					name = g.Keys[0]
				}
				s := bySvc[name]
				if s == nil {
					s = &service{Name: name}
					bySvc[name] = s
				}
				if m == this {
					s.MTD += v
				} else {
					s.Last += v
				}
			}
			if i := slices.IndexFunc(months, func(x month) bool { return x.YM == m }); i >= 0 {
				months[i].Total += sum
			} else {
				months = append(months, month{m, sum})
			}
		}
		if r.NextPageToken == nil {
			break
		}
		in.NextPageToken = r.NextPageToken
	}
	slices.SortFunc(months, func(a, b month) int { return cmp.Compare(a.YM, b.YM) })
	var services []service
	for _, s := range bySvc {
		if s.MTD >= 0.01 || s.Last >= 0.01 {
			services = append(services, *s)
		}
	}
	slices.SortFunc(services, func(a, b service) int {
		return cmp.Or(cmp.Compare(b.MTD, a.MTD), cmp.Compare(b.Last, a.Last), cmp.Compare(a.Name, b.Name))
	})

	// 2. Daily spend for the last 30 days.
	r, err := p.ce.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		TimePeriod:  iv(today.AddDate(0, 0, -30), tomorrow),
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
	})
	if err != nil {
		return nil, apiErr(err)
	}
	daily := []float64{}
	for _, t := range r.ResultsByTime {
		daily = append(daily, amount(t.Total))
	}

	// 3. Forecast for the rest of the month (fails early in a month with little data: then no projection).
	var fc *forecast
	f, err := p.ce.GetCostForecast(ctx, &costexplorer.GetCostForecastInput{
		TimePeriod:              iv(today, monthStart.AddDate(0, 1, 0)),
		Metric:                  cetypes.MetricUnblendedCost,
		Granularity:             cetypes.GranularityMonthly,
		PredictionIntervalLevel: aws.Int32(80),
	})
	if err == nil {
		fc = &forecast{}
		if f.Total != nil {
			fc.Mean = num(f.Total.Amount)
		}
		for _, x := range f.ForecastResultsByTime {
			fc.Lo += num(x.PredictionIntervalLowerBound)
			fc.Hi += num(x.PredictionIntervalUpperBound)
		}
	}

	if services == nil {
		services = []service{}
	}
	if months == nil {
		months = []month{}
	}
	return &data{Fetched: time.Now().Unix(), Months: months, Services: services, Daily: daily, Forecast: fc}, nil
}

func (p *Billing) Name() string { return "Billing" }

func (p *Billing) Title() ui.Line {
	if p.data == nil {
		return ui.Line{}
	}
	when := "cost explorer · fetched just now"
	if a := ui.Ago(p.data.Fetched); a != "now" {
		when = "cost explorer · fetched " + a + " ago"
	}
	return ui.LT(when, ui.Fg(ui.Muted()))
}

func (p *Billing) Hints() []ui.Hint {
	return []ui.Hint{{Key: "↑↓", Desc: "services"}, {Key: "o", Desc: "Cost Explorer"}}
}

func (p *Billing) Every() time.Duration { return p.every }

func (p *Billing) Status() ui.Status {
	d := p.data
	if d == nil {
		return ui.Status{State: "working", Msg: "loading"}
	}
	msg := "MTD " + money(d.mtd())
	if v, ok := d.projected(); ok {
		msg += " · projected " + money(v)
	}
	return ui.Status{State: "idle", Msg: msg}
}

func (p *Billing) Refresh() error {
	if p.first {
		p.first = false
		if d, ok := p.loadCache(); ok && ui.Now()-d.Fetched < int64(p.every/time.Second) {
			p.data = d
			return nil
		}
	}
	d, err := p.fetch(context.Background())
	if err != nil {
		return err
	}
	p.saveCache(d)
	p.data = d
	ui.Clamp(&p.st.Sel, len(d.Services))
	return nil
}

func (p *Billing) Key(ev *tcell.EventKey) error {
	if ui.IsRune(ev, 'o') {
		ui.OpenURL("https://us-east-1.console.aws.amazon.com/costmanagement/home#/cost-explorer")
		return nil
	}
	n := 0
	if p.data != nil {
		n = len(p.data.Services)
	}
	ui.Nav(&p.st.Sel, n, ev, 10)
	return nil
}

// arrow is ↑ or ↓ by the sign of x.
func arrow(x float64) string {
	if x >= 0 {
		return UP.S()
	}
	return DOWN.S()
}

// kpi draws a boxed figure: label, big value, and a muted line under it.
func kpi(b *ui.Buffer, area ui.Rect, label, value, sub string, color ui.Color) {
	inner := ui.Panel{Title: ui.LT(" "+label+" ", ui.Fg(ui.Muted()))}.Draw(b, area)
	lines := []ui.Line{
		ui.LT(value, ui.Style{Fg: color, Bold: true}).Centered(),
		ui.LT(sub, ui.Fg(ui.Muted())).Centered(),
	}
	ui.Paragraph(b, ui.Middle(inner, 2), lines, false)
}

func (p *Billing) Draw(b *ui.Buffer, area ui.Rect) {
	d := p.data
	if d == nil {
		ui.EmptyState(b, area, DOLLAR.S(), ui.Muted(), "Loading costs…", "Cost Explorer")
		return
	}
	parts := ui.Vertical(area, 0, ui.Len(5), ui.Len(10), ui.Min(4))
	ka, ca, sa := parts[0], parts[1], parts[2]

	// KPI tiles: this month so far, projected, last month.
	cols := ui.Horizontal(ka, 1, ui.Fill(1), ui.Fill(1), ui.Fill(1))
	today := time.Now().UTC()
	days := daysInMonth(today)
	last, hasLast := d.lastMonth()
	proj, hasProj := d.projected()
	kpi(b, cols[0], "This month", money(d.mtd()), fmt.Sprintf("day %d of %d", today.Day(), days), ui.FgC())
	if hasProj {
		txt, color := "", ui.Muted()
		if x, ok := delta(proj, last); hasLast && ok {
			txt = fmt.Sprintf("%s %.0f%% vs last month", arrow(x), math.Abs(x))
			if x > 5 {
				color = ui.Red()
			} else if x < -5 {
				color = ui.Green()
			}
		}
		lo, hi := d.mtd()+d.Forecast.Lo, d.mtd()+d.Forecast.Hi
		if hi > lo {
			txt = fmt.Sprintf("%s · %s–%s", txt, money(lo), money(hi))
		}
		kpi(b, cols[1], "Projected", money(proj), txt, color)
	} else {
		kpi(b, cols[1], "Projected", "—", "not enough data yet", ui.Muted())
	}
	sub, lastTxt := "", "—"
	if hasLast {
		lastTxt = money(last)
		if before, ok := d.nthLast(2); ok {
			if x, ok := delta(last, before); ok {
				sub = fmt.Sprintf("%s %.0f%% vs month before", arrow(x), math.Abs(x))
			}
		}
	}
	kpi(b, cols[2], "Last month", lastTxt, sub, ui.FgC())

	// Monthly bars (current month drawn as its projection) and daily spend.
	halves := ui.Horizontal(ca, 1, ui.Fill(1), ui.Fill(1))
	inner := ui.Panel{Title: ui.LT(fmt.Sprintf(" %s Months ", CHART.S()), ui.Style{})}.Draw(b, halves[0])
	n := max(len(d.Months), 1)
	width := min(max(max(inner.W-(n-1), 0)/n, 1), 9)
	var bars []ui.Bar
	for i, m := range d.Months {
		current := i == len(d.Months)-1
		label := ""
		if t, err := time.Parse("2006-01", m.YM); err == nil {
			label = t.Format("Jan")
		}
		value, color, text := m.Total, ui.Blue(), shortMoney(m.Total)
		if current {
			label += "*"
			color = ui.Accent()
			if hasProj {
				value, color, text = proj, ui.Yellow(), "~"+shortMoney(proj)
			}
		}
		bars = append(bars, ui.Bar{
			Value:      uint64(math.Max(value, 0)),
			Label:      label,
			Text:       text,
			Style:      ui.Fg(color),
			ValueStyle: ui.Style{Fg: ui.Bg(), Bg: color},
		})
	}
	ui.BarChart(b, inner, bars, width, 1, ui.Fg(ui.Muted()))

	sum, top := 0.0, 0.0
	for _, v := range d.Daily {
		sum += v
		top = math.Max(top, v)
	}
	avg := 0.0
	if len(d.Daily) > 0 {
		avg = sum / float64(len(d.Daily))
	}
	inner = ui.Panel{
		Title: ui.LT(fmt.Sprintf(" %s Daily · 30d ", ui.CALENDAR.S()), ui.Style{}),
		Right: ui.LT(fmt.Sprintf(" avg %s · max %s ", money(avg), money(top)), ui.Fg(ui.Muted())),
	}.Draw(b, halves[1])
	cents := make([]uint64, len(d.Daily))
	for i, v := range d.Daily {
		cents[i] = uint64(math.Max(v*100, 0))
	}
	ui.Sparkline(b, inner, ui.Stretch(cents, inner.W), 0, ui.Fg(ui.Accent()))

	// Services: this month so far, projected at the month's pace, last month, change.
	factor := float64(days) / float64(max(today.Day(), 1))
	if hasProj && d.mtd() > 0 {
		factor = proj / d.mtd()
	}
	most := 1.0
	if len(d.Services) > 0 {
		most = math.Max(d.Services[0].MTD*factor, d.Services[0].Last)
	}
	most = math.Max(most, 0.01)
	right := func(s string, st ui.Style) ui.Line { return ui.LT(s, st).RightAligned() }
	muted := ui.Fg(ui.Muted())
	var rows []ui.Row
	for _, s := range d.Services {
		proj := s.MTD * factor
		change := right("new", ui.Fg(ui.Yellow()))
		if x, ok := delta(proj, s.Last); ok && math.Abs(x) >= 1 {
			sign, color := "+", ui.Muted()
			if x < 0 {
				sign = "−"
			}
			if x > 10 {
				color = ui.Red()
			} else if x < -10 {
				color = ui.Green()
			}
			change = right(fmt.Sprintf("%s%.0f%%", sign, math.Abs(x)), ui.Fg(color))
		} else if ok {
			change = right("same", muted)
		}
		bar := strings.Repeat("▇", int(math.Max(0, math.Min(12, math.Round(proj/most*12)))))
		rows = append(rows, ui.R(
			ui.LT(serviceName(s.Name), ui.Style{}),
			right(money(s.MTD), ui.Style{}),
			right(money(proj), muted),
			right(money(s.Last), muted),
			change,
			ui.LT(bar, ui.Fg(ui.Blue())),
		))
	}
	ui.Table{
		Cols: []ui.Constraint{ui.Min(14), ui.Len(9), ui.Len(9), ui.Len(10), ui.Len(6), ui.Len(12)},
		Header: []ui.Line{
			ui.LT("Service", ui.Style{}), right("So far", ui.Style{}), right("Projected", ui.Style{}),
			right("Last month", ui.Style{}), right("Change", ui.Style{}), {},
		},
		Rows:      rows,
		Spacing:   2,
		Highlight: true,
		Block:     &ui.Panel{Title: ui.LT(fmt.Sprintf(" %s Services ", ui.CHECKLIST.S()), ui.Style{})},
	}.Draw(b, sa, &p.st)
	ui.Scrollbar(b, sa, len(d.Services), max(p.st.Sel, 0), sa.H-3)
}
