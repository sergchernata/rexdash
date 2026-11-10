#!/bin/zsh
# Open a Rex tab with the standard layout:
#   top:    Claude (55%) | PRs / Git / Pipeline
#   bottom: Logs | Infra (45%)
#
# usage: rexup NAME DIR AWS_PROFILE PIPELINE LOG_GROUP[:STREAM_PREFIX] CLUSTER [SERVICE] [DB]
set -euo pipefail

name=$1 dir=$2 profile=$3 pipeline=$4 logs=$5; shift 5
infra=("$@")
group=${logs%%:*} stream=""
[[ $logs == *:* ]] && stream=${logs#*:}
bin=$HOME/.local/bin/rexdash  # restarts itself when a new build is installed
s=${REX_SESSION:?run inside Rex}
id() { python3 -c 'import sys,json; print(json.load(sys.stdin)["block_ids"][0])'; }
pane() { # anchor direction ratio label cmd...
  local anchor=$1 dir_=$2 ratio=$3 label=$4; shift 4
  rex split -s "$s" -b "$anchor" --split="$dir_" --ratio "$ratio" --label "$label" --focus=false \
    --keep-open --shell=none -c "$dir" --json -- env AWS_PROFILE="$profile" "$@" | id
}

claude=$(rex window new -s "$s" "$name" --focus=false -c "$dir" --json \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["block_ids"][0])')

logs_args=("$group"); [[ -n $stream ]] && logs_args+=(--stream "$stream")
logsb=$(pane "$claude" below 50 Logs "$bin" logs "${logs_args[@]}")
pane "$logsb" right 45 Infra "$bin" infra "${infra[@]}" >/dev/null
prs=$(pane "$claude" right 45 PRs "$bin" prs)
git=$(pane "$prs" below 67 Git "$bin" git)
pane "$git" below 50 Pipeline "$bin" pipeline "$pipeline" >/dev/null

sleep 1
rex send -s "$s" -b "$claude" $'claude\n'
echo "$name: ready"
