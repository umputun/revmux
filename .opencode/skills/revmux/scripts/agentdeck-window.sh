# shellcheck shell=bash
# revmux - tmux window backend. SOURCED by launch-revmux.sh, never run standalone.
#
# Runs revmux in a server-owned tmux window instead of a display-popup, so the review survives a
# client disconnect. Under agent-deck this is mandatory: its control-mode client does not render
# display-popup at all, and `display-popup -E` would block forever on an invisible overlay.
#
# Activation:
#   REVMUX_TMUX_WINDOW=1   window mode, focused, prior window restored on exit
#   REVMUX_TMUX_WINDOW=0   skip this backend, use the popup
#   unset                  window mode only when agent-deck is detected
#
# Reuses from the caller: TMPBASE, CWD, DIR_NAME, TITLE_TASK, RC_LAUNCH_FAIL and the helpers sq() /
# write_rc_cmd() / read_rc() / print_report_and_exit(). The caller guarantees $TMUX is set and tmux
# is on PATH.
#
# Being sourced imposes two rules: install no EXIT trap, which would clobber the caller's cleanup,
# and namespace every variable `_rm_`. It either returns 0 (caller falls through to the popup) or
# exits the process.

# capture the opt-in before the auto-detection below folds both triggers into _rm_winmode=1 - focus
# and restore are opt-in only, since agent-deck exists not to steal focus
_rm_focus=0
[ "${REVMUX_TMUX_WINDOW:-}" = 1 ] && _rm_focus=1

_rm_winmode="${REVMUX_TMUX_WINDOW:-}"
if [ -z "$_rm_winmode" ]; then
    # agent-deck markers: its env var, mirrored into the tmux session env, plus the session-name prefix
    if [ -n "${AGENTDECK_INSTANCE_ID:-}" ] \
        || tmux show-environment AGENTDECK_INSTANCE_ID >/dev/null 2>&1 \
        || tmux display-message -p '#{session_name}' 2>/dev/null | grep -q '^agentdeck_'; then
        _rm_winmode=1
    else
        _rm_winmode=0
    fi
fi
# return before any trap or sentinel work, so the caller's environment is untouched
[ "$_rm_winmode" = 1 ] || return 0

_rm_winname="revmux: ${DIR_NAME}${TITLE_TASK:+ [$TITLE_TASK]}"

_rm_sentinel=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
rm -f "$_rm_sentinel"

_rm_prevwin=""
if [ "$_rm_focus" = 1 ]; then
    _rm_prevwin=$(tmux display-message -p '#{window_id}' 2>/dev/null || true)
fi

# -d does not steal the active window, -c sets the start dir, -P -F prints the window id to watch.
# Fail loudly rather than waiting on a sentinel that will never appear.
if ! _rm_winid=$(tmux new-window -d -P -F '#{window_id}' -c "$CWD" -n "$_rm_winname" \
        -- sh -c "$(write_rc_cmd "$_rm_sentinel")"); then
    rm -f "$_rm_sentinel" "$_rm_sentinel".tmp "$_rm_sentinel".pid
    echo "revmux: failed to open tmux review window" >&2
    exit "$RC_LAUNCH_FAIL"
fi

if [ "$_rm_focus" = 1 ]; then
    tmux select-window -t "$_rm_winid" 2>/dev/null || true
fi

# bounded on the window existing, never on a timer: a review runs as long as it runs, but a window
# killed without writing a sentinel must not spin forever
while [ ! -f "$_rm_sentinel" ]; do
    tmux list-windows -F '#{window_id}' 2>/dev/null | grep -qxF "$_rm_winid" || break
    sleep 0.3
done

if [ "$_rm_focus" = 1 ] && [ -n "$_rm_prevwin" ]; then
    tmux select-window -t "$_rm_prevwin" 2>/dev/null || true
fi

# no sentinel means the window died mid-review; that is a launcher failure, never revmux's exit 1
_rm_rc="$RC_LAUNCH_FAIL"
[ -f "$_rm_sentinel" ] && _rm_rc=$(read_rc "$_rm_sentinel")
# `|| true` because the exit code is already read: this is tidying, and letting it abort here would
# lose a report that is sitting in $REPORT_FILE with print_report_and_exit one line away
rm -f "$_rm_sentinel" "$_rm_sentinel".tmp "$_rm_sentinel".pid || true
print_report_and_exit "${_rm_rc:-$RC_LAUNCH_FAIL}"
