#!/usr/bin/env bash
# launch-revmux.sh - run revmux with its TUI in a terminal overlay and return the report.
#
# Two ways exist to run a review, and this script is the second one:
#
#   headless  revmux --task X --run Y --no-tui > findings.json   agent launches it in the background,
#             reads the JSON when it finishes. Nothing to watch.
#   overlay   this script                                  the TUI runs on screen in an overlay
#             (agterm, tmux popup, zellij float, kitty overlay, ...) so the user watches agents
#             work, then the report comes back on stdout exactly as in headless mode.
#
# Same review either way. The overlay exists because a review takes minutes and a live view of
# which agent is doing what is the difference between waiting and watching.
#
# usage: launch-revmux.sh --task <id> --run <name> [any other revmux flag]
# output: the report (JSON by default, markdown with --markdown) on stdout
# exit:   0 no findings, 1 findings reported, 2 tool error (all three are revmux's own)
#         3 launcher failure - revmux never ran, or the overlay died before it finished
#         127 revmux not installed
#
# 0, 1 and 2 are revmux's and pass through unchanged; 1 means the review found things and is a
# SUCCESS. No launcher failure may ever exit 0, 1 or 2 - use RC_LAUNCH_FAIL.
#
# env overrides:
#   REVMUX_AGTERM_PERCENT  agterm overlay size, 1-100      (default 80)
#   REVMUX_POPUP_WIDTH     tmux/zellij popup width         (default 90%)
#   REVMUX_POPUP_HEIGHT    tmux/zellij/wezterm popup height (default 90%)
#   REVMUX_AUTO_EXIT       TUI self-close delay            (default 30s; 0 waits for the reader)

set -euEo pipefail

# the launcher's own failure code, outside revmux's 0/1/2 vocabulary. See the exit-code note above:
# every path that fails before revmux produced a report must use this and never 1.
RC_LAUNCH_FAIL=3

# the backstop that makes the rule above true of the whole file rather than of the paths that remembered
# it. Under `set -e` an unguarded failure aborts with the failing command's OWN status, and 1 is the
# ordinary failure code of every CLI this script drives - kitty, wezterm, tmux, zellij, emacsclient,
# mktemp - so a backend that cannot start would surface as revmux's "findings reported, do not retry".
# Anything landing here failed before revmux produced a report, so a code in revmux's vocabulary is not
# revmux's to claim. Guarded paths never reach it: a command in an `if` condition or followed by `||` is
# not a `set -e` failure. `-E` above is what extends this into functions and subshells.
trap 'rc=$?; if [ "$rc" -le 2 ]; then rc=$RC_LAUNCH_FAIL; fi; exit "$rc"' ERR

# how long to wait for the overlay's inner shell to publish its pid before giving up on it. Only
# covers process startup, never the review itself, so it stays short.
PID_GRACE_SECONDS=10

# resolve revmux to an absolute path so overlay shells (sh -c) find it even when the launching
# server's PATH predates the user's shell rc files
REVMUX_BIN=$(command -v revmux 2>/dev/null || true)
if [ -z "$REVMUX_BIN" ]; then
    echo "error: revmux not found in PATH" >&2
    echo "install: go install github.com/umputun/revmux/app@latest (installs as 'app', rename it)" >&2
    echo "     or: git clone https://github.com/umputun/revmux.git && cd revmux && make install" >&2
    exit 127
fi

if [ "$#" -eq 0 ]; then
    echo "error: no arguments - revmux needs --task <id> and --run <name>" >&2
    exit "$RC_LAUNCH_FAIL"
fi

TMPBASE="${TMPDIR:-/tmp}"
REPORT_FILE=$(mktemp "$TMPBASE/revmux-report-XXXXXX")
STDERR_FILE=$(mktemp "$TMPBASE/revmux-stderr-XXXXXX")
trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" || true' EXIT

# shell-quote a single argument for safe embedding in sh -c strings
sq() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

REVMUX_CMD="$(sq "$REVMUX_BIN")"
HAS_AUTO_EXIT=0
for arg in "$@"; do
    case "$arg" in
        --auto-exit|--auto-exit=*) HAS_AUTO_EXIT=1 ;;
        --no-tui)
            echo "error: --no-tui defeats the purpose of an overlay - run revmux directly for headless mode" >&2
            exit "$RC_LAUNCH_FAIL"
            ;;
    esac
    REVMUX_CMD="$REVMUX_CMD $(sq "$arg")"
done

# revmux leaves the TUI open until the reader quits with q or ctrl+c (--auto-exit=0s). In an overlay opened on the
# user's behalf that means the launcher blocks forever if nobody comes back to it, so give the TUI a
# self-close delay unless the caller chose one. REVMUX_AUTO_EXIT=0 restores waiting for the reader.
AUTO_EXIT="${REVMUX_AUTO_EXIT:-30s}"
if [ "$HAS_AUTO_EXIT" -eq 0 ] && [ "$AUTO_EXIT" != "0" ]; then
    REVMUX_CMD="$REVMUX_CMD $(sq "--auto-exit=$AUTO_EXIT")"
fi

# **revmux spawns claude and codex itself**, so resolving the revmux binary is not enough: the
# overlay shell needs a PATH that reaches those too. Overlay backends (agterm, tmux display-popup,
# kitty @ launch, zellij run) start children from a server or app process whose environment predates
# the user's shell rc files, so a Homebrew or ~/.local/bin claude is otherwise simply not found and
# every agent degrades. Forward the caller's own PATH plus the variables that decide where the CLIs
# and revmux look for their configuration.
#
# ANTHROPIC_API_KEY is deliberately NOT forwarded: an `env KEY=VAL` prefix puts the value in the
# process argv where any `ps` can read it. revmux strips that variable from its children by default
# anyway, so overlay runs use interactive subscription auth. Use headless mode for key-based auth.
ENV_PREFIX=" $(sq "PATH=$PATH")"
for _name in HOME XDG_CONFIG_HOME CODEX_HOME TMPDIR; do
    if [ "${!_name+x}" = x ]; then
        ENV_PREFIX="$ENV_PREFIX $(sq "${_name}=${!_name}")"
    fi
done
unset _name
REVMUX_CMD="/usr/bin/env$ENV_PREFIX $REVMUX_CMD"

# the report is revmux's stdout and nothing else is, so redirecting it captures exactly the report
# while the TUI keeps rendering to the tty
REVMUX_CMD="$REVMUX_CMD > $(sq "$REPORT_FILE") 2> $(sq "$STDERR_FILE")"

# the inner command every sentinel-based backend runs: publish pid, run revmux, write the exit code.
# The pid is what lets await_sentinel bound its wait - a closed overlay kills the inner shell by
# SIGHUP before it can write the sentinel, so waiting on the file alone never returns.
write_rc_cmd() {
    local sentinel="$1"
    # single-quoted format keeps $$/$?/$rc literal for the generated inner script
    # shellcheck disable=SC2016
    printf 'printf "%%s" "$$" > %s.pid; %s; rc=$?; printf "%%s" "$rc" > %s.tmp && mv -f %s.tmp %s' \
        "$(sq "$sentinel")" "$REVMUX_CMD" "$(sq "$sentinel")" "$(sq "$sentinel")" "$(sq "$sentinel")"
}

# same as write_rc_cmd, plus the trailing `exit` the emacs launcher needs. The write-then-rename is not
# decoration: await_sentinel polls for the file's existence, so a plain `> $sentinel` lets a poll land in
# the gap between the create and the write, read an empty file, and substitute RC_LAUNCH_FAIL for a
# review that finished - reporting a complete report under the code that means revmux never ran.
write_fifo_rc_cmd() {
    local sentinel="$1"
    # shellcheck disable=SC2016
    printf 'printf "%%s" "$$" > %s.pid; %s; rc=$?; printf "%%s" "$rc" > %s.tmp && mv -f %s.tmp %s; exit' \
        "$(sq "$sentinel")" "$REVMUX_CMD" "$(sq "$sentinel")" "$(sq "$sentinel")" "$(sq "$sentinel")"
}

read_rc() {
    cat "$1" 2>/dev/null || echo "$RC_LAUNCH_FAIL"
}

# block until the sentinel appears or the overlay's inner shell is gone. 0 = sentinel landed,
# 1 = overlay died first. PID_GRACE_SECONDS covers only the startup window before the pid is
# published; after that the wait is bounded on the process, never on a timer, since a review runs as
# long as it runs.
await_sentinel() {
    local sentinel="$1" pid="" waited=0
    while [ ! -f "$sentinel" ]; do
        if [ -z "$pid" ]; then
            [ -s "$sentinel.pid" ] && pid=$(cat "$sentinel.pid" 2>/dev/null)
            if [ -z "$pid" ] && [ "$waited" -ge "$((PID_GRACE_SECONDS * 10))" ]; then
                echo "error: overlay never started (no pid after ${PID_GRACE_SECONDS}s)" >&2
                return 1
            fi
            waited=$((waited + 1))
        elif ! kill -0 "$pid" 2>/dev/null; then
            # one last look: the process can exit in the window between writing the sentinel and
            # our next stat, and that case is a completed run, not a dead overlay
            [ -f "$sentinel" ] && return 0
            echo "error: overlay closed before the review finished" >&2
            return 1
        fi
        sleep 0.1
    done
    return 0
}

# print the report and pass the exit code through, surfacing revmux's stderr when no report exists -
# the overlay has already closed by then and the reason would go with it.
print_report_and_exit() {
    local rc="${1:-0}"
    if [ -s "$REPORT_FILE" ]; then
        cat "$REPORT_FILE"
        exit "$rc"
    fi

    # a revmux run OF A REVIEW writes its report before returning 0 or 1, so neither code can reach here
    # over an empty one - the status came from the backend the overlay was driven through, whose own
    # failures are 1. Passing it on would tell a caller a review that never started found nothing, or
    # found things it then cannot parse, and both are codes it is told never to retry.
    #
    # The qualifier is load-bearing: --help and --dump-defaults write to stderr and return 0 with stdout
    # untouched, so they land here legitimately. This script takes `[any other revmux flag]` and so can be
    # handed one, and re-coding it says the launcher failed when revmux did the work. That is the inverse
    # error, and it is accepted only because neither is a review - there is no report to lose, and their
    # own output already reached the terminal.
    #
    # --init is not one of them: it materializes ./.revmux/ and prints the paths as JSON on stdout, so it
    # is returned by the report branch above with its own exit code and never reaches this one. The test
    # for a new flag is therefore what it writes to stdout, not whether it runs a review.
    if [ "$rc" = "0" ] || [ "$rc" = "1" ]; then
        rc=$RC_LAUNCH_FAIL
    fi

    if [ "$rc" = "$RC_LAUNCH_FAIL" ]; then
        # never attribute this code to revmux: it is the launcher's, and revmux may never have run
        echo "error: no report produced (overlay closed, or revmux never started)" >&2
    else
        echo "error: revmux exited $rc without writing a report" >&2
    fi
    if [ -s "$STDERR_FILE" ]; then
        sed 's/^/  /' "$STDERR_FILE" >&2
    fi
    exit "$rc"
}

is_cmux_session() {
    [ -n "${CMUX_SURFACE_ID:-}" ] && return 0
    [ "${__CFBundleIdentifier:-}" = "com.cmuxterm.app" ] && return 0
    case "${GHOSTTY_RESOURCES_DIR:-}:${GHOSTTY_BIN_DIR:-}" in
        *cmux.app*) return 0 ;;
    esac
    return 1
}

CWD="$(pwd)"

# descriptive title: "rm: dirname [task]"
DIR_NAME=$(basename "$CWD")
TITLE_TASK=""
NEXT_IS_TASK=0
for arg in "$@"; do
    if [ "$NEXT_IS_TASK" -eq 1 ]; then TITLE_TASK="$arg"; break; fi
    case "$arg" in
        --task) NEXT_IS_TASK=1 ;;
        --task=*) TITLE_TASK="${arg#--task=}"; break ;;
    esac
done
OVERLAY_TITLE="rm: ${DIR_NAME}${TITLE_TASK:+ [$TITLE_TASK]}"

POPUP_W="${REVMUX_POPUP_WIDTH:-90%}"
POPUP_H="${REVMUX_POPUP_HEIGHT:-90%}"

# agterm: `session overlay open --block` runs revmux over the agent's own session and blocks until it
# exits, returning revmux's exit code directly - so no sentinel file is needed, unlike every backend
# below. Checked first so an agterm session uses its native overlay even when a multiplexer is also
# present. --size-percent renders a floating framed panel rather than covering the whole pane, which
# keeps the session visible around it; 80% is enough for the status table plus a readable detail pane.
# --cwd pins the overlay to the launcher's directory instead of the agent session's own.
if [ -n "${AGTERM_SESSION_ID:-}" ] && command -v agtermctl >/dev/null 2>&1; then
    AGTERM_TARGET=(--target "$AGTERM_SESSION_ID")
    [ -n "${AGTERM_SOCKET:-}" ] && AGTERM_TARGET+=(--socket "$AGTERM_SOCKET")

    # Mark the session active for the duration. Deliberately `active` and not `blocked --blink`,
    # which is what an interactive tool would set: a review is work in progress, not a prompt waiting
    # on the user, and blinking for attention over ten minutes during which nothing needs them is
    # noise. Under a harness that drives the glyph itself this is redundant; under one that does not,
    # it is the only sign the session is busy. No restore on exit for the same reason - there is no
    # prior state this owns.
    AGTERM_STATUS=(session status active)
    case "${AGTERM_PANE:-}" in
        left|right|scratch) AGTERM_STATUS+=(--pane "$AGTERM_PANE") ;;
    esac
    agtermctl "${AGTERM_STATUS[@]}" "${AGTERM_TARGET[@]}" >/dev/null 2>&1 || true
    # the EXIT trap from the top already removes both temp files; these two only ensure it runs on a
    # signal rather than being skipped
    trap 'exit 130' INT
    trap 'exit 143' TERM

    AGTERM_PERCENT="${REVMUX_AGTERM_PERCENT:-80}"
    rc=0
    agtermctl session overlay open "$REVMUX_CMD" "${AGTERM_TARGET[@]}" \
        --cwd "$CWD" --size-percent "$AGTERM_PERCENT" --block || rc=$?
    print_report_and_exit "$rc"
fi

# tmux window backend: a server-owned window instead of a popup, so the review survives a dropped
# SSH or tmux client. Triggered by REVMUX_TMUX_WINDOW=1 or by agent-deck auto-detection, whose
# control-mode client cannot render display-popup at all. Sourced, and it exits the process when it
# takes the run; it returns here as a no-op for every other tmux, leaving the popup path below intact.
if [ -n "${TMUX:-}" ] && command -v tmux >/dev/null 2>&1; then
    _RM_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
    # shellcheck source=/dev/null  # sibling backend resolved at runtime; not followed at lint time
    # shellcheck disable=SC1091
    [ -f "$_RM_SCRIPT_DIR/agentdeck-window.sh" ] && . "$_RM_SCRIPT_DIR/agentdeck-window.sh"
fi

# tmux: display-popup -E blocks until the command exits
if [ -n "${TMUX:-}" ] && command -v tmux >/dev/null 2>&1; then
    # -T (title) requires tmux 3.3+; skip on older versions
    TMUX_ARGS=(tmux display-popup -E -w "$POPUP_W" -h "$POPUP_H")
    if [[ "$(tmux -V 2>/dev/null)" =~ ([0-9]+)\.([0-9]+) ]]; then
        if [ "${BASH_REMATCH[1]}" -gt 3 ] || { [ "${BASH_REMATCH[1]}" -eq 3 ] && [ "${BASH_REMATCH[2]}" -ge 3 ]; }; then
            TMUX_ARGS+=(-T " $OVERLAY_TITLE ")
        fi
    fi
    TMUX_ARGS+=(-d "$CWD" -- sh -c "$REVMUX_CMD")
    rc=0
    "${TMUX_ARGS[@]}" || rc=$?
    print_report_and_exit "$rc"
fi

# zellij: floating pane with a sentinel file for blocking
if [ -n "${ZELLIJ:-}" ] && command -v zellij >/dev/null 2>&1; then
    SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
    rm -f "$SENTINEL"
    LAUNCH_SCRIPT=$(mktemp "$TMPBASE/revmux-launch-XXXXXX")
    trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" "$LAUNCH_SCRIPT" || true' EXIT
    cat > "$LAUNCH_SCRIPT" <<LAUNCHER
#!/bin/sh
$(write_rc_cmd "$SENTINEL")
LAUNCHER
    chmod +x "$LAUNCH_SCRIPT"

    ZELLIJ_ORIG_TAB_ID=""
    if [ -n "${ZELLIJ_PANE_ID:-}" ] && command -v jq >/dev/null 2>&1; then
        ZELLIJ_ORIG_TAB_ID=$(zellij action list-panes --json --tab 2>/dev/null \
            | jq -r --arg p "$ZELLIJ_PANE_ID" \
                '.[] | select((.is_plugin // false) == false and .tab_id != null and .id == ($p | tonumber)) | .tab_id' 2>/dev/null \
            | head -1 || true)
    fi

    if [ -n "$ZELLIJ_ORIG_TAB_ID" ] && zellij run --floating --close-on-exit --tab-id "$ZELLIJ_ORIG_TAB_ID" \
            --width "$POPUP_W" --height "$POPUP_H" --name "$OVERLAY_TITLE" --cwd "$CWD" \
            -- "$LAUNCH_SCRIPT" >/dev/null 2>&1; then
        :
    else
        zellij run --floating --close-on-exit --width "$POPUP_W" --height "$POPUP_H" \
            --name "$OVERLAY_TITLE" --cwd "$CWD" -- "$LAUNCH_SCRIPT" >/dev/null 2>&1
    fi

    await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
    rc=$(read_rc "$SENTINEL")
    print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
fi

# herdr: a new fullscreen tab via the herdr CLI. Must precede kitty - inside herdr-in-kitty
# KITTY_LISTEN_ON is set, so the kitty branch would otherwise open a window herdr cannot composite.
if [ "${HERDR_ENV:-}" = "1" ] && command -v herdr >/dev/null 2>&1; then
    SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
    rm -f "$SENTINEL"
    LAUNCH_SCRIPT=$(mktemp "$TMPBASE/revmux-launch-XXXXXX")
    trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" "$LAUNCH_SCRIPT" || true' EXIT
    cat > "$LAUNCH_SCRIPT" <<LAUNCHER
#!/bin/sh
$(write_rc_cmd "$SENTINEL")
LAUNCHER
    chmod +x "$LAUNCH_SCRIPT"

    # pin the tab to the caller's workspace: without --workspace, herdr targets the server's focused
    # workspace, which is whatever the user is looking at rather than where the review belongs
    HERDR_TAB_ARGS=(tab create --cwd "$CWD" --label "$OVERLAY_TITLE")
    [ -n "${HERDR_WORKSPACE_ID:-}" ] && HERDR_TAB_ARGS+=(--workspace "$HERDR_WORKSPACE_ID")
    HERDR_TAB_ARGS+=(--focus)
    HERDR_NEW=$(herdr "${HERDR_TAB_ARGS[@]}" 2>&1) || {
        echo "error: herdr tab create failed: $HERDR_NEW" >&2
        exit "$RC_LAUNCH_FAIL"
    }
    HERDR_TAB_ID=""
    HERDR_PANE_ID=""
    if command -v jq >/dev/null 2>&1; then
        HERDR_TAB_ID=$(printf '%s' "$HERDR_NEW" | jq -r '.result.tab.tab_id // empty' 2>/dev/null || true)
        HERDR_PANE_ID=$(printf '%s' "$HERDR_NEW" | jq -r '.result.root_pane.pane_id // empty' 2>/dev/null || true)
    fi
    [ -z "$HERDR_TAB_ID" ] && HERDR_TAB_ID=$(printf '%s' "$HERDR_NEW" | grep -o '"tab_id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
    [ -z "$HERDR_PANE_ID" ] && HERDR_PANE_ID=$(printf '%s' "$HERDR_NEW" | grep -o '"pane_id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)

    # bail explicitly when ids are missing - sending the launch command into the wrong pane would
    # type it into the caller's interactive shell
    if [ -z "$HERDR_PANE_ID" ] || [ -z "$HERDR_TAB_ID" ]; then
        echo "error: herdr tab create did not return pane/tab ids: $HERDR_NEW" >&2
        if [ -n "$HERDR_TAB_ID" ]; then
            herdr tab close "$HERDR_TAB_ID" >/dev/null 2>&1 || true
        fi
        exit "$RC_LAUNCH_FAIL"
    fi

    if ! herdr pane run "$HERDR_PANE_ID" "sh $(sq "$LAUNCH_SCRIPT")" >/dev/null 2>&1; then
        echo "error: herdr pane run failed for pane $HERDR_PANE_ID" >&2
        herdr tab close "$HERDR_TAB_ID" >/dev/null 2>&1 || true
        exit "$RC_LAUNCH_FAIL"
    fi

    await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
    rc=$(read_rc "$SENTINEL")
    herdr tab close "$HERDR_TAB_ID" >/dev/null 2>&1 || true
    print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
fi

# kitty: overlay window with a sentinel file for blocking
KITTY_SOCK="${KITTY_LISTEN_ON:-}"
if [ -n "$KITTY_SOCK" ] && command -v kitty >/dev/null 2>&1; then
    SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
    rm -f "$SENTINEL"
    trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" || true' EXIT

    KITTY_ARGS=(kitty @ --to "$KITTY_SOCK" launch --type=overlay --title="$OVERLAY_TITLE" --cwd=current)
    [ -n "${KITTY_WINDOW_ID:-}" ] && KITTY_ARGS+=(--match "window_id:${KITTY_WINDOW_ID}")
    KITTY_ARGS+=(sh -c "cd $(sq "$CWD") && $(write_rc_cmd "$SENTINEL")")
    "${KITTY_ARGS[@]}" >/dev/null 2>&1

    await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
    rc=$(read_rc "$SENTINEL")
    print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
fi

# wezterm/kaku: split pane with a sentinel file for blocking
if [ -n "${WEZTERM_PANE:-}" ]; then
    WEZTERM_CLI=()
    if command -v wezterm >/dev/null 2>&1; then
        WEZTERM_CLI=(wezterm cli)
    elif command -v kaku >/dev/null 2>&1; then
        WEZTERM_CLI=(kaku cli)
    fi

    if [ ${#WEZTERM_CLI[@]} -gt 0 ]; then
        SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
        rm -f "$SENTINEL"
        trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" || true' EXIT

        WEZTERM_PCT="${REVMUX_POPUP_HEIGHT:-90%}"
        WEZTERM_PCT="${WEZTERM_PCT%%%}"
        "${WEZTERM_CLI[@]}" split-pane --bottom --percent "$WEZTERM_PCT" \
            --pane-id "$WEZTERM_PANE" --cwd "$CWD" -- sh -c "$(write_rc_cmd "$SENTINEL")" >/dev/null 2>&1

        await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
        rc=$(read_rc "$SENTINEL")
        print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
    fi
fi

# cmux: split pane via the cmux CLI. Must precede ghostty, which cmux may impersonate through env.
if is_cmux_session; then
    if ! command -v cmux >/dev/null 2>&1; then
        echo "error: cmux session detected but cmux CLI not found" >&2
        exit "$RC_LAUNCH_FAIL"
    fi
    SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
    rm -f "$SENTINEL"
    LAUNCH_SCRIPT=$(mktemp "$TMPBASE/revmux-launch-XXXXXX")
    trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" "$LAUNCH_SCRIPT" || true' EXIT
    cat > "$LAUNCH_SCRIPT" <<LAUNCHER
#!/bin/sh
$(write_rc_cmd "$SENTINEL")
LAUNCHER
    chmod +x "$LAUNCH_SCRIPT"

    CMUX_NEW=$(cmux new-split down 2>&1) || true
    CMUX_SURF=$(echo "$CMUX_NEW" | grep -o 'surface:[0-9]*' | head -1 || true)
    # bail explicitly when the new surface cannot be identified - `cmux send` without --surface
    # targets the caller's own pane and would replace the user's shell via exec
    if [ -z "$CMUX_SURF" ]; then
        echo "error: cmux new-split did not return a surface id: $CMUX_NEW" >&2
        exit "$RC_LAUNCH_FAIL"
    fi
    cmux send --surface "$CMUX_SURF" "exec $(sq "$LAUNCH_SCRIPT")\n" >/dev/null 2>&1

    await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
    rc=$(read_rc "$SENTINEL")
    print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
fi

# ghostty: split pane via AppleScript (macOS, Ghostty 1.3.0+)
if [ "${TERM_PROGRAM:-}" = "ghostty" ] && command -v osascript >/dev/null 2>&1; then
    SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
    rm -f "$SENTINEL"
    LAUNCH_SCRIPT=$(mktemp "$TMPBASE/revmux-launch-XXXXXX")
    trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" "$LAUNCH_SCRIPT" || true' EXIT
    cat > "$LAUNCH_SCRIPT" <<LAUNCHER
#!/bin/sh
$(write_rc_cmd "$SENTINEL")
LAUNCHER
    chmod +x "$LAUNCH_SCRIPT"

    if ! GHOSTTY_TERM_ID=$(osascript - "$LAUNCH_SCRIPT" "$CWD" <<'APPLESCRIPT'
on run argv
    set launchScript to item 1 of argv
    set cwd to item 2 of argv
    tell application "Ghostty"
        set cfg to new surface configuration
        set command of cfg to launchScript
        set initial working directory of cfg to cwd
        set wait after command of cfg to false
        set ft to focused terminal of selected tab of front window
        set newTerm to split ft direction down with configuration cfg
        perform action "toggle_split_zoom" on newTerm
        return id of newTerm
    end tell
end run
APPLESCRIPT
    ); then
        exit "$RC_LAUNCH_FAIL"
    fi

    await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
    rc=$(read_rc "$SENTINEL")
    # `|| true` because the review is already over and its report is in hand: closing the surface is
    # tidying, and its failure is never the run's result. Ghostty closes itself once the inner script
    # exits, so this often races an id that is already gone - and an unguarded failure here would abort
    # before print_report_and_exit, losing a report the EXIT trap then deletes.
    osascript - "$GHOSTTY_TERM_ID" <<'APPLESCRIPT' 2>/dev/null || true
on run argv
    tell application "Ghostty" to close terminal id (item 1 of argv)
end run
APPLESCRIPT
    print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
fi

# iterm2: split pane via AppleScript (macOS)
if [ -n "${ITERM_SESSION_ID:-}" ] && command -v osascript >/dev/null 2>&1; then
    SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
    rm -f "$SENTINEL"
    LAUNCH_SCRIPT=$(mktemp "$TMPBASE/revmux-launch-XXXXXX")
    trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" "$LAUNCH_SCRIPT" || true' EXIT
    cat > "$LAUNCH_SCRIPT" <<LAUNCHER
#!/bin/sh
cd "\$1" && $REVMUX_CMD; rc=\$?; printf "%s" "\$rc" > "\$2.tmp" && mv -f "\$2.tmp" "\$2"
LAUNCHER
    chmod +x "$LAUNCH_SCRIPT"

    # ITERM_SESSION_ID is "w0t0p0:UUID"; the AppleScript session id is the UUID part
    ITERM_UUID="${ITERM_SESSION_ID##*:}"
    ITERM_NEW_SESSION=$(osascript - "$ITERM_UUID" "$LAUNCH_SCRIPT" "$CWD" "$SENTINEL" <<'APPLESCRIPT' 2>&1
on run argv
    set targetId to item 1 of argv
    set launchScript to item 2 of argv
    set cwd to item 3 of argv
    set sentinel to item 4 of argv
    set cmd to quoted form of launchScript & " " & quoted form of cwd & " " & quoted form of sentinel
    tell application id "com.googlecode.iterm2"
        repeat with w in windows
            repeat with t in tabs of w
                repeat with s in sessions of t
                    if id of s is targetId then
                        set colCount to columns of s
                        set rowCount to rows of s
                        tell s
                            if colCount >= 160 and colCount > (rowCount * 2) then
                                set newSession to split vertically with same profile command cmd
                            else
                                set newSession to split horizontally with same profile command cmd
                            end if
                        end tell
                        return id of newSession
                    end if
                end repeat
            end repeat
        end repeat
    end tell
    error "session not found: " & targetId
end run
APPLESCRIPT
    ) || {
        echo "error: failed to open iTerm2 split via osascript: $ITERM_NEW_SESSION" >&2
        exit "$RC_LAUNCH_FAIL"
    }

    await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
    rc=$(read_rc "$SENTINEL")
    # `|| true` for the same reason as ghostty: the report is already in hand and closing the session
    # is tidying, so an AppleScript error on a session the user closed himself must not take it down
    osascript - "$ITERM_NEW_SESSION" <<'APPLESCRIPT' 2>/dev/null || true
on run argv
    set sid to item 1 of argv
    tell application id "com.googlecode.iterm2"
        repeat with w in windows
            repeat with t in tabs of w
                repeat with s in sessions of t
                    if id of s is sid then
                        tell s to close
                        return
                    end if
                end repeat
            end repeat
        end repeat
    end tell
end run
APPLESCRIPT
    print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
fi

# emacs vterm: a new vterm buffer via emacsclient
if [ "${INSIDE_EMACS:-}" = "vterm" ] && command -v emacsclient >/dev/null 2>&1; then
    SENTINEL=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
    rm -f "$SENTINEL"
    LAUNCH_SCRIPT=$(mktemp "$TMPBASE/revmux-launch-XXXXXX")
    trap 'rm -f "$REPORT_FILE" "$STDERR_FILE" "$SENTINEL" "$SENTINEL.tmp" "$SENTINEL.pid" "$LAUNCH_SCRIPT" || true' EXIT
    cat > "$LAUNCH_SCRIPT" <<LAUNCHER
#!/bin/sh
cd $(sq "$CWD") && $(write_fifo_rc_cmd "$SENTINEL")
LAUNCHER
    chmod +x "$LAUNCH_SCRIPT"

    # find the calling vterm shell PID (a direct child of Emacs) to tag the caller's frame
    EMACS_PID=$(emacsclient --eval '(emacs-pid)' 2>/dev/null | tr -d '"')
    VTERM_PID=$$
    if [ -z "$EMACS_PID" ] || ! [ "$EMACS_PID" -gt 0 ] 2>/dev/null; then
        echo "error: emacs server not reachable" >&2
        exit "$RC_LAUNCH_FAIL"
    fi
    while P=$(ps -o ppid= -p "$VTERM_PID" 2>/dev/null | tr -d ' '); [ "$P" != "$EMACS_PID" ] && [ "$P" != "1" ] && [ -n "$P" ]; do VTERM_PID=$P; done

    elisp_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }
    ESCAPED_TITLE=$(elisp_escape "$OVERLAY_TITLE")
    ESCAPED_SCRIPT=$(elisp_escape "$LAUNCH_SCRIPT")

    emacsclient --eval "(progn (require 'cl-lib)
      (when-let* ((b (cl-find-if (lambda (b) (let ((p (get-buffer-process b))) (and p (= (process-id p) $VTERM_PID)))) (buffer-list)))
                  (w (get-buffer-window b t)))
        (set-frame-parameter (window-frame w) 'revmux-caller t))
      (let* ((buf (generate-new-buffer \"*revmux*\"))
             (win (display-buffer buf '((display-buffer-pop-up-frame)
                     (pop-up-frame-parameters . ((name . \"$ESCAPED_TITLE\")))))))
        (set-frame-parameter (window-frame win) 'revmux-buf (buffer-name buf))))" >/dev/null 2>&1
    emacsclient --no-wait --eval "(progn (require 'cl-lib)
      (when-let* ((f (cl-find-if (lambda (f) (string= (frame-parameter f 'name) \"$ESCAPED_TITLE\")) (frame-list)))
                  (bn (frame-parameter f 'revmux-buf))
                  (buf (get-buffer bn)))
        (with-current-buffer buf
          (let ((vterm-shell \"$ESCAPED_SCRIPT\"))
            (vterm-mode)))))" >/dev/null 2>&1

    await_sentinel "$SENTINEL" || print_report_and_exit "$RC_LAUNCH_FAIL"
    rc=$(read_rc "$SENTINEL")
    emacsclient --no-wait --eval "(progn (require 'cl-lib)
      (when-let ((f (cl-find-if (lambda (f) (string= (frame-parameter f 'name) \"$ESCAPED_TITLE\")) (frame-list))))
        (let ((bn (frame-parameter f 'revmux-buf)))
          (delete-frame f)
          (when-let ((b (and bn (get-buffer bn)))) (kill-buffer b))))
      (when-let ((f (cl-find-if (lambda (f) (frame-parameter f 'revmux-caller)) (frame-list))))
        (set-frame-parameter f 'revmux-caller nil)
        (select-frame-set-input-focus f)))" >/dev/null 2>&1 || true
    print_report_and_exit "${rc:-$RC_LAUNCH_FAIL}"
fi

echo "error: no overlay terminal available (requires agterm, tmux, zellij, herdr, kitty, wezterm, cmux, ghostty, iTerm2, or emacs vterm)" >&2
echo "hint: run revmux directly with --no-tui for headless mode" >&2
exit "$RC_LAUNCH_FAIL"
