#!/usr/bin/env bash
# task-state.sh - report what a task already holds, before opening a round in it.
#
# Everything revmux itself knows comes from `revmux config`: the tasks root, what task.md says, and
# which rounds have run. This script re-derives none of it — a second parser of task.md or a second
# reading of the round marker would disagree with the tool the caller is about to run. What it adds is
# the one thing `revmux config` does not report: the per-round input/ state, which is caller-written.
#
# Two things decide the next call:
#
#   1. whether this task exists and what its task.md says it covers.
#   2. which rounds are there, whether each has already run, and whether its input/ is filled.
#      A round that has run holds a filled-in manifest.json and revmux refuses to reuse it. One holding
#      an empty manifest.json was claimed by a review that never finished, and is still open — though
#      only while that review wrote nothing else into it, which revmux checks and reports itself.
#
# Everything under a round's input/ is caller-written, so an existing scope.md is a decision
# someone made and must not be clobbered blind.
#
# usage: task-state.sh <task-id>
#
# output: `key: value` lines
#   tasks_dir    resolved root
#   task_dir     this task's directory
#   exists       true|false
#   description  task.md front matter, printed only when set
#   url          task.md front matter, printed only when set
#   branch       task.md front matter, printed only when set
#   base         task.md front matter, printed only when set
#   meta_error   why the anchors are empty, printed only when task.md would not parse
#   rounds_error why the round list is empty, printed only when the rounds could not be read
#   rounds       round names in order, or `none`
#   round        one per round: <name> ran|claimed|prepared scope=present|MISSING goal=present|absent
#                profile=present|absent context=<number of files>
# exit: 0 when revmux resolves; 1 on a bad task id, a missing revmux, or a failing `revmux config`

set -u

task="${1:-}"
if [ -z "$task" ]; then
    echo "usage: task-state.sh <task-id>" >&2
    exit 1
fi

# this script joins the id into a path and globs it, so it guards its own walk; revmux applies the same
# rule to what it is passed. A branch name is the common trigger: `feature/foo` contains a separator.
case "$task" in
    /*)     echo "error: task id \"$task\" is absolute" >&2; exit 1 ;;
    .*)     echo "error: task id \"$task\" starts with a dot" >&2; exit 1 ;;
    */*|*\\*) echo "error: task id \"$task\" contains a path separator; replace / with -" >&2; exit 1 ;;
    *..*)   echo "error: task id \"$task\" references a parent directory" >&2; exit 1 ;;
esac

command -v revmux >/dev/null 2>&1 || { echo "error: revmux not on PATH" >&2; exit 1; }

cfg=$(revmux config 2>/dev/null) || { echo "error: revmux config failed" >&2; exit 1; }

has_jq=false
if command -v jq >/dev/null 2>&1; then
    has_jq=true
    tasks_dir=$(printf '%s' "$cfg" | jq -r '.paths.tasks_dir')
else
    # crude but dependency-free: the value of the tasks_dir key, quotes stripped. Only this one scalar
    # is reachable without jq — the anchors and the round list are nested, and are reported as missing.
    tasks_dir=$(printf '%s' "$cfg" | tr ',' '\n' | grep '"tasks_dir"' | head -1 | sed 's/.*"tasks_dir"[[:space:]]*:[[:space:]]*"//; s/".*//')
fi

[ -n "$tasks_dir" ] || { echo "error: could not resolve tasks_dir from revmux config" >&2; exit 1; }

task_dir="$tasks_dir/$task"
echo "tasks_dir: $tasks_dir"
echo "task_dir: $task_dir"

if [ ! -d "$task_dir" ]; then
    echo "exists: false"
    echo "rounds: none"
    exit 0
fi

echo "exists: true"

# this task's entry in `revmux config`: the task.md anchors revmux parsed, the meta_error saying why
# they are empty when it would not parse, and the rounds it counts as having run
ran_rounds=""
if [ "$has_jq" = true ]; then
    entry=$(printf '%s' "$cfg" | jq -c --arg t "$task" 'first(.paths.tasks[] | select(.id == $t)) // empty')
    if [ -n "$entry" ]; then
        for key in description url branch base meta_error rounds_error; do
            # newlines folded out: a parse error spans several lines and the output is one key per line
            value=$(printf '%s' "$entry" | jq -r --arg k "$key" '.[$k] // empty' | tr '\n' ' ' | sed 's/[[:space:]]*$//')
            [ -n "$value" ] && echo "$key: $value"
        done
        ran_rounds=$(printf '%s' "$entry" | jq -r '.rounds[]?')
    fi
else
    echo "jq: MISSING - task.md anchors and which rounds ran come from revmux config, which needs jq"
fi

# round directories sort lexically into review order, which is what the NN-label naming rule buys
names=""
detail=""
for dir in "$task_dir"/*/; do
    [ -d "$dir" ] || continue
    name=${dir%/}
    name=${name##*/}
    names="$names $name"

    # `ran` is revmux's own verdict on the marker and is never re-derived here. A round it does not
    # list is still open: claimed by a review that never came back if a marker is there, prepared if not
    state=prepared
    { [ -e "$task_dir/$name/manifest.json" ] || [ -L "$task_dir/$name/manifest.json" ]; } && state=claimed
    printf '%s\n' "$ran_rounds" | grep -qxF "$name" && state=ran

    input="$task_dir/$name/input"
    # an empty scope.md fails the run exactly like a missing one, so size is what matters
    scope=MISSING
    [ -s "$input/scope.md" ] && scope=present
    goal=absent
    [ -s "$input/goal.md" ] && goal=present
    profile=absent
    [ -s "$input/profile.md" ] && profile=present
    context=0
    if [ -d "$input/context" ]; then
        context=$(find "$input/context" -type f 2>/dev/null | wc -l | tr -d ' ')
    fi

    detail="${detail}round: $name $state scope=$scope goal=$goal profile=$profile context=$context
"
done

if [ -n "$names" ]; then
    echo "rounds:$names"
else
    echo "rounds: none"
fi
printf '%s' "$detail"
