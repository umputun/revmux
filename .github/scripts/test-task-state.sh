#!/usr/bin/env bash
# tests for the skill's scripts/task-state.sh, run against a fake revmux first on PATH.
# regression: an explicit tasks root never reached `revmux config`, so a task there read as exists: false.
set -u

script="${1:-.claude-plugin/skills/revmux/scripts/task-state.sh}"
case "$script" in
    /*) ;;
    *) script="$PWD/$script" ;;
esac
[ -r "$script" ] || { echo "$script: cannot read" >&2; exit 1; }
jq_bin=$(command -v jq) || { echo "jq is required to run these tests" >&2; exit 1; }

work=$(mktemp -d "${TMPDIR:-/tmp}/test-task-state.XXXXXX") || exit 1
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/bin"
ln -s "$jq_bin" "$work/bin/jq"
cat > "$work/bin/revmux" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$FAKE_ARGV"
[ -z "${FAKE_FAIL:-}" ] || exit 1
root="$FAKE_DEFAULT_ROOT"
explicit=""
confdir=""
while [ $# -gt 0 ]; do
    case "$1" in
        --tasks-dir) explicit="$2" ;;
        --config-dir) confdir="$2" ;;
    esac
    shift
done
if [ -z "$confdir" ]; then
    [ -z "${FAKE_FAIL_DEFAULT_CONFIG:-}" ] || exit 1
elif [ -s "$confdir/tasks-root" ]; then
    root=$(cat "$confdir/tasks-root")
fi
[ -z "$explicit" ] || root="$explicit"
tasks='[]'
for dir in "$root"/*/; do
    [ -d "$dir" ] || continue
    id=${dir%/}
    id=${id##*/}
    ran='[]'
    for round in "$dir"*/; do
        [ -s "${round}manifest.json" ] || continue
        name=${round%/}
        ran=$(jq -c --arg r "${name##*/}" '. + [$r]' <<<"$ran")
    done
    tasks=$(jq -c --arg id "$id" --argjson ran "$ran" '. + [{id: $id, description: "task \($id)", rounds: $ran}]' <<<"$tasks")
done
jq -n --arg root "$root" --argjson tasks "$tasks" '{paths: {tasks_dir: $root, tasks: $tasks}}'
FAKE
chmod +x "$work/bin/revmux"

export PATH="$work/bin:/usr/bin:/bin"
export FAKE_ARGV="$work/argv"
export FAKE_DEFAULT_ROOT="$work/default root"

failures=0
fail() {
    echo "FAIL: $1" >&2
    failures=$((failures + 1))
}

expect_line() {
    printf '%s\n' "$2" | grep -qxF -- "$3" || fail "$1: no line '$3' in output:
$2"
}

expect_argv() {
    local want got
    want=$(printf '%s\n' "${@:2}")
    got=$(cat "$FAKE_ARGV")
    [ "$got" = "$want" ] || fail "$1: revmux argv
want: $want
got:  $got"
}

run() {
    out=$("$script" "$@" 2>"$work/stderr")
    code=$?
    err=$(cat "$work/stderr")
}

explicit="$work/explicit root"
mkdir -p "$FAKE_DEFAULT_ROOT/other" "$explicit/demo/01-initial/input/context" "$explicit/demo/02-after-fix" \
    "$explicit/demo/03-final/input"
echo '{"finished_at": "2026-10-18T00:00:00Z"}' > "$explicit/demo/01-initial/manifest.json"
echo 'scope' > "$explicit/demo/01-initial/input/scope.md"
echo 'goal' > "$explicit/demo/01-initial/input/goal.md"
echo 'a' > "$explicit/demo/01-initial/input/context/a.md"
echo 'b' > "$explicit/demo/01-initial/input/context/b.md"
: > "$explicit/demo/02-after-fix/manifest.json"
echo 'profile' > "$explicit/demo/03-final/input/profile.md"

run demo
[ "$code" -eq 0 ] || fail "default root: exit $code, stderr: $err"
expect_argv "default root" config
expect_line "default root" "$out" "tasks_dir: $FAKE_DEFAULT_ROOT"
expect_line "default root" "$out" "exists: false"
expect_line "default root" "$out" "rounds: none"

run --tasks-dir "$explicit" demo
[ "$code" -eq 0 ] || fail "explicit root: exit $code, stderr: $err"
expect_argv "explicit root" config --tasks-dir "$explicit"
expect_line "explicit root" "$out" "tasks_dir: $explicit"
expect_line "explicit root" "$out" "task_dir: $explicit/demo"
expect_line "explicit root" "$out" "exists: true"
expect_line "explicit root" "$out" "description: task demo"
expect_line "explicit root" "$out" "rounds: 01-initial 02-after-fix 03-final"
expect_line "explicit root" "$out" "round: 01-initial ran scope=present goal=present profile=absent context=2"
expect_line "explicit root" "$out" "round: 02-after-fix claimed scope=MISSING goal=absent profile=absent context=0"
expect_line "explicit root" "$out" "round: 03-final prepared scope=MISSING goal=absent profile=present context=0"

run demo --tasks-dir "$explicit"
[ "$code" -eq 0 ] || fail "option after id: exit $code, stderr: $err"
expect_argv "option after id" config --tasks-dir "$explicit"
expect_line "option after id" "$out" "exists: true"

confdir="$work/config dir"
mkdir -p "$confdir"
printf '%s' "$explicit" > "$confdir/tasks-root"

FAKE_FAIL_DEFAULT_CONFIG=1 run --config-dir "$confdir" demo
[ "$code" -eq 0 ] || fail "config dir with a broken default: exit $code, stderr: $err"
expect_argv "config dir with a broken default" config --config-dir "$confdir"
expect_line "config dir with a broken default" "$out" "tasks_dir: $explicit"
expect_line "config dir with a broken default" "$out" "exists: true"

FAKE_FAIL_DEFAULT_CONFIG=1 run demo
[ "$code" -eq 1 ] || fail "broken default without config dir: exit $code, want 1"
expect_line "broken default without config dir" "$err" "error: revmux config failed"

run --config-dir "$confdir" --tasks-dir "$FAKE_DEFAULT_ROOT" demo
[ "$code" -eq 0 ] || fail "tasks dir over the config dir's root: exit $code, stderr: $err"
expect_argv "tasks dir over the config dir's root" config --config-dir "$confdir" --tasks-dir "$FAKE_DEFAULT_ROOT"
expect_line "tasks dir over the config dir's root" "$out" "tasks_dir: $FAKE_DEFAULT_ROOT"
expect_line "tasks dir over the config dir's root" "$out" "exists: false"

run demo --tasks-dir "$explicit" --config-dir "$confdir"
[ "$code" -eq 0 ] || fail "both flags after the id: exit $code, stderr: $err"
expect_argv "both flags after the id" config --tasks-dir "$explicit" --config-dir "$confdir"
expect_line "both flags after the id" "$out" "exists: true"

run --tasks-dir "$FAKE_DEFAULT_ROOT" other
[ "$code" -eq 0 ] || fail "explicit root with no rounds: exit $code, stderr: $err"
expect_line "explicit root with no rounds" "$out" "exists: true"
expect_line "explicit root with no rounds" "$out" "rounds: none"

usage_cases=(
    "no arguments|"
    "option without a value|--tasks-dir"
    "empty root|--tasks-dir||demo"
    "config dir without a value|demo|--config-dir"
    "empty config dir|--config-dir||demo"
    "unknown option|--task-dir|$explicit|demo"
    "two task ids|demo|other"
    "separator in the id|feature/foo"
    "parent in the id|a..b"
)
for c in "${usage_cases[@]}"; do
    IFS='|' read -r -a parts <<<"$c"
    name=${parts[0]}
    rm -f "$FAKE_ARGV"
    run "${parts[@]:1}"
    [ "$code" -eq 1 ] || fail "$name: exit $code, want 1"
    [ -n "$err" ] || fail "$name: no error on stderr"
    [ -z "$out" ] || fail "$name: stdout not empty: $out"
    [ ! -e "$FAKE_ARGV" ] || fail "$name: revmux ran"
done

FAKE_FAIL=1 run --tasks-dir "$explicit" demo
[ "$code" -eq 1 ] || fail "config failure: exit $code, want 1"
expect_line "config failure" "$err" "error: revmux config failed"
[ -z "$out" ] || fail "config failure: stdout not empty: $out"

if [ "$failures" -gt 0 ]; then
    echo "$failures failure(s)" >&2
    exit 1
fi
echo "task-state.sh: all tests passed"
