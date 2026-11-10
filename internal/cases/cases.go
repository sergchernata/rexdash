// Package cases runs the shared test cases in testdata/cases/*.json ([{name, args, want}]),
// the same files the Rust version was tested against.
package cases

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type tcase struct {
	Name string `json:"name"`
	Args []any  `json:"args"`
	Want any    `json:"want"`
}

func dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "cases")
}

// normalize turns any Go value into the shape encoding/json would decode it to.
func normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<unmarshalable %T>", v)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func same(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && math.Abs(x-y) < 1e-6
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !same(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !same(v, w) {
				return false
			}
		}
		return true
	}
	return a == b
}

// Check runs f on every case in testdata/cases/{file}.json and reports every mismatch.
func Check(t *testing.T, file string, f func(a []any) any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir(), file+".json"))
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var cs []tcase
	if err := json.Unmarshal(data, &cs); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if len(cs) == 0 {
		t.Fatalf("%s: no cases", file)
	}
	var bad []string
	for _, c := range cs {
		got := normalize(f(c.Args))
		if !same(got, c.Want) {
			w, _ := json.Marshal(c.Want)
			g, _ := json.Marshal(got)
			bad = append(bad, fmt.Sprintf("  %s: want %s got %s", c.Name, w, g))
		}
	}
	if len(bad) > 0 {
		t.Errorf("%s: %d of %d cases failed\n%s", file, len(bad), len(cs), strings.Join(bad, "\n"))
	}
}

// S, F, I and B read case arguments.
func S(v any) string {
	s, _ := v.(string)
	return s
}

func F(v any) float64 {
	f, _ := v.(float64)
	return f
}

func I(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func B(v any) bool {
	b, _ := v.(bool)
	return b
}

// Floats reads a list of numbers.
func Floats(v any) []float64 {
	xs, _ := v.([]any)
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = F(x)
	}
	return out
}

// Strings reads a list of strings.
func Strings(v any) []string {
	xs, _ := v.([]any)
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = S(x)
	}
	return out
}

// JSON re-encodes an argument, for code that parses raw API responses.
func JSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Opt is nil when ok is false, else v: for "null when missing" results.
func Opt[T any](v T, ok bool) any {
	if !ok {
		return nil
	}
	return v
}
