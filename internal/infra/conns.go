package infra

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// ConnLimit is a database's max_connections. Approx is set when it comes from AWS's memory
// formula: RDS reserves some memory for itself, so the real figure is a little lower.
type ConnLimit struct {
	N      float64
	Approx bool
}

func (l ConnLimit) String() string {
	s := strconv.FormatFloat(l.N, 'f', 0, 64)
	if l.Approx {
		return "~" + s
	}
	return s
}

// MaxConns works out max_connections from its parameter value for an instance class: a plain
// number, or a formula such as LEAST({DBInstanceClassMemory/9531392},5000).
func MaxConns(value, class string) (ConnLimit, bool) {
	value = strings.ReplaceAll(value, " ", "")
	if n, err := strconv.ParseFloat(value, 64); err == nil {
		return ConnLimit{N: n}, true
	}
	gib, ok := ClassGiB(class)
	if !ok {
		return ConnLimit{}, false
	}
	n, ok := evalParam(value, gib*(1<<30))
	if !ok {
		return ConnLimit{}, false
	}
	return ConnLimit{N: math.Floor(n), Approx: true}, true
}

// evalParam evaluates the parameter formula forms RDS uses for max_connections.
func evalParam(s string, memory float64) (float64, bool) {
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n, true
	}
	if div, ok := strings.CutPrefix(s, "{DBInstanceClassMemory/"); ok {
		d, err := strconv.ParseFloat(strings.TrimSuffix(div, "}"), 64)
		if err != nil || d <= 0 || !strings.HasSuffix(div, "}") {
			return 0, false
		}
		return memory / d, true
	}
	for name, pick := range map[string]func(a, b float64) float64{"LEAST(": math.Min, "GREATEST(": math.Max} {
		args, ok := strings.CutPrefix(s, name)
		if !ok || !strings.HasSuffix(args, ")") {
			continue
		}
		var out float64
		for i, a := range splitArgs(strings.TrimSuffix(args, ")")) {
			v, ok := evalParam(a, memory)
			if !ok {
				return 0, false
			}
			if i == 0 {
				out = v
			} else {
				out = pick(out, v)
			}
		}
		return out, true
	}
	return 0, false
}

// splitArgs splits a,b at top-level commas.
func splitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(', '{':
			depth++
		case ')', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// ParamsAPI reads DB parameter groups (the SDK paginator takes this method).
type ParamsAPI interface {
	DescribeDBParameters(context.Context, *rds.DescribeDBParametersInput, ...func(*rds.Options)) (*rds.DescribeDBParametersOutput, error)
}

// ConnParams caches max_connections per parameter group. A group is read once; one that can't
// be read (no permission, say) is remembered as unknown rather than retried every refresh.
type ConnParams map[string]string

// Limit is the connection limit for a database in group with the given class.
func (c ConnParams) Limit(ctx context.Context, api ParamsAPI, group, class string) (ConnLimit, bool) {
	if group == "" {
		return ConnLimit{}, false
	}
	v, seen := c[group]
	if !seen {
		v = readMaxConns(ctx, api, group)
		c[group] = v
	}
	return MaxConns(v, class)
}

func readMaxConns(ctx context.Context, api ParamsAPI, group string) string {
	pg := rds.NewDescribeDBParametersPaginator(api, &rds.DescribeDBParametersInput{DBParameterGroupName: &group})
	for pg.HasMorePages() {
		r, err := pg.NextPage(ctx)
		if err != nil {
			return ""
		}
		for _, p := range r.Parameters {
			if aws.ToString(p.ParameterName) == "max_connections" {
				return aws.ToString(p.ParameterValue)
			}
		}
	}
	return ""
}
