# rexdash

Small, always-running display panes for [Rex](https://www.superlogical.com/rex): pull requests,
git, CI, Expo builds, AWS CodePipeline, CloudWatch logs, ECS/RDS health, the whole fleet,
billing and Terraform state. Each pane is a terminal program; Rex shows its title and status.

```
make install          # builds bin/rexdash and bin/rexdash-aws, installs to ~/.local/bin
make test             # unit, shared-case and end-to-end tests
```

| Command | Shows |
|---|---|
| `rexdash git` | changed files, commits, diffs (refreshes on file changes) |
| `rexdash prs [-r OWNER/REPO]` | open/merged PRs with CI and review state |
| `rexdash ci [-r OWNER/REPO]` | GitHub Actions runs |
| `rexdash eas` / `rexdash builds` | Expo release lanes, workflow runs, EAS builds (run in the app dir) |
| `rexdash pipeline NAME` | CodePipeline stages, runs, actions and build logs |
| `rexdash logs GROUP [--stream PREFIX] [--since 15m] [--hide TEXT]` | CloudWatch live tail |
| `rexdash infra CLUSTER [SERVICE] [DB]` | one ECS service and its RDS database |
| `rexdash fleet` | every ECS service and RDS instance |
| `rexdash billing` | spend by month, this month's projection, services (Cost Explorer, cached 6h) |
| `rexdash terraform [BUCKET [KEY]]` | S3 state write history, changes per write, lock |
| `rexdash session install` | launchd agent that relaunches panes after a Rex restart |

All panes take `-i SECS`. AWS panes use the normal credential chain (`AWS_PROFILE`); GitHub
uses `GH_TOKEN`, else `gh auth token`. `rexup.sh` opens a Rex tab with the standard layout.

**Layout.** `cmd/rexdash` and `cmd/rexdash-aws` are thin mains; each pane is a package under
`internal/`; `internal/ui` is the shared drawing layer (cell buffer, layout, panels, tables,
charts), theme (follows Rex's theme live), icons and the run loop. `testdata/cases/*.json` are
shared input → output cases the tests run against.

**Upgrades.** A pane restarts itself in place when its binary is replaced, so `make install`
updates every open pane. Set `REXDASH_TRACE=/path/log` to log startup and restarts.
