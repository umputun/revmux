#!/usr/bin/env bash
# preflight.sh - verify revmux and the model CLIs a profile actually needs.
#
# revmux drives `claude` and `codex` as subprocesses, so a missing binary is a run that
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
# may name a binary nothing else uses.
#
# usage: preflight.sh [profile] [--lenses]
#   profile   profile to check; defaults to whatever revmux resolves as its default
#   --lenses  check the invocation --lenses produces rather than the profile's own roster
#
# output: one `key: value` line per check, then `ok: true|false`
# exit:   0 all good, 1 something missing

set -u

usage() {
    echo "usage: $(basename "$0") [profile] [--lenses]" >&2
    echo "  profile   profile to check; defaults to whatever revmux resolves as its default" >&2
    echo "  --lenses  check the invocation --lenses produces rather than the profile's own roster" >&2
}

# strict: an argument that is not understood is a caller mistake, and silently dropping it checks a
# different invocation than the one asked for — `--lenses=bugs` or a misspelled flag would report a
# clean host for a run that then fails to launch its only finder
profile=""
lenses=""
for arg in "$@"; do
    case "$arg" in
        --lenses)
            lenses=1
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

# executorQuery is the jq that names the binaries this invocation needs. The stages always count; the
# roster counts only when it will actually run, and the profile's base runner only when --lenses
# replaces that roster with one agent on it. Unioning both over-checks and turns a review revmux can
# run into a preflight failure, which is worse than the gap it would close.
# $p is jq's own variable, bound by --arg, so the single quotes are deliberate.
# shellcheck disable=SC2016
executorQuery() {
    if [ -n "$lenses" ]; then
        echo '[.profiles[] | select(.name == $p) | (.runner.executor, .stages[].executor)] | unique | .[]'
        return
    fi
    echo '[.profiles[] | select(.name == $p) | (.roster[].executor, .stages[].executor)] | unique | .[]'
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
    # a project profile that will not resolve fails the review at load, so reporting ok here would
    # send the caller into a run that cannot start
    profile_err=$(printf '%s' "$cfg" | jq -r '.paths.profile_fallback_error // empty')
    if [ -n "$profile_err" ]; then
        fail "project profile: UNREADABLE - $profile_err"
        echo "hint: fix or remove ./.revmux/profile.md"
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
        executors=$(printf '%s' "$cfg" | jq -r --arg p "$profile" \
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
        executors=$(printf '%s' "$cfg" | jq -r --arg p "$profile" \
            "$(executorQuery)")
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
