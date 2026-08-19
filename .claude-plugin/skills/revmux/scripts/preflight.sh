#!/usr/bin/env bash
# preflight.sh - verify revmux and the model CLIs a profile actually needs.
#
# revmux drives `claude`, `codex` and `agy` as subprocesses, so a missing binary is a run that
# starts, launches agents, and degrades every source before failing with exit 2. Checking
# first turns that into one line of output.
#
# which binaries are needed depends on the resolved profile, not on a fixed list: a profile, a
# roster entry or a stage names one in its `model:`, and a user override can change that. The
# authority is `revmux config`, which reports what resolved rather than what ships.
#
# it also depends on the invocation. --lenses replaces the roster with one agent running on the
# profile's own base runner, so that run needs the base plus the stages and none of the roster's
# own binaries — while an ordinary run needs the roster and the stages and not the base, which
# may name a binary nothing else uses. --runners filters the roster by binary — a roster agent on
# an excluded binary is dropped, a stage on one falls back to the first listed — so a filtered run
# needs fewer binaries than the profile's full roster, and checking the full roster refuses exactly
# the host the filter exists for.
#
# usage: preflight.sh [profile] [--lenses] [--runners a,b]
#   profile        profile to check; defaults to whatever revmux resolves as its default
#   --lenses       check the invocation --lenses produces rather than the profile's own roster
#   --runners a,b  check the invocation --runners produces: pass the same value the run will use
#
# output: one `key: value` line per check, then `ok: true|false`
# exit:   0 all good, 1 something missing

set -u

usage() {
    echo "usage: $(basename "$0") [profile] [--lenses] [--runners a,b]" >&2
    echo "  profile        profile to check; defaults to whatever revmux resolves as its default" >&2
    echo "  --lenses       check the invocation --lenses produces rather than the profile's own roster" >&2
    echo "  --runners a,b  check the invocation --runners produces: pass the same value the run will use" >&2
}

# strict: an argument that is not understood is a caller mistake, and silently dropping it checks a
# different invocation than the one asked for — `--lenses=bugs` or a misspelled flag would report a
# clean host for a run that then fails to launch its only finder
profile=""
lenses=""
runners=""
expect=""
for arg in "$@"; do
    if [ "$expect" = "runners" ]; then
        runners="$arg"
        expect=""
        continue
    fi
    case "$arg" in
        --lenses)
            lenses=1
            ;;
        --runners)
            expect=runners
            ;;
        --runners=*)
            runners="${arg#--runners=}"
            if [ -z "$runners" ]; then
                echo "--runners needs a comma-separated list of binaries" >&2
                usage
                echo "ok: false"
                exit 1
            fi
            ;;
        -*)
            echo "unknown option: $arg" >&2
            usage
            echo "ok: false"
            exit 1
            ;;
        *)
            if [ -n "$profile" ]; then
                echo "unexpected argument: $arg" >&2
                usage
                echo "ok: false"
                exit 1
            fi
            profile="$arg"
            ;;
    esac
done
if { [ "$expect" = "runners" ] || [ -n "$runners" ]; } && [ -z "${runners//,/}" ]; then
    echo "--runners needs a comma-separated list of binaries" >&2
    usage
    echo "ok: false"
    exit 1
fi

# executorQuery is the jq that names the binaries this invocation needs. The stages always count; the
# roster counts only when it will actually run, and the profile's base runner only when --lenses
# replaces that roster with one agent on it. Unioning both over-checks and turns a review revmux can
# run into a preflight failure, which is worse than the gap it would close.
# A --runners filter narrows the set the same way revmux does: a roster agent on an excluded binary
# is dropped, while a stage — and the --lenses base runner — falls back to the first listed binary.
# Checking the unfiltered roster instead refuses exactly the host the filter exists for.
# $p and $r are jq's own variables, bound by --arg, so the single quotes are deliberate.
# shellcheck disable=SC2016
executorQuery() {
    if [ -n "$lenses" ]; then
        echo '($r | if . == "" then [] else split(",") end) as $rs
              | [.profiles[] | select(.name == $p)
                 | ((.runner.executor, .stages[].executor) | . as $e
                    | if ($rs | length) == 0 or ($rs | index($e)) then $e else $rs[0] end)]
              | unique | .[]'
        return
    fi
    echo '($r | if . == "" then [] else split(",") end) as $rs
          | [.profiles[] | select(.name == $p)
             | (.roster[].executor | select(. as $e | ($rs | length) == 0 or ($rs | index($e)))),
               (.stages[].executor | . as $e
                | if ($rs | length) == 0 or ($rs | index($e)) then $e else $rs[0] end)]
          | unique | .[]'
}

ok=true

fail() {
    echo "$1"
    ok=false
}

if ! command -v revmux >/dev/null 2>&1; then
    echo "revmux: MISSING"
    echo "install: brew install umputun/apps/revmux (macOS)"
    echo "     or: go install github.com/umputun/revmux/app@latest (binary lands as 'app', rename it)"
    echo "     or: git clone https://github.com/umputun/revmux.git && cd revmux && make install"
    echo "ok: false"
    exit 1
fi
echo "revmux: $(command -v revmux) ($(revmux --version 2>/dev/null || echo 'version unknown'))"

# `revmux config` resolves the whole tree: profiles, rosters, stages and the executor
# vocabulary. It runs no pipeline and writes nothing, so it is safe to call for a probe.
cfg=$(revmux config 2>/dev/null) || {
    fail "config: FAILED - revmux could not resolve its configuration"
    echo "hint: run 'revmux config' directly to see the error"
    echo "ok: false"
    exit 1
}

if command -v jq >/dev/null 2>&1; then
    # a --runners name outside the executor vocabulary is refused by revmux at load, so passing it
    # through would compute requirements for a run that cannot launch
    # shellcheck disable=SC2016
    if [ -n "$runners" ]; then
        bad=$(printf '%s' "$cfg" | jq -r --arg r "$runners" \
            '.vocabulary.executors as $known | [$r | split(",")[] | . as $x | select($x == "" or (($known | index($x)) | not))] | join(", ")')
        if [ -n "$bad" ]; then
            fail "runners: UNKNOWN '$bad'"
            echo "available: $(printf '%s' "$cfg" | jq -r '.vocabulary.executors | join(", ")')"
            echo "ok: false"
            exit 1
        fi
        echo "runners: $runners"
    fi
    if [ -n "$profile" ]; then
        known=$(printf '%s' "$cfg" | jq -r --arg p "$profile" '[.profiles[].name] | index($p) // "null"')
        if [ "$known" = "null" ]; then
            fail "profile: UNKNOWN '$profile'"
            echo "available: $(printf '%s' "$cfg" | jq -r '[.profiles[].name] | join(", ")')"
            echo "ok: false"
            exit 1
        fi
        echo "profile: $profile"
        # the stages always count, since a stage runs on every review regardless of the roster. What
        # joins them is the roster, or — under --lenses, which replaces it — the profile's base runner
        executors=$(printf '%s' "$cfg" | jq -r --arg p "$profile" --arg r "$runners" \
            "$(executorQuery)")
    else
        # the resolved default profile, not every profile: checking rosters that will not run reports
        # a missing binary the review never needed
        profile=$(printf '%s' "$cfg" | jq -r '(.knobs[] | select(.name == "profile") | .value) // empty')
        if [ -z "$profile" ]; then
            fail "profile: could not resolve the default from revmux config"
            echo "ok: false"
            exit 1
        fi
        echo "profile: $profile (resolved default)"
        executors=$(printf '%s' "$cfg" | jq -r --arg p "$profile" --arg r "$runners" \
            "$(executorQuery)")
    fi
    # a filter that empties the roster is a load error in revmux, so a clean binary report here would
    # bless a run that then refuses to launch
    # shellcheck disable=SC2016
    if [ -n "$runners" ] && [ -z "$lenses" ]; then
        left=$(printf '%s' "$cfg" | jq -r --arg p "$profile" --arg r "$runners" \
            '($r | split(",")) as $rs | [.profiles[] | select(.name == $p) | .roster[].executor | select(. as $e | $rs | index($e))] | length')
        if [ "$left" = "0" ]; then
            fail "runners: EMPTY - no roster agent in '$profile' runs on '$runners', revmux refuses an empty roster"
            echo "ok: false"
            exit 1
        fi
    fi
else
    # jq is how the resolved profile is read, and there is no honest answer without it: checking both
    # binaries instead refuses a single-binary profile on a host that can run it, which is a false
    # failure on a mandatory gate. Saying so names the one thing that fixes it.
    fail "jq: MISSING - cannot read which binaries this profile needs"
    echo "ok: false"
    exit 1
fi

for exe in $executors; do
    if command -v "$exe" >/dev/null 2>&1; then
        echo "$exe: $(command -v "$exe")"
    else
        fail "$exe: MISSING - required by this invocation"
    fi
done

echo "ok: $ok"
[ "$ok" = true ] || exit 1
