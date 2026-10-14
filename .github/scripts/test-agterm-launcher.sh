#!/usr/bin/env bash
# Stub-only agterm launcher checks: no Go build, TUI, or model processes.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/app"
cat > "$TMP/bin/revmux" <<'STUB'
#!/bin/bash
printf '%s\n' "$@" > "$TEST_ARGS"
printf '%s' "${TEST_REPORT:-}"
if [ -n "${TEST_SIGNAL:-}" ]; then kill -s "$TEST_SIGNAL" "$PPID"; exit 0; fi
printf '%s' "${TEST_STDERR:-}" >&2
exit "${TEST_REVIEW_RC:-0}"
STUB
cat > "$TMP/bin/agtermctl" <<'STUB'
#!/bin/bash
printf '%s\n' "$0" >> "$TEST_CLI"
printf '%q ' "$@" >> "$TEST_LOG"; printf '\n' >> "$TEST_LOG"
case "$*" in
    'session status --help'*) [ -n "${TEST_OLD_CLI:-}" ] || echo '--pane-id'; exit 0 ;;
    'session overlay open --help'*) [ -n "${TEST_OLD_CLI:-}" ] || echo '--pane'; exit 0 ;;
    tree*) printf '%s\n' "$TEST_TREE"; exit ;;
    'theme list'*) exit ;;
    'session status '*) exit "${TEST_STATUS_RC:-0}" ;;
esac
if [ "$1 $2 $3" = 'session overlay open' ]; then
    case "${TEST_MODE:-run}" in
        replaced) exit "${TEST_CTL_RC:-0}" ;;
        refused) echo 'pane overlay already open' >&2; exit 1 ;;
    esac
    /bin/bash -c "$4"; child_rc=$?
    exit "${TEST_CTL_RC:-$child_rc}"
fi
exit 9
STUB
cat > "$TMP/bin/kitty" <<'STUB'
#!/bin/bash
printf 'kitty invoked\n' >> "$TEST_LOG"
exit 9
STUB
cat > "$TMP/bin/git" <<'STUB'
#!/bin/bash
printf '/common-checkout\n'
STUB
cp "$TMP/bin/agtermctl" "$TMP/app/agtermctl"
chmod +x "$TMP/bin/"* "$TMP/app/agtermctl"
TREE='{"result":{"tree":{"workspaces":[{"sessions":[{"id":"owner","split":true,"background":{"kind":"color","colorHex":"#303030"},"surfaces":[{"kind":"left","paneID":"sibling","visible":true},{"kind":"right","paneID":"mine","visible":true}]}]}]}}}'
run() {
    : > "$TMP/log"; : > "$TMP/cli"
    rc=0
    env -i HOME="$TMP" PATH="$TMP/bin:/usr/bin:/bin" TMPDIR="$TMP" \
        TEST_LOG="$TMP/log" TEST_CLI="$TMP/cli" TEST_ARGS="$TMP/args" TEST_TREE="$TREE" TEST_REPORT='{"findings":[]}' \
        AGTERM_ENABLED=1 AGTERM_SESSION_ID=owner AGTERM_SOCKET='/tmp/owner socket' \
        AGTERM_WINDOW_ID=window AGTERM_PANE=left AGTERM_PANE_ID=mine \
        KITTY_LISTEN_ON=inherited "$@" \
        /bin/bash "$LAUNCHER" --task "a b;\$(touch nope)" --run initial > "$TMP/out" 2> "$TMP/err" || rc=$?
}
assert_rc() { [ "$rc" = "$1" ] || { echo "expected rc $1, got $rc" >&2; cat "$TMP/err" >&2; exit 1; }; }
assert_log() { grep -q -- "$1" "$TMP/log" || { echo "missing log: $1" >&2; cat "$TMP/log" >&2; exit 1; }; }
assert_absent() { if grep -q -- "$1" "$TMP/log"; then echo "unexpected log: $1" >&2; exit 1; fi; }
for LAUNCHER in "$ROOT/.claude-plugin/skills/revmux/scripts/launch-revmux.sh" "$ROOT/plugins/codex/skills/revmux/scripts/launch-revmux.sh"; do
    for result in 0 1 2; do
        run TEST_REVIEW_RC="$result" TEST_CTL_RC=23
        assert_rc "$result"
        [ "$(cat "$TMP/out")" = '{"findings":[]}' ]
    done
    assert_log '^session overlay open .*--pane right'
    assert_log '^session status active.*--pane-id mine.*--socket /tmp/owner\\ socket.*--window window'
    assert_log '^session status active.*--target owner'
    assert_log '^tree --json.*--socket /tmp/owner\\ socket.*--window window'
    assert_absent kitty
    # shellcheck disable=SC2016 # The literal argument must not execute as shell code.
    grep -Fxq 'a b;$(touch nope)' "$TMP/args"
    [ ! -e "$PWD/nope" ]
    run TEST_REPORT= TEST_REVIEW_RC=2 TEST_STDERR='archive failed'
    assert_rc 2
    grep -q 'archive failed' "$TMP/err"
    run TEST_REPORT= TEST_REVIEW_RC=0
    assert_rc 3
    for result in 0 1 2 23; do
        run TEST_MODE=replaced TEST_CTL_RC="$result"
        assert_rc 3
    done
    run TEST_MODE=refused
    assert_rc 3
    [ "$(grep -c '^session overlay open ' "$TMP/log")" = 2 ] # one help, one open
    assert_absent '^session status active'
    run TEST_STATUS_RC=6
    assert_rc 0
    for token in unknown ''; do
        run AGTERM_PANE_ID="$token"
        assert_rc 3
        assert_absent '^session overlay open '
        assert_absent '^session status active'
    done
    run TEST_TREE="${TREE//\"visible\":true/\"visible\":false}" REVMUX_AGTERM_PERCENT=75
    assert_rc 3
    SCRATCH=${TREE//\"kind\":\"right\"/\"kind\":\"scratch\"}
    run TEST_TREE="$SCRATCH"
    assert_rc 3
    run TEST_TREE="$SCRATCH" REVMUX_AGTERM_PERCENT=75
    assert_rc 0
    assert_log '^session overlay open .*--size-percent 75'
    if grep '^session overlay open ' "$TMP/log" | grep -q -- '--pane '; then exit 1; fi
    run TEST_TREE="${TREE//\"split\":true/\"split\":false}"
    assert_rc 0
    assert_log '^session overlay open .*--size-percent 80'
    LEGACY='{"result":{"tree":{"workspaces":[{"sessions":[{"id":"owner","split":true,"surfaces":[{"kind":"left","visible":true}]}]}]}}}'
    run TEST_TREE="$LEGACY" TEST_OLD_CLI=1
    assert_rc 0
    assert_log '^session status active.*--pane left'
    assert_log '^session overlay open .*--size-percent 80'
    run TEST_TREE="${LEGACY//\"visible\":true/\"visible\":false}" REVMUX_AGTERM_PERCENT=75
    assert_rc 3
    run TEST_TREE="$LEGACY" AGTERM_PANE=right REVMUX_AGTERM_PERCENT=75
    assert_rc 3
    RECOVER='{"result":{"tree":{"workspaces":[{"sessions":[{"id":"owner","cwd":"/checkout","split":false,"surfaces":[{"kind":"right","paneID":"mine","visible":true}]}]}]}}}'
    run AGTERM_SESSION_ID= TEST_TREE="$RECOVER"
    assert_rc 0
    assert_log '^session status active.*--pane-id mine'
    # A checkout match cannot identify the caller's pane in a visible split.
    run AGTERM_SESSION_ID= TEST_TREE="${TREE//\"id\":\"owner\"/\"id\":\"owner\",\"cwd\":\"\/checkout\"}"
    assert_rc 3
    TWO_MATCHES='{"result":{"tree":{"workspaces":[{"sessions":[{"id":"owner","cwd":"/checkout","surfaces":[{"kind":"right","paneID":"mine","visible":true}]},{"id":"other","cwd":"/other-worktree","surfaces":[{"kind":"left","paneID":"a","visible":true},{"kind":"right","paneID":"b","visible":true}]}]}]}}}'
    run AGTERM_SESSION_ID= TEST_TREE="$TWO_MATCHES"
    assert_rc 3
    assert_absent '^session overlay open '
    run AGTERM_SESSION_ID= AGTERMCTL=/does/not/exist
    assert_rc 3
    # A fragment emitted before cancellation is not a completed invocation.
    run TEST_SIGNAL=TERM TEST_CTL_RC=0 TEST_REPORT=partial-report
    assert_rc 3
    [ "$(cat "$TMP/out")" = partial-report ]
    run GHOSTTY_BIN_DIR="$TMP/app"
    assert_rc 0
    [ "$(sort -u "$TMP/cli")" = "$TMP/app/agtermctl" ]
    run GHOSTTY_BIN_DIR="$TMP/app" AGTERMCTL="$TMP/bin/agtermctl"
    assert_rc 0
    [ "$(sort -u "$TMP/cli")" = "$TMP/bin/agtermctl" ]
done
printf 'revmux agterm launcher regressions passed\n'
