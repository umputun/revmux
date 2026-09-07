---
worth: maybe
where: app/prompt/roster.go:263
added: 2026-09-07
---
# `AgentSpec.Paint` emits raw SGR whatever the terminal reports

`Paint` and `SGR` build escape sequences from the roster color with no reference to the color profile,
so an agent name is painted under `NO_COLOR` and on a `TERM=dumb` terminal, in the TUI and under
`--no-tui` alike. The lipgloss styles beside it go colorless there, since `newStyles` profiles the tty.

The combined pane's text paint checks `styles.profile` and stays plain on an Ascii profile; the name
prefix does not, and the plain renderer has no profile to check at all. Honoring the profile means
threading it to `Paint`'s two callers or gating in each, and deciding whether `--no-tui` output written
to a log file should ever carry color.

Surfaced by the issue #27 review as pre-existing. `maybe` because nobody has asked, and a log a caller
model tails may be better off without color regardless of the profile.
