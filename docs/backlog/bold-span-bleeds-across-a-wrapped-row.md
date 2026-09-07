---
worth: later
where: app/ui/agentpane.go:19
added: 2026-09-07
---
# a bold span broken by a wrap bleeds past the row in the agent pane and the plain renderer

`markdown` emits `\x1b[1m`/`\x1b[22m` for `**emphasis**`, and `ansi.Wrap` passes escapes through
without re-emitting them at a row start. A row that ends inside a bold span therefore leaves bold on,
and `detailPane` slices rows arbitrarily: when that row is the last visible one, bold leaks into
whatever is drawn next until the following frame's first lipgloss render resets it, and when the
continuation row is the first visible one after a scroll it renders un-bold.

The combined pane closes and re-opens bold per row in `textRows`; `agentpane.go` and
`app/progress.go` go through `Wrap` with no per-row pass at all. `findings.rowLines` closes it too,
since `ansiHeadOff` carries `\x1b[22m`. The fix is one per-row helper the three share.

Surfaced by the issue #27 review as pre-existing. The symptom is a row of bold prose rather than lost
information, which is why it waits for the shared helper rather than a third copy of the loop.
