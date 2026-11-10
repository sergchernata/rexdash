package infra

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"

	"rexdash/internal/ui"
)

// Parameter groups as RDS pages them: max_connections sits on the second page.
func (f *fakeRDS) DescribeDBParameters(_ context.Context, in *rds.DescribeDBParametersInput, _ ...func(*rds.Options)) (*rds.DescribeDBParametersOutput, error) {
	group := aws.ToString(in.DBParameterGroupName)
	if f.paramCalls == nil {
		f.paramCalls = map[string]int{}
	}
	f.paramCalls[group]++
	if f.paramErr != nil {
		return nil, f.paramErr
	}
	if in.Marker == nil {
		return &rds.DescribeDBParametersOutput{Marker: aws.String("2"), Parameters: []rdstypes.Parameter{
			{ParameterName: aws.String("work_mem"), ParameterValue: aws.String("4096")},
		}}, nil
	}
	return &rds.DescribeDBParametersOutput{Parameters: []rdstypes.Parameter{
		{ParameterName: aws.String("max_connections"), ParameterValue: aws.String(f.params[group])},
	}}, nil
}

func withGroup(r *fakeRDS, value string) {
	r.db.DBParameterGroups = []rdstypes.DBParameterGroupStatus{{DBParameterGroupName: aws.String("app-pg")}}
	r.params = map[string]string{"app-pg": value}
}

// The Conns tile shows the count out of the database's limit, worked out once per parameter group.
func TestConnsTileShowsLimit(t *testing.T) {
	e, r, c := healthy()
	withGroup(r, "LEAST({DBInstanceClassMemory/9531392},5000)")
	p := newPane(e, r, c)
	for range 2 {
		if err := p.Refresh(); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.paramCalls["app-pg"]; n != 2 { // two pages, read on the first refresh only
		t.Errorf("parameter group read %d times, want 2 pages once", n)
	}
	find(t, draw(p), "14 of ~1802") // db.r6g.large: 16 GiB
}

// A fixed limit is exact, and a busy pool takes the load colour.
func TestConnsTileColoursByUse(t *testing.T) {
	e, r, c := healthy()
	withGroup(r, "20")
	p := newPane(e, r, c)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	b := draw(p)
	x, y := find(t, b, "14 of 20")
	if st := b.StyleAt(x, y); st.Fg != ui.Yellow() {
		t.Errorf("70%% of the pool drawn %v, want yellow", st.Fg)
	}
}

// Without permission to read parameters the count still shows, just without a limit.
func TestConnsTileWithoutLimit(t *testing.T) {
	e, r, c := healthy()
	withGroup(r, "100")
	r.paramErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "nope"}
	p := newPane(e, r, c)
	for range 2 {
		if err := p.Refresh(); err != nil {
			t.Fatalf("a refused parameter read failed the pane: %v", err)
		}
	}
	if n := r.paramCalls["app-pg"]; n != 1 {
		t.Errorf("refused read retried %d times", n)
	}
	text := draw(p).Text()
	if !strings.Contains(text, "Conns") || strings.Contains(text, "14 of") {
		t.Errorf("want a bare count:\n%s", text)
	}
}
