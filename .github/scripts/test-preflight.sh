#!/usr/bin/env bash
# tests for the skill's scripts/preflight.sh, run against a fake revmux and fake model CLIs.
# regression: an explicit --config-dir never reached `revmux config`, so a profile defined only there
# failed as unknown and a roster changed there was checked against the default one.
set -u

script="${1:-.claude-plugin/skills/revmux/scripts/preflight.sh}"
case "$script" in
    /*) ;;
    *) script="$PWD/$script" ;;
esac
[ -r "$script" ] || { echo "$script: cannot read" >&2; exit 1; }
jq_bin=$(command -v jq) || { echo "jq is required to run these tests" >&2; exit 1; }

work=$(mktemp -d "${TMPDIR:-/tmp}/test-preflight.XXXXXX") || exit 1
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/bin" "$work/models"
ln -s "$jq_bin" "$work/bin/jq"
cat > "$work/bin/revmux" <<'FAKE'
#!/usr/bin/env bash
if [ "${1:-}" = "--version" ]; then
    echo "revmux fake"
    exit 0
fi
printf '%s\n' "$@" > "$FAKE_ARGV"
[ -z "${FAKE_FAIL:-}" ] || exit 1
dir=""
while [ $# -gt 0 ]; do
    [ "$1" = "--config-dir" ] && dir="$2"
    shift
done
claude='{name: "base", runner: {executor: "claude"}, roster: [{executor: "claude"}], stages: [{executor: "claude"}]}'
local='{name: "local", runner: {executor: "claude"}, roster: [{executor: "codex"}], stages: [{executor: "claude"}]}'
if [ -n "$dir" ]; then
    jq -n "{knobs: [{name: \"profile\", value: \"local\"}], profiles: [$claude, $local]}"
    exit 0
fi
jq -n "{knobs: [{name: \"profile\", value: \"base\"}], profiles: [$claude]}"
FAKE
printf '#!/bin/sh\nexit 0\n' > "$work/models/claude"
printf '#!/bin/sh\nexit 0\n' > "$work/models/codex"
chmod +x "$work/bin/revmux" "$work/models/claude" "$work/models/codex"
mkdir -p "$work/claude-only"
ln -s "$work/models/claude" "$work/claude-only/claude"

export FAKE_ARGV="$work/argv"
base_path="$work/bin:/usr/bin:/bin"

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
    got=$(cat "$FAKE_ARGV" 2>/dev/null)
    [ "$got" = "$want" ] || fail "$1: revmux argv
want: $want
got:  $got"
}

run() {
    local models=$1
    shift
    rm -f "$FAKE_ARGV"
    out=$(PATH="$base_path:$models" "$script" "$@" 2>"$work/stderr")
    code=$?
    err=$(cat "$work/stderr")
}

confdir="$work/config dir"
mkdir -p "$confdir"

run "$work/models" local --config-dir "$confdir"
[ "$code" -eq 0 ] || fail "explicit config dir: exit $code, out: $out, stderr: $err"
expect_argv "explicit config dir" config --config-dir "$confdir"
expect_line "explicit config dir" "$out" "profile: local"
expect_line "explicit config dir" "$out" "codex: $work/models/codex"
expect_line "explicit config dir" "$out" "ok: true"

run "$work/models" --config-dir "$confdir"
[ "$code" -eq 0 ] || fail "default from config dir: exit $code, out: $out"
expect_line "default from config dir" "$out" "profile: local (resolved default)"
expect_line "default from config dir" "$out" "ok: true"

run "$work/claude-only" --config-dir "$confdir" local
[ "$code" -eq 1 ] || fail "missing executor under config dir: exit $code, want 1"
expect_line "missing executor under config dir" "$out" "codex: MISSING - required by this invocation"
expect_line "missing executor under config dir" "$out" "ok: false"

run "$work/models" local
[ "$code" -eq 1 ] || fail "no config dir: exit $code, want 1"
expect_argv "no config dir" config
expect_line "no config dir" "$out" "profile: UNKNOWN 'local'"

run "$work/claude-only"
[ "$code" -eq 0 ] || fail "bare default: exit $code, out: $out"
expect_argv "bare default" config
expect_line "bare default" "$out" "profile: base (resolved default)"
expect_line "bare default" "$out" "ok: true"

usage_cases=(
    "option without a value|--config-dir"
    "empty config dir|--config-dir||local"
    "unknown option|--config|$confdir"
    "two profiles|local|base"
)
for c in "${usage_cases[@]}"; do
    IFS='|' read -r -a parts <<<"$c"
    name=${parts[0]}
    run "$work/models" "${parts[@]:1}"
    [ "$code" -eq 1 ] || fail "$name: exit $code, want 1"
    [ -n "$err" ] || fail "$name: no error on stderr"
    expect_line "$name" "$out" "ok: false"
    [ ! -e "$FAKE_ARGV" ] || fail "$name: revmux config ran"
done

FAKE_FAIL=1 run "$work/models" --config-dir "$confdir" local
[ "$code" -eq 1 ] || fail "config failure: exit $code, want 1"
expect_line "config failure" "$out" "config: FAILED - revmux could not resolve its configuration"
expect_line "config failure" "$out" "hint: run 'revmux config' with the same --config-dir to see the error"

FAKE_FAIL=1 run "$work/models" local
[ "$code" -eq 1 ] || fail "bare config failure: exit $code, want 1"
expect_line "bare config failure" "$out" "hint: run 'revmux config' directly to see the error"

if [ "$failures" -gt 0 ]; then
    echo "$failures failure(s)" >&2
    exit 1
fi
echo "preflight.sh: all tests passed"
