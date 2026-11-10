package eas

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rexdash/internal/cases"
	"rexdash/internal/ui"
)

func TestProjectID(t *testing.T) {
	for in, want := range map[string]string{
		`{"expo":{"extra":{"eas":{"projectId":"c259"}}}}`: "c259",
		`{"extra":{"eas":{"projectId":"x"}}}`:             "x",
		`{"expo":{}}`:                                     "",
	} {
		if got, _ := projectID(gjson.Parse(in)); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestVersionsAndLanes(t *testing.T) {
	if v := version(gjson.Parse(`{"appVersion":"1.2.9","appBuildVersion":"94"}`)); v != "1.2.9 (94)" {
		t.Error(v)
	}
	if v := version(gjson.Parse(`{}`)); v != "" {
		t.Error(v)
	}
	if l := laneName("Staging iOS Build & Submit (TestFlight)"); l != "Staging iOS" {
		t.Error(l)
	}
	if l := laneName("Nightly"); l != "Nightly" {
		t.Error(l)
	}
}

func TestEasStatuses(t *testing.T) {
	for s, want := range map[string]ui.Tone{"FINISHED": ui.ToneOk, "SUCCESS": ui.ToneOk, "ERRORED": ui.ToneBad, "FAILURE": ui.ToneBad,
		"CANCELED": ui.ToneBad, "IN_PROGRESS": ui.ToneBusy, "IN_QUEUE": ui.ToneBusy, "SKIPPED": ui.ToneIdle} {
		if got := easTone(s); got != want {
			t.Errorf("%s: %v want %v", s, got, want)
		}
	}
}

func arg(v any) gjson.Result { return gjson.ParseBytes(cases.JSON(v)) }

func TestShared(t *testing.T) {
	cases.Check(t, "eas_tone", func(a []any) any { return easTone(cases.S(a[0])).String() })
	cases.Check(t, "eas_version", func(a []any) any { return version(arg(a[0])) })
	cases.Check(t, "lane_name", func(a []any) any { return laneName(cases.S(a[0])) })
	cases.Check(t, "project_id", func(a []any) any { return cases.Opt(projectID(arg(a[0]))) })
	cases.Check(t, "eas_parse", func(a []any) any {
		url, runs, builds := parse(arg(a[0]))
		rs, bs := []any{}, []any{}
		for _, r := range runs {
			jobs := []any{}
			for _, j := range r.Jobs {
				jobs = append(jobs, map[string]any{"name": j.Name, "kind": j.Kind, "status": j.Status, "secs": j.Secs, "version": j.Version})
			}
			rs = append(rs, map[string]any{"id": r.ID, "name": r.Name, "status": r.Status, "ref": r.Ref, "msg": r.Msg,
				"created": r.Created, "secs": r.Secs, "jobs": jobs})
		}
		for _, b := range builds {
			bs = append(bs, map[string]any{"id": b.ID, "platform": b.Platform, "status": b.Status, "profile": b.Profile,
				"version": b.Version, "created": b.Created, "secs": b.Secs, "ref": b.Ref, "msg": b.Msg, "who": b.Who,
				"error": b.Error, "dev": b.Dev})
		}
		return map[string]any{"url": url, "runs": rs, "builds": bs}
	})
}

const sample = `{"slug":"app","ownerAccount":{"name":"me"},
 "workflowRunsPaginated":{"edges":[{"node":{"id":"r1","name":"Staging iOS Build & Submit","status":"FAILURE","requestedGitRef":"refs/heads/staging",
  "gitCommitMessage":"Ship it\n\nbody","createdAt":"2026-11-07T12:50:25Z","durationSeconds":694,
  "jobs":[{"name":"build_ios","type":"BUILD","status":"SUCCESS","createdAt":"2026-11-07T12:50:26Z","updatedAt":"2026-11-07T12:59:26Z",
   "turtleBuild":{"appVersion":"1.2.9","appBuildVersion":"94"}},{"name":"submit_ios","type":"SUBMISSION","status":"FAILURE"}]}}]},
 "builds":[{"id":"b1","platform":"IOS","status":"FINISHED","buildProfile":"staging","appVersion":"1.2.9","appBuildVersion":"94",
  "createdAt":"2026-11-07T12:50:26Z","completedAt":"2026-11-07T12:52:26Z","gitRef":"refs/heads/staging","gitCommitMessage":"Ship it"}]}`

func TestDraw(t *testing.T) {
	t.Setenv("REX_SESSION", "") // openSelected zooms the Rex pane otherwise
	p := &pane{loaded: true, st: ui.TableState{Sel: 0}}
	p.url, p.runs, p.builds = parse(gjson.Parse(sample))
	b := ui.NewBuffer(110, 20)
	p.Draw(b, b.Area())
	for _, want := range []string{"Staging iOS", "1.2.9 (94)", "build", "submission", "Workflow runs", "Ship it"} {
		if !strings.Contains(b.Text(), want) {
			t.Errorf("runs: missing %q in\n%s", want, b.Text())
		}
	}
	if s := p.Status(); s.State != "error" || !strings.HasPrefix(s.Msg, "Staging iOS · failure · ") {
		t.Errorf("status %+v", s)
	}
	p.openSelected()
	b = ui.NewBuffer(110, 20)
	p.Draw(b, b.Area())
	if !strings.Contains(b.Text(), "Staging iOS · staging") || !strings.Contains(b.Text(), "submission") {
		t.Errorf("details:\n%s", b.Text())
	}
	p.view, p.mode = nil, Builds
	b = ui.NewBuffer(110, 20)
	p.Draw(b, b.Area())
	for _, want := range []string{"Builds", "avg 2m00s", "staging", "Ship it"} {
		if !strings.Contains(b.Text(), want) {
			t.Errorf("builds: missing %q in\n%s", want, b.Text())
		}
	}
}
