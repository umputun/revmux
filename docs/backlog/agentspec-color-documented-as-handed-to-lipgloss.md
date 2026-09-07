---
worth: yes
where: app/prompt/roster.go:107
added: 2026-09-07
---
# `AgentSpec.Color` is documented as handed to lipgloss, and nothing hands it there

The `AgentSpec` godoc says `Color` is "the resolved form a renderer hands straight to lipgloss", and
`.claude/rules/prompts.md` repeats it under "Agent color", then concludes that lipgloss downsamples a
`#RRGGBB` value on a terminal that cannot show it. No renderer reads `Color` directly: `app/ui` and
`app/progress.go` reach it only through `Paint` and `SGR`, which build the escape themselves and emit
`\x1b[38;2;r;g;bm` for a hex value unconditionally. The only downsampler in the tree is
`mdRenderer.downsample`, which covers glamour documents and not the agent paint.

A reader of the rule choosing `#RRGGBB` on a 16-color terminal is told a fallback exists that does not.
Fix is prose in both places; whether hex should downsample against the profile is a separate call.

Surfaced by the issue #27 review as pre-existing.
