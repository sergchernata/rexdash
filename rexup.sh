#!/bin/zsh
# Open a Rex tab with the standard layout:
#   top:    Claude (55%) | PRs / Git / Pipeline
#   bottom: Logs | Infra (45%)
# or, for an infrastructure (Terraform) repo, with --terraform:
#   top:    Claude (55%) | PRs / Git / Terraform
#   bottom: Billing | Fleet (45%)
#
# usage: rexup [-s SESSION] NAME DIR AWS_PROFILE PIPELINE LOG_GROUP[:STREAM_PREFIX] CLUSTER [SERVICE] [DB]
#        rexup [-s SESSION] --terraform NAME DIR AWS_PROFILE
#
# SESSION is a session label (e.g. one per company); it's created if missing. Without -s the
# tab opens in the current session. Each pane is a shell that starts its program, so quitting a
# pane leaves a prompt and `rexdash session` can relaunch it after a Rex restart.
set -euo pipefail

session="" kind=app
if [[ ${1:-} == -s ]]; then session=$2; shift 2; fi
if [[ ${1:-} == --terraform ]]; then kind=terraform; shift; fi
if [[ $kind == app && $# -lt 6 || $kind == terraform && $# -ne 3 ]]; then
  sed -n '9,10p' "$0" | sed 's/^# //'; exit 2
fi
name=$1 dir=${2:A} profile=$3; shift 3

python3 - "$kind" "$session" "$name" "$dir" "$profile" "$@" <<'PY'
import json, os, shlex, subprocess, sys

kind, session, name, cwd, profile, *rest = sys.argv[1:]

def rex(*args):
    out = subprocess.run(["rex", *args], capture_output=True, text=True)
    if out.returncode:
        sys.exit(f"rex {' '.join(args)}: {out.stderr.strip()}")
    return json.loads(out.stdout or "{}")

def pane(label, command):
    return {"block": {"flavor": "com.superlogical.terminal.shell", "label": label,
                      "options": {"cwd": cwd, "initial_input": command + "\n"}}}

def dash(label, *args):
    return pane(label, f"AWS_PROFILE={shlex.quote(profile)} rexdash " + " ".join(shlex.quote(a) for a in args))

def split(direction, ratio, before, after):
    return {"split": {"direction": direction, "ratio": ratio, "before": before, "after": after}}

def tab(third, bottom_left, bottom_right):
    return split("vertical", 0.5,
        split("horizontal", 0.55,
            pane("claude", "claude"),
            split("vertical", 1 / 3, dash("PRs", "prs"), split("vertical", 0.5, dash("Git", "git"), third))),
        split("horizontal", 0.55, bottom_left, bottom_right))

if kind == "terraform":
    layout = tab(dash("Terraform", "terraform"), dash("Billing", "billing"), dash("Fleet", "fleet"))
else:
    pipeline, logs, *infra = rest
    group, _, stream = logs.partition(":")
    logs_args = [group] + (["--stream", stream] if stream else [])
    layout = tab(dash("Pipeline", "pipeline", pipeline), dash("Logs", "logs", *logs_args), dash("Infra", "infra", *infra))

# The target session: a label (created if missing) or the current one.
starter = None
if session:
    ids = {s["label"]: s["session_id"] for s in rex("session", "ls", "--json")["sessions"]}
    if session in ids:
        sid = ids[session]
    else:
        sid = rex("new", session, "--json")["session_id"]
        starter = rex("window", "ls", "-s", sid, "--json")["windows"][0]["window_id"]
else:
    sid = os.environ.get("REX_SESSION") or sys.exit("run inside Rex, or pass -s SESSION")

rex("api", "call", "session.new_window", json.dumps(
    {"session_id": sid, "window_label": name, "focus": False, "layout": layout}))
if starter:  # the empty first window of a new session
    rex("api", "call", "session.close_window", json.dumps({"session_id": sid, "window_id": starter}))
print(f"{name}: ready in {session or 'this session'}")
PY
