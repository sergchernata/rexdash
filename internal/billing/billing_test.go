package billing

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"rexdash/internal/cases"
)

func TestMoneyFormat(t *testing.T) {
	for in, want := range map[float64]string{0: "$0.00", 4.567: "$4.57", 518.06: "$518", 2175.4: "$2,175", 1234567: "$1,234,567", -1500: "-$1,500"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
	if shortMoney(2175) != "2.2k" || shortMoney(640.2) != "640" {
		t.Error("shortMoney")
	}
}

func TestChangePercent(t *testing.T) {
	if x, ok := delta(110, 100); !ok || x != 10 {
		t.Error("up 10%")
	}
	if x, ok := delta(50, 100); !ok || x != -50 {
		t.Error("down 50%")
	}
	if _, ok := delta(5, 0); ok {
		t.Error("no base, no percentage")
	}
}

func TestServiceNames(t *testing.T) {
	for in, want := range map[string]string{"Amazon Relational Database Service": "RDS", "AmazonCloudWatch": "CloudWatch", "AWS Lambda": "Lambda", "Amazon CloudFront": "CloudFront", "Tax": "Tax"} {
		if got := serviceName(in); got != want {
			t.Errorf("serviceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMonthLengths(t *testing.T) {
	for _, c := range []struct {
		y, m, d, want int
	}{{2026, 11, 10, 30}, {2026, 12, 31, 31}, {2028, 2, 3, 29}} {
		if got := daysInMonth(time.Date(c.y, time.Month(c.m), c.d, 0, 0, 0, 0, time.UTC)); got != c.want {
			t.Errorf("%v: got %d", c, got)
		}
	}
}

func TestProjectionAndCacheRoundTrip(t *testing.T) {
	d := &data{
		Fetched:  1,
		Months:   []month{{"2026-09", 1900}, {"2026-10", 1646}, {"2026-11", 518}},
		Services: []service{{"Amazon Relational Database Service", 200, 700}},
		Daily:    []float64{50, 60},
		Forecast: &forecast{1657, 1400, 1900},
	}
	if p, _ := d.projected(); d.mtd() != 518 || p != 2175 {
		t.Error("projection")
	}
	if l, ok := d.lastMonth(); !ok || l != 1646 {
		t.Error("last month")
	}
	b, _ := json.Marshal(d)
	if got, ok := parse(b); !ok || !reflect.DeepEqual(got, d) {
		t.Errorf("round trip: %s", b)
	}
	d.Forecast = nil
	b, _ = json.Marshal(d)
	if got, ok := parse(b); !ok || got.Forecast != nil {
		t.Errorf("no forecast: %s", b)
	}
}

func TestShared(t *testing.T) {
	cases.Check(t, "money", func(a []any) any { return money(cases.F(a[0])) })
	cases.Check(t, "short_money", func(a []any) any { return shortMoney(cases.F(a[0])) })
	cases.Check(t, "delta", func(a []any) any { return cases.Opt(delta(cases.F(a[0]), cases.F(a[1]))) })
	cases.Check(t, "service_name", func(a []any) any { return serviceName(cases.S(a[0])) })
	cases.Check(t, "days_in_month", func(a []any) any {
		d, _ := time.Parse(time.DateOnly, cases.S(a[0]))
		return daysInMonth(d)
	})
	cases.Check(t, "billing_cache", func(a []any) any {
		d, ok := parse(cases.JSON(a[0]))
		if !ok {
			return nil
		}
		return map[string]any{"mtd": d.mtd(), "last_month": cases.Opt(d.lastMonth()), "projected": cases.Opt(d.projected())}
	})
}
