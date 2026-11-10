// rexdash-aws: the rexdash panes that need the AWS SDK. Usually started through rexdash,
// which hands these commands off so only AWS panes pay for loading the SDK.
//
//	rexdash pipeline NAME [-i SECS]                    CodePipeline stages, runs, build logs
//	rexdash infra    CLUSTER [SERVICE] [DB] [-i SECS]  ECS + RDS health, tasks, events
//	rexdash fleet    [-i SECS]                         every ECS service and RDS instance
//	rexdash billing  [-i SECS]                         AWS spend by month, projection, services
//	rexdash terraform [BUCKET [KEY]] [-i SECS]         S3 state apply history and lock
//	rexdash logs     GROUP [--stream PREFIX] [--since 15m] [--hide TEXT]...  CloudWatch live tail
//
// AWS uses the normal credential chain (AWS_PROFILE, AWS_REGION, ~/.aws).
package main

import (
	"errors"
	"os"
	"time"

	"rexdash/internal/app"
	"rexdash/internal/billing"
	"rexdash/internal/cli"
	"rexdash/internal/fleet"
	"rexdash/internal/infra"
	"rexdash/internal/logs"
	"rexdash/internal/pipeline"
	"rexdash/internal/tfstate"
	"rexdash/internal/ui"
)

const usage = "usage: rexdash-aws <pipeline NAME|infra CLUSTER [SERVICE] [DB]|fleet|billing|terraform [BUCKET [KEY]]|logs GROUP> [-i SECS] [--stream PREFIX] [--since 15m] [--hide TEXT]"

func pane(a cli.Args) (ui.Pane, error) {
	every := func(d time.Duration) time.Duration { return app.Every(a, d) }
	pos := func(i int, def string) string { return app.Pos(a, i, def) }
	switch a.Cmd {
	case "billing":
		return billing.New(every(6 * time.Hour))
	case "fleet":
		return fleet.New(every(5 * time.Minute))
	case "terraform":
		return tfstate.New(pos(0, ""), pos(1, ""), every(5*time.Minute))
	case "pipeline":
		if len(a.Pos) > 0 {
			return pipeline.New(a.Pos[0], every(30*time.Second))
		}
	case "infra":
		if len(a.Pos) > 0 {
			service := pos(1, a.Pos[0])
			return infra.New(a.Pos[0], service, pos(2, service), every(60*time.Second))
		}
	case "logs":
		if len(a.Pos) > 0 {
			return logs.New(a.Pos[0], a.Stream, a.Since, a.Hide)
		}
	}
	return nil, errors.New(usage)
}

func main() {
	app.TuneMemory()
	app.Show(pane(cli.Parse(os.Args[1:])))
}
