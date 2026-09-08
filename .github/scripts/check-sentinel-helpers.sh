#!/bin/sh
# the eight overlay backends inside launch-revmux.sh block on a sentinel, and each must claim its round
# through new_sentinel and build its inner command through write_rc_cmd or write_fifo_rc_cmd. The three
# that change directory inside the generated shell pass it as the helper's second argument; the other
# five hand it to the backend itself, through --cwd or an initial-working-directory setting, and pass
# nothing here. A backend that hand-rolls any of that is still valid shell, which the linter
# passes, and both plugin trees carry the same mistake, which `diff -r` passes. That is how the iTerm2
# path shipped waiting ten seconds for a pid nothing ever wrote (#29), losing the report of a review
# that had finished.
#
# Two shapes of that drift, and each needs its own check. A backend that never calls the helper moves
# the totals apart. A backend that calls the helper behind a `cd X &&` prefix does not - it still calls
# it, so the totals stay equal while the `&&` binds to the pid write alone and a failed cd leaves the
# pid unpublished. Counting cannot see the second shape at all, so it is matched directly.
#
# What a green run establishes is still narrow. The totals agreeing does not pair them up per backend,
# and nothing here says whether the generated command runs.
#
# agentdeck-window.sh is a ninth sentinel backend and is deliberately not in the list. It is sourced
# rather than inlined, so reading launch-revmux.sh cannot see it, and it cannot comply: its own header
# forbids an EXIT trap, which new_sentinel installs. It is also not exposed to #29 - it never calls
# await_sentinel, bounding its wait on the tmux window rather than on a published pid.
set -eu

files="$*"
if [ -z "$files" ]; then
    files=".claude-plugin/skills/revmux/scripts/launch-revmux.sh plugins/codex/skills/revmux/scripts/launch-revmux.sh"
fi

status=0
for f in $files; do
    # an unreadable file is not a passing file: without this the counts are all zero, agree, and the
    # check reports success for a file it never opened
    if [ ! -r "$f" ]; then
        echo "$f: cannot read - the check did not run, which is not the same as passing" >&2
        status=1
        continue
    fi

    # whole-line comments come out first: prose naming a helper would otherwise count as a backend
    # calling it, which is enough to make the totals agree while a backend is still hand-rolled
    code=$(grep -v '^[[:space:]]*#' "$f" || true)

    # the single quotes are the point: these are patterns matching a literal $SENTINEL in the source
    # shellcheck disable=SC2016
    waits=$(printf '%s\n' "$code" | grep -cE 'await_sentinel "\$SENTINEL"' || true)
    claims=$(printf '%s\n' "$code" | grep -cE '^ +new_sentinel$' || true)
    # shellcheck disable=SC2016
    builds=$(printf '%s\n' "$code" | grep -cE '\$\(write_(fifo_)?rc_cmd "\$SENTINEL"' || true)

    # zero of everything is what a rename of $SENTINEL or of the helpers looks like, and it agrees with
    # itself, so without this the check goes green having covered nothing
    if [ "$waits" -eq 0 ]; then
        echo "$f: no sentinel backend matched - this file is unchecked, not clean" >&2
        echo "  either it has no sentinel backend and does not belong in the list, or a rename broke" >&2
        echo "  the patterns and the check silently stopped covering it" >&2
        status=1
        continue
    fi

    if prefixed=$(printf '%s\n' "$code" | grep -nE 'cd .*&&.*write_(fifo_)?rc_cmd'); then
        echo "$f: a directory change is prefixed to the helper instead of passed to it" >&2
        printf '%s\n' "$prefixed" | sed 's/^/  /' >&2
        echo "  the && binds to the pid write alone, so a failed cd leaves the pid unpublished and" >&2
        echo "  await_sentinel waits out the whole grace period - pass the directory as the helper's" >&2
        echo "  second argument instead" >&2
        status=1
    fi

    if [ "$waits" = "$claims" ] && [ "$waits" = "$builds" ]; then
        continue
    fi
    echo "$f: sentinel backends disagree" >&2
    echo "  $waits wait on a sentinel (await_sentinel)" >&2
    echo "  $claims claim a round (new_sentinel)" >&2
    echo "  $builds build the inner command through the helper (write_rc_cmd/write_fifo_rc_cmd)" >&2
    echo "  a backend that waits must do both, or it waits out the grace period and loses the report" >&2
    status=1
done

exit "$status"
