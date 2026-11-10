package tfstate

import (
	"encoding/json"
	"slices"
	"testing"

	"rexdash/internal/cases"
)

func addrs(s snap) []string {
	out := []string{}
	for a := range s.managed {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

func mustSnap(t *testing.T, v any) snap {
	t.Helper()
	b, _ := json.Marshal(v)
	s, err := snapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBackendBlock(t *testing.T) {
	tf := `
terraform {
  required_version = "~> 1.17"
  backend "s3" {
    bucket  = "example-terraform-state"
    key     = "terraform.tfstate"
    region  = "us-east-1"
  }
}`
	if b, k, ok := findBackend(tf); !ok || b != "example-terraform-state" || k != "terraform.tfstate" {
		t.Errorf("got %q %q %v", b, k, ok)
	}
	if _, _, ok := findBackend(`backend "local" {}`); ok {
		t.Error("local backend matched")
	}
}

func testState(serial int, acl string, extra bool) map[string]any {
	res := []any{
		map[string]any{"mode": "managed", "type": "aws_s3_bucket", "name": "b", "instances": []any{map[string]any{"attributes": map[string]any{"acl": acl}}}},
		map[string]any{"mode": "managed", "type": "aws_ecs_service", "name": "svc", "module": "module.app", "instances": []any{
			map[string]any{"index_key": 0, "attributes": map[string]any{"n": 1}},
			map[string]any{"index_key": "web", "attributes": map[string]any{"n": 2}},
		}},
		map[string]any{"mode": "data", "type": "aws_caller_identity", "name": "me", "instances": []any{map[string]any{"attributes": map[string]any{"id": "1"}}}},
	}
	if extra {
		res = append(res, map[string]any{"mode": "managed", "type": "aws_sqs_queue", "name": "q", "instances": []any{map[string]any{"attributes": map[string]any{}}}})
	}
	return map[string]any{"serial": serial, "terraform_version": "1.17.2", "resources": res}
}

func TestSnapshotAddresses(t *testing.T) {
	s := mustSnap(t, testState(7, "private", false))
	if s.serial != 7 || s.tf != "1.17.2" || s.data != 1 {
		t.Errorf("got %d %q %d", s.serial, s.tf, s.data)
	}
	want := []string{"aws_s3_bucket.b", `module.app.aws_ecs_service.svc["web"]`, "module.app.aws_ecs_service.svc[0]"}
	if got := addrs(s); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestDiffsBetweenWrites(t *testing.T) {
	a := mustSnap(t, testState(1, "private", true))
	b := mustSnap(t, testState(2, "public-read", false))
	d := diffSnaps(a, b)
	if len(d.added) != 0 || !slices.Equal(d.changed, []string{"aws_s3_bucket.b"}) || !slices.Equal(d.removed, []string{"aws_sqs_queue.q"}) {
		t.Errorf("got %+v", d)
	}
	if back := diffSnaps(b, a); !slices.Equal(back.added, []string{"aws_sqs_queue.q"}) {
		t.Errorf("back %+v", back)
	}
	if !diffSnaps(a, a).empty() {
		t.Error("self diff not empty")
	}
}

func TestRefreshOnlyAttributesAreNotChanges(t *testing.T) {
	a := attrHash([]byte(`{"id": "db", "latest_restorable_time": "2026-11-09T10:00:00Z"}`))
	b := attrHash([]byte(`{"latest_restorable_time": "2026-11-09T15:00:00Z", "id":"db"}`))
	if a != b {
		t.Error("volatile attribute or key order changed the hash")
	}
	if a == attrHash([]byte(`{"id": "db2", "latest_restorable_time": "x"}`)) {
		t.Error("real change kept the hash")
	}
}

func TestShortAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"module.app.aws_ecs_service.svc[0]": "aws_ecs_service.svc",
		"aws_s3_bucket.b":                   "aws_s3_bucket.b",
		"weird":                             "weird",
	} {
		if got := shortAddr(in); got != want {
			t.Errorf("shortAddr(%q) = %q", in, got)
		}
	}
}

func TestShared(t *testing.T) {
	cases.Check(t, "find_backend", func(a []any) any {
		b, k, ok := findBackend(cases.S(a[0]))
		return cases.Opt([]string{b, k}, ok)
	})
	cases.Check(t, "tf_snapshot", func(a []any) any {
		s, err := snapshot(cases.JSON(a[0]))
		if err != nil {
			return err.Error()
		}
		return map[string]any{"serial": s.serial, "tf": s.tf, "data": s.data, "addresses": addrs(s)}
	})
	cases.Check(t, "tf_diff", func(a []any) any {
		old, _ := snapshot(cases.JSON(a[0]))
		new, _ := snapshot(cases.JSON(a[1]))
		d := diffSnaps(old, new)
		return map[string]any{"added": d.added, "changed": d.changed, "removed": d.removed}
	})
	cases.Check(t, "short_addr", func(a []any) any { return shortAddr(cases.S(a[0])) })
}
