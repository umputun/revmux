---
worth: later
where: app/ui/combined.go:81
added: 2026-09-07
---
# a bold span broken by a wrap bleeds past the row and is lost on the next one

`markdown` emits `\x1b[1m`/`\x1b[22m` for `**emphasis**`, and `ansi.Wrap` passes escapes through
without re-emitting them at a row start. A row that ends inside a bold span therefore leaves bold on,
and `detailPane` slices rows arbitrarily: when that row is the last visible one, bold leaks into
whatever is drawn next until the following frame's first lipgloss render resets it, and when the
continuation row is the first visible one after a scroll it renders un-bold.

`textRows` closes and re-opens the agent color and the underline of a code span per row, and could
track bold the same way; `agentpane.go` and `app/progress.go` go through `Wrap` with no per-row pass at
all. `findings.rowLines` already closes bold per row, since `ansiHeadOff` carries `\x1b[22m`.

Surfaced by the issue #27 review as pre-existing. Deferred because a per-row bold state in three
renderers wants one helper, and the symptom is a row of bold prose rather than lost information.
